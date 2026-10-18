//go:build integration

package api

import (
	"context"
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
)

func TestHomeTimelineStatusIDsMatchesJoinQueryIntegration(t *testing.T) {
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
	accounts := map[string]int64{}
	accountIDs := []int64{}
	for _, username := range []string{"owner", "followed", "languages", "exclusive", "muted", "blocked", "other", "newcomer"} {
		account := models.Account{Username: username, CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		accounts[username] = account.ID
		accountIDs = append(accountIDs, account.ID)
	}
	posters := accountIDs[:7]
	posterArray := "{" + strings.Trim(strings.Join(strings.Fields(fmt.Sprint(posters)), ","), "[]") + "}"
	// Statuses cycle through every poster, visibility, reply target, language,
	// reblog and deletion so each home filter has rows to keep and to drop.
	if err := database.Exec(`
		INSERT INTO statuses (id, text, created_at, updated_at, account_id, visibility, reply, in_reply_to_id, in_reply_to_account_id, language, reblog_of_id, deleted_at)
		SELECT i, 'status', now(), now(),
		       (CAST(? AS bigint[]))[i % 7 + 1],
		       (i / 7) % 4,
		       (i / 28) % 3 <> 0,
		       CASE WHEN (i / 28) % 3 = 0 OR i % 5 = 0 OR i = 1 THEN NULL ELSE i - 1 END,
		       CASE WHEN (i / 28) % 3 = 0 THEN NULL ELSE (CAST(? AS bigint[]))[(i / 84) % 7 + 1] END,
		       (ARRAY['ja', 'en', NULL])[i % 3 + 1],
		       CASE WHEN i % 11 = 0 AND i > 11 THEN i - 11 END,
		       CASE WHEN i % 97 = 0 THEN now() END
		FROM generate_series(1, 4000) AS i`, posterArray, posterArray).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`
		INSERT INTO mentions (status_id, account_id, silent, created_at, updated_at)
		SELECT i, CASE WHEN i % 2 = 0 THEN CAST(? AS bigint) ELSE CAST(? AS bigint) END, i % 3 = 0, now(), now()
		FROM generate_series(1, 4000, 17) AS i`, accounts["muted"], accounts["blocked"]).Error; err != nil {
		t.Fatal(err)
	}

	owner := accounts["owner"]
	for _, follow := range []models.Follow{
		{TargetAccountID: accounts["followed"], ShowReblogs: true},
		{TargetAccountID: accounts["languages"], ShowReblogs: false, Languages: models.StringArray{"ja"}},
		{TargetAccountID: accounts["exclusive"], ShowReblogs: true},
		{TargetAccountID: accounts["muted"], ShowReblogs: true},
		{TargetAccountID: accounts["blocked"], ShowReblogs: true},
	} {
		follow.AccountID, follow.CreatedAt, follow.UpdatedAt = owner, now, now
		if err := database.Create(&follow).Error; err != nil {
			t.Fatal(err)
		}
	}
	list := models.List{AccountID: owner, Title: "exclusive", Exclusive: true, CreatedAt: now, UpdatedAt: now}
	if err := database.Create(&list).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.ListAccount{ListID: list.ID, AccountID: accounts["exclusive"]}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Mute{AccountID: owner, TargetAccountID: accounts["muted"], CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Block{AccountID: owner, TargetAccountID: accounts["blocked"], CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: cfg, db: database}
	for _, username := range []string{"owner", "newcomer"} {
		var account models.Account
		if err := database.First(&account, accounts[username]).Error; err != nil {
			t.Fatal(err)
		}
		for _, rawQuery := range []string{"", "limit=40", "limit=50", "max_id=1500", "since_id=3900", "min_id=100", "min_id=100&max_id=400&limit=5"} {
			t.Run(username+"/"+rawQuery, func(t *testing.T) {
				c := echo.NewContext(httptest.NewRequest("GET", "/api/v1/timelines/home?"+rawQuery, nil), httptest.NewRecorder(), echo.New())
				got, err := server.homeTimelineStatusIDs(c, &account)
				if err != nil {
					t.Fatal(err)
				}
				want := homeTimelineJoinReferenceIDs(t, server, c, &account)
				if username == "newcomer" {
					if len(got) != 0 {
						t.Fatalf("homeTimelineStatusIDs = %v, want none for an account without follows or statuses", got)
					}
					return
				}
				if len(want) == 0 {
					t.Fatal("reference query returned no statuses; fixture does not exercise the home timeline")
				}
				if wantLen, ok := map[string]int{"": 20, "limit=40": 40, "limit=50": 40}[rawQuery]; ok && len(got) != wantLen {
					t.Fatalf("len(homeTimelineStatusIDs) = %d, want %d", len(got), wantLen)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("homeTimelineStatusIDs = %v, want %v", got, want)
				}
			})
		}
	}
}

// homeTimelineJoinReferenceIDs is the previous LEFT JOIN-based home timeline query.
func homeTimelineJoinReferenceIDs(t *testing.T, server *Server, c *echo.Context, account *models.Account) []int64 {
	t.Helper()
	query := homeTimelineConditions(server.db.Table("statuses").Select("statuses.id"), account)
	ids := []int64{}
	if err := applyStatusListPagination(c, query, "statuses.id").Limit(limit(c, 20, 40)).Scan(&ids).Error; err != nil {
		t.Fatal(err)
	}
	return ids
}
