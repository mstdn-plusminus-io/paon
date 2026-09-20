package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

func TestAccountUpdateDistributionUsesMastodon43AccountReach(t *testing.T) {
	src, err := os.ReadFile("activitypub_delivery.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "deliverActivityPubAccountUpdateNowWithSigningKey", `deliverActivityPubActivityToAccountReach(fresh, activityPubActorUpdate(s, fresh))`) {
		t.Fatal("normal actor Update must use AccountReachFinder-equivalent recipients")
	}
	if !functionBodyContains(t, src, "deliverActivityPubActivityToAccountReach", `deliverActivityPubRawToAccountReach(account, body, nil)`) {
		t.Fatal("actor Update helper must fan out to the complete account reach set")
	}
	for _, want := range []string{
		`s.activityPubRemoteFollowerInboxes(account.ID)`,
		`s.activityPubReporterInboxes(account.ID)`,
		`s.activityPubRecentlyMentionedInboxes(account.ID, cutoff)`,
		`s.activityPubRecentlyFollowedInboxes(account.ID, cutoff)`,
		`s.activityPubRecentlyRequestedInboxes(account.ID, cutoff)`,
		`s.activityPubEnabledRelayInboxes()`,
	} {
		if !functionBodyContains(t, src, "activityPubAccountReachInboxes", want) {
			t.Fatalf("account reach is missing Mastodon 4.3 recipient source %q", want)
		}
	}
}

func TestJob43PayloadVersionsEmitCurrentAndAcceptLegacy(t *testing.T) {
	accountTask, err := newAsynqAccountUpdateTask(42, "old-private-key")
	if err != nil {
		t.Fatal(err)
	}
	var accountPayload asynqAccountPayload
	if err := json.Unmarshal(accountTask.Payload(), &accountPayload); err != nil {
		t.Fatal(err)
	}
	if accountPayload.Version != asynqPayloadVersion43 {
		t.Fatalf("account update version = %d, want %d", accountPayload.Version, asynqPayloadVersion43)
	}

	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`asynqMediaAttachmentPayload{Version: asynqPayloadVersion43, MediaAttachmentID: mediaAttachmentID}`,
		`asynqFollowersSynchronizationPayload{Version: asynqPayloadVersion43, AccountID: accountID, URL: collectionURL}`,
	} {
		if !bytes.Contains(src, []byte(want)) {
			t.Fatalf("JOB43 payload producer is missing explicit version: %s", want)
		}
	}

	server := &Server{}
	legacyTasks := []struct {
		name    string
		task    *asynq.Task
		handler func(context.Context, *asynq.Task) error
	}{
		{name: "mention", task: asynq.NewTask(asynqTaskMentionResolve, []byte(`{"status_id":1,"uri":"https://remote.example/users/alice"}`)), handler: server.handleAsynqMentionResolve},
		{name: "annual report", task: asynq.NewTask(asynqTaskGenerateAnnualReport, []byte(`{"account_id":1,"year":2025}`)), handler: server.handleAsynqGenerateAnnualReport},
		{name: "account update", task: asynq.NewTask(asynqTaskAccountUpdate, []byte(`{"account_id":1}`)), handler: server.handleAsynqAccountUpdate},
		{name: "followers synchronization", task: asynq.NewTask(asynqTaskFollowersSync, []byte(`{"account_id":1,"url":"https://remote.example/followers"}`)), handler: server.handleAsynqFollowersSynchronization},
		{name: "account media", task: asynq.NewTask(asynqTaskRedownloadAvatar, []byte(`{"account_id":1}`)), handler: server.handleAsynqRedownloadAccountMedia},
		{name: "attachment media", task: asynq.NewTask(asynqTaskRedownloadMedia, []byte(`{"media_attachment_id":1}`)), handler: server.handleAsynqRedownloadMedia},
	}
	for _, test := range legacyTasks {
		t.Run(test.name, func(t *testing.T) {
			if err := test.handler(context.Background(), test.task); err != nil {
				t.Fatalf("legacy versionless payload was rejected: %v", err)
			}
		})
	}

	legacyNotification := asynq.NewTask(asynqTaskFilteredNotificationCleanup, []byte(`{"account_id":1,"from_account_id":2}`))
	if _, err := notificationPairPayload(legacyNotification); err != nil {
		t.Fatalf("legacy versionless notification payload was rejected: %v", err)
	}
}

func TestJob43PayloadVersionsRejectUnknownFutureVersion(t *testing.T) {
	if err := validateAsynqPayloadVersion("job43", asynqPayloadVersion43+1); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("future payload version error = %v, want permanent SkipRetry", err)
	}
	for _, version := range []int{0, asynqPayloadVersion43} {
		if err := validateAsynqPayloadVersion("job43", version); err != nil {
			t.Fatalf("supported payload version %d rejected: %v", version, err)
		}
	}
}

func TestMentionResolveDelayMatchesMastodonRange(t *testing.T) {
	if got := mentionResolveDelayWithRand(func(int64) int64 { return 0 }); got != 30*time.Second {
		t.Fatalf("minimum delay = %s, want 30s", got)
	}
	if got := mentionResolveDelayWithRand(func(limit int64) int64 {
		if limit != 570 {
			t.Fatalf("random limit = %d, want 570", limit)
		}
		return limit - 1
	}); got != 599*time.Second {
		t.Fatalf("maximum delay = %s, want 599s", got)
	}
}

func TestMentionResolveWorkerRetriesOnlyTransientErrors(t *testing.T) {
	if got := mentionResolveWorkerError(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v, want retryable deadline", got)
	}
	if got := mentionResolveWorkerError(errors.New("invalid actor document")); got != nil {
		t.Fatalf("validation error = %v, want permanent discard", got)
	}
}

func TestAnnualReportArchetypes(t *testing.T) {
	tests := []struct {
		name                        string
		standalone, replies, boosts int64
		polls                       int64
		want                        string
	}{
		{name: "lurker", standalone: 112, want: "lurker"},
		{name: "booster", standalone: 40, boosts: 100, want: "booster"},
		{name: "pollster", standalone: 113, polls: 12, want: "pollster"},
		{name: "replier", standalone: 40, replies: 100, want: "replier"},
		{name: "oracle", standalone: 113, want: "oracle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := annualReportArchetype(test.standalone, test.replies, test.boosts, test.polls); got != test.want {
				t.Fatalf("archetype = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOrphanedFeedIDFromRedisKeyCollapsesAuxiliaryKeys(t *testing.T) {
	for _, key := range []string{
		"paon:feed:home:42",
		"paon:feed:home:42:reblogs",
		"paon:feed:home:42:reblogs:99",
	} {
		id, ok := orphanedFeedIDFromRedisKey("paon:", "home", key)
		if !ok || id != 42 {
			t.Fatalf("orphanedFeedIDFromRedisKey(%q) = (%d, %t), want (42, true)", key, id, ok)
		}
	}
	if _, ok := orphanedFeedIDFromRedisKey("paon:", "home", "paon:feed:list:42"); ok {
		t.Fatal("list key unexpectedly parsed as a home key")
	}
}

func TestStorageSchemaVersionAndPaths(t *testing.T) {
	if !storageSchemaOutdated(sql.NullInt64{}) || !storageSchemaOutdated(sql.NullInt64{Int64: 0, Valid: true}) {
		t.Fatal("missing/version-zero storage schema should be outdated")
	}
	if storageSchemaOutdated(sql.NullInt64{Int64: 1, Valid: true}) {
		t.Fatal("storage schema version 1 should be current")
	}
	if got := customEmojiStorageObjectKey(42, "original", "party.png", true); got != "cache/custom_emojis/images/000/000/042/original/party.png" {
		t.Fatalf("custom emoji upgraded key = %q", got)
	}
	if got := previewCardStorageObjectKey(42, "cover.png", true); got != "cache/preview_cards/images/000/000/042/original/cover.png" {
		t.Fatalf("preview-card upgraded key = %q", got)
	}
}
