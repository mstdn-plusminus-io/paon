//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestSelfDestructAccountTransitionRetainsRowsAndAppliesSeparateCaps(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		LocalDomain:          "paon.example",
		SecretKeyBase:        "integration-secret",
	}
	token, err := GenerateSelfDestructToken(cfg.SecretKeyBase, cfg.LocalDomain)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SelfDestruct = token
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	now := time.Now().UTC()
	// A fresh Mastodon schema already contains the unsuspended local
	// mastodon.internal instance actor. Add 54 users so this group still has
	// 55 eligible rows and exercises the 50-account cap without pretending the
	// seeded actor is outside Account.local.
	for index := 0; index < 54; index++ {
		account := models.Account{Username: "active" + strconv.Itoa(index), CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 55; index++ {
		account := models.Account{
			Username:         "requested" + strconv.Itoa(index),
			SuspendedAt:      sql.NullTime{Time: now.Add(-time.Hour), Valid: true},
			SuspensionOrigin: sql.NullInt64{Int64: 0, Valid: true},
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := database.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		request := models.AccountDeletionRequest{AccountID: models.AccountDeletionRequestAccountID(account.ID), CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&request).Error; err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{cfg: cfg, db: database}
	enqueued := 0
	enqueue := func(_ context.Context, _ models.Account, inboxes []string) error {
		if len(inboxes) != 1 {
			t.Fatalf("inboxes = %d", len(inboxes))
		}
		enqueued++
		return nil
	}
	active, err := server.processSelfDestructAccountGroup(context.Background(), false, []string{"https://remote.example/inbox"}, enqueue)
	if err != nil || active != 50 {
		t.Fatalf("active group = %d, %v", active, err)
	}
	requested, err := server.processSelfDestructAccountGroup(context.Background(), true, []string{"https://remote.example/inbox"}, enqueue)
	if err != nil || requested != 50 {
		t.Fatalf("requested group = %d, %v", requested, err)
	}
	if enqueued != 100 {
		t.Fatalf("enqueued accounts = %d, want 100", enqueued)
	}

	var accounts int64
	if err := database.Model(&models.Account{}).Count(&accounts).Error; err != nil || accounts != 110 {
		t.Fatalf("retained accounts = %d, %v", accounts, err)
	}
	var requests int64
	if err := database.Model(&models.AccountDeletionRequest{}).Count(&requests).Error; err != nil || requests != 5 {
		t.Fatalf("remaining deletion requests = %d, %v", requests, err)
	}
	var localSuspensions int64
	if err := database.Model(&models.Account{}).Where("suspended_at IS NOT NULL AND suspension_origin = ?", 0).Count(&localSuspensions).Error; err != nil || localSuspensions != 105 {
		t.Fatalf("local suspensions = %d, %v", localSuspensions, err)
	}

	failed, err := server.processSelfDestructAccountGroup(context.Background(), false, []string{"https://remote.example/inbox"}, func(context.Context, models.Account, []string) error {
		return errors.New("enqueue unavailable")
	})
	if err == nil || failed != 0 {
		t.Fatalf("failed group = %d, %v", failed, err)
	}
	var stillActive int64
	if err := database.Model(&models.Account{}).Where("domain IS NULL AND suspended_at IS NULL").Count(&stillActive).Error; err != nil || stillActive != 5 {
		t.Fatalf("active accounts after enqueue failure = %d, %v", stillActive, err)
	}
}
