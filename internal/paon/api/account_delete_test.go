package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/bcrypt"
)

func TestDeleteChallengePassedWithPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{EncryptedPassword: string(hash)}
	account := models.Account{Username: "alice"}
	if !deleteChallengePassed(user, account, "secret", "") {
		t.Fatal("expected password challenge to pass")
	}
	if deleteChallengePassed(user, account, "wrong", "alice") {
		t.Fatal("wrong password passed")
	}
}

func TestSettingsDeleteRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/delete", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsDeletePage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/delete")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDeleteAccountHTMLMatchesRailsSimpleFormMarkup(t *testing.T) {
	confirmed := deleteAccountHTML(models.User{EncryptedPassword: "hash", ConfirmedAt: sql.NullTime{Time: time.Now(), Valid: true}, Approved: true}, models.Account{}, "", "en")
	for _, want := range []string{
		`class="simple_form new_form_delete_confirmation"`,
		`id="new_form_delete_confirmation"`,
		`class="input with_block_label password optional form_delete_confirmation_password field_with_hint"`,
		`class="password optional"`,
		`class="btn negative"`,
	} {
		if !strings.Contains(confirmed, want) {
			t.Fatalf("delete form missing Rails fragment %q: %s", want, confirmed)
		}
	}

	unconfirmed := deleteAccountHTML(models.User{}, models.Account{}, "", "en", "default", "", "Paon", "help@example.test")
	for _, want := range []string{
		`form_delete_confirmation_username field_with_hint`,
		`/auth/confirmation/new`,
		`mailto:help@example.test`,
	} {
		if !strings.Contains(unconfirmed, want) {
			t.Fatalf("unconfirmed delete form missing Rails fragment %q: %s", want, unconfirmed)
		}
	}
}

func TestOwnAccountSuspensionClearsRailsFeedCaches(t *testing.T) {
	src, err := os.ReadFile("account_delete.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "suspendOwnAccount", `s.clearAdminSuspendedAccountFeedCaches(context.Background(), s.db, adminSingleAccountIDSubquery(s.db, accountID))`) {
		t.Fatal("suspendOwnAccount does not clear Rails feed caches")
	}
}

func TestOwnAccountDeletionRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("account_delete.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "destroyOwnAccount", `settingsDatabaseUnavailableMessage(locale)`) {
		t.Fatal("destroyOwnAccount must use localized database-unavailable flash")
	}
	if functionBodyContains(t, src, "destroyOwnAccount", `QueryEscape("DATABASE_URL is not set")`) {
		t.Fatal("destroyOwnAccount must not redirect with fixed Go-only database flash")
	}
}

func TestAccountDeletionWorkerIsStarted(t *testing.T) {
	src, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "StartBackgroundWorkers", "workers.Go(ctx, s.runAccountDeletionWorker)") {
		t.Fatal("StartBackgroundWorkers does not start account deletion worker")
	}
}

func TestAccountDeletionDelayMatchesRails(t *testing.T) {
	if accountDeletionWorkerInterval != time.Minute {
		t.Fatalf("accountDeletionWorkerInterval = %s", accountDeletionWorkerInterval)
	}
	if accountDeletionDelay != 30*24*time.Hour {
		t.Fatalf("accountDeletionDelay = %s", accountDeletionDelay)
	}
	if accountDeletionMaxPerRun != 10 {
		t.Fatalf("accountDeletionMaxPerRun = %d", accountDeletionMaxPerRun)
	}
	if accountDeletionMaxPullQueue != 50 {
		t.Fatalf("accountDeletionMaxPullQueue = %d", accountDeletionMaxPullQueue)
	}
}

func TestAccountDeletionPublishesPreparedStatusDeletesAfterCommit(t *testing.T) {
	src, err := os.ReadFile("account_deletion_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.prepareBatchedAccountDeletionStatusDeletes(ctx, tx, now, statusIDs, reblogIDs)`,
		`s.publishPreparedBatchedAccountDeletionStatusDeletes(publishCtx, statusDeleteBroadcasts)`,
		`s.tombstoneAccountDeletionStatuses(ctx, tx, accountIDs, reportedStatusIDs, now)`,
	} {
		if !functionBodyContains(t, src, "purgeAccountDeletionRequestWithOptions", want) {
			t.Fatalf("purgeAccountDeletionRequestWithOptions missing %q", want)
		}
	}
	if functionBodyContains(t, src, "tombstoneAccountDeletionStatuses", `context.WithTimeout`) {
		t.Fatal("tombstoneAccountDeletionStatuses must not cancel database work on its transaction connection")
	}
	if functionBodyContains(t, src, "tombstoneAccountDeletionStatuses", `publishBatchedAccountDeletionStatusDeletesForQuery`) {
		t.Fatal("tombstoneAccountDeletionStatuses must not publish Redis events inside the database transaction")
	}
	if !functionBodyContains(t, src, "tombstoneAccountDeletionStatuses", `recalculateStatusCountersForStatusIDs(database, affectedStatusIDs, now)`) {
		t.Fatal("tombstoneAccountDeletionStatuses must only recount affected indexed status rows")
	}
}

func TestRemoteActivityPubActorDeleteQueuesBoundedAccountDeletion(t *testing.T) {
	inboxSource, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	body := mustFunctionBody(t, string(inboxSource), "processActivityPubDeleteWithContext")
	for _, want := range []string{
		`s.acquireActivityPubRedisLock(ctx, "delete_in_progress:"`,
		`s.suspendRemoteActivityPubActor(ctx, actor, now)`,
		`s.enqueueAccountDeletionTaskContext(ctx, actor.ID)`,
		`s.purgeAccountDeletionRequest(ctx, actor.ID, now)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("processActivityPubDeleteWithContext missing %q", want)
		}
	}
	if strings.Contains(body, `s.deleteRemoteActivityPubActorNow(context.Background()`) {
		t.Fatal("remote actor Delete must not perform an unbounded synchronous purge on the ingress task")
	}

	retrySource, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, retrySource, "performActivityPubInboxProcessingOnce", `processActivityPubInboxForDeliveredToWithContext(ctx`) {
		t.Fatal("ActivityPub processing worker context must reach the Delete handler")
	}

	deletionSource, err := os.ReadFile("account_deletion_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, deletionSource, "runOwnAccountDeletionWorkerEffects", `account deletion target=%s account_id=%d domain=%q`) {
		t.Fatal("account deletion errors must identify the local or remote target")
	}
}

func TestRejectedLocalAccountDeletionDestroysRowsLikeRailsReject(t *testing.T) {
	src, err := os.ReadFile("account_deletion_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`func (s *Server) deleteRejectedLocalAccountRows`,
		`s.clearAdminSuspendedAccountFeedCaches(ctx, tx, accountIDs)`,
		`s.clearAccountOwnedFeedCaches(ctx, tx, accountIDs)`,
		`s.prepareAccountDeletionLocalFiles(tx, accountIDs, reportedStatusIDs, now)`,
		`purgeAdminDomainSuspendedAccountAssociations(tx, accountIDs, now)`,
		`s.purgeAccountDeletionInteractionAssociations(ctx, tx, accountIDs, now)`,
		`purgeAccountDeletionExtraAssociations(tx, accountIDs, reportedStatusIDs, true)`,
		`s.logAdminAccountAction(tx, actorAccountID, account, "reject", now)`,
		`Delete(&models.WebauthnCredential{})`,
		`tx.Delete(&models.Account{}, account.ID)`,
		`tx.Delete(&models.User{}, account.User.ID)`,
		`fileCleanup.run(s)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("deleteRejectedLocalAccountRows missing %q", want)
		}
	}
}

func TestStreamingKillPayloadParsesForSystemStreams(t *testing.T) {
	message, ok := redisPubSubMessage([]any{"message", "mastodon:timeline:system:42", streamingKillPayload()})
	if !ok {
		t.Fatal("kill payload did not parse")
	}
	if message.Event != "kill" || message.Channel != "mastodon:timeline:system:42" || string(message.Payload) != "{}" {
		t.Fatalf("message = %#v payload=%s", message, string(message.Payload))
	}
	if !streamingSystemChannel(message.Channel) {
		t.Fatalf("expected %q to be recognized as a system channel", message.Channel)
	}
}

func TestPublishStreamingKillForLocalAccountOnlyKillsLocalAccounts(t *testing.T) {
	src, err := os.ReadFile("streaming_kill.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if !account.Local()`,
		`return`,
		`s.publishStreamingKill(account.ID, nil)`,
	} {
		if !functionBodyContains(t, src, "publishStreamingKillForLocalAccount", want) {
			t.Fatalf("publishStreamingKillForLocalAccount missing %q", want)
		}
	}
}

func TestStreamingSystemChannelIDs(t *testing.T) {
	got := streamingSystemChannelIDs(streamingSession{Account: &models.Account{ID: 42}, AccessTokenID: 7})
	want := []string{"timeline:access_token:7", "timeline:system:42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system channels = %#v, want %#v", got, want)
	}
	if got := streamingSystemChannelIDs(streamingSession{}); got != nil {
		t.Fatalf("empty session channels = %#v", got)
	}
}

func TestAccessTokenKillChannelsDeduplicatesTokenIDs(t *testing.T) {
	got := accessTokenKillChannels("mastodon:", []int64{7, 0, 8, 7})
	want := []string{"mastodon:timeline:access_token:7", "mastodon:timeline:access_token:8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kill channels = %#v, want %#v", got, want)
	}
}
