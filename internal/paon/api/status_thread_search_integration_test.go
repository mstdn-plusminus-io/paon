//go:build integration

package api

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestStatusAncestorsAndSearchFiltersMatchRailsIntegration(t *testing.T) {
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
		SecretKeyBase:        "status-thread-search-integration-secret",
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
	create := func(value any) {
		t.Helper()
		if err := database.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{cfg: cfg, db: database}

	t.Run("ancestors keep the closest statuses like Rails ancestor_ids", func(t *testing.T) {
		author := models.Account{Username: "threader", CreatedAt: now, UpdatedAt: now}
		create(&author)
		chain := make([]int64, 0, 45)
		for i := range 45 {
			status := models.Status{Text: "reply", AccountID: author.ID, CreatedAt: now, UpdatedAt: now}
			if i > 0 {
				status.InReplyToID = sql.NullInt64{Int64: chain[i-1], Valid: true}
				status.InReplyToAccountID = sql.NullInt64{Int64: author.ID, Valid: true}
			}
			create(&status)
			chain = append(chain, status.ID)
		}
		leaf := models.Status{ID: chain[len(chain)-1], InReplyToID: sql.NullInt64{Int64: chain[len(chain)-2], Valid: true}}
		for _, limitValue := range []int{anonymousAncestorsLimit, 3, 100} {
			ancestors, err := server.statusAncestors(leaf, limitValue, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, 0, len(ancestors))
			for _, status := range ancestors {
				got = append(got, status.ID)
			}
			parents := chain[:len(chain)-1]
			want := parents[max(0, len(parents)-limitValue):]
			if !slices.Equal(got, want) {
				t.Fatalf("limit %d ancestors = %v, want closest ancestors root first %v", limitValue, got, want)
			}
		}
	})

	t.Run("search hits are rejected by StatusFilter like Rails filter_results", func(t *testing.T) {
		accounts := map[string]models.Account{}
		for _, username := range []string{"searcher", "plain", "muted", "blocked", "remote", "silenced", "silenced_followed"} {
			account := models.Account{Username: username, CreatedAt: now, UpdatedAt: now}
			switch username {
			case "remote":
				account.Domain = sql.NullString{String: "Remote.example", Valid: true}
			case "silenced", "silenced_followed":
				account.SilencedAt = sql.NullTime{Time: now, Valid: true}
			}
			create(&account)
			accounts[username] = account
		}
		searcher := accounts["searcher"]
		create(&models.Mute{AccountID: searcher.ID, TargetAccountID: accounts["muted"].ID, CreatedAt: now, UpdatedAt: now})
		create(&models.Block{AccountID: searcher.ID, TargetAccountID: accounts["blocked"].ID, CreatedAt: now, UpdatedAt: now})
		create(&models.Follow{AccountID: searcher.ID, TargetAccountID: accounts["silenced_followed"].ID, CreatedAt: now, UpdatedAt: now})
		if err := database.Exec(`INSERT INTO account_domain_blocks (account_id, domain, created_at, updated_at) VALUES (?, 'remote.example', now(), now())`, searcher.ID).Error; err != nil {
			t.Fatal(err)
		}

		statusIDs := map[string]int64{}
		ids := []int64{}
		for _, username := range []string{"searcher", "plain", "muted", "blocked", "remote", "silenced", "silenced_followed"} {
			status := models.Status{Text: "needle", AccountID: accounts[username].ID, CreatedAt: now, UpdatedAt: now}
			create(&status)
			statusIDs[username] = status.ID
			ids = append(ids, status.ID)
		}

		var statuses []models.Status
		if err := server.visibleSearchStatusQuery(&searcher).Where("statuses.id IN ?", ids).Order("statuses.id").Find(&statuses).Error; err != nil {
			t.Fatal(err)
		}
		got := make([]int64, 0, len(statuses))
		for _, status := range statuses {
			got = append(got, status.ID)
		}
		want := []int64{statusIDs["searcher"], statusIDs["plain"], statusIDs["silenced_followed"]}
		if !slices.Equal(got, want) {
			t.Fatalf("search statuses = %v, want own, plain and followed silenced %v (all: %v)", got, want, statusIDs)
		}
	})
}
