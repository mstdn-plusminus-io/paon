//go:build integration

package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm/clause"
)

func TestListTimelineStatusIDsMatchesJoinQueryIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		LocalDomain:          "paon.example",
		WebDomain:            "paon.example",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	now := time.Now().UTC()
	accountIDs := make([]int64, 0, 6)
	for _, username := range []string{"owner", "member1", "member2", "member3", "other1", "other2"} {
		account := models.Account{Username: username, CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		accountIDs = append(accountIDs, account.ID)
	}
	accountArray := "{" + strings.Trim(strings.Join(strings.Fields(fmt.Sprint(accountIDs)), ","), "[]") + "}"
	// Statuses cycle through all accounts, visibilities, reply targets,
	// deletions and reblogs of deleted statuses so every filter has rows to
	// keep and to drop.
	if err := database.Exec(`
		INSERT INTO statuses (id, text, created_at, updated_at, account_id, visibility, reply, in_reply_to_account_id, deleted_at, reblog_of_id)
		SELECT i, 'status', now(), now(),
		       (CAST(? AS bigint[]))[i % 6 + 1],
		       (i / 6) % 4,
		       (i / 24) % 3 <> 0,
		       CASE WHEN (i / 24) % 3 = 0 THEN NULL ELSE (CAST(? AS bigint[]))[(i / 72) % 6 + 1] END,
		       CASE WHEN i % 97 = 0 THEN now() END,
		       CASE WHEN i % 13 = 0 THEN i - 6 END
		FROM generate_series(1, 3000) AS i`, accountArray, accountArray).Error; err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: cfg, db: database}
	for _, policy := range []int{0, 1, 2} {
		list := models.List{AccountID: accountIDs[0], Title: fmt.Sprintf("policy %d", policy), RepliesPolicy: policy, CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&list).Error; err != nil {
			t.Fatal(err)
		}
		// The owner and followed members are shown; member3 lacks an accepted
		// follow and must be excluded.
		for i, memberID := range accountIDs[:4] {
			item := models.ListAccount{ListID: list.ID, AccountID: memberID}
			if i == 1 || i == 2 {
				follow := models.Follow{AccountID: accountIDs[0], TargetAccountID: memberID, CreatedAt: now, UpdatedAt: now}
				if err := database.Omit(clause.Associations).Where(models.Follow{AccountID: follow.AccountID, TargetAccountID: memberID}).FirstOrCreate(&follow).Error; err != nil {
					t.Fatal(err)
				}
				item.FollowID = sql.NullInt64{Int64: follow.ID, Valid: true}
			}
			if err := database.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, rawQuery := range []string{"", "limit=40", "limit=50", "max_id=1500", "since_id=2900", "min_id=100", "min_id=100&max_id=400&limit=5"} {
			t.Run(fmt.Sprintf("policy=%d/%s", policy, rawQuery), func(t *testing.T) {
				c := echo.NewContext(httptest.NewRequest("GET", "/api/v1/timelines/list/1?"+rawQuery, nil), httptest.NewRecorder(), echo.New())
				got, err := server.listTimelineStatusIDs(c, list)
				if err != nil {
					t.Fatal(err)
				}
				want := listTimelineJoinReferenceIDs(t, server, c, list)
				if len(want) == 0 {
					t.Fatal("reference query returned no statuses; fixture does not exercise the list")
				}
				// Members' statuses are interleaved and far exceed the limit, so
				// the unbounded pages must be filled up to the (capped) limit.
				if wantLen, ok := map[string]int{"": 20, "limit=40": 40, "limit=50": 40}[rawQuery]; ok && len(got) != wantLen {
					t.Fatalf("len(listTimelineStatusIDs) = %d, want %d", len(got), wantLen)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("listTimelineStatusIDs = %v, want %v", got, want)
				}
			})
		}
	}
}

// listTimelineJoinReferenceIDs is the previous JOIN-based list timeline query.
func listTimelineJoinReferenceIDs(t *testing.T, server *Server, c *echo.Context, list models.List) []int64 {
	t.Helper()
	query := server.db.Table("statuses").
		Select("statuses.id").
		Joins("JOIN list_accounts ON list_accounts.account_id = statuses.account_id").
		Where("list_accounts.list_id = ?", list.ID).
		Where("(list_accounts.follow_id IS NOT NULL OR list_accounts.account_id = ?)", list.AccountID).
		Where("statuses.deleted_at IS NULL").
		Where("statuses.reblog_of_id IS NULL OR EXISTS (SELECT 1 FROM statuses reblogged_statuses WHERE reblogged_statuses.id = statuses.reblog_of_id AND reblogged_statuses.deleted_at IS NULL)").
		Where("statuses.visibility IN ?", []int{0, 1, 2})
	switch list.RepliesPolicy {
	case 2:
		query = query.Where("(statuses.reply = false OR statuses.in_reply_to_account_id = statuses.account_id)")
	case 1:
	default:
		query = query.Where(`(
			statuses.reply = false
			OR statuses.in_reply_to_account_id = statuses.account_id
			OR EXISTS (
				SELECT 1 FROM list_accounts reply_members
				WHERE reply_members.list_id = ?
				  AND reply_members.account_id = statuses.in_reply_to_account_id
			)
		)`, list.ID)
	}
	ids := []int64{}
	if err := applyStatusListPagination(c, query, "statuses.id").Limit(limit(c, 20, 40)).Scan(&ids).Error; err != nil {
		t.Fatal(err)
	}
	return ids
}
