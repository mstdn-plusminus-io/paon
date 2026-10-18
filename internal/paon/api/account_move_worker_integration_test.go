//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestAccountMoveWorkerIntegration(t *testing.T) {
	databaseURL, redisURL := os.Getenv("PAON_TEST_DATABASE_URL"), os.Getenv("PAON_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL and PAON_TEST_REDIS_URL are required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example",
		SecretKeyBase: "account-move-worker-integration-secret",
		RedisURL:      redisURL,
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

	t.Run("a remote account moving into a local account carries notes, blocks and mutes like MoveWorker", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		accountMigrationMockDeliveries(t)
		old := settingsResolveRemoteAccount(t, f.tx, "alice", "old.example", "https://old.example/users/alice", time.Now().UTC())
		target := f.local("alice", "")

		carol := f.local("carol", "ja")
		f.note(carol.ID, old.ID, "carol note")
		dave := f.local("dave", "")
		f.note(dave.ID, old.ID, "dave old")
		f.note(dave.ID, target.ID, "dave new")
		erin := f.local("erin", "")
		long := strings.Repeat("x", 1990)
		f.note(erin.ID, old.ID, long)
		frank := f.local("frank", "")
		f.note(frank.ID, old.ID, long)
		f.note(frank.ID, target.ID, "frank new")

		gina := f.local("gina", "")
		f.create(&models.Block{AccountID: gina.ID, TargetAccountID: old.ID, CreatedAt: f.now, UpdatedAt: f.now})
		// The new account already follows gina and has a pending request to her.
		f.create(&models.Follow{AccountID: target.ID, TargetAccountID: gina.ID, ShowReblogs: true, CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.FollowRequest{AccountID: target.ID, TargetAccountID: gina.ID, ShowReblogs: true, CreatedAt: f.now, UpdatedAt: f.now})

		hank := f.local("hank", "ja")
		f.create(&models.Mute{AccountID: hank.ID, TargetAccountID: old.ID, HideNotifications: false, ExpiresAt: sql.NullTime{Time: f.now.Add(24 * time.Hour), Valid: true}, CreatedAt: f.now, UpdatedAt: f.now})
		ivan := f.local("ivan", "")
		f.create(&models.Mute{AccountID: ivan.ID, TargetAccountID: old.ID, HideNotifications: true, CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.Follow{AccountID: ivan.ID, TargetAccountID: target.ID, ShowReblogs: true, CreatedAt: f.now, UpdatedAt: f.now})

		if err := f.server.performMoveWorker(ctx, old.ID, target.ID); err != nil {
			t.Fatal(err)
		}

		if got := f.noteText(carol.ID, target.ID); got != "このユーザーはalice@old.exampleから引っ越しました。これは以前のメモです。\ncarol note" {
			t.Fatalf("carol note = %q", got)
		}
		if got := f.noteText(dave.ID, target.ID); got != "This user moved from alice@old.example, here were your previous notes about them:\ndave old\n\n\ndave new" {
			t.Fatalf("dave merged note = %q", got)
		}
		if got := f.noteText(erin.ID, target.ID); got != long {
			t.Fatalf("erin note = %q..., want the bare 1990-character note", got[:min(len(got), 40)])
		}
		if got := f.noteText(frank.ID, target.ID); got != "frank new" {
			t.Fatalf("frank note = %q, want the over-length merge skipped", got)
		}

		var block models.Block
		if err := f.tx.Where("account_id = ? AND target_account_id = ?", gina.ID, target.ID).First(&block).Error; err != nil {
			t.Fatalf("carried block: %v", err)
		}
		if block.URI == "" {
			t.Fatal("carried block must get an ActivityPub URI")
		}
		f.assertCount(&models.Follow{}, target.ID, gina.ID, 0)
		f.assertCount(&models.FollowRequest{}, target.ID, gina.ID, 0)
		if got := f.noteText(gina.ID, target.ID); got != "This user moved from alice@old.example, which you had blocked." {
			t.Fatalf("gina note = %q", got)
		}

		var mute models.Mute
		if err := f.tx.Where("account_id = ? AND target_account_id = ?", hank.ID, target.ID).First(&mute).Error; err != nil {
			t.Fatalf("carried mute: %v", err)
		}
		if mute.ExpiresAt.Valid || mute.HideNotifications {
			t.Fatalf("carried mute = %+v, want no expiry and hide_notifications=false", mute)
		}
		if got := f.noteText(hank.ID, target.ID); got != "このユーザーは、あなたがミュートしていたalice@old.exampleから引っ越しました。" {
			t.Fatalf("hank note = %q", got)
		}
		// ivan already follows the new account: skip_mute_move? skips both the
		// mute and the note.
		f.assertCount(&models.Mute{}, ivan.ID, target.ID, 0)
		f.assertCount(&models.AccountNote{}, ivan.ID, target.ID, 0)

		unmerge := accountMigrationPendingTask(t, f.inspector, f.server.asynqQueue(asynqQueuePull), asynqTaskUnmerge)
		var unmergePayload asynqRelationshipPayload
		if err := json.Unmarshal(unmerge.Payload, &unmergePayload); err != nil {
			t.Fatal(err)
		}
		if unmergePayload != (asynqRelationshipPayload{AccountID: gina.ID, TargetAccountID: target.ID, FeedType: "home"}) {
			t.Fatalf("unmerge payload = %+v, want gina's statuses out of the new account's home", unmergePayload)
		}
	})

	t.Run("a local account moving to a remote account delivers the carried Block", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		accountMigrationMockDeliveries(t)
		source := f.local("bob", "")
		target := settingsResolveRemoteAccount(t, f.tx, "bob", "new.example", "https://new.example/users/bob", time.Now().UTC())
		gina := f.local("gina", "")
		f.create(&models.Block{AccountID: gina.ID, TargetAccountID: source.ID, CreatedAt: f.now, UpdatedAt: f.now})

		if err := f.server.performMoveWorker(ctx, source.ID, target.ID); err != nil {
			t.Fatal(err)
		}
		var block models.Block
		if err := f.tx.Where("account_id = ? AND target_account_id = ?", gina.ID, target.ID).First(&block).Error; err != nil {
			t.Fatalf("carried block: %v", err)
		}
		delivery := accountMigrationPendingTask(t, f.inspector, f.server.asynqQueue(asynqQueuePush), asynqTaskActivityPubDelivery)
		var job activityPubDeliveryRetryJob
		if err := json.Unmarshal(delivery.Payload, &job); err != nil {
			t.Fatal(err)
		}
		var activity struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Actor  string `json:"actor"`
			Object string `json:"object"`
		}
		if err := json.Unmarshal(job.Body, &activity); err != nil {
			t.Fatal(err)
		}
		if job.InboxURL != target.InboxURL || job.SourceAccountID != gina.ID || activity.Type != "Block" ||
			activity.ID != string(block.URI) || activity.Object != target.URI {
			t.Fatalf("delivery = %+v body=%s", job, job.Body)
		}
	})

	t.Run("a local to local move authorizes pending requests from local and remote followers", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		accountMigrationMockDeliveries(t)
		source := f.local("bob", "")
		target := f.local("bob2", "")
		if err := f.tx.Model(&models.Account{}).Where("id = ?", target.ID).Update("locked", true).Error; err != nil {
			t.Fatal(err)
		}
		carol := f.local("carol", "")
		remote := settingsResolveRemoteAccount(t, f.tx, "dan", "remote.example", "https://remote.example/users/dan", time.Now().UTC())
		for _, follower := range []models.Account{carol, remote} {
			f.create(&models.Follow{AccountID: follower.ID, TargetAccountID: source.ID, ShowReblogs: true, CreatedAt: f.now, UpdatedAt: f.now})
			f.create(&models.FollowRequest{AccountID: follower.ID, TargetAccountID: target.ID, ShowReblogs: false, Notify: true, CreatedAt: f.now, UpdatedAt: f.now})
		}

		if err := f.server.performMoveWorker(ctx, source.ID, target.ID); err != nil {
			t.Fatal(err)
		}
		for _, follower := range []models.Account{carol, remote} {
			var follow models.Follow
			if err := f.tx.Where("account_id = ? AND target_account_id = ?", follower.ID, target.ID).First(&follow).Error; err != nil {
				t.Fatalf("%s follow: %v", follower.Username, err)
			}
			if follow.ShowReblogs || !follow.Notify {
				t.Fatalf("%s follow = %+v, want the request's options", follower.Username, follow)
			}
			f.assertCount(&models.FollowRequest{}, follower.ID, target.ID, 0)
		}
		merge := accountMigrationPendingTask(t, f.inspector, f.server.asynqQueue(asynqQueueDefault), asynqTaskMerge)
		var mergePayload asynqRelationshipPayload
		if err := json.Unmarshal(merge.Payload, &mergePayload); err != nil {
			t.Fatal(err)
		}
		if mergePayload != (asynqRelationshipPayload{AccountID: target.ID, TargetAccountID: carol.ID}) {
			t.Fatalf("merge payload = %+v, want the new account merged into carol's home only", mergePayload)
		}
	})

	t.Run("local relationship migration runs in the move task", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		old := settingsResolveRemoteAccount(t, f.tx, "alice", "old.example", "https://old.example/users/alice", time.Now().UTC())
		target := f.local("alice", "")
		carol := f.local("carol", "")
		f.note(carol.ID, old.ID, "carol note")

		if err := f.server.processLocalAccountMigration(old.ID, target.ID); err != nil {
			t.Fatal(err)
		}
		f.assertCount(&models.AccountNote{}, carol.ID, target.ID, 0)
		task := f.moveTask(old.ID, target.ID)
		if err := f.server.handleAsynqMove(ctx, asynq.NewTask(task.Type, task.Payload)); err != nil {
			t.Fatal(err)
		}
		f.assertCount(&models.AccountNote{}, carol.ID, target.ID, 1)
	})

	t.Run("settings migration enqueues the move task beside the Move distribution", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		accountMigrationMockDeliveries(t)
		source := f.local("bob", "")
		target := f.local("bob2", "")
		if err := f.tx.Model(&models.Account{}).Where("id = ?", target.ID).Update("also_known_as", models.StringArray{activityPubActorURL(f.server, source)}).Error; err != nil {
			t.Fatal(err)
		}
		carol := f.local("carol", "")
		f.note(carol.ID, source.ID, "carol note")
		var user models.User
		if err := f.tx.Where("account_id = ?", source.ID).First(&user).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.tx.Model(&user).Update("encrypted_password", "").Error; err != nil {
			t.Fatal(err)
		}
		token := "move-worker-" + randomHex(8)
		f.create(&models.OAuthAccessToken{Token: token, ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true}, Scopes: "read write", CreatedAt: f.now})

		form := url.Values{"account_migration[acct]": {"bob2"}, "account_migration[current_username]": {"bob"}}
		req := httptest.NewRequest(http.MethodPost, "/settings/migration", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		if err := f.server.createSettingsMigration(echo.NewContext(req, rec, echo.New())); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		f.moveTask(source.ID, target.ID)
		accountMigrationPendingTask(t, f.inspector, f.server.asynqQueue(asynqQueuePush), asynqTaskMoveDistribution)
		// MoveWorker has not run yet: the note is copied by the move task.
		f.assertCount(&models.AccountNote{}, carol.ID, target.ID, 0)
	})

	t.Run("inbound Move", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		meili := accountMoveWorkerMockMeili(t)
		f.server.cfg.MeiliEnabled = true
		f.server.cfg.MeiliHost = "http://meili.test"
		old := settingsResolveRemoteAccount(t, f.tx, "alice", "old.example", "https://old.example/users/alice", time.Now().UTC())
		target := f.local("alice", "")
		targetURI := activityPubActorURL(f.server, target)
		if err := f.tx.Model(&models.Account{}).Where("id = ?", target.ID).Update("also_known_as", models.StringArray{old.URI}).Error; err != nil {
			t.Fatal(err)
		}
		noAlias := f.local("alice3", "")
		move := func(object string, target string) activityPayload {
			t.Helper()
			payload, err := parseActivityPayload([]byte(`{"type":"Move","actor":"` + old.URI + `","object":"` + object + `","target":"` + target + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			return payload
		}
		processing := func() bool {
			t.Helper()
			marked, err := f.server.markActivityPubMoveAsProcessing(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			if marked {
				f.server.unmarkActivityPubMoveAsProcessing(ctx, old.ID)
			}
			return !marked
		}
		pendingMoves := func() int {
			t.Helper()
			tasks, err := f.inspector.ListPendingTasks(f.server.asynqQueue(asynqQueueDefault))
			if err != nil {
				return 0
			}
			n := 0
			for _, task := range tasks {
				if task.Type == asynqTaskMove {
					n++
				}
			}
			return n
		}

		// Not-applied Moves return nil (no ingress retry) and release the marker.
		if err := f.server.processActivityPubMove(move("https://old.example/users/someone-else", targetURI), &old); err != nil {
			t.Fatalf("object mismatch = %v, want nil", err)
		}
		if err := f.server.processActivityPubMove(move(old.URI, activityPubActorURL(f.server, noAlias)), &old); err != nil {
			t.Fatalf("missing alsoKnownAs = %v, want nil", err)
		}
		if processing() {
			t.Fatal("missing alsoKnownAs must release the move_in_progress marker")
		}
		settingsResolveMockRemote(t, nil)
		if err := f.server.processActivityPubMove(move(old.URI, "https://new.example/users/alice"), &old); err != nil {
			t.Fatalf("404 target = %v, want nil", err)
		}
		if processing() {
			t.Fatal("an unavailable target must release the move_in_progress marker")
		}
		accountMoveWorkerFailingRemote(t)
		if err := f.server.processActivityPubMove(move(old.URI, "https://new.example/users/alice"), &old); err == nil || errors.Is(err, errActivityPubEventNotApplied) {
			t.Fatalf("network failure = %v, want a retryable error", err)
		}
		if processing() {
			t.Fatal("a retryable failure must release the move_in_progress marker")
		}
		if pendingMoves() != 0 || meili.count() != 0 {
			t.Fatalf("not-applied Moves enqueued %d move tasks and %d Meilisearch requests", pendingMoves(), meili.count())
		}

		if err := f.server.processActivityPubMove(move(old.URI, targetURI), &old); err != nil {
			t.Fatal(err)
		}
		var reloaded models.Account
		if err := f.tx.First(&reloaded, old.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reloaded.MovedToAccountID.Valid || reloaded.MovedToAccountID.Int64 != target.ID {
			t.Fatalf("moved_to_account_id = %v, want %d", reloaded.MovedToAccountID, target.ID)
		}
		f.moveTask(old.ID, target.ID)
		if !processing() {
			t.Fatal("an applied Move must keep the move_in_progress marker")
		}
		if got := meili.all(); len(got) != 1 || got[0] != "DELETE /indexes/accounts/documents/"+intString(old.ID) {
			t.Fatalf("meilisearch requests = %v, want the moved account removed from the index", got)
		}

		// A duplicate delivery of the same Move is ignored without a retry.
		if err := f.server.processActivityPubMove(move(old.URI, targetURI), &reloaded); err != nil {
			t.Fatalf("duplicate Move = %v, want nil", err)
		}
		if pendingMoves() != 1 {
			t.Fatalf("move tasks = %d, want 1", pendingMoves())
		}
	})

	t.Run("a failed follower migration does not fail the move task", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		accountMigrationMockDeliveries(t)
		old := settingsResolveRemoteAccount(t, f.tx, "alice", "old.example", "https://old.example/users/alice", time.Now().UTC())
		target := f.local("alice", "")
		carol := f.local("carol", "")
		f.create(&models.Follow{AccountID: carol.ID, TargetAccountID: old.ID, ShowReblogs: true, CreatedAt: f.now, UpdatedAt: f.now})
		f.note(carol.ID, old.ID, "carol note")
		// The inline UnfollowFollowWorker fallback fails on the new follow.
		if err := f.tx.Exec(`CREATE FUNCTION paon_test_reject_follow() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'follow rejected'; END $$`).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.tx.Exec(`CREATE TRIGGER paon_test_reject_follow BEFORE INSERT ON follows FOR EACH ROW EXECUTE FUNCTION paon_test_reject_follow()`).Error; err != nil {
			t.Fatal(err)
		}
		client := f.server.asynqClient
		f.server.asynqClient = nil
		t.Cleanup(func() { f.server.asynqClient = client })

		// MoveWorker#perform clears @deferred_error after queue_follow_unfollows!, so
		// the move is not retried and the note is not prefixed again.
		if err := f.server.performMoveWorker(ctx, old.ID, target.ID); err != nil {
			t.Fatalf("move = %v, want follower migration failures dropped", err)
		}
		f.assertCount(&models.Follow{}, carol.ID, target.ID, 0)
		f.assertCount(&models.Follow{}, carol.ID, old.ID, 1)
		if got := f.noteText(carol.ID, target.ID); got != "This user moved from alice@old.example, here were your previous notes about them:\ncarol note" {
			t.Fatalf("carol note = %q", got)
		}
	})

	t.Run("the old target unfollow waits for the relationship lock", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		accountMigrationMockDeliveries(t)
		old := settingsResolveRemoteAccount(t, f.tx, "alice", "old.example", "https://old.example/users/alice", time.Now().UTC())
		bob := f.local("bob", "")
		f.create(&models.Follow{AccountID: bob.ID, TargetAccountID: old.ID, ShowReblogs: true, URI: models.NullSafeString("https://paon.example/follows/1"), CreatedAt: f.now, UpdatedAt: f.now})
		deliveries := func() int {
			t.Helper()
			tasks, err := f.inspector.ListPendingTasks(f.server.asynqQueue(asynqQueuePush))
			if err != nil {
				return 0
			}
			return len(tasks)
		}

		acquired, release, err := f.server.acquireActivityPubRedisLock(ctx, relationshipRedisLockName(old.ID, bob.ID), time.Minute)
		if err != nil || !acquired {
			t.Fatalf("lock = %v, %v", acquired, err)
		}
		if err := f.server.unfollowAccountMigrationOldTarget(ctx, bob, old); err == nil {
			t.Fatal("a held relationship lock must make the unfollow retry")
		}
		f.assertCount(&models.Follow{}, bob.ID, old.ID, 1)
		if deliveries() != 0 {
			t.Fatalf("deliveries = %d, want no Undo while the lock is held", deliveries())
		}
		release()
		if err := f.server.unfollowAccountMigrationOldTarget(ctx, bob, old); err != nil {
			t.Fatal(err)
		}
		f.assertCount(&models.Follow{}, bob.ID, old.ID, 0)
		if deliveries() != 1 {
			t.Fatalf("deliveries = %d, want one Undo", deliveries())
		}
	})

	t.Run("migration fan-out reindexes private statuses once per account in a deferred task", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		accountMigrationMockDeliveries(t)
		meili := accountMoveWorkerMockMeili(t)
		f.server.cfg.MeiliEnabled = true
		f.server.cfg.MeiliHost = "http://meili.test"
		old := settingsResolveRemoteAccount(t, f.tx, "alice", "old.example", "https://old.example/users/alice", time.Now().UTC())
		target := f.local("alice", "")
		f.create(&models.Status{Text: "followers only", AccountID: target.ID, Visibility: 2, CreatedAt: f.now, UpdatedAt: f.now})
		gina := f.local("gina", "")
		hank := f.local("hank", "")
		started := time.Now().UTC()

		for _, name := range []string{"carol", "dave", "erin"} {
			follower := f.local(name, "")
			f.create(&models.Follow{AccountID: follower.ID, TargetAccountID: old.ID, ShowReblogs: true, CreatedAt: f.now, UpdatedAt: f.now})
			if err := f.server.performUnfollowFollowMigration(ctx, follower.ID, old.ID, target.ID, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, blocker := range []models.Account{gina, hank} {
			f.create(&models.Block{AccountID: blocker.ID, TargetAccountID: old.ID, CreatedAt: f.now, UpdatedAt: f.now})
		}
		if err := f.server.carryAccountMigrationRelationships(old, target); err != nil {
			t.Fatal(err)
		}
		windows := 1
		if time.Now().UTC().Truncate(meiliPrivateReindexWindow) != started.Truncate(meiliPrivateReindexWindow) {
			windows = 2
		}

		for _, request := range meili.all() {
			if strings.Contains(request, "/indexes/statuses/") {
				t.Fatalf("meilisearch requests = %v, want no inline status reindex", meili.all())
			}
		}
		tasks, err := f.inspector.ListScheduledTasks(f.server.asynqQueue(asynqQueuePull))
		if err != nil {
			t.Fatal(err)
		}
		counts := map[int64]int{}
		for _, task := range tasks {
			if task.Type != asynqTaskMeiliPrivateReindex {
				continue
			}
			var payload asynqAccountPayload
			if err := json.Unmarshal(task.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			counts[payload.AccountID]++
		}
		for _, id := range []int64{target.ID, old.ID, gina.ID, hank.ID} {
			if counts[id] < 1 || counts[id] > windows {
				t.Fatalf("reindex tasks = %v, want one per account and window for %d", counts, id)
			}
		}
		if len(counts) != 4 {
			t.Fatalf("reindex tasks = %v, want only the new, old and blocking accounts", counts)
		}
	})

	t.Run("deferred private reindex tasks are shared within a window", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		meili := accountMoveWorkerMockMeili(t)
		f.server.cfg.MeiliEnabled = true
		f.server.cfg.MeiliHost = "http://meili.test"
		target := f.local("alice", "")
		status := models.Status{Text: "followers only", AccountID: target.ID, Visibility: 2, CreatedAt: f.now, UpdatedAt: f.now}
		f.create(&status)
		// A future window keeps the tasks scheduled for inspection.
		window := time.Now().UTC().Add(time.Hour).Truncate(meiliPrivateReindexWindow)

		for _, now := range []time.Time{window, window.Add(30 * time.Second), window.Add(meiliPrivateReindexWindow)} {
			if !f.server.enqueueMeiliPrivateReindexTask(ctx, target.ID, now) {
				t.Fatalf("enqueue at %s failed", now)
			}
		}
		tasks, err := f.inspector.ListScheduledTasks(f.server.asynqQueue(asynqQueuePull))
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 2 {
			t.Fatalf("scheduled tasks = %d, want one per window", len(tasks))
		}
		for _, task := range tasks {
			if task.NextProcessAt.Before(window.Add(meiliPrivateReindexWindow)) {
				t.Fatalf("task runs at %s, want after its window closes", task.NextProcessAt)
			}
		}

		if err := f.server.handleAsynqMeiliPrivateReindex(ctx, asynq.NewTask(tasks[0].Type, tasks[0].Payload)); err != nil {
			t.Fatal(err)
		}
		if got := meili.all(); len(got) != 1 || !strings.Contains(got[0], "/indexes/statuses/documents") || !strings.HasSuffix(got[0], "/"+intString(status.ID)) {
			t.Fatalf("meilisearch requests = %v, want the private status %d reindexed", got, status.ID)
		}
	})

	t.Run("settings resolve queues the deletion of a gone account like queue_deletion!", func(t *testing.T) {
		f := newAccountMoveWorkerFixture(t, database, cfg)
		stale := settingsResolveRemoteAccount(t, f.tx, "dave", "remote.example", "https://remote.example/users/dave", time.Now().UTC().Add(-48*time.Hour))
		f.create(&models.Status{Text: "hello", AccountID: stale.ID, Visibility: 0, CreatedAt: f.now, UpdatedAt: f.now})
		settingsResolveMockRemoteStatus(t, map[string]int{
			settingsResolveWebFingerURL("remote.example", "dave@remote.example"): http.StatusGone,
		})

		account, err := f.server.resolveSettingsAccount("dave@remote.example", false)
		if err != nil || account != nil {
			t.Fatalf("resolve = %v, %v, want nil", account, err)
		}
		var gone models.Account
		if err := f.tx.First(&gone, stale.ID).Error; err != nil {
			t.Fatalf("gone account must be kept for AccountDeletionWorker: %v", err)
		}
		if !gone.SuspendedAt.Valid || gone.SuspensionOrigin.Int64 != 1 {
			t.Fatalf("gone account suspended_at=%v suspension_origin=%v, want a remote suspension", gone.SuspendedAt, gone.SuspensionOrigin)
		}
		var statuses int64
		if err := f.tx.Model(&models.Status{}).Where("account_id = ?", stale.ID).Count(&statuses).Error; err != nil {
			t.Fatal(err)
		}
		if statuses != 1 {
			t.Fatalf("statuses = %d, want the purge left to the deletion task", statuses)
		}
		task := accountMigrationPendingTask(t, f.inspector, f.server.asynqQueue(asynqQueuePull), asynqTaskAccountDeletion)
		var payload asynqAccountPayload
		if err := json.Unmarshal(task.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.AccountID != stale.ID {
			t.Fatalf("account deletion payload = %+v, want %d", payload, stale.ID)
		}
	})
}

type accountMoveWorkerFixture struct {
	t         *testing.T
	tx        *gorm.DB
	server    *Server
	inspector *asynq.Inspector
	now       time.Time
}

// newAccountMoveWorkerFixture wires a rolled-back transaction and an isolated
// asynq/Redis namespace, so side-effect workers are enqueued instead of
// running in goroutines against the test transaction.
func newAccountMoveWorkerFixture(t *testing.T, database *gorm.DB, cfg config.Config) *accountMoveWorkerFixture {
	t.Helper()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	cfg.RedisNamespace = "account-move-worker-" + randomHex(6)
	server := &Server{cfg: cfg, db: tx, asynqClient: asynq.NewClient(asynqRedisOpt(cfg))}
	inspector := asynq.NewInspector(asynqRedisOpt(cfg))
	t.Cleanup(func() {
		for _, queue := range []string{asynqQueueDefault, asynqQueuePush, asynqQueuePull, asynqQueueMailers} {
			_ = inspector.DeleteQueue(server.asynqQueue(queue), true)
		}
		_ = inspector.Close()
		_ = server.asynqClient.Close()
	})
	return &accountMoveWorkerFixture{t: t, tx: tx, server: server, inspector: inspector, now: time.Now().UTC()}
}

func (f *accountMoveWorkerFixture) create(value any) {
	f.t.Helper()
	if err := f.tx.Create(value).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *accountMoveWorkerFixture) local(username string, locale string) models.Account {
	f.t.Helper()
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		f.t.Fatal(err)
	}
	account := models.Account{Username: username, PrivateKey: sql.NullString{String: privateKey, Valid: true}, PublicKey: publicKey, CreatedAt: f.now, UpdatedAt: f.now}
	f.create(&account)
	f.create(&models.User{AccountID: account.ID, Email: username + "@paon.example", EncryptedPassword: "x", Locale: sql.NullString{String: locale, Valid: locale != ""}, CreatedAt: f.now, UpdatedAt: f.now})
	return account
}

func (f *accountMoveWorkerFixture) note(accountID, targetAccountID int64, comment string) {
	f.t.Helper()
	f.create(&models.AccountNote{AccountID: models.AccountNoteAccountID(accountID), TargetAccountID: models.AccountNoteAccountID(targetAccountID), Comment: comment, CreatedAt: f.now, UpdatedAt: f.now})
}

func (f *accountMoveWorkerFixture) noteText(accountID, targetAccountID int64) string {
	f.t.Helper()
	var note models.AccountNote
	if err := f.tx.Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).First(&note).Error; err != nil {
		f.t.Fatalf("note %d -> %d: %v", accountID, targetAccountID, err)
	}
	return note.Comment
}

func (f *accountMoveWorkerFixture) assertCount(model any, accountID, targetAccountID int64, want int64) {
	f.t.Helper()
	var count int64
	if err := f.tx.Model(model).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		f.t.Fatal(err)
	}
	if count != want {
		f.t.Fatalf("%T %d -> %d count = %d, want %d", model, accountID, targetAccountID, count, want)
	}
}

func (f *accountMoveWorkerFixture) moveTask(sourceAccountID, targetAccountID int64) *asynq.TaskInfo {
	f.t.Helper()
	task := accountMigrationPendingTask(f.t, f.inspector, f.server.asynqQueue(asynqQueueDefault), asynqTaskMove)
	var payload asynqMovePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		f.t.Fatal(err)
	}
	if payload != (asynqMovePayload{SourceAccountID: sourceAccountID, TargetAccountID: targetAccountID}) {
		f.t.Fatalf("move payload = %+v, want %d -> %d", payload, sourceAccountID, targetAccountID)
	}
	return task
}

type accountMoveWorkerMeiliRequests struct {
	mu   sync.Mutex
	list []string
}

func (r *accountMoveWorkerMeiliRequests) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.list...)
}

func (r *accountMoveWorkerMeiliRequests) count() int {
	return len(r.all())
}

func accountMoveWorkerMockMeili(t *testing.T) *accountMoveWorkerMeiliRequests {
	t.Helper()
	requests := &accountMoveWorkerMeiliRequests{}
	previous := meiliHTTPClient
	t.Cleanup(func() { meiliHTTPClient = previous })
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.mu.Lock()
		requests.list = append(requests.list, r.Method+" "+r.URL.Path)
		requests.mu.Unlock()
		return textResponse(http.StatusAccepted, "application/json", `{"taskUid":1}`), nil
	})}
	return requests
}

func accountMoveWorkerFailingRemote(t *testing.T) {
	t.Helper()
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
}
