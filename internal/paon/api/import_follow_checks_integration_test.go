//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

// TestImportAndBatchFollowsUseFollowServiceChecksIntegration covers the
// following/lists CSV imports and the relationships batch form: like Rails
// FollowService they refuse follows that blocks or domain blocks forbid, and
// they turn follows of remote ActivityPub accounts into delivered follow
// requests instead of follows that skip the remote Accept.
func TestImportAndBatchFollowsUseFollowServiceChecksIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example",
		SecretKeyBase: "import-follow-checks-integration-secret",
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
	ctx := context.Background()
	options := map[string]any{"show_reblogs": false, "notify": true, "languages": []string{"ja"}}

	t.Run("following import fails rows for an unlocked account that blocks the importer", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		victim := f.localAccount("victim", false)
		f.block(victim.ID, f.source.ID)

		bulkImport, rows := f.bulkImport("following", map[string]any{"acct": "victim", "show_reblogs": true, "notify": false})
		f.processRows(ctx, rows)

		f.assertNoFollow(f.source.ID, victim.ID)
		f.assertNoFollowRequest(f.source.ID, victim.ID)
		f.assertProgress(bulkImport.ID, 1, 0)
		f.assertRowKept(rows[0].ID)
		if got := deliveries.all(); len(got) != 0 {
			t.Fatalf("refused follow delivered %+v", got)
		}
	})

	t.Run("following import fails rows the importer blocks or whose domain it blocks", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		blocked := f.localAccount("blocked", false)
		f.block(f.source.ID, blocked.ID)
		remote := settingsResolveRemoteAccount(t, f.tx, "alice", "remote.example", "https://remote.example/users/alice", time.Now().UTC())
		now := time.Now().UTC()
		if err := f.tx.Create(&models.AccountDomainBlock{AccountID: models.AccountDomainBlockAccountID(f.source.ID), Domain: "remote.example", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}

		bulkImport, rows := f.bulkImport("following", map[string]any{"acct": "blocked"}, map[string]any{"acct": "alice@remote.example"})
		f.processRows(ctx, rows)

		f.assertNoFollow(f.source.ID, blocked.ID)
		f.assertNoFollowRequest(f.source.ID, blocked.ID)
		f.assertNoFollow(f.source.ID, remote.ID)
		f.assertNoFollowRequest(f.source.ID, remote.ID)
		f.assertProgress(bulkImport.ID, 2, 0)
		if got := deliveries.all(); len(got) != 0 {
			t.Fatalf("refused follows delivered %+v", got)
		}
	})

	t.Run("following import requests remote ActivityPub accounts and delivers the Follow", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		remote := settingsResolveRemoteAccount(t, f.tx, "alice", "remote.example", "https://remote.example/users/alice", time.Now().UTC())

		bulkImport, rows := f.bulkImport("following", importFollowChecksRow("alice@remote.example", options))
		f.processRows(ctx, rows)

		f.assertNoFollow(f.source.ID, remote.ID)
		request := f.followRequest(f.source.ID, remote.ID)
		importFollowChecksAssertOptions(t, request.ShowReblogs, request.Notify, request.Languages, false, true, "ja")
		f.assertProgress(bulkImport.ID, 1, 1)
		importFollowChecksAssertFollowDelivered(t, deliveries.all(), remote, request)
	})

	t.Run("following import keeps a pending remote request pending and only updates its options", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		remote := settingsResolveRemoteAccount(t, f.tx, "alice", "remote.example", "https://remote.example/users/alice", time.Now().UTC())
		now := time.Now().UTC()
		pending := models.FollowRequest{AccountID: f.source.ID, TargetAccountID: remote.ID, ShowReblogs: true, Notify: false, URI: "https://paon.example/pending", CreatedAt: now, UpdatedAt: now}
		if err := f.tx.Create(&pending).Error; err != nil {
			t.Fatal(err)
		}

		bulkImport, rows := f.bulkImport("following", importFollowChecksRow("alice@remote.example", options))
		f.processRows(ctx, rows)

		f.assertNoFollow(f.source.ID, remote.ID)
		request := f.followRequest(f.source.ID, remote.ID)
		if request.ID != pending.ID {
			t.Fatalf("follow request = %d, want the pending request %d", request.ID, pending.ID)
		}
		importFollowChecksAssertOptions(t, request.ShowReblogs, request.Notify, request.Languages, false, true, "ja")
		f.assertProgress(bulkImport.ID, 1, 1)
		if got := deliveries.all(); len(got) != 0 {
			t.Fatalf("pending request must not be delivered again: %+v", got)
		}
	})

	t.Run("following import requests locked accounts with the CSV options", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		locked := f.localAccount("locked", true)

		bulkImport, rows := f.bulkImport("following", importFollowChecksRow("locked", options))
		f.processRows(ctx, rows)

		f.assertNoFollow(f.source.ID, locked.ID)
		request := f.followRequest(f.source.ID, locked.ID)
		importFollowChecksAssertOptions(t, request.ShowReblogs, request.Notify, request.Languages, false, true, "ja")
		f.assertNotification(locked.ID, "FollowRequest", request.ID, "follow_request")
		f.assertProgress(bulkImport.ID, 1, 1)
		if got := deliveries.all(); len(got) != 0 {
			t.Fatalf("local follow request delivered %+v", got)
		}
	})

	t.Run("following import follows unlocked local accounts with the CSV options", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		carol := f.localAccount("carol", false)

		bulkImport, rows := f.bulkImport("following", importFollowChecksRow("carol", options))
		f.processRows(ctx, rows)

		follow := f.follow(f.source.ID, carol.ID)
		importFollowChecksAssertOptions(t, follow.ShowReblogs, follow.Notify, follow.Languages, false, true, "ja")
		f.assertNoFollowRequest(f.source.ID, carol.ID)
		f.assertNotification(carol.ID, "Follow", follow.ID, "follow")
		f.assertProgress(bulkImport.ID, 1, 1)
		f.assertRowDeleted(rows[0].ID)
	})

	t.Run("following import by a silenced account requests unlocked local accounts", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		carol := f.localAccount("carol", false)
		if err := f.tx.Model(&models.Account{}).Where("id = ?", f.source.ID).Update("silenced_at", time.Now().UTC()).Error; err != nil {
			t.Fatal(err)
		}

		bulkImport, rows := f.bulkImport("following", importFollowChecksRow("carol", options))
		f.processRows(ctx, rows)

		f.assertNoFollow(f.source.ID, carol.ID)
		f.followRequest(f.source.ID, carol.ID)
		f.assertProgress(bulkImport.ID, 1, 1)
	})

	t.Run("lists import applies FollowService before adding accounts", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		victim := f.localAccount("victim", false)
		f.block(victim.ID, f.source.ID)
		stale := f.localAccount("stale", false)
		f.block(stale.ID, f.source.ID)
		now := time.Now().UTC()
		// A follow left behind next to a block must not let the import list the account.
		if err := f.tx.Create(&models.Follow{AccountID: f.source.ID, TargetAccountID: stale.ID, ShowReblogs: true, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		carol := f.localAccount("carol", false)
		remote := settingsResolveRemoteAccount(t, f.tx, "alice", "remote.example", "https://remote.example/users/alice", now)

		bulkImport, rows := f.bulkImport("lists",
			map[string]any{"list_name": "friends", "acct": "victim"},
			map[string]any{"list_name": "friends", "acct": "stale"},
			map[string]any{"list_name": "friends", "acct": "alice@remote.example"},
			map[string]any{"list_name": "friends", "acct": "carol"},
		)
		f.processRows(ctx, rows)

		list := f.list("friends")
		f.assertNoFollow(f.source.ID, victim.ID)
		f.assertNoFollowRequest(f.source.ID, victim.ID)
		f.assertNotListed(list.ID, victim.ID)
		f.assertNotListed(list.ID, stale.ID)

		f.assertNoFollow(f.source.ID, remote.ID)
		request := f.followRequest(f.source.ID, remote.ID)
		importFollowChecksAssertOptions(t, request.ShowReblogs, request.Notify, request.Languages, true, false, "")
		f.assertListed(list.ID, remote.ID, sql.NullInt64{}, sql.NullInt64{Int64: request.ID, Valid: true})

		follow := f.follow(f.source.ID, carol.ID)
		importFollowChecksAssertOptions(t, follow.ShowReblogs, follow.Notify, follow.Languages, true, false, "")
		f.assertListed(list.ID, carol.ID, sql.NullInt64{Int64: follow.ID, Valid: true}, sql.NullInt64{})

		f.assertProgress(bulkImport.ID, 4, 2)
		f.assertRowKept(rows[0].ID)
		f.assertRowKept(rows[1].ID)
		importFollowChecksAssertFollowDelivered(t, deliveries.all(), remote, request)
	})

	t.Run("batch follow skips accounts it may not follow and requests remote accounts", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		victim := f.localAccount("victim", false)
		f.block(victim.ID, f.source.ID)
		carol := f.localAccount("carol", false)
		locked := f.localAccount("locked", true)
		remote := settingsResolveRemoteAccount(t, f.tx, "alice", "remote.example", "https://remote.example/users/alice", time.Now().UTC())

		err := f.server.applyRelationshipBatch(ctx, f.source.ID, []int64{victim.ID, carol.ID, locked.ID, remote.ID}, "follow")
		if !errors.Is(err, errRelationshipFollowNotPermitted) {
			t.Fatalf("batch follow error = %v, want errRelationshipFollowNotPermitted", err)
		}
		f.assertNoFollow(f.source.ID, victim.ID)
		f.assertNoFollowRequest(f.source.ID, victim.ID)
		f.follow(f.source.ID, carol.ID)
		f.assertNoFollow(f.source.ID, locked.ID)
		f.followRequest(f.source.ID, locked.ID)
		f.assertNoFollow(f.source.ID, remote.ID)
		request := f.followRequest(f.source.ID, remote.ID)
		importFollowChecksAssertFollowDelivered(t, deliveries.all(), remote, request)
	})

	t.Run("batch follow by a silenced account requests unlocked local accounts", func(t *testing.T) {
		f := newImportFollowChecksFixture(t, database, cfg)
		carol := f.localAccount("carol", false)
		if err := f.tx.Model(&models.Account{}).Where("id = ?", f.source.ID).Update("silenced_at", time.Now().UTC()).Error; err != nil {
			t.Fatal(err)
		}

		if err := f.server.applyRelationshipBatch(ctx, f.source.ID, []int64{carol.ID}, "follow"); err != nil {
			t.Fatal(err)
		}
		f.assertNoFollow(f.source.ID, carol.ID)
		f.followRequest(f.source.ID, carol.ID)
	})
}

type importFollowChecksFixture struct {
	t      *testing.T
	tx     *gorm.DB
	server *Server
	source models.Account
}

func newImportFollowChecksFixture(t *testing.T, database *gorm.DB, cfg config.Config) *importFollowChecksFixture {
	t.Helper()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	f := &importFollowChecksFixture{t: t, tx: tx, server: &Server{cfg: cfg, db: tx}}
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	f.source = models.Account{Username: "bob", PrivateKey: sql.NullString{String: privateKey, Valid: true}, PublicKey: publicKey, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&f.source).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.User{AccountID: f.source.ID, Email: "bob@paon.example", EncryptedPassword: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *importFollowChecksFixture) localAccount(username string, locked bool) models.Account {
	f.t.Helper()
	now := time.Now().UTC()
	account := models.Account{Username: username, Locked: locked, CreatedAt: now, UpdatedAt: now}
	if err := f.tx.Create(&account).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.tx.Create(&models.User{AccountID: account.ID, Email: username + "@paon.example", EncryptedPassword: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		f.t.Fatal(err)
	}
	return account
}

func (f *importFollowChecksFixture) block(accountID, targetAccountID int64) {
	f.t.Helper()
	now := time.Now().UTC()
	if err := f.tx.Create(&models.Block{AccountID: accountID, TargetAccountID: targetAccountID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *importFollowChecksFixture) bulkImport(typeName string, rows ...map[string]any) (models.BulkImport, []models.BulkImportRow) {
	f.t.Helper()
	now := time.Now().UTC()
	bulkImport := models.BulkImport{Type: importTypeByName[typeName], State: bulkImportStateInProgress, TotalItems: len(rows), AccountID: f.source.ID, OriginalFilename: typeName + ".csv", CreatedAt: now, UpdatedAt: now}
	if err := f.tx.Create(&bulkImport).Error; err != nil {
		f.t.Fatal(err)
	}
	created := make([]models.BulkImportRow, 0, len(rows))
	for _, data := range rows {
		raw, err := json.Marshal(data)
		if err != nil {
			f.t.Fatal(err)
		}
		row := models.BulkImportRow{BulkImportID: bulkImport.ID, Data: models.JSONValue(raw), CreatedAt: now, UpdatedAt: now}
		if err := f.tx.Create(&row).Error; err != nil {
			f.t.Fatal(err)
		}
		created = append(created, row)
	}
	return bulkImport, created
}

func (f *importFollowChecksFixture) processRows(ctx context.Context, rows []models.BulkImportRow) {
	f.t.Helper()
	// Without an asynq client this is the synchronous per-row fallback.
	if err := f.server.enqueueOrProcessImportRows(ctx, rows); err != nil {
		f.t.Fatal(err)
	}
}

func (f *importFollowChecksFixture) follow(accountID, targetAccountID int64) models.Follow {
	f.t.Helper()
	var follow models.Follow
	if err := f.tx.Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).First(&follow).Error; err != nil {
		f.t.Fatalf("follow %d -> %d: %v", accountID, targetAccountID, err)
	}
	return follow
}

func (f *importFollowChecksFixture) followRequest(accountID, targetAccountID int64) models.FollowRequest {
	f.t.Helper()
	var request models.FollowRequest
	if err := f.tx.Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).First(&request).Error; err != nil {
		f.t.Fatalf("follow request %d -> %d: %v", accountID, targetAccountID, err)
	}
	return request
}

func (f *importFollowChecksFixture) assertNoFollow(accountID, targetAccountID int64) {
	f.t.Helper()
	var count int64
	if err := f.tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		f.t.Fatal(err)
	}
	if count != 0 {
		f.t.Fatalf("follow %d -> %d exists", accountID, targetAccountID)
	}
}

func (f *importFollowChecksFixture) assertNoFollowRequest(accountID, targetAccountID int64) {
	f.t.Helper()
	var count int64
	if err := f.tx.Model(&models.FollowRequest{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		f.t.Fatal(err)
	}
	if count != 0 {
		f.t.Fatalf("follow request %d -> %d exists", accountID, targetAccountID)
	}
}

func (f *importFollowChecksFixture) assertProgress(bulkImportID int64, processed, imported int) {
	f.t.Helper()
	var bulkImport models.BulkImport
	if err := f.tx.First(&bulkImport, bulkImportID).Error; err != nil {
		f.t.Fatal(err)
	}
	if bulkImport.ProcessedItems != processed || bulkImport.ImportedItems != imported {
		f.t.Fatalf("bulk import progress = processed %d / imported %d, want %d / %d", bulkImport.ProcessedItems, bulkImport.ImportedItems, processed, imported)
	}
	if bulkImport.State != bulkImportStateFinished {
		f.t.Fatalf("bulk import state = %d, want finished", bulkImport.State)
	}
}

func (f *importFollowChecksFixture) assertRowKept(rowID int64) {
	f.t.Helper()
	if err := f.tx.First(&models.BulkImportRow{}, rowID).Error; err != nil {
		f.t.Fatalf("failed row %d must be kept for the failures CSV: %v", rowID, err)
	}
}

func (f *importFollowChecksFixture) assertRowDeleted(rowID int64) {
	f.t.Helper()
	if err := f.tx.First(&models.BulkImportRow{}, rowID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		f.t.Fatalf("imported row %d lookup = %v, want deleted", rowID, err)
	}
}

func (f *importFollowChecksFixture) assertNotification(accountID int64, activityType string, activityID int64, kind string) {
	f.t.Helper()
	var notification models.Notification
	if err := f.tx.Where("account_id = ? AND activity_type = ? AND activity_id = ?", accountID, activityType, activityID).First(&notification).Error; err != nil {
		f.t.Fatalf("%s notification for %d: %v", kind, accountID, err)
	}
	if string(notification.Type) != kind || notification.FromAccountID != f.source.ID {
		f.t.Fatalf("notification = %+v, want %s from %d", notification, kind, f.source.ID)
	}
}

func (f *importFollowChecksFixture) list(title string) models.List {
	f.t.Helper()
	var list models.List
	if err := f.tx.Where("account_id = ? AND title = ?", f.source.ID, title).First(&list).Error; err != nil {
		f.t.Fatalf("list %q: %v", title, err)
	}
	return list
}

func (f *importFollowChecksFixture) assertListed(listID, accountID int64, followID, followRequestID sql.NullInt64) {
	f.t.Helper()
	var row models.ListAccount
	if err := f.tx.Where("list_id = ? AND account_id = ?", listID, accountID).First(&row).Error; err != nil {
		f.t.Fatalf("list membership for %d: %v", accountID, err)
	}
	if row.FollowID != followID || row.FollowRequestID != followRequestID {
		f.t.Fatalf("list membership = follow %v / request %v, want %v / %v", row.FollowID, row.FollowRequestID, followID, followRequestID)
	}
}

func (f *importFollowChecksFixture) assertNotListed(listID, accountID int64) {
	f.t.Helper()
	var count int64
	if err := f.tx.Model(&models.ListAccount{}).Where("list_id = ? AND account_id = ?", listID, accountID).Count(&count).Error; err != nil {
		f.t.Fatal(err)
	}
	if count != 0 {
		f.t.Fatalf("account %d was added to list %d", accountID, listID)
	}
}

func importFollowChecksRow(acct string, options map[string]any) map[string]any {
	row := map[string]any{"acct": acct}
	for key, value := range options {
		row[key] = value
	}
	return row
}

func importFollowChecksAssertOptions(t *testing.T, showReblogs, notify bool, languages models.StringArray, wantShowReblogs, wantNotify bool, wantLanguages string) {
	t.Helper()
	if showReblogs != wantShowReblogs || notify != wantNotify || strings.Join(languages, ",") != wantLanguages {
		t.Fatalf("follow options = show_reblogs %v notify %v languages %v, want %v %v %q", showReblogs, notify, languages, wantShowReblogs, wantNotify, wantLanguages)
	}
}

func importFollowChecksAssertFollowDelivered(t *testing.T, got []accountMigrationDelivery, remote models.Account, request models.FollowRequest) {
	t.Helper()
	if len(got) != 1 || got[0].url != remote.InboxURL {
		t.Fatalf("deliveries = %+v, want one Follow to %s", got, remote.InboxURL)
	}
	var activity struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Object string `json:"object"`
	}
	if err := json.Unmarshal(got[0].body, &activity); err != nil {
		t.Fatalf("follow body %s: %v", got[0].body, err)
	}
	if activity.Type != "Follow" || activity.ID != string(request.URI) || activity.Object != remote.URI {
		t.Fatalf("follow activity = %s, want Follow %s of %s", got[0].body, request.URI, remote.URI)
	}
}
