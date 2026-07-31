package api

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestStatusDeleteStreamPayloadMatchesMastodonShape(t *testing.T) {
	if got, want := statusDeleteStreamPayload(42), `{"event":"delete","payload":"42"}`; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestStatusUpdateStreamPayloadUsesRESTStatusObject(t *testing.T) {
	status := models.Status{
		ID:         42,
		Text:       "hello",
		CreatedAt:  time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		Visibility: 0,
		Account:    models.Account{ID: 7, Username: "alice", CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)},
	}
	payload := statusUpdateStreamPayload(config.Config{LocalDomain: "example.test"}, "update", status)
	for _, want := range []string{`"event":"update"`, `"id":"42"`, `"content":"\u003cp\u003ehello\u003c/p\u003e"`, `"account"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload missing %q: %s", want, payload)
		}
	}
}

func TestStatusDeletePublicStreamingChannels(t *testing.T) {
	status := models.Status{
		ID:         42,
		Visibility: 0,
		AccountID:  7,
		Local:      sql.NullBool{Bool: true, Valid: true},
		MediaAttachments: []models.MediaAttachment{
			{ID: 7},
		},
		Tags: []models.Tag{{Name: "GoLang"}},
	}
	got := (&Server{}).statusStreamingChannels(nil, nil, "mastodon:", status)
	want := []string{
		"mastodon:timeline:public",
		"mastodon:timeline:public:local",
		"mastodon:timeline:public:media",
		"mastodon:timeline:public:local:media",
		"mastodon:timeline:hashtag:golang",
		"mastodon:timeline:hashtag:golang:local",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %#v, want %#v", got, want)
	}
}

func TestBatchedAccountDeletionStreamingChannelsMatchRailsBatchRemoval(t *testing.T) {
	goSrc, err := os.ReadFile("status_streaming_delete.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`publicCutoffID := mastodonSnowflakeIDAt(now.Add(-14*24*time.Hour), false)`,
		`statusBatchedDeletePublicStreamingChannels(cfg.prefix, status)`,
		`statusBatchedDeleteTagStreamingChannels(cfg.prefix, status)`,
		`s.statusHomeStreamingChannels(ctx, database, cfg.prefix, status.AccountID)`,
		`s.statusListStreamingChannels(ctx, database, cfg.prefix, status.AccountID)`,
	} {
		if !functionBodyContains(t, goSrc, "prepareBatchedAccountDeletionStatusDeletes", want) {
			t.Fatalf("prepareBatchedAccountDeletionStatusDeletes missing %q", want)
		}
	}
	if strings.Contains(string(goSrc), `statusMentionHomeDeleteChannels(ctx, database, prefix, status.ID, publicCutoffID)`) {
		t.Fatal("batched account deletion streaming must not use single-status active mention delete channels")
	}

	recentReblog := models.Status{
		ID:         200,
		Visibility: 0,
		AccountID:  7,
		ReblogOfID: sql.NullInt64{
			Int64: 100,
			Valid: true,
		},
		Local: sql.NullBool{Bool: false, Valid: true},
		MediaAttachments: []models.MediaAttachment{
			{ID: 8},
		},
		Tags: []models.Tag{{Name: "GoLang"}},
	}
	want := []string{
		"mastodon:timeline:public",
		"mastodon:timeline:public:remote",
		"mastodon:timeline:public:media",
		"mastodon:timeline:public:remote:media",
		"mastodon:timeline:hashtag:golang",
	}
	if got := (&Server{}).statusBatchedDeletionStreamingChannels(nil, nil, "mastodon:", recentReblog, 100); !reflect.DeepEqual(got, want) {
		t.Fatalf("recent batched delete channels = %#v, want %#v", got, want)
	}
	if got := (&Server{}).statusBatchedDeletionStreamingChannels(nil, nil, "mastodon:", recentReblog, 200); len(got) != 0 {
		t.Fatalf("old batched delete channels = %#v", got)
	}
}

func TestStatusDeletePrivateStatusDoesNotPublishPublicChannels(t *testing.T) {
	status := models.Status{
		ID:               42,
		Visibility:       2,
		AccountID:        7,
		Local:            sql.NullBool{Bool: true, Valid: true},
		MediaAttachments: []models.MediaAttachment{{ID: 7}},
		Tags:             []models.Tag{{Name: "GoLang"}},
	}
	got := (&Server{}).statusStreamingChannels(nil, nil, "mastodon:", status)
	if len(got) != 0 {
		t.Fatalf("private channels = %#v", got)
	}
}

func TestStatusStreamingSurfacesPublishStatusEvents(t *testing.T) {
	checks := map[string]map[string]string{
		"server.go": {
			"updateStatus":   `s.publishStatusUpdateEvent("status.update", *updated)`,
			"deleteStatus":   `s.enqueueRemovalTask(asynqRemovalPayload{StatusID: status.ID, Redraft: true})`,
			"reblogStatus":   `s.fanOutStatusToLocalRecipientsSkipNotifications(c.Request().Context(), s.db, *createdStatus)`,
			"unreblogStatus": `s.enqueueRemovalTask(asynqRemovalPayload{StatusID: reblog.ID})`,
		},
		"local_status_postcommit.go": {
			"runLocalStatusCreatePostCommit": `s.publishStatusUpdateEvent("update", created)`,
		},
		"asynq_workers.go": {
			"applyDeletedStatusRemovalSideEffects": `s.publishStatusDelete(status)`,
			"handleAsynqRemoval":                   `s.publishStatusAndReblogDeletesForIDs(ctx, s.db, []int64{p.StatusID})`,
		},
		"statuses_cleanup_worker.go": {
			"cleanupStatusesForPolicy": `s.enqueueRemovalTask(asynqRemovalPayload{StatusID: status.ID})`,
		},
		"scheduled_status_publish.go": {
			"publishScheduledStatus": `s.publishStatusUpdateEventWithContext(ctx, s.db, "update", *created)`,
		},
		"admin_account_subroutes.go": {
			"applyAdminStatusBatchAction": `s.applyAdminDeletedStatusSideEffects(context.Background(), s.db, deletedStatusIDs)`,
		},
		"admin_reports_web.go": {
			"createAdminReportActionWeb": `s.applyAdminDeletedStatusSideEffects(context.Background(), s.db, deletedStatusIDs)`,
		},
		"activitypub_inbox.go": {
			"processActivityPubAnnounce":                  `s.publishStatusUpdateEvent("update", *created)`,
			"processActivityPubUndoAnnounceWithTombstone": `s.publishStatusDelete(reblog)`,
		},
	}
	for file, bodyChecks := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for fn, want := range bodyChecks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s:%s does not contain %q", file, fn, want)
			}
		}
	}
	src, err := os.ReadFile("status_streaming_delete.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.publishStatusAndReblogDeletesForIDs(ctx, database, ids)`,
		`s.invalidateStatusCache(ctx, status.ID)`,
		`_ = s.removeStatusFromRailsFeeds(ctx, database, status)`,
		`s.meiliDeleteStatusBestEffort(ctx, status.ID)`,
		`s.deleteStatusQuoteBestEffort(ctx, status.ID)`,
	} {
		if !functionBodyContains(t, src, "applyAdminDeletedStatusSideEffects", want) {
			t.Fatalf("applyAdminDeletedStatusSideEffects does not contain %q", want)
		}
	}
}

func TestStatusUpdateStreamingLeavesRecipientStreamsToPushUpdate(t *testing.T) {
	src, err := os.ReadFile("status_streaming_delete.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "statusStreamingChannels")
	for _, forbidden := range []string{"statusHomeStreamingChannels", "statusListStreamingChannels"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("statusStreamingChannels must not publish recipient-specific streams directly; found %q", forbidden)
		}
	}
}
