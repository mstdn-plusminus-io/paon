//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestAccountMigrationUnfollowFollowIntegration(t *testing.T) {
	databaseURL, redisURL := os.Getenv("PAON_TEST_DATABASE_URL"), os.Getenv("PAON_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL and PAON_TEST_REDIS_URL are required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example",
		SecretKeyBase: "account-migration-follow-integration-secret",
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

	t.Run("direct follow to a local target notifies, merges and undoes the old remote follow", func(t *testing.T) {
		f := newAccountMigrationFollowFixture(t, database, cfg)
		asynqCfg := cfg
		asynqCfg.RedisURL = redisURL
		asynqCfg.RedisNamespace = "account-migration-follow-" + randomHex(6)
		f.server.cfg = asynqCfg
		f.server.asynqClient = asynq.NewClient(asynqRedisOpt(asynqCfg))
		inspector := asynq.NewInspector(asynqRedisOpt(asynqCfg))
		t.Cleanup(func() {
			for _, queue := range []string{asynqQueueDefault, asynqQueuePush, asynqQueueMailers} {
				_ = inspector.DeleteQueue(f.server.asynqQueue(queue), true)
			}
			_ = inspector.Close()
			_ = f.server.asynqClient.Close()
		})
		deliveries := accountMigrationMockDeliveries(t)
		// The local target is locked, but Move bypasses it (bypass_locked: target.local?).
		target := f.localAccount("alice2", true)

		if err := f.server.performUnfollowFollowMigration(ctx, f.follower.ID, f.oldRemote.ID, target.ID, true); err != nil {
			t.Fatal(err)
		}
		var follow models.Follow
		if err := f.tx.Where("account_id = ? AND target_account_id = ?", f.follower.ID, target.ID).First(&follow).Error; err != nil {
			t.Fatalf("migrated follow: %v", err)
		}
		if follow.ShowReblogs || !follow.Notify || strings.Join(follow.Languages, ",") != "ja" {
			t.Fatalf("migrated follow options = %+v", follow)
		}
		f.assertNoFollow(f.follower.ID, f.oldRemote.ID)
		f.assertListMember(target.ID, sql.NullInt64{Int64: follow.ID, Valid: true}, sql.NullInt64{})

		merge := accountMigrationPendingTask(t, inspector, f.server.asynqQueue(asynqQueueDefault), asynqTaskMerge)
		var mergePayload asynqRelationshipPayload
		if err := json.Unmarshal(merge.Payload, &mergePayload); err != nil {
			t.Fatal(err)
		}
		if mergePayload.AccountID != target.ID || mergePayload.TargetAccountID != f.follower.ID {
			t.Fatalf("merge payload = %+v, want from %d into %d", mergePayload, target.ID, f.follower.ID)
		}
		notification := accountMigrationPendingTask(t, inspector, f.server.asynqQueue(asynqQueueDefault), asynqTaskLocalNotification)
		var notificationPayload asynqLocalNotificationPayload
		if err := json.Unmarshal(notification.Payload, &notificationPayload); err != nil {
			t.Fatal(err)
		}
		if notificationPayload != (asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: f.follower.ID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"}) {
			t.Fatalf("notification payload = %+v", notificationPayload)
		}
		delivery := accountMigrationPendingTask(t, inspector, f.server.asynqQueue(asynqQueuePush), asynqTaskActivityPubDelivery)
		var job activityPubDeliveryRetryJob
		if err := json.Unmarshal(delivery.Payload, &job); err != nil {
			t.Fatal(err)
		}
		if job.InboxURL != f.oldRemote.InboxURL || job.SourceAccountID != f.follower.ID {
			t.Fatalf("delivery job = %+v", job)
		}
		accountMigrationAssertUndoFollow(t, job.Body, f.originalFollow)
		if got := deliveries.all(); len(got) != 0 {
			t.Fatalf("asynq-backed deliveries must not be sent inline: %+v", got)
		}
	})

	t.Run("silenced follower requests the local target with a follow_request notification and undoes the old follow", func(t *testing.T) {
		f := newAccountMigrationFollowFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		target := f.localAccount("alice2", false)
		if err := f.tx.Model(&models.Account{}).Where("id = ?", f.follower.ID).Update("silenced_at", time.Now().UTC()).Error; err != nil {
			t.Fatal(err)
		}
		// NotifyService drops notifications from silenced accounts the recipient does not follow.
		now := time.Now().UTC()
		if err := f.tx.Create(&models.Follow{AccountID: target.ID, TargetAccountID: f.follower.ID, ShowReblogs: true, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}

		if err := f.server.performUnfollowFollowMigration(ctx, f.follower.ID, f.oldRemote.ID, target.ID, true); err != nil {
			t.Fatal(err)
		}
		var request models.FollowRequest
		if err := f.tx.Where("account_id = ? AND target_account_id = ?", f.follower.ID, target.ID).First(&request).Error; err != nil {
			t.Fatalf("migrated follow request: %v", err)
		}
		f.assertNoFollow(f.follower.ID, target.ID)
		f.assertNoFollow(f.follower.ID, f.oldRemote.ID)
		f.assertListMember(target.ID, sql.NullInt64{}, sql.NullInt64{Int64: request.ID, Valid: true})
		var notification models.Notification
		if err := f.tx.Where("account_id = ? AND activity_type = ? AND activity_id = ?", target.ID, "FollowRequest", request.ID).First(&notification).Error; err != nil {
			t.Fatalf("follow_request notification: %v", err)
		}
		if string(notification.Type) != "follow_request" || notification.FromAccountID != f.follower.ID {
			t.Fatalf("notification = %+v", notification)
		}
		got := deliveries.all()
		if len(got) != 1 || got[0].url != f.oldRemote.InboxURL {
			t.Fatalf("deliveries = %+v, want one Undo to %s", got, f.oldRemote.InboxURL)
		}
		accountMigrationAssertUndoFollow(t, got[0].body, f.originalFollow)
	})

	t.Run("an existing follow takes the original options and keeps the old follow", func(t *testing.T) {
		f := newAccountMigrationFollowFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		target := f.localAccount("alice2", false)
		if err := f.tx.Model(&models.Follow{}).Where("id = ?", f.originalFollow.ID).Update("languages", nil).Error; err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		existing := models.Follow{AccountID: f.follower.ID, TargetAccountID: target.ID, ShowReblogs: true, Notify: false, Languages: models.StringArray{"en"}, CreatedAt: now, UpdatedAt: now}
		if err := f.tx.Create(&existing).Error; err != nil {
			t.Fatal(err)
		}

		if err := f.server.performUnfollowFollowMigration(ctx, f.follower.ID, f.oldRemote.ID, target.ID, true); err != nil {
			t.Fatal(err)
		}
		var reloaded models.Follow
		if err := f.tx.First(&reloaded, existing.ID).Error; err != nil {
			t.Fatal(err)
		}
		if reloaded.ShowReblogs || !reloaded.Notify || strings.Join(reloaded.Languages, ",") != "en" {
			t.Fatalf("existing follow = %+v, want original show_reblogs/notify and untouched languages", reloaded)
		}
		f.assertListMember(target.ID, sql.NullInt64{Int64: existing.ID, Valid: true}, sql.NullInt64{})
		f.assertFollowExists(f.follower.ID, f.oldRemote.ID)
		f.assertNoNotifications(target.ID)
		if got := deliveries.all(); len(got) != 0 {
			t.Fatalf("existing follow must not deliver: %+v", got)
		}
	})

	t.Run("a pending follow request to the target is updated instead of following directly", func(t *testing.T) {
		f := newAccountMigrationFollowFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		target := f.localAccount("alice2", true)
		now := time.Now().UTC()
		pending := models.FollowRequest{AccountID: f.follower.ID, TargetAccountID: target.ID, ShowReblogs: true, Notify: false, CreatedAt: now, UpdatedAt: now}
		if err := f.tx.Create(&pending).Error; err != nil {
			t.Fatal(err)
		}

		if err := f.server.performUnfollowFollowMigration(ctx, f.follower.ID, f.oldRemote.ID, target.ID, true); err != nil {
			t.Fatal(err)
		}
		f.assertNoFollow(f.follower.ID, target.ID)
		var reloaded models.FollowRequest
		if err := f.tx.First(&reloaded, pending.ID).Error; err != nil {
			t.Fatalf("pending follow request: %v", err)
		}
		if reloaded.ShowReblogs || !reloaded.Notify || strings.Join(reloaded.Languages, ",") != "ja" {
			t.Fatalf("pending follow request = %+v, want original options", reloaded)
		}
		f.assertListMember(target.ID, sql.NullInt64{}, sql.NullInt64{Int64: pending.ID, Valid: true})
		f.assertFollowExists(f.follower.ID, f.oldRemote.ID)
		f.assertNoNotifications(target.ID)
		if got := deliveries.all(); len(got) != 0 {
			t.Fatalf("pending follow request must not deliver: %+v", got)
		}
	})

	t.Run("a remote target receives the Follow before the old remote follow is undone", func(t *testing.T) {
		f := newAccountMigrationFollowFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		target := settingsResolveRemoteAccount(t, f.tx, "alice", "new.example", "https://new.example/users/alice", time.Now().UTC())

		if err := f.server.performUnfollowFollowMigration(ctx, f.follower.ID, f.oldRemote.ID, target.ID, false); err != nil {
			t.Fatal(err)
		}
		var request models.FollowRequest
		if err := f.tx.Where("account_id = ? AND target_account_id = ?", f.follower.ID, target.ID).First(&request).Error; err != nil {
			t.Fatalf("migrated follow request: %v", err)
		}
		f.assertNoFollow(f.follower.ID, f.oldRemote.ID)
		got := deliveries.all()
		if len(got) != 2 || got[0].url != target.InboxURL || got[1].url != f.oldRemote.InboxURL {
			t.Fatalf("deliveries = %+v, want Follow to new inbox then Undo to old inbox", got)
		}
		if !strings.Contains(string(got[0].body), `"type":"Follow"`) || !strings.Contains(string(got[0].body), string(request.URI)) {
			t.Fatalf("Follow body = %s", got[0].body)
		}
		accountMigrationAssertUndoFollow(t, got[1].body, f.originalFollow)
	})

	t.Run("migrated follow delivery completion undoes a pending request to the old remote target", func(t *testing.T) {
		f := newAccountMigrationFollowFixture(t, database, cfg)
		deliveries := accountMigrationMockDeliveries(t)
		if err := f.tx.Delete(&models.Follow{}, f.originalFollow.ID).Error; err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		request := models.FollowRequest{AccountID: f.follower.ID, TargetAccountID: f.oldRemote.ID, ShowReblogs: true, URI: models.NullSafeString("https://paon.example/old-request"), CreatedAt: now, UpdatedAt: now}
		if err := f.tx.Create(&request).Error; err != nil {
			t.Fatal(err)
		}

		if err := f.server.afterActivityPubDeliveryRetrySuccess(ctx, activityPubDeliveryRetryJob{SourceAccountID: f.follower.ID, MigratedFollowOldTargetAccountID: f.oldRemote.ID}); err != nil {
			t.Fatal(err)
		}
		if err := f.tx.First(&models.FollowRequest{}, request.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("old follow request lookup = %v, want deleted", err)
		}
		got := deliveries.all()
		if len(got) != 1 || got[0].url != f.oldRemote.InboxURL {
			t.Fatalf("deliveries = %+v, want one Undo to %s", got, f.oldRemote.InboxURL)
		}
		accountMigrationAssertUndoFollow(t, got[0].body, models.Follow{ID: request.ID, URI: request.URI})
	})
}

type accountMigrationFollowFixture struct {
	t              *testing.T
	tx             *gorm.DB
	server         *Server
	follower       models.Account
	oldRemote      models.Account
	originalFollow models.Follow
	listID         int64
}

// newAccountMigrationFollowFixture creates a local follower of a remote old
// account, with the follow listed in one of the follower's lists.
func newAccountMigrationFollowFixture(t *testing.T, database *gorm.DB, cfg config.Config) *accountMigrationFollowFixture {
	t.Helper()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	f := &accountMigrationFollowFixture{t: t, tx: tx, server: &Server{cfg: cfg, db: tx}}
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	f.follower = models.Account{Username: "bob", PrivateKey: sql.NullString{String: privateKey, Valid: true}, PublicKey: publicKey, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&f.follower).Error; err != nil {
		t.Fatal(err)
	}
	f.oldRemote = settingsResolveRemoteAccount(t, tx, "alice", "old.example", "https://old.example/users/alice", now)
	f.originalFollow = models.Follow{
		AccountID: f.follower.ID, TargetAccountID: f.oldRemote.ID, ShowReblogs: false, Notify: true,
		Languages: models.StringArray{"ja"}, URI: models.NullSafeString("https://paon.example/original-follow"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&f.originalFollow).Error; err != nil {
		t.Fatal(err)
	}
	list := models.List{AccountID: f.follower.ID, Title: "friends", CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&list).Error; err != nil {
		t.Fatal(err)
	}
	f.listID = list.ID
	if err := tx.Create(&models.ListAccount{ListID: list.ID, AccountID: f.oldRemote.ID, FollowID: sql.NullInt64{Int64: f.originalFollow.ID, Valid: true}}).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *accountMigrationFollowFixture) localAccount(username string, locked bool) models.Account {
	f.t.Helper()
	now := time.Now().UTC()
	account := models.Account{Username: username, Locked: locked, CreatedAt: now, UpdatedAt: now}
	if err := f.tx.Create(&account).Error; err != nil {
		f.t.Fatal(err)
	}
	user := models.User{AccountID: account.ID, Email: username + "@paon.example", EncryptedPassword: "x", CreatedAt: now, UpdatedAt: now}
	if err := f.tx.Create(&user).Error; err != nil {
		f.t.Fatal(err)
	}
	return account
}

func (f *accountMigrationFollowFixture) assertNoFollow(accountID, targetAccountID int64) {
	f.t.Helper()
	var count int64
	if err := f.tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		f.t.Fatal(err)
	}
	if count != 0 {
		f.t.Fatalf("follow %d -> %d still exists", accountID, targetAccountID)
	}
}

func (f *accountMigrationFollowFixture) assertFollowExists(accountID, targetAccountID int64) {
	f.t.Helper()
	if err := f.tx.Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).First(&models.Follow{}).Error; err != nil {
		f.t.Fatalf("follow %d -> %d: %v", accountID, targetAccountID, err)
	}
}

func (f *accountMigrationFollowFixture) assertListMember(accountID int64, followID, followRequestID sql.NullInt64) {
	f.t.Helper()
	var row models.ListAccount
	if err := f.tx.Where("list_id = ? AND account_id = ?", f.listID, accountID).First(&row).Error; err != nil {
		f.t.Fatalf("list membership for %d: %v", accountID, err)
	}
	if row.FollowID != followID || row.FollowRequestID != followRequestID {
		f.t.Fatalf("list membership = follow %v / request %v, want %v / %v", row.FollowID, row.FollowRequestID, followID, followRequestID)
	}
}

func (f *accountMigrationFollowFixture) assertNoNotifications(accountID int64) {
	f.t.Helper()
	var count int64
	if err := f.tx.Model(&models.Notification{}).Where("account_id = ?", accountID).Count(&count).Error; err != nil {
		f.t.Fatal(err)
	}
	if count != 0 {
		f.t.Fatalf("notifications for %d = %d, want 0", accountID, count)
	}
}

type accountMigrationDelivery struct {
	url  string
	body []byte
}

type accountMigrationDeliveries struct {
	mu   sync.Mutex
	list []accountMigrationDelivery
}

func (d *accountMigrationDeliveries) all() []accountMigrationDelivery {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]accountMigrationDelivery(nil), d.list...)
}

func accountMigrationMockDeliveries(t *testing.T) *accountMigrationDeliveries {
	t.Helper()
	deliveries := &accountMigrationDeliveries{}
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			return textResponse(http.StatusNotFound, "text/plain", "not found"), nil
		}
		body, _ := io.ReadAll(r.Body)
		deliveries.mu.Lock()
		deliveries.list = append(deliveries.list, accountMigrationDelivery{url: r.URL.String(), body: body})
		deliveries.mu.Unlock()
		return textResponse(http.StatusAccepted, "text/plain", ""), nil
	})}
	return deliveries
}

func accountMigrationPendingTask(t *testing.T, inspector *asynq.Inspector, queue string, taskType string) *asynq.TaskInfo {
	t.Helper()
	tasks, err := inspector.ListPendingTasks(queue)
	if err != nil {
		t.Fatalf("list %s: %v", queue, err)
	}
	var found *asynq.TaskInfo
	for _, task := range tasks {
		if task.Type != taskType {
			continue
		}
		if found != nil {
			t.Fatalf("multiple %s tasks in %s", taskType, queue)
		}
		found = task
	}
	if found == nil {
		t.Fatalf("no %s task in %s (%d tasks)", taskType, queue, len(tasks))
	}
	return found
}

func accountMigrationAssertUndoFollow(t *testing.T, body []byte, follow models.Follow) {
	t.Helper()
	var activity struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Actor  string `json:"actor"`
		Object struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Object string `json:"object"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &activity); err != nil {
		t.Fatalf("undo body %s: %v", body, err)
	}
	if activity.Type != "Undo" || activity.Actor != "https://paon.example/users/bob" ||
		activity.ID != "https://paon.example/users/bob#follows/"+strconv.FormatInt(follow.ID, 10)+"/undo" ||
		activity.Object.Type != "Follow" || activity.Object.ID != string(follow.URI) ||
		activity.Object.Object != "https://old.example/users/alice" {
		t.Fatalf("undo activity = %s", body)
	}
}
