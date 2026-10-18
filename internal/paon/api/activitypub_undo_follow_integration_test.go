//go:build integration

package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestActivityPubUndoFollowIsScopedToActorIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example",
		SecretKeyBase: "activitypub-undo-follow-integration-secret",
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

	setup := func(t *testing.T) (*Server, *gorm.DB, models.Account, models.Account, models.Account) {
		t.Helper()
		tx := database.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		t.Cleanup(func() { tx.Rollback() })
		now := time.Now().UTC()
		target := models.Account{Username: "carol", Locked: true, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&target).Error; err != nil {
			t.Fatal(err)
		}
		victim := settingsResolveRemoteAccount(t, tx, "victim", "victim.example", "https://victim.example/users/victim", now)
		attacker := settingsResolveRemoteAccount(t, tx, "mallory", "evil.example", "https://evil.example/users/mallory", now)
		return &Server{cfg: cfg, db: tx}, tx, target, victim, attacker
	}
	undoFollow := func(id string, target models.Account) activityObject {
		return activityObject{
			ID: id, IDPresent: true, Type: "Follow", TypeExact: "Follow", TypePresent: true,
			ObjectIDRaw: "https://paon.example/users/" + target.Username, ObjectIDPresent: true,
		}
	}
	count := func(t *testing.T, tx *gorm.DB, model any, accountID, targetAccountID int64) int64 {
		t.Helper()
		var n int64
		if err := tx.Model(model).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("an Undo carrying another account's Follow URI does not delete that follow", func(t *testing.T) {
		s, tx, target, victim, attacker := setup(t)
		now := time.Now().UTC()
		victimFollowURI := "https://victim.example/follows/1"
		if err := tx.Create(&models.Follow{AccountID: victim.ID, TargetAccountID: target.ID, URI: models.NullSafeString(victimFollowURI), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}

		handled, err := s.processActivityPubUndoFollowWithTombstone(undoFollow(victimFollowURI, target), &attacker, false)
		if err != nil {
			t.Fatal(err)
		}
		if handled {
			t.Fatal("Undo from an actor without its own relationship must not be handled")
		}
		if got := count(t, tx, &models.Follow{}, victim.ID, target.ID); got != 1 {
			t.Fatalf("victim follow count = %d, want 1", got)
		}
	})

	t.Run("an Undo carrying another account's FollowRequest URI does not delete that request", func(t *testing.T) {
		s, tx, target, victim, attacker := setup(t)
		now := time.Now().UTC()
		victimRequestURI := "https://victim.example/follows/2"
		request := models.FollowRequest{AccountID: victim.ID, TargetAccountID: target.ID, URI: models.NullSafeString(victimRequestURI), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&request).Error; err != nil {
			t.Fatal(err)
		}
		notification := models.Notification{AccountID: target.ID, FromAccountID: victim.ID, ActivityType: "FollowRequest", ActivityID: request.ID, Type: models.NullSafeString("follow_request"), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&notification).Error; err != nil {
			t.Fatal(err)
		}

		handled, err := s.processActivityPubUndoFollowWithTombstone(undoFollow(victimRequestURI, target), &attacker, false)
		if err != nil {
			t.Fatal(err)
		}
		if handled {
			t.Fatal("Undo from an actor without its own request must not be handled")
		}
		if got := count(t, tx, &models.FollowRequest{}, victim.ID, target.ID); got != 1 {
			t.Fatalf("victim follow request count = %d, want 1", got)
		}
		var notifications int64
		if err := tx.Model(&models.Notification{}).Where("id = ?", notification.ID).Count(&notifications).Error; err != nil {
			t.Fatal(err)
		}
		if notifications != 1 {
			t.Fatalf("victim follow_request notification count = %d, want 1", notifications)
		}
	})

	t.Run("an Undo removes the actor's own follow of the target like Rails undo_follow", func(t *testing.T) {
		s, tx, target, victim, attacker := setup(t)
		now := time.Now().UTC()
		if err := tx.Create(&models.Follow{AccountID: victim.ID, TargetAccountID: target.ID, URI: models.NullSafeString("https://victim.example/follows/3"), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Create(&models.Follow{AccountID: attacker.ID, TargetAccountID: target.ID, URI: models.NullSafeString("https://evil.example/follows/original"), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}

		// Rails looks up @account.following?(target_account), not the Undo object id.
		handled, err := s.processActivityPubUndoFollowWithTombstone(undoFollow("https://evil.example/follows/other-id", target), &attacker, false)
		if err != nil {
			t.Fatal(err)
		}
		if !handled {
			t.Fatal("Undo of the actor's own follow must be handled")
		}
		if got := count(t, tx, &models.Follow{}, attacker.ID, target.ID); got != 0 {
			t.Fatalf("actor follow count = %d, want 0", got)
		}
		if got := count(t, tx, &models.Follow{}, victim.ID, target.ID); got != 1 {
			t.Fatalf("victim follow count = %d, want 1", got)
		}
	})
}
