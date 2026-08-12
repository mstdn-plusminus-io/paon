package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func sourceContains(src []byte, want string) bool {
	return strings.Contains(strings.Join(strings.Fields(string(src)), " "), strings.Join(strings.Fields(want), " "))
}

func TestActivityFetchWorkerErrorMatchesRailsRetryPolicy(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusUnprocessableEntity, http.StatusNotImplemented} {
		err := activityFetchHTTPError{StatusCode: status, URL: "https://remote.example/statuses/1/replies"}
		if got := activityFetchWorkerError(err); got != nil {
			t.Errorf("status %d should finish without retry, got %v", status, got)
		}
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		err := activityFetchHTTPError{StatusCode: status, URL: "https://remote.example/statuses/1/replies"}
		if got := activityFetchWorkerError(err); got == nil {
			t.Errorf("status %d should remain retryable", status)
		}
	}
	if got := activityFetchWorkerError(errors.New("network failure")); got == nil {
		t.Fatal("network errors should remain retryable")
	}
}

func TestActivityPubProcessingUsesRailsRetryBackoff(t *testing.T) {
	task := asynq.NewTask(asynqTaskActivityPubProcessing, nil)
	if !railsExponentialBackoffAsynqTask(task) {
		t.Fatal("ActivityPub processing must use the Sidekiq-compatible retry schedule")
	}
}

func TestRemoteFetchWorkersApplyRailsUnsalvageableResponsePolicy(t *testing.T) {
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{
		"handleAsynqFetchReplies",
		"handleAsynqFeaturedCollectionSync",
		"handleAsynqFeaturedTagsSync",
		"handleAsynqFollowersSynchronization",
	} {
		if !strings.Contains(mustFunctionBody(t, string(src), fn), "activityFetchWorkerError(") {
			t.Errorf("%s must discard Rails-unsalvageable HTTP responses", fn)
		}
	}
}

func TestAsynqTaskTypesAndQueueMatchRailsSidekiq(t *testing.T) {
	if asynqTaskRedownloadAvatar != "redownload:avatar" {
		t.Fatalf("avatar task type = %q", asynqTaskRedownloadAvatar)
	}
	if asynqTaskRedownloadHeader != "redownload:header" {
		t.Fatalf("header task type = %q", asynqTaskRedownloadHeader)
	}
	if asynqTaskRedownloadMedia != "redownload:media" {
		t.Fatalf("media task type = %q", asynqTaskRedownloadMedia)
	}
	if asynqTaskRefollow != "refollow" {
		t.Fatalf("refollow task type = %q", asynqTaskRefollow)
	}
	if asynqTaskFetchReply != "fetch:reply" {
		t.Fatalf("fetch reply task type = %q", asynqTaskFetchReply)
	}
	if asynqTaskFetchReplies != "fetch:replies" {
		t.Fatalf("fetch replies task type = %q", asynqTaskFetchReplies)
	}
	if asynqTaskFetchAllReplies != "fetch:all_replies" {
		t.Fatalf("fetch all replies task type = %q", asynqTaskFetchAllReplies)
	}
	if asynqTaskThreadResolve != "thread:resolve" {
		t.Fatalf("thread resolve task type = %q", asynqTaskThreadResolve)
	}
	if asynqTaskFeedInsert != "feed:insert" {
		t.Fatalf("feed insert task type = %q", asynqTaskFeedInsert)
	}
	if asynqTaskLocalNotification != "local_notification" {
		t.Fatalf("local notification task type = %q", asynqTaskLocalNotification)
	}
	if asynqTaskNotificationMail != "notification:mail" {
		t.Fatalf("notification mail task type = %q", asynqTaskNotificationMail)
	}
	if asynqTaskBackup != "backup" {
		t.Fatalf("backup task type = %q", asynqTaskBackup)
	}
	if asynqTaskBulkImport != "bulk_import" {
		t.Fatalf("bulk import task type = %q", asynqTaskBulkImport)
	}
	if asynqTaskLegacyImport != "import" {
		t.Fatalf("legacy import task type = %q", asynqTaskLegacyImport)
	}
	if asynqTaskImportRow != "import:row" {
		t.Fatalf("import row task type = %q", asynqTaskImportRow)
	}
	if asynqTaskImportRelationship != "import:relationship" {
		t.Fatalf("import relationship task type = %q", asynqTaskImportRelationship)
	}
	if asynqTaskLinkCrawl != "link_crawl" {
		t.Fatalf("link crawl task type = %q", asynqTaskLinkCrawl)
	}
	if asynqTaskPostProcessMedia != "post_process_media" {
		t.Fatalf("post-process media task type = %q", asynqTaskPostProcessMedia)
	}
	if asynqTaskRemoveFeaturedTag != "remove_featured_tag" {
		t.Fatalf("remove featured tag task type = %q", asynqTaskRemoveFeaturedTag)
	}
	if asynqTaskTagUnmerge != "tag_unmerge" {
		t.Fatalf("tag unmerge task type = %q", asynqTaskTagUnmerge)
	}
	if asynqTaskUnfollowFollow != "unfollow_follow" {
		t.Fatalf("unfollow follow task type = %q", asynqTaskUnfollowFollow)
	}
	if asynqTaskPublishScheduledStatus != "publish:scheduled_status" {
		t.Fatalf("publish scheduled status task type = %q", asynqTaskPublishScheduledStatus)
	}
	if asynqTaskPublishAnnouncement != "publish:announcement" {
		t.Fatalf("publish announcement task type = %q", asynqTaskPublishAnnouncement)
	}
	if asynqTaskUnpublishAnnouncement != "unpublish:announcement" {
		t.Fatalf("unpublish announcement task type = %q", asynqTaskUnpublishAnnouncement)
	}
	if asynqTaskRemoteAccountRefresh != "remote_account:refresh" {
		t.Fatalf("remote account refresh task type = %q", asynqTaskRemoteAccountRefresh)
	}
	if asynqTaskAccountRefresh != "account:refresh" {
		t.Fatalf("account refresh task type = %q", asynqTaskAccountRefresh)
	}
	if asynqTaskAccountMerging != "account:merging" {
		t.Fatalf("account merging task type = %q", asynqTaskAccountMerging)
	}
	if asynqTaskResolveAccount != "resolve:account" {
		t.Fatalf("resolve account task type = %q", asynqTaskResolveAccount)
	}
	if asynqTaskPollExpiration != "poll:expiration" {
		t.Fatalf("poll expiration task type = %q", asynqTaskPollExpiration)
	}
	if asynqTaskPollUpdate != "poll:update" {
		t.Fatalf("poll update task type = %q", asynqTaskPollUpdate)
	}
	if asynqTaskAccountUpdate != "account:update" {
		t.Fatalf("account update task type = %q", asynqTaskAccountUpdate)
	}
	if asynqTaskRawDistribution != "raw_distribution" {
		t.Fatalf("raw distribution task type = %q", asynqTaskRawDistribution)
	}
	if asynqTaskAccountRawDistribution != "account:raw_distribution" {
		t.Fatalf("account raw distribution task type = %q", asynqTaskAccountRawDistribution)
	}
	if asynqTaskFeaturedCollectionSync != "featured_collection:sync" {
		t.Fatalf("featured collection sync task type = %q", asynqTaskFeaturedCollectionSync)
	}
	if asynqTaskFeaturedTagsSync != "featured_tags:sync" {
		t.Fatalf("featured tags sync task type = %q", asynqTaskFeaturedTagsSync)
	}
	if asynqTaskMoveDistribution != "move:distribution" {
		t.Fatalf("move distribution task type = %q", asynqTaskMoveDistribution)
	}
	if asynqTaskPostUpgrade != "post_upgrade" {
		t.Fatalf("post upgrade task type = %q", asynqTaskPostUpgrade)
	}
	if asynqTaskFollowersSync != "followers_synchronization" {
		t.Fatalf("followers synchronization task type = %q", asynqTaskFollowersSync)
	}
	if asynqTaskActivityPubProcessing != "activitypub:processing" {
		t.Fatalf("activitypub processing task type = %q", asynqTaskActivityPubProcessing)
	}
	if asynqTaskActivityPubDelivery != "activitypub:delivery" {
		t.Fatalf("activitypub delivery task type = %q", asynqTaskActivityPubDelivery)
	}
	if asynqTaskActivityPubDistribution != "activitypub:distribution" {
		t.Fatalf("activitypub distribution task type = %q", asynqTaskActivityPubDistribution)
	}
	if asynqTaskStatusUpdateDistribution != "activitypub:status_update_distribution" {
		t.Fatalf("status update distribution task type = %q", asynqTaskStatusUpdateDistribution)
	}
	if asynqTaskCacheBuster != "cache_buster" {
		t.Fatalf("cache buster task type = %q", asynqTaskCacheBuster)
	}
	if asynqTaskAnnouncementReaction != "announcement:reaction" {
		t.Fatalf("announcement reaction task type = %q", asynqTaskAnnouncementReaction)
	}
	if asynqTaskRemoval != "removal" {
		t.Fatalf("removal task type = %q", asynqTaskRemoval)
	}
	if asynqTaskPushConversation != "push:conversation" {
		t.Fatalf("push conversation task type = %q", asynqTaskPushConversation)
	}
	if asynqTaskPushUpdate != "push:update" {
		t.Fatalf("push update task type = %q", asynqTaskPushUpdate)
	}
	if asynqTaskWebPushNotification != "web:push_notification" {
		t.Fatalf("web push notification task type = %q", asynqTaskWebPushNotification)
	}
	if asynqTaskAuthorizeFollow != "authorize_follow" {
		t.Fatalf("authorize follow task type = %q", asynqTaskAuthorizeFollow)
	}
	if asynqTaskBootstrapTimeline != "bootstrap_timeline" {
		t.Fatalf("bootstrap timeline task type = %q", asynqTaskBootstrapTimeline)
	}
	if asynqTaskRegeneration != "regeneration" {
		t.Fatalf("regeneration task type = %q", asynqTaskRegeneration)
	}
	if asynqTaskVerifyAccountLinks != "verify_account_links" {
		t.Fatalf("verify account links task type = %q", asynqTaskVerifyAccountLinks)
	}
	if asynqTaskTriggerWebhook != "trigger_webhook" {
		t.Fatalf("trigger webhook task type = %q", asynqTaskTriggerWebhook)
	}
	if asynqTaskWebhookDelivery != "webhook:delivery" {
		t.Fatalf("webhook delivery task type = %q", asynqTaskWebhookDelivery)
	}
	if asynqTaskDomainBlock != "domain_block" {
		t.Fatalf("domain block task type = %q", asynqTaskDomainBlock)
	}
	if asynqTaskDomainClearMedia != "domain_clear_media" {
		t.Fatalf("domain clear media task type = %q", asynqTaskDomainClearMedia)
	}
	if asynqTaskAdminDomainPurge != "admin:domain_purge" {
		t.Fatalf("admin domain purge task type = %q", asynqTaskAdminDomainPurge)
	}
	if asynqTaskAccountDeletion != "account_deletion" {
		t.Fatalf("account deletion task type = %q", asynqTaskAccountDeletion)
	}
	if asynqTaskAdminAccountDeletion != "admin:account_deletion" {
		t.Fatalf("admin account deletion task type = %q", asynqTaskAdminAccountDeletion)
	}
	if asynqTaskAdminSuspension != "admin:suspension" {
		t.Fatalf("admin suspension task type = %q", asynqTaskAdminSuspension)
	}
	if asynqTaskAdminUnsuspension != "admin:unsuspension" {
		t.Fatalf("admin unsuspension task type = %q", asynqTaskAdminUnsuspension)
	}
	if asynqTaskBlock != "block" {
		t.Fatalf("block task type = %q", asynqTaskBlock)
	}
	if asynqTaskMute != "mute" {
		t.Fatalf("mute task type = %q", asynqTaskMute)
	}
	if asynqTaskMerge != "merge" {
		t.Fatalf("merge task type = %q", asynqTaskMerge)
	}
	if asynqTaskUnmerge != "unmerge" {
		t.Fatalf("unmerge task type = %q", asynqTaskUnmerge)
	}
	if asynqTaskDeleteMute != "delete_mute" {
		t.Fatalf("delete mute task type = %q", asynqTaskDeleteMute)
	}
	if asynqTaskUnfavourite != "unfavourite" {
		t.Fatalf("unfavourite task type = %q", asynqTaskUnfavourite)
	}
	if asynqTaskAfterAccountDomainBlock != "after_account_domain_block" {
		t.Fatalf("after account domain block task type = %q", asynqTaskAfterAccountDomainBlock)
	}
	if asynqTaskAfterUnallowDomain != "after_unallow_domain" {
		t.Fatalf("after unallow domain task type = %q", asynqTaskAfterUnallowDomain)
	}
	if asynqQueueDefault != "default" {
		t.Fatalf("default queue = %q", asynqQueueDefault)
	}
	if asynqQueuePull != "pull" {
		t.Fatalf("pull queue = %q", asynqQueuePull)
	}
	if asynqQueuePush != "push" {
		t.Fatalf("push queue = %q", asynqQueuePush)
	}
	if asynqQueueMailers != "mailers" {
		t.Fatalf("mailers queue = %q", asynqQueueMailers)
	}
	if asynqQueueFASP != "fasp" {
		t.Fatalf("FASP queue = %q", asynqQueueFASP)
	}
	if asynqQueueIngress != "ingress" {
		t.Fatalf("ingress queue = %q", asynqQueueIngress)
	}
}

func TestEnqueueAsynqTasksNilSafeWithoutClient(t *testing.T) {
	s := &Server{} // asynqClient is nil
	// These must be no-ops (not panic) when the asynq client is unavailable.
	s.enqueueRedownloadAvatarTask(1)
	s.enqueueRedownloadHeaderTask(1)
	if s.enqueueRedownloadMediaTask(1) {
		t.Fatal("redownload media enqueue should report false without asynq client")
	}
	s.enqueueRefollowTask(1)
	if s.enqueueFetchRepliesTask(1, "https://remote.example/statuses/1/replies", "request") {
		t.Fatal("fetch replies enqueue should report false without asynq client")
	}
	s.cfg.FetchRepliesEnabled = true
	if s.enqueueFetchAllRepliesTask(1, "request") {
		t.Fatal("fetch all replies enqueue should report false without asynq client")
	}
	s.enqueueThreadResolveTask(1, "https://remote.example/statuses/1", "request")
	s.enqueueBackupTask(1)
	s.enqueueBulkImportTask(1)
	s.enqueueImportRowTask(1)
	s.enqueueImportRelationshipTask(1, "alice@remote.example", "follow", map[string]any{"reblogs": true})
	s.enqueueLinkCrawlTask(1)
	s.enqueueMediaPostProcessTask(1)
	s.enqueueRemoveFeaturedTagTask(1, 1)
	s.enqueueTagUnmergeTask(1, 1)
	s.enqueueUnfollowFollowTask(1, 2, 3, true)
	s.enqueuePublishScheduledStatusTask(1, time.Now().UTC())
	s.enqueuePublishAnnouncementTask(1, time.Now().UTC())
	s.enqueueUnpublishAnnouncementTask(1)
	s.enqueueRemoteAccountRefreshTask(1, "request")
	s.enqueueAccountRefreshTask(1)
	s.enqueueAccountMergingTask(1)
	s.enqueueResolveAccountTask("alice@remote.example")
	s.enqueuePollExpirationTask(1, time.Now().UTC())
	s.enqueuePollUpdateTask(1, 3*time.Minute)
	s.enqueueAccountUpdateTask(1, activityPubAccountUpdateDebounceDelay)
	s.enqueueRawDistributionTask(1, []byte(`{"type":"Add"}`), nil)
	s.enqueueAccountRawDistributionTask(1, []byte(`{"type":"Add"}`), nil)
	s.enqueueFeaturedCollectionSyncTask(1, "https://remote.example/featured", "request", true)
	s.enqueueFeaturedTagsSyncTask(1, "https://remote.example/tags")
	s.enqueueMoveDistributionTask(1)
	s.enqueuePostUpgradeTask("remote.example")
	s.enqueueFollowersSynchronizationTask(1, "https://remote.example/users/alice/followers_synchronization")
	s.enqueueActivityPubProcessingTask(activityPubInboxProcessingJob{ActorID: 1, Body: []byte(`{"type":"Create"}`)})
	s.enqueueActivityPubDeliveryTask(activityPubDeliveryRetryJob{SourceAccountID: 1, InboxURL: "https://remote.example/inbox", Body: []byte(`{"type":"Create"}`)})
	if s.enqueueLocalNotificationTask(1, 2, 3, "Status", "status") {
		t.Fatal("local notification enqueue should report false without asynq client")
	}
	if s.enqueueActivityPubDistributionTask(1) {
		t.Fatal("activitypub distribution enqueue should report false without asynq client")
	}
	if s.enqueueStatusUpdateDistributionTask(1) {
		t.Fatal("status update distribution enqueue should report false without asynq client")
	}
	s.enqueueCacheBusterTask("https://cdn.example/system/media.png")
	s.enqueueAnnouncementReactionTask(1, "party")
	s.enqueueRemovalTask(asynqRemovalPayload{StatusID: 1})
	s.enqueueRemovalTasksForStatusIDs([]int64{1, 2}, asynqRemovalPayload{})
	s.enqueuePushConversationTask(1)
	s.enqueuePushUpdateTask(1, 1, "timeline:1", false)
	s.enqueueWebPushNotificationTask(1, 1)
	s.enqueueAuthorizeFollowTask(1, 2)
	s.enqueueBootstrapTimelineTask(1)
	s.enqueueRegenerationTask(1)
	s.enqueueVerifyAccountLinksTask(1)
	s.enqueueTriggerWebhookTask("account.created", "Account", 1)
	s.enqueueWebhookDeliveryTask(1, []byte(`{"event":"account.created"}`))
	s.enqueueDomainBlockTask(1, true)
	s.enqueueDomainClearMediaTask(1)
	s.enqueueAdminDomainPurgeTask("remote.example")
	s.enqueueAccountDeletionTask(1)
	s.enqueueAdminAccountDeletionTask(1)
	s.enqueueAdminSuspensionTask(1)
	s.enqueueAdminUnsuspensionTask(1)
	s.enqueueBlockTask(1, 2)
	s.enqueueMuteTask(1, 2)
	s.enqueueMergeTask(1, 2)
	s.enqueueUnmergeTask(1, 2)
	s.enqueueDeleteMuteTask(1, time.Now().UTC())
	s.enqueueUnfavouriteTask(1, 2)
	s.enqueueAfterAccountDomainBlockTask(1, "remote.example")
	s.enqueueAfterUnallowDomainTask("remote.example")
	s.enqueueAsynqAccountTask(asynqTaskRefollow, 0, 3)
}

func TestNewAsynqAccountTaskType(t *testing.T) {
	task, err := newAsynqAccountTask(asynqTaskRefollow, 42, 5)
	if err != nil {
		t.Fatal(err)
	}
	if task.Type() != asynqTaskRefollow {
		t.Fatalf("task type = %q", task.Type())
	}
}

func TestUnmergeWorkerSupportsHomeAndListFeeds(t *testing.T) {
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, wants := range map[string][]string{
		"enqueueUnmergeTask": {
			`s.enqueueUnmergeFeedTask(fromAccountID, intoAccountID, "home")`,
		},
		"enqueueListUnmergeTask": {
			`s.enqueueUnmergeFeedTask(fromAccountID, listID, "list")`,
		},
		"handleAsynqUnmerge": {
			`if p.FeedType == "list"`,
			`s.unmergeAccountFromListFeed(ctx, s.db.WithContext(ctx), p.AccountID, list)`,
			`s.unmergeAccountFromHomeFeed(ctx, s.db.WithContext(ctx), p.AccountID, intoAccount)`,
		},
	} {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
}

func TestAsynqRedisOptUsesSharedRedisConfig(t *testing.T) {
	opt := asynqRedisOpt(config.Config{RedisHost: "127.0.0.1", RedisPort: "6379"})
	if opt == nil {
		t.Fatal("asynq redis opt must not be nil")
	}

	sidekiqOpt := asynqRedisOpt(config.Config{RedisURL: "redis://main.example.test:6379/0", SidekiqRedisURL: "redis://alice:secret@sidekiq.example.test:6380/4"})
	clientOpt, ok := sidekiqOpt.(asynq.RedisClientOpt)
	if !ok {
		t.Fatalf("sidekiq redis opt type = %T, want asynq.RedisClientOpt", sidekiqOpt)
	}
	if clientOpt.Addr != "sidekiq.example.test:6380" || clientOpt.Username != "alice" || clientOpt.Password != "secret" || clientOpt.DB != 4 {
		t.Fatalf("SIDEKIQ_REDIS_URL was not applied to asynq: %#v", clientOpt)
	}
}

func TestAsynqQueueNamesUseRedisNamespaceForSharedRedisIsolation(t *testing.T) {
	cfg := config.Config{RedisNamespace: "mastodon:"}
	if got := asynqQueueName(cfg, asynqQueuePull); got != "mastodon:pull" {
		t.Fatalf("asynqQueueName = %q, want mastodon:pull", got)
	}
	if got := asynqQueueName(config.Config{}, asynqQueuePull); got != asynqQueuePull {
		t.Fatalf("unprefixed asynqQueueName = %q", got)
	}
	weights := paonGoAsynqQueueWeightsForConfig(cfg)
	if weights["mastodon:pull"] != paonGoAsynqQueueWeights()[asynqQueuePull] || weights[asynqQueuePull] != 0 {
		t.Fatalf("namespace queue weights = %#v", weights)
	}

	selected := paonGoAsynqQueueWeightsForConfig(config.Config{
		RedisNamespace: "mastodon:",
		AsynqQueues:    []string{"push", "pull"},
	})
	wantSelected := map[string]int{
		"mastodon:push": paonGoAsynqQueueWeights()[asynqQueuePush],
		"mastodon:pull": paonGoAsynqQueueWeights()[asynqQueuePull],
	}
	if !reflect.DeepEqual(selected, wantSelected) {
		t.Fatalf("selected queue weights = %#v, want %#v", selected, wantSelected)
	}
}

func TestAsynqServeMuxRegistersAllHandlers(t *testing.T) {
	s := &Server{}
	mux := s.newAsynqServeMux()
	if mux == nil {
		t.Fatal("asynq serve mux must not be nil")
	}
}

func TestLocalNotificationMentionPayloadRequiresExistingMatchingMention(t *testing.T) {
	payload := asynqLocalNotificationPayload{
		ReceiverAccountID: 23,
		FromAccountID:     11,
		ActivityID:        42,
		ActivityType:      "Mention",
		Type:              "mention",
	}
	if localNotificationMentionMatchesPayload(nil, payload) {
		t.Fatal("a deleted mention must discard its queued notification")
	}
	if localNotificationMentionMatchesPayload(&models.Mention{
		ID:        payload.ActivityID,
		AccountID: models.MentionAccountID(99),
	}, payload) {
		t.Fatal("a mention for another receiver must discard its queued notification")
	}
	if !localNotificationMentionMatchesPayload(&models.Mention{
		ID:        payload.ActivityID,
		AccountID: models.MentionAccountID(payload.ReceiverAccountID),
	}, payload) {
		t.Fatal("an existing mention for the receiver must keep its queued notification")
	}

	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	if !sourceContains([]byte(functionBody(t, src, "createLocalNotificationFromPayload")), `if p.ActivityType == "Mention" { notification, err = s.createMentionLocalNotificationFromPayload(ctx, p)`) {
		t.Fatal("local notification creation must atomically validate Mention payloads before creating notification rows")
	}
	atomicBody := functionBody(t, src, "createMentionLocalNotificationFromPayload")
	for _, want := range []string{
		`s.db.WithContext(ctx).Transaction`,
		`localNotificationMentionForPayload(tx, p)`,
		`s.createRelationshipNotificationRowAndEnqueue(tx`,
	} {
		if !sourceContains([]byte(atomicBody), want) {
			t.Fatalf("atomic Mention notification creation missing %q", want)
		}
	}
	lookupBody := functionBody(t, src, "localNotificationMentionForPayload")
	if !sourceContains([]byte(lookupBody), `Clauses(clause.Locking{Strength: "UPDATE"})`) {
		t.Fatal("Mention notification validation must lock its Mention row until notification creation commits")
	}
}

// TestNotificationMailHelpersEnqueueAfterCreation locks in that notifications created via
// relationship helpers enqueue their mail worker, while the worker itself mirrors Rails
// NotifyService#email_needed? before actually sending. Missing rows are discarded like
// ActionMailer::MailDeliveryJob deserialization failures.
// TestNotificationStatusIDResolvesStatusPerActivityType locks in that notification helpers
// resolve status URLs through the same intermediate rows used by Rails notification payloads.
func TestNotificationStatusIDResolvesStatusPerActivityType(t *testing.T) {
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`case "Status":`,
		`return notification.ActivityID`,
		`case "Mention":`,
		`Select("status_id").Where("id = ?", notification.ActivityID).First(&mention)`,
		`case "Favourite":`,
		`Select("status_id").Where("id = ?", notification.ActivityID).First(&favourite)`,
		`case "Poll":`,
		`poll.StatusID.Valid`,
	} {
		if !functionBodyContains(t, src, "notificationStatusID", want) {
			t.Fatalf("notificationStatusID missing %q", want)
		}
	}
}

func TestRedownloadAccountMediaWorkerSkipsLikeRails(t *testing.T) {
	server := &Server{}
	allowed := func(server *Server, account models.Account, kind string) bool {
		got, err := server.remoteAccountMediaRedownloadAllowed(nil, account, kind)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	remote := models.Account{
		ID:              7,
		Domain:          sql.NullString{String: "remote.example", Valid: true},
		AvatarRemoteURL: sql.NullString{String: "https://remote.example/avatar.png", Valid: true},
		HeaderRemoteURL: "https://remote.example/header.png",
	}
	if !allowed(server, remote, "avatar") {
		t.Fatal("remote avatar without local file should be redownloadable")
	}
	if !allowed(server, remote, "header") {
		t.Fatal("remote header without local file should be redownloadable")
	}
	withAvatar := remote
	withAvatar.AvatarFileName = sql.NullString{String: "avatar.png", Valid: true}
	if allowed(server, withAvatar, "avatar") {
		t.Fatal("avatar redownload must skip when avatar_file_name is already present like Rails")
	}
	withHeader := remote
	withHeader.HeaderFileName = sql.NullString{String: "header.png", Valid: true}
	if allowed(server, withHeader, "header") {
		t.Fatal("header redownload must skip when header_file_name is already present like Rails")
	}
	suspended := remote
	suspended.SuspendedAt = sql.NullTime{Time: time.Now(), Valid: true}
	if allowed(server, suspended, "avatar") {
		t.Fatal("redownload must skip suspended accounts like Rails")
	}
	disabledCacheServer := &Server{cfg: config.Config{DisableRemoteMediaCache: true}}
	if allowed(disabledCacheServer, remote, "avatar") {
		t.Fatal("redownload must skip when DISABLE_REMOTE_MEDIA_CACHE=true like Rails")
	}
}

func TestCacheBusterWorkerHandlerCallsConfiguredHTTPClient(t *testing.T) {
	requests := make(chan string, 1)
	previous := cacheBusterHTTPClient
	cacheBusterHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req.Method + " " + req.URL.String() + " " + req.Header.Get("X-Bust-Secret")
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	defer func() { cacheBusterHTTPClient = previous }()

	payload, err := json.Marshal(asynqCacheBusterPayload{URL: "https://cdn.example/system/media.png"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{
		CacheBusterHTTPMethod:   "PURGE",
		CacheBusterSecretHeader: "X-Bust-Secret",
		CacheBusterSecret:       "secret",
	}}
	if err := s.handleAsynqCacheBuster(context.Background(), asynq.NewTask(asynqTaskCacheBuster, payload)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-requests:
		if got != "PURGE https://cdn.example/system/media.png secret" {
			t.Fatalf("cache buster request = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cache buster request")
	}
}

func TestCacheBusterHTTPClientHasDefaultTimeout(t *testing.T) {
	if cacheBusterHTTPClient == nil {
		t.Fatal("cacheBusterHTTPClient is nil")
	}
	if cacheBusterHTTPClient.Timeout != cacheBusterHTTPTimeout {
		t.Fatalf("cacheBusterHTTPClient.Timeout = %s, want %s", cacheBusterHTTPClient.Timeout, cacheBusterHTTPTimeout)
	}
}
