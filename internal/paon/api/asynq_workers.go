package api

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// asynq task types mirroring Rails Sidekiq workers. Queue names match Rails where sensible
// (redownload avatar/header and refollow run on Rails' "pull" queue). This gives Paon a
// Sidekiq-like Redis-backed async job queue (github.com/hibiken/asynq) for jobs that must
// not block request handlers and need retry semantics.
const (
	asynqTaskRedownloadAvatar            = "redownload:avatar"
	asynqTaskRedownloadHeader            = "redownload:header"
	asynqTaskRedownloadMedia             = "redownload:media"
	asynqTaskRefollow                    = "refollow"
	asynqTaskFetchReply                  = "fetch:reply"
	asynqTaskFetchReplies                = "fetch:replies"
	asynqTaskThreadResolve               = "thread:resolve"
	asynqTaskMentionResolve              = "mention:resolve"
	asynqTaskFeedInsert                  = "feed:insert"
	asynqTaskLocalNotification           = "local_notification"
	asynqTaskFilteredNotificationCleanup = "notification:filtered_cleanup"
	asynqTaskUnfilterNotifications       = "notification:unfilter"
	asynqTaskGenerateAnnualReport        = "annual_report:generate"
	asynqTaskNotificationMail            = "notification:mail"
	asynqTaskConfirmationMail            = "confirmation:mail"
	asynqTaskMailerDelivery              = "mailer:delivery"
	asynqTaskBackup                      = "backup"
	asynqTaskBulkImport                  = "bulk_import"
	asynqTaskLegacyImport                = "import"
	asynqTaskImportRow                   = "import:row"
	asynqTaskImportRelationship          = "import:relationship"
	asynqTaskLinkCrawl                   = "link_crawl"
	asynqTaskPostProcessMedia            = "post_process_media"
	asynqTaskRemoveFeaturedTag           = "remove_featured_tag"
	asynqTaskTagUnmerge                  = "tag_unmerge"
	asynqTaskUnfollowFollow              = "unfollow_follow"
	asynqTaskPublishScheduledStatus      = "publish:scheduled_status"
	asynqTaskPublishAnnouncement         = "publish:announcement"
	asynqTaskUnpublishAnnouncement       = "unpublish:announcement"
	asynqTaskRemoteAccountRefresh        = "remote_account:refresh"
	asynqTaskAccountRefresh              = "account:refresh"
	asynqTaskAccountMerging              = "account:merging"
	asynqTaskResolveAccount              = "resolve:account"
	asynqTaskPollExpiration              = "poll:expiration"
	asynqTaskPollUpdate                  = "poll:update"
	asynqTaskAccountUpdate               = "account:update"
	asynqTaskRawDistribution             = "raw_distribution"
	asynqTaskAccountRawDistribution      = "account:raw_distribution"
	asynqTaskFeaturedCollectionSync      = "featured_collection:sync"
	asynqTaskFeaturedTagsSync            = "featured_tags:sync"
	asynqTaskMoveDistribution            = "move:distribution"
	asynqTaskPostUpgrade                 = "post_upgrade"
	asynqTaskFollowersSync               = "followers_synchronization"
	asynqTaskActivityPubProcessing       = "activitypub:processing"
	asynqTaskActivityPubDelivery         = "activitypub:delivery"
	asynqTaskActivityPubDistribution     = "activitypub:distribution"
	asynqTaskStatusUpdateDistribution    = "activitypub:status_update_distribution"
	asynqTaskCacheBuster                 = "cache_buster"
	asynqTaskAnnouncementReaction        = "announcement:reaction"
	asynqTaskRemoval                     = "removal"
	asynqTaskPushConversation            = "push:conversation"
	asynqTaskPushUpdate                  = "push:update"
	asynqTaskWebPushNotification         = "web:push_notification"
	asynqTaskAuthorizeFollow             = "authorize_follow"
	asynqTaskBootstrapTimeline           = "bootstrap_timeline"
	asynqTaskRegeneration                = "regeneration"
	asynqTaskVerifyAccountLinks          = "verify_account_links"
	asynqTaskTriggerWebhook              = "trigger_webhook"
	asynqTaskWebhookDelivery             = "webhook:delivery"
	asynqTaskDomainBlock                 = "domain_block"
	asynqTaskDomainClearMedia            = "domain_clear_media"
	asynqTaskAdminDomainPurge            = "admin:domain_purge"
	asynqTaskAccountDeletion             = "account_deletion"
	asynqTaskAdminAccountDeletion        = "admin:account_deletion"
	asynqTaskAdminSuspension             = "admin:suspension"
	asynqTaskAdminUnsuspension           = "admin:unsuspension"
	asynqTaskBlock                       = "block"
	asynqTaskMute                        = "mute"
	asynqTaskMerge                       = "merge"
	asynqTaskUnmerge                     = "unmerge"
	asynqTaskDeleteMute                  = "delete_mute"
	asynqTaskUnfavourite                 = "unfavourite"
	asynqTaskAfterUnallowDomain          = "after_unallow_domain"
	asynqQueueDefault                    = "default"
	asynqQueuePull                       = "pull"
	asynqQueuePush                       = "push"
	asynqQueueMailers                    = "mailers"
	asynqQueueIngress                    = "ingress"
	railsSidekiqUniqueDefaultLockTTL     = 50 * 24 * time.Hour
)

const asynqTaskAfterAccountDomainBlock = "after_account_domain_block"

const activityPubAccountUpdateDebounceDelay = 5 * time.Second
const featuredCollectionWorkerTimeout = 2 * time.Minute

func asynqEnqueueAccepted(err error) bool {
	return err == nil || errors.Is(err, asynq.ErrDuplicateTask) || errors.Is(err, asynq.ErrTaskIDConflict)
}

func workerLookupError(operation string, err error) error {
	if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// asynqAccountPayload is the JSON payload for account-scoped asynq tasks.
type asynqAccountPayload struct {
	Version       int    `json:"version"`
	AccountID     int64  `json:"account_id"`
	OldPrivateKey string `json:"old_private_key,omitempty"`
}

// asynqFetchReplyPayload mirrors Rails FetchReplyWorker.perform(child_url, options):
// it fetches a single remote reply status by URI with retry semantics.
type asynqFetchReplyPayload struct {
	URI       string `json:"uri"`
	RequestID string `json:"request_id,omitempty"`
}

// asynqFetchRepliesPayload mirrors ActivityPub::FetchRepliesWorker.perform:
// fetch a replies collection for one parent status, then enqueue FetchReplyWorker jobs.
type asynqFetchRepliesPayload struct {
	ParentStatusID int64  `json:"parent_status_id"`
	CollectionURI  string `json:"collection_uri"`
	RequestID      string `json:"request_id,omitempty"`
}

// asynqThreadResolvePayload mirrors ThreadResolveWorker.perform(child_status_id,
// parent_url, options).
type asynqThreadResolvePayload struct {
	ChildStatusID int64  `json:"child_status_id"`
	ParentURL     string `json:"parent_url"`
	RequestID     string `json:"request_id,omitempty"`
}

const asynqPayloadVersion43 = 1

// validateAsynqPayloadVersion keeps jobs already queued by an older Paon
// binary readable while reserving positive versions for explicit dispatch.
// Version zero is the legacy, versionless JSON shape produced before the 4.3
// upgrade. Unknown future versions are permanent failures rather than retrying
// a payload with semantics this binary does not understand.
func validateAsynqPayloadVersion(name string, version int) error {
	if version == 0 || version == asynqPayloadVersion43 {
		return nil
	}
	return fmt.Errorf("%s payload version %d: %w", name, version, asynq.SkipRetry)
}

// marshalAsynqTaskPayload adds the Paon payload contract version at the Asynq
// boundary. Some payload structs are also persisted by the Redis fallback
// queues, so the version belongs to the Asynq representation rather than the
// shared in-memory struct.
func marshalAsynqTaskPayload(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("payload is not a JSON object")
		}
		return nil, fmt.Errorf("asynq payload: %w", err)
	}
	object["version"] = json.RawMessage(strconv.Itoa(asynqPayloadVersion43))
	return json.Marshal(object)
}

// validateAsynqTaskPayloadVersion is intentionally a worker-boundary check.
// Versionless payloads already queued by Paon 4.2 remain readable, while a
// newer producer cannot be retried indefinitely by a worker that does not
// understand its payload semantics.
func validateAsynqTaskPayloadVersion(task *asynq.Task) error {
	if task == nil {
		return nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(task.Payload(), &envelope); err != nil {
		// Let the concrete handler retain its existing invalid-JSON error.
		return nil
	}
	rawVersion, ok := envelope["version"]
	if !ok {
		return validateAsynqPayloadVersion(task.Type(), 0)
	}
	var version *int
	if err := json.Unmarshal(rawVersion, &version); err != nil || version == nil {
		return fmt.Errorf("%s payload has an invalid version: %w", task.Type(), asynq.SkipRetry)
	}
	return validateAsynqPayloadVersion(task.Type(), *version)
}

func asynqPayloadVersionMiddleware() asynq.MiddlewareFunc {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
			if err := validateAsynqTaskPayloadVersion(task); err != nil {
				return err
			}
			return next.ProcessTask(ctx, task)
		})
	}
}

// Version is explicit so future payload changes can be dispatched without
// interpreting old retry/dead-letter entries using a new shape.
type asynqMentionResolvePayload struct {
	Version   int    `json:"version"`
	StatusID  int64  `json:"status_id"`
	URI       string `json:"uri"`
	RequestID string `json:"request_id,omitempty"`
}

type asynqNotificationPairPayload struct {
	Version       int   `json:"version"`
	AccountID     int64 `json:"account_id"`
	FromAccountID int64 `json:"from_account_id"`
}

type asynqAnnualReportPayload struct {
	Version   int   `json:"version"`
	AccountID int64 `json:"account_id"`
	Year      int   `json:"year"`
}

// asynqResolveAccountPayload mirrors ResolveAccountWorker.perform(uri).
type asynqResolveAccountPayload struct {
	URI string `json:"uri"`
}

// asynqFeedInsertPayload mirrors Rails FeedInsertWorker.perform(status_id, id, type, options):
// it inserts a single status into one recipient's home/tags or list feed.
type asynqFeedInsertPayload struct {
	StatusID         int64  `json:"status_id"`
	FeedType         string `json:"feed_type"`
	FeedID           int64  `json:"feed_id"`
	AggregateReblogs bool   `json:"aggregate_reblogs"`
	Update           bool   `json:"update,omitempty"`
}

type feedInsertFilterResult uint8

const (
	feedInsertFilterNone feedInsertFilterResult = iota
	feedInsertFilterStatus
	feedInsertSkipHome
)

// asynqLocalNotificationPayload mirrors LocalNotificationWorker.perform(receiver_account_id,
// activity_id, activity_class_name, type).
type asynqLocalNotificationPayload struct {
	ReceiverAccountID int64  `json:"receiver_account_id"`
	FromAccountID     int64  `json:"from_account_id,omitempty"`
	ActivityID        int64  `json:"activity_id"`
	ActivityType      string `json:"activity_type"`
	Type              string `json:"type"`
}

// asynqNotificationMailPayload mirrors Rails LocalNotificationWorker -> NotifyService#send_email!:
// it sends the notification e-mail for one created notification.
type asynqNotificationMailPayload struct {
	NotificationID int64 `json:"notification_id"`
}

// asynqConfirmationMailPayload mirrors Devise's deliver_later arguments. The
// raw token is required because only its digest is stored; user data is reloaded.
type asynqConfirmationMailPayload struct {
	UserID int64  `json:"user_id"`
	Token  string `json:"token"`
}

type asynqMailerDeliveryPayload struct {
	UserID      int64       `json:"user_id"`
	Eligibility string      `json:"eligibility"`
	Message     mailMessage `json:"message"`
}

// asynqStatusPayload is used by status-scoped workers such as
// ActivityPub::DistributePollUpdateWorker.
type asynqStatusPayload struct {
	StatusID int64 `json:"status_id"`
}

// asynqPollPayload is used by poll-scoped workers such as
// PollExpirationNotifyWorker.
type asynqPollPayload struct {
	PollID int64 `json:"poll_id"`
}

type asynqScheduledStatusPayload struct {
	ScheduledStatusID int64 `json:"scheduled_status_id"`
}

type asynqAnnouncementPayload struct {
	AnnouncementID int64 `json:"announcement_id"`
}

// asynqAccountRawDistributionPayload mirrors ActivityPub::AccountRawDistributionWorker:
// the ActivityPub JSON is already serialized and signed, and the worker distributes it to
// the AccountReachFinder inbox set for the source account.
type asynqAccountRawDistributionPayload struct {
	SourceAccountID int64    `json:"source_account_id"`
	JSON            string   `json:"json"`
	ExcludeInboxes  []string `json:"exclude_inboxes,omitempty"`
}

// asynqRawDistributionPayload mirrors ActivityPub::RawDistributionWorker: the ActivityPub
// JSON is already serialized and signed, and the worker distributes it to followers.inboxes.
type asynqRawDistributionPayload struct {
	SourceAccountID int64    `json:"source_account_id"`
	JSON            string   `json:"json"`
	ExcludeInboxes  []string `json:"exclude_inboxes,omitempty"`
}

// asynqFeaturedCollectionPayload mirrors ActivityPub::SynchronizeFeaturedCollectionWorker.
type asynqFeaturedCollectionPayload struct {
	AccountID     int64  `json:"account_id"`
	CollectionURI string `json:"collection_uri,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	SyncTags      bool   `json:"sync_tags,omitempty"`
}

// asynqFeaturedTagsPayload mirrors ActivityPub::SynchronizeFeaturedTagsCollectionWorker.
type asynqFeaturedTagsPayload struct {
	AccountID     int64  `json:"account_id"`
	CollectionURI string `json:"collection_uri,omitempty"`
}

// asynqFollowersSynchronizationPayload mirrors ActivityPub::FollowersSynchronizationWorker.
type asynqFollowersSynchronizationPayload struct {
	Version   int    `json:"version"`
	AccountID int64  `json:"account_id"`
	URL       string `json:"url"`
}

// asynqMigrationPayload is used by account-migration workers such as
// ActivityPub::MoveDistributionWorker.
type asynqMigrationPayload struct {
	MigrationID int64 `json:"migration_id"`
}

// asynqDomainPayload is used by domain-scoped workers such as
// ActivityPub::PostUpgradeWorker.
type asynqDomainPayload struct {
	Domain string `json:"domain"`
}

// asynqAccountDomainPayload mirrors AfterAccountDomainBlockWorker.perform(account_id, domain).
type asynqAccountDomainPayload struct {
	AccountID int64  `json:"account_id"`
	Domain    string `json:"domain"`
}

// asynqBackupPayload mirrors BackupWorker.perform(backup_id).
type asynqBackupPayload struct {
	BackupID int64 `json:"backup_id"`
}

// asynqBulkImportPayload mirrors BulkImportWorker.perform(import_id).
type asynqBulkImportPayload struct {
	BulkImportID int64 `json:"bulk_import_id"`
}

// asynqLegacyImportPayload mirrors deprecated ImportWorker.perform(import_id).
type asynqLegacyImportPayload struct {
	ImportID int64 `json:"import_id"`
}

// asynqImportRowPayload mirrors Import::RowWorker.perform(row_id).
type asynqImportRowPayload struct {
	BulkImportRowID int64 `json:"bulk_import_row_id"`
}

// asynqImportRelationshipPayload mirrors deprecated Import::RelationshipWorker.perform.
type asynqImportRelationshipPayload struct {
	AccountID        int64          `json:"account_id"`
	TargetAccountURI string         `json:"target_account_uri"`
	Relationship     string         `json:"relationship"`
	Options          map[string]any `json:"options,omitempty"`
}

// asynqMediaPostProcessPayload mirrors PostProcessMediaWorker.perform(media_attachment_id).
type asynqMediaPostProcessPayload struct {
	MediaAttachmentID int64 `json:"media_attachment_id"`
}

// asynqMediaAttachmentPayload mirrors RedownloadMediaWorker.perform(media_attachment_id).
type asynqMediaAttachmentPayload struct {
	Version           int   `json:"version"`
	MediaAttachmentID int64 `json:"media_attachment_id"`
}

// asynqCacheBusterPayload mirrors CacheBusterWorker.perform(path). Go passes the fully
// resolved public asset URL because cache_buster.go already applies the Rails
// RoutingHelper#full_asset_url equivalent for filesystem and object-storage assets.
type asynqCacheBusterPayload struct {
	URL string `json:"url"`
}

// asynqAnnouncementReactionPayload mirrors PublishAnnouncementReactionWorker.perform.
type asynqAnnouncementReactionPayload struct {
	AnnouncementID int64  `json:"announcement_id"`
	Name           string `json:"name"`
}

// asynqRemovalPayload mirrors RemovalWorker.perform(status_id, options).
type asynqRemovalPayload struct {
	StatusID        int64 `json:"status_id"`
	Redraft         bool  `json:"redraft,omitempty"`
	Immediate       bool  `json:"immediate,omitempty"`
	Preserve        bool  `json:"preserve,omitempty"`
	OriginalRemoved bool  `json:"original_removed,omitempty"`
	SkipStreaming   bool  `json:"skip_streaming,omitempty"`
}

// asynqConversationPayload mirrors PushConversationWorker.perform.
type asynqConversationPayload struct {
	ConversationAccountID int64 `json:"conversation_account_id"`
}

// asynqPushUpdatePayload mirrors PushUpdateWorker.perform.
type asynqPushUpdatePayload struct {
	AccountID  int64  `json:"account_id"`
	StatusID   int64  `json:"status_id"`
	TimelineID string `json:"timeline_id,omitempty"`
	Update     bool   `json:"update,omitempty"`
}

// asynqStatusAccountPayload mirrors workers that receive account_id and status_id.
type asynqStatusAccountPayload struct {
	AccountID int64 `json:"account_id"`
	StatusID  int64 `json:"status_id"`
}

// asynqAuthorizeFollowPayload mirrors AuthorizeFollowWorker.perform.
type asynqAuthorizeFollowPayload struct {
	SourceAccountID int64 `json:"source_account_id"`
	TargetAccountID int64 `json:"target_account_id"`
}

// asynqRelationshipPayload mirrors simple relationship workers such as BlockWorker and
// MuteWorker, which receive account_id and target_account_id.
type asynqRelationshipPayload struct {
	AccountID       int64 `json:"account_id"`
	TargetAccountID int64 `json:"target_account_id"`
}

type asynqMutePayload struct {
	MuteID int64 `json:"mute_id"`
}

type asynqFeaturedTagPayload struct {
	AccountID     int64 `json:"account_id"`
	FeaturedTagID int64 `json:"featured_tag_id"`
}

type asynqTagUnmergePayload struct {
	TagID     int64 `json:"tag_id"`
	AccountID int64 `json:"account_id"`
}

// asynqUnfollowFollowPayload mirrors UnfollowFollowWorker.perform.
type asynqUnfollowFollowPayload struct {
	FollowerAccountID  int64 `json:"follower_account_id"`
	OldTargetAccountID int64 `json:"old_target_account_id"`
	NewTargetAccountID int64 `json:"new_target_account_id"`
	BypassLocked       bool  `json:"bypass_locked,omitempty"`
}

// asynqTriggerWebhookPayload mirrors TriggerWebhookWorker.perform.
type asynqTriggerWebhookPayload struct {
	Event     string `json:"event"`
	ClassName string `json:"class_name"`
	ID        int64  `json:"id"`
}

// asynqWebhookDeliveryPayload mirrors Webhooks::DeliveryWorker.perform.
type asynqWebhookDeliveryPayload struct {
	WebhookID int64           `json:"webhook_id"`
	Body      json.RawMessage `json:"body"`
}

// asynqWebPushNotificationPayload mirrors Web::PushNotificationWorker.perform.
type asynqWebPushNotificationPayload struct {
	SubscriptionID int64 `json:"subscription_id"`
	NotificationID int64 `json:"notification_id"`
}

// asynqDomainBlockPayload mirrors DomainBlockWorker.perform(domain_block_id, update=false).
type asynqDomainBlockPayload struct {
	DomainBlockID int64 `json:"domain_block_id"`
	Update        bool  `json:"update,omitempty"`
}

// asynqRemoteAccountRefreshPayload mirrors RemoteAccountRefreshWorker.perform(id):
// refresh a known remote account by its ActivityPub actor URI.
type asynqRemoteAccountRefreshPayload struct {
	AccountID int64  `json:"account_id"`
	RequestID string `json:"request_id,omitempty"`
}

// asynqAccountRefreshPayload mirrors AccountRefreshWorker.perform(account_id):
// refresh a stale known remote account through WebFinger/ResolveAccount semantics.
type asynqAccountRefreshPayload struct {
	AccountID int64 `json:"account_id"`
}

// asynqRedisOpt builds asynq Redis connection options from the shared redisConfig so the
// asynq client/server talk to the same Redis instance (and DB) as the rest of the app,
// matching the Rails+Sidekiq Redis backend.
func asynqRedisOpt(cfg config.Config) asynq.RedisConnOpt {
	if strings.TrimSpace(cfg.SidekiqRedisURL) != "" {
		cfg.RedisURL = cfg.SidekiqRedisURL
	}
	cfg.RedisSentinel = cfg.SidekiqRedisSentinel
	rc := redisConfig(cfg)
	var tlsConfig *tls.Config
	if rc.tls {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if strings.TrimSpace(rc.sentinelMaster) != "" && len(rc.sentinelAddrs) > 0 {
		return asynq.RedisFailoverClientOpt{
			MasterName:       rc.sentinelMaster,
			SentinelAddrs:    append([]string(nil), rc.sentinelAddrs...),
			SentinelUsername: rc.sentinelUsername,
			SentinelPassword: rc.sentinelPassword,
			Username:         rc.username,
			Password:         rc.password,
			DB:               rc.db,
			TLSConfig:        tlsConfig,
		}
	}
	return asynq.RedisClientOpt{
		Addr:      rc.address,
		Username:  rc.username,
		Password:  rc.password,
		DB:        rc.db,
		TLSConfig: tlsConfig,
	}
}

func newAsynqAccountTask(typ string, accountID int64, retry int) (*asynq.Task, error) {
	return newAsynqAccountTaskWithDelay(typ, accountID, retry, 0)
}

func newAsynqAccountTaskWithDelay(typ string, accountID int64, retry int, delay time.Duration) (*asynq.Task, error) {
	return newAsynqAccountTaskWithDelayAndQueue(typ, accountID, retry, delay, asynqQueuePull)
}

func newAsynqAccountTaskWithDelayAndQueue(typ string, accountID int64, retry int, delay time.Duration, queue string) (*asynq.Task, error) {
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return nil, err
	}
	options := []asynq.Option{asynq.Queue(queue), asynq.MaxRetry(retry)}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	return asynq.NewTask(typ, payload, options...), nil
}

// enqueueAsynqAccountTask enqueues an account-scoped task on the pull queue, mirroring
// Sidekiq's perform_async. It is a no-op when the asynq client is unavailable so callers
// can invoke it unconditionally.
func (s *Server) enqueueAsynqAccountTask(typ string, accountID int64, retry int) {
	s.enqueueAsynqAccountTaskWithDelay(typ, accountID, retry, 0)
}

func (s *Server) enqueueAsynqAccountTaskWithDelay(typ string, accountID int64, retry int, delay time.Duration) {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return
	}
	task, err := newAsynqAccountTaskWithDelayAndQueue(typ, accountID, retry, delay, s.asynqQueue(asynqQueuePull))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.asynqClient.EnqueueContext(ctx, task)
}

// enqueueRedownloadAvatarTask mirrors RedownloadAvatarWorker.perform_in(rand(30..600).seconds, id).
func (s *Server) enqueueRedownloadAvatarTask(accountID int64) {
	s.enqueueAsynqAccountTaskWithDelay(asynqTaskRedownloadAvatar, accountID, 7, remoteAccountMediaRedownloadDelay())
}

// enqueueRedownloadHeaderTask mirrors RedownloadHeaderWorker.perform_in(rand(30..600).seconds, id).
func (s *Server) enqueueRedownloadHeaderTask(accountID int64) {
	s.enqueueAsynqAccountTaskWithDelay(asynqTaskRedownloadHeader, accountID, 7, remoteAccountMediaRedownloadDelay())
}

// enqueueRedownloadMediaTask mirrors RedownloadMediaWorker.perform_in(..., media_id)
// on the pull queue with retry 3. It returns false when asynq is unavailable so callers
// can fall back to the older in-process retry queue.
func (s *Server) enqueueRedownloadMediaTask(mediaAttachmentID int64) bool {
	if s == nil || s.asynqClient == nil || mediaAttachmentID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqMediaAttachmentPayload{Version: asynqPayloadVersion43, MediaAttachmentID: mediaAttachmentID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskRedownloadMedia, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(3), asynq.ProcessIn(remoteMediaRedownloadDelay()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueRefollowTask mirrors RefollowWorker.perform_async (no retry).
func (s *Server) enqueueRefollowTask(accountID int64) {
	s.enqueueAsynqAccountTask(asynqTaskRefollow, accountID, 0)
}

// enqueueFetchReplyTask mirrors FetchReplyWorker.perform_async (queue pull, retry 3).
func (s *Server) enqueueFetchReplyTask(uri string, requestID string) {
	if s == nil || s.asynqClient == nil || strings.TrimSpace(uri) == "" {
		return
	}
	payload, err := marshalAsynqTaskPayload(asynqFetchReplyPayload{URI: uri, RequestID: requestID})
	if err != nil {
		return
	}
	task := asynq.NewTask(asynqTaskFetchReply, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(3))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.asynqClient.EnqueueContext(ctx, task)
}

// enqueueFetchRepliesTask mirrors ActivityPub::FetchRepliesWorker.perform_async
// (queue pull, retry 3). It returns false so callers can keep the previous
// request-adjacent fallback when asynq is unavailable.
func (s *Server) enqueueFetchRepliesTask(parentStatusID int64, collectionURI string, requestID string) bool {
	if s == nil || s.asynqClient == nil || parentStatusID == 0 || strings.TrimSpace(collectionURI) == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqFetchRepliesPayload{ParentStatusID: parentStatusID, CollectionURI: collectionURI, RequestID: requestID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskFetchReplies, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(3))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueThreadResolveTask mirrors ThreadResolveWorker.perform_async.
func (s *Server) enqueueThreadResolveTask(childStatusID int64, parentURL string, requestID string) bool {
	parentURL = activityPubHTTPURI(parentURL)
	if s == nil || s.asynqClient == nil || childStatusID == 0 || parentURL == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqThreadResolvePayload{ChildStatusID: childStatusID, ParentURL: parentURL, RequestID: requestID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskThreadResolve, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(3))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueMentionResolveTask mirrors MentionResolveWorker.perform_in(rand(30...600), ...).
// The database uniqueness constraint on (status_id, account_id) is the final
// idempotency boundary; Asynq uniqueness prevents duplicate remote fetches.
func (s *Server) enqueueMentionResolveTask(statusID int64, uri string, requestID string) bool {
	uri = activityPubHTTPURI(uri)
	if s == nil || s.asynqClient == nil || statusID == 0 || uri == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqMentionResolvePayload{Version: asynqPayloadVersion43, StatusID: statusID, URI: uri, RequestID: requestID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskMentionResolve, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task,
		asynq.Queue(s.asynqQueue(asynqQueuePull)),
		asynq.MaxRetry(7),
		asynq.Timeout(2*time.Minute),
		asynq.ProcessIn(mentionResolveDelay()),
		asynq.Unique(24*time.Hour),
	)
	return asynqEnqueueAccepted(err)
}

func mentionResolveDelay() time.Duration {
	return mentionResolveDelayWithRand(rand.Int63n)
}

func mentionResolveDelayWithRand(int63n func(int64) int64) time.Duration {
	if int63n == nil {
		return 30 * time.Second
	}
	return time.Duration(30+int63n(570)) * time.Second
}

func (s *Server) enqueueFilteredNotificationCleanupTask(accountID int64, fromAccountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || fromAccountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqNotificationPairPayload{Version: asynqPayloadVersion43, AccountID: accountID, FromAccountID: fromAccountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskFilteredNotificationCleanup, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25), asynq.Timeout(5*time.Minute), asynq.Unique(30*time.Minute))
	return asynqEnqueueAccepted(err)
}

// enqueueUnfilterNotificationsTask increments the completion barrier before
// enqueueing. A rejected/duplicate enqueue compensates the increment, so a
// duplicate request cannot leave notifications_merged permanently blocked.
func (s *Server) enqueueUnfilterNotificationsTask(ctx context.Context, accountID int64, fromAccountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || fromAccountID == 0 {
		return false
	}
	key := notificationUnfilterJobsRedisKey(s.cfg, accountID)
	if _, err := s.redisCommand(ctx, "INCR", key); err != nil {
		return false
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.FormatInt(int64((30*time.Minute)/time.Second), 10))
	payload, err := marshalAsynqTaskPayload(asynqNotificationPairPayload{Version: asynqPayloadVersion43, AccountID: accountID, FromAccountID: fromAccountID})
	if err != nil {
		_, _ = s.redisCommand(ctx, "DECR", key)
		return false
	}
	task := asynq.NewTask(asynqTaskUnfilterNotifications, payload)
	enqueueCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(enqueueCtx, task, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25), asynq.Timeout(15*time.Minute), asynq.Unique(30*time.Minute))
	if err != nil {
		_, _ = s.redisCommand(ctx, "DECR", key)
		return errors.Is(err, asynq.ErrDuplicateTask) || errors.Is(err, asynq.ErrTaskIDConflict)
	}
	return true
}

func (s *Server) enqueueGenerateAnnualReportTask(accountID int64, year int) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || year < 2000 || year > 9999 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAnnualReportPayload{Version: asynqPayloadVersion43, AccountID: accountID, Year: year})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskGenerateAnnualReport, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25), asynq.Timeout(30*time.Minute), asynq.Unique(50*24*time.Hour))
	return asynqEnqueueAccepted(err)
}

// enqueueFeedInsertTask mirrors FeedInsertWorker.perform_async: insert one status into one
// recipient's home or list feed on Sidekiq's default queue with the default retry limit.
func (s *Server) enqueueFeedInsertTask(statusID int64, feedType string, feedID int64, aggregateReblogs bool) bool {
	return s.enqueueFeedInsertTaskWithOptions(statusID, feedType, feedID, aggregateReblogs, false)
}

func (s *Server) enqueueFeedInsertUpdateTask(statusID int64, feedType string, feedID int64, aggregateReblogs bool) bool {
	return s.enqueueFeedInsertTaskWithOptions(statusID, feedType, feedID, aggregateReblogs, true)
}

func (s *Server) enqueueFeedInsertTaskWithOptions(statusID int64, feedType string, feedID int64, aggregateReblogs bool, update bool) bool {
	if s == nil || s.asynqClient == nil || statusID == 0 || feedID == 0 || feedType == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqFeedInsertPayload{StatusID: statusID, FeedType: feedType, FeedID: feedID, AggregateReblogs: aggregateReblogs, Update: update})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskFeedInsert, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueLocalNotificationTask mirrors LocalNotificationWorker.perform_async.
func (s *Server) enqueueLocalNotificationTask(receiverAccountID int64, fromAccountID int64, activityID int64, activityType string, kind string) bool {
	if s == nil || s.asynqClient == nil || receiverAccountID == 0 || activityID == 0 || strings.TrimSpace(activityType) == "" || strings.TrimSpace(kind) == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqLocalNotificationPayload{
		ReceiverAccountID: receiverAccountID,
		FromAccountID:     fromAccountID,
		ActivityID:        activityID,
		ActivityType:      activityType,
		Type:              kind,
	})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskLocalNotification, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueNotificationMailTask mirrors Rails LocalNotificationWorker -> send_email!: it
// enqueues a notification e-mail for one created notification on the mailers queue.
func (s *Server) enqueueNotificationMailTask(notificationID int64) {
	if s == nil || s.asynqClient == nil || notificationID == 0 {
		return
	}
	payload, err := marshalAsynqTaskPayload(asynqNotificationMailPayload{NotificationID: notificationID})
	if err != nil {
		return
	}
	task := asynq.NewTask(asynqTaskNotificationMail, payload, asynq.Queue(s.asynqQueue(asynqQueueMailers)), asynq.MaxRetry(5))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.asynqClient.EnqueueContext(ctx, task, asynq.ProcessIn(2*time.Minute))
}

func newAsynqConfirmationMailTask(userID int64, token string, queue string) (*asynq.Task, error) {
	token = strings.TrimSpace(token)
	if userID <= 0 || token == "" {
		return nil, fmt.Errorf("confirmation mail: user id and token are required")
	}
	payload, err := marshalAsynqTaskPayload(asynqConfirmationMailPayload{UserID: userID, Token: token})
	if err != nil {
		return nil, fmt.Errorf("confirmation mail payload: %w", err)
	}
	return asynq.NewTask(asynqTaskConfirmationMail, payload, asynq.Queue(queue), asynq.MaxRetry(25)), nil
}

// enqueueConfirmationMailTask mirrors User#send_devise_notification followed by
// ActionMailer deliver_later on Rails' mailers queue.
func (s *Server) enqueueConfirmationMailTask(userID int64, token string) error {
	if s == nil || s.asynqClient == nil {
		return fmt.Errorf("confirmation mail: asynq client is not configured")
	}
	task, err := newAsynqConfirmationMailTask(userID, token, s.asynqQueue(asynqQueueMailers))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.asynqClient.EnqueueContext(ctx, task); err != nil {
		return fmt.Errorf("enqueue confirmation mail: %w", err)
	}
	return nil
}

func newAsynqMailerDeliveryTask(userID int64, eligibility string, message mailMessage, queue string) (*asynq.Task, error) {
	if userID <= 0 || strings.TrimSpace(message.To) == "" || strings.TrimSpace(message.Subject) == "" {
		return nil, fmt.Errorf("mailer delivery: user, recipient, and subject are required")
	}
	switch eligibility {
	case "present", "security", "functional":
	default:
		return nil, fmt.Errorf("mailer delivery: unsupported eligibility %q", eligibility)
	}
	payload, err := marshalAsynqTaskPayload(asynqMailerDeliveryPayload{UserID: userID, Eligibility: eligibility, Message: message})
	if err != nil {
		return nil, fmt.Errorf("mailer delivery payload: %w", err)
	}
	return asynq.NewTask(asynqTaskMailerDelivery, payload, asynq.Queue(queue), asynq.MaxRetry(25)), nil
}

func (s *Server) enqueueMailerDeliveryTask(userID int64, eligibility string, message mailMessage) error {
	if s == nil || s.asynqClient == nil {
		return fmt.Errorf("mailer delivery: asynq client is not configured")
	}
	task, err := newAsynqMailerDeliveryTask(userID, eligibility, message, s.asynqQueue(asynqQueueMailers))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.asynqClient.EnqueueContext(ctx, task); err != nil {
		return fmt.Errorf("enqueue mailer delivery: %w", err)
	}
	return nil
}

// enqueueBackupTask mirrors Rails BackupWorker.perform_async (pull queue, retry 5).
func (s *Server) enqueueBackupTask(backupID int64) {
	if s == nil || s.asynqClient == nil || backupID == 0 {
		return
	}
	payload, err := marshalAsynqTaskPayload(asynqBackupPayload{BackupID: backupID})
	if err != nil {
		return
	}
	task := asynq.NewTask(asynqTaskBackup, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(5))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.asynqClient.EnqueueContext(ctx, task)
}

// enqueueBulkImportTask mirrors BulkImportWorker.perform_async(import_id).
func (s *Server) enqueueBulkImportTask(bulkImportID int64) bool {
	if s == nil || s.asynqClient == nil || bulkImportID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqBulkImportPayload{BulkImportID: bulkImportID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskBulkImport, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(0))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueLegacyImportTask mirrors deprecated ImportWorker.perform_async(import_id).
func (s *Server) enqueueLegacyImportTask(importID int64) bool {
	if s == nil || s.asynqClient == nil || importID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqLegacyImportPayload{ImportID: importID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskLegacyImport, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(0))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueImportRowTask mirrors Import::RowWorker.perform_async(row_id).
func (s *Server) enqueueImportRowTask(rowID int64) bool {
	if s == nil || s.asynqClient == nil || rowID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqImportRowPayload{BulkImportRowID: rowID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskImportRow, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(6))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueImportRelationshipTask mirrors deprecated Import::RelationshipWorker.perform_async.
func (s *Server) enqueueImportRelationshipTask(accountID int64, targetAccountURI string, relationship string, options map[string]any) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || strings.TrimSpace(targetAccountURI) == "" || strings.TrimSpace(relationship) == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqImportRelationshipPayload{
		AccountID:        accountID,
		TargetAccountURI: targetAccountURI,
		Relationship:     relationship,
		Options:          options,
	})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskImportRelationship, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(8))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueLinkCrawlTask mirrors LinkCrawlWorker.perform_async(status_id).
func (s *Server) enqueueLinkCrawlTask(statusID int64) bool {
	return s.enqueueLinkCrawlTaskWithDelay(statusID, 0)
}

// enqueueLinkCrawlTaskWithDelay mirrors LinkCrawlWorker.perform_in(delay, status_id).
func (s *Server) enqueueLinkCrawlTaskWithDelay(statusID int64, delay time.Duration) bool {
	if s == nil || s.asynqClient == nil || statusID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqStatusPayload{StatusID: statusID})
	if err != nil {
		return false
	}
	options := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(0)}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	task := asynq.NewTask(asynqTaskLinkCrawl, payload, options...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueMediaPostProcessTask mirrors PostProcessMediaWorker.perform_async.
func (s *Server) enqueueMediaPostProcessTask(mediaAttachmentID int64) bool {
	if s == nil || s.asynqClient == nil || mediaAttachmentID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqMediaPostProcessPayload{MediaAttachmentID: mediaAttachmentID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskPostProcessMedia, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(1))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueRemoveFeaturedTagTask mirrors RemoveFeaturedTagWorker.perform_async.
func (s *Server) enqueueRemoveFeaturedTagTask(accountID int64, featuredTagID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || featuredTagID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqFeaturedTagPayload{AccountID: accountID, FeaturedTagID: featuredTagID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskRemoveFeaturedTag, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueTagUnmergeTask mirrors TagUnmergeWorker.perform_async.
func (s *Server) enqueueTagUnmergeTask(tagID int64, accountID int64) bool {
	if s == nil || s.asynqClient == nil || tagID == 0 || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqTagUnmergePayload{TagID: tagID, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskTagUnmerge, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueUnfollowFollowTask mirrors UnfollowFollowWorker.perform_async on Rails'
// pull queue.
func (s *Server) enqueueUnfollowFollowTask(followerAccountID int64, oldTargetAccountID int64, newTargetAccountID int64, bypassLocked bool) bool {
	if s == nil || s.asynqClient == nil || followerAccountID == 0 || oldTargetAccountID == 0 || newTargetAccountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqUnfollowFollowPayload{
		FollowerAccountID:  followerAccountID,
		OldTargetAccountID: oldTargetAccountID,
		NewTargetAccountID: newTargetAccountID,
		BypassLocked:       bypassLocked,
	})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskUnfollowFollow, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueuePublishScheduledStatusTask mirrors PublishScheduledStatusWorker.perform_at.
func (s *Server) enqueuePublishScheduledStatusTask(scheduledStatusID int64, scheduledAt time.Time) bool {
	if s == nil || s.asynqClient == nil || scheduledStatusID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqScheduledStatusPayload{ScheduledStatusID: scheduledStatusID})
	if err != nil {
		return false
	}
	opts := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25), asynq.Unique(time.Hour)}
	if !scheduledAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(scheduledAt.UTC()))
	}
	task := asynq.NewTask(asynqTaskPublishScheduledStatus, payload, opts...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueuePublishAnnouncementTask mirrors PublishScheduledAnnouncementWorker.
func (s *Server) enqueuePublishAnnouncementTask(announcementID int64, scheduledAt time.Time) bool {
	if s == nil || s.asynqClient == nil || announcementID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAnnouncementPayload{AnnouncementID: announcementID})
	if err != nil {
		return false
	}
	opts := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25)}
	if !scheduledAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(scheduledAt.UTC()))
	}
	task := asynq.NewTask(asynqTaskPublishAnnouncement, payload, opts...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueUnpublishAnnouncementTask mirrors UnpublishAnnouncementWorker.
func (s *Server) enqueueUnpublishAnnouncementTask(announcementID int64) bool {
	if s == nil || s.asynqClient == nil || announcementID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAnnouncementPayload{AnnouncementID: announcementID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskUnpublishAnnouncement, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueCacheBusterTask mirrors CacheBusterWorker.perform_async (pull queue). It returns
// false when the async worker is unavailable so callers can fall back to synchronous busting.
func (s *Server) enqueueCacheBusterTask(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if s == nil || s.asynqClient == nil || rawURL == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqCacheBusterPayload{URL: rawURL})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskCacheBuster, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueAnnouncementReactionTask mirrors PublishAnnouncementReactionWorker.perform_async
// on Rails' default queue.
func (s *Server) enqueueAnnouncementReactionTask(announcementID int64, name string) bool {
	name = strings.TrimSpace(name)
	if s == nil || s.asynqClient == nil || announcementID == 0 || name == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAnnouncementReactionPayload{AnnouncementID: announcementID, Name: name})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAnnouncementReaction, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueRemovalTask mirrors RemovalWorker.perform_async on Rails' default queue.
func (s *Server) enqueueRemovalTask(payload asynqRemovalPayload) bool {
	if s == nil || s.asynqClient == nil || payload.StatusID == 0 {
		return false
	}
	raw, err := marshalAsynqTaskPayload(payload)
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskRemoval, raw, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

func (s *Server) enqueueRemovalTasksForStatusIDs(statusIDs []int64, options asynqRemovalPayload) bool {
	if s == nil || s.asynqClient == nil || len(statusIDs) == 0 {
		return false
	}
	for _, statusID := range statusIDs {
		payload := options
		payload.StatusID = statusID
		if !s.enqueueRemovalTask(payload) {
			return false
		}
	}
	return true
}

// enqueueActivityPubDeliveryTask mirrors ActivityPub::DeliveryWorker.perform_async and
// ActivityPub::LowPriorityDeliveryWorker for initial delivery. Asynq owns retries after
// accepting the task; the leased Redis ZSET is used only when Asynq was unavailable.
func (s *Server) enqueueActivityPubDeliveryTask(job activityPubDeliveryRetryJob) bool {
	if s == nil || s.asynqClient == nil || job.SourceAccountID == 0 || strings.TrimSpace(job.InboxURL) == "" || len(job.Body) == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(job)
	if err != nil {
		return false
	}
	queue := asynqQueuePush
	if job.RetryLimit == 8 {
		queue = asynqQueuePull
	}
	task := asynq.NewTask(asynqTaskActivityPubDelivery, payload, asynq.Queue(s.asynqQueue(queue)), asynq.MaxRetry(job.activityPubDeliveryRetryLimit()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueActivityPubDistributionTask mirrors ActivityPub::DistributionWorker.perform_async
// on Rails' push queue.
func (s *Server) enqueueActivityPubDistributionTask(statusID int64) bool {
	return s.enqueueActivityPubStatusDistributionTask(asynqTaskActivityPubDistribution, statusID)
}

// enqueueStatusUpdateDistributionTask mirrors ActivityPub::StatusUpdateDistributionWorker.
func (s *Server) enqueueStatusUpdateDistributionTask(statusID int64) bool {
	return s.enqueueActivityPubStatusDistributionTask(asynqTaskStatusUpdateDistribution, statusID)
}

func (s *Server) enqueueActivityPubStatusDistributionTask(taskType string, statusID int64) bool {
	if s == nil || s.asynqClient == nil || statusID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqStatusPayload{StatusID: statusID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(taskType, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePush)), asynq.MaxRetry(25))
	return err == nil
}

// enqueuePushConversationTask mirrors PushConversationWorker.perform_async on Rails'
// default queue.
func (s *Server) enqueuePushConversationTask(conversationAccountID int64) bool {
	if s == nil || s.asynqClient == nil || conversationAccountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqConversationPayload{ConversationAccountID: conversationAccountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskPushConversation, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueuePushUpdateTask mirrors PushUpdateWorker.perform_async on Rails' default queue.
func (s *Server) enqueuePushUpdateTask(accountID int64, statusID int64, timelineID string, update bool) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || statusID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqPushUpdatePayload{AccountID: accountID, StatusID: statusID, TimelineID: timelineID, Update: update})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskPushUpdate, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueWebPushNotificationTask mirrors Web::PushNotificationWorker.perform_async on
// Rails' push queue.
func (s *Server) enqueueWebPushNotificationTask(subscriptionID int64, notificationID int64) bool {
	if s == nil || s.asynqClient == nil || subscriptionID == 0 || notificationID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqWebPushNotificationPayload{SubscriptionID: subscriptionID, NotificationID: notificationID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskWebPushNotification, payload, asynq.Queue(s.asynqQueue(asynqQueuePush)), asynq.MaxRetry(5))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueAuthorizeFollowTask mirrors AuthorizeFollowWorker.perform_async on Rails'
// default queue. It accepts one pending follow request from source to target.
func (s *Server) enqueueAuthorizeFollowTask(sourceAccountID int64, targetAccountID int64) bool {
	if s == nil || s.asynqClient == nil || sourceAccountID == 0 || targetAccountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAuthorizeFollowPayload{SourceAccountID: sourceAccountID, TargetAccountID: targetAccountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAuthorizeFollow, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueBootstrapTimelineTask mirrors BootstrapTimelineWorker.perform_async on Rails'
// default queue. It runs approved-account onboarding side effects outside the request path.
func (s *Server) enqueueBootstrapTimelineTask(accountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskBootstrapTimeline, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueRegenerationTask mirrors RegenerationWorker.perform_async on Rails' default
// queue. The caller owns the account:<id>:regeneration Redis lock.
func (s *Server) enqueueRegenerationTask(accountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskRegeneration, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25), asynq.Unique(railsSidekiqUniqueDefaultLockTTL))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueueVerifyAccountLinksTask mirrors VerifyAccountLinksWorker.perform_async on
// Rails' default queue (retry false, until-executed one-hour lock).
func (s *Server) enqueueVerifyAccountLinksTask(accountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskVerifyAccountLinks, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(0), asynq.Unique(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueueTriggerWebhookTask mirrors TriggerWebhookWorker.perform_async on Rails'
// default queue.
func (s *Server) enqueueTriggerWebhookTask(event string, className string, id int64) bool {
	event = strings.TrimSpace(event)
	className = strings.TrimSpace(className)
	if s == nil || s.asynqClient == nil || event == "" || className == "" || id == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqTriggerWebhookPayload{Event: event, ClassName: className, ID: id})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskTriggerWebhook, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueueWebhookDeliveryTask mirrors Webhooks::DeliveryWorker.perform_async on Rails'
// push queue with retry 16.
func (s *Server) enqueueWebhookDeliveryTask(webhookID int64, body []byte) bool {
	if s == nil || s.asynqClient == nil || webhookID == 0 || len(body) == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqWebhookDeliveryPayload{WebhookID: webhookID, Body: append(json.RawMessage(nil), body...)})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskWebhookDelivery, payload, asynq.Queue(s.asynqQueue(asynqQueuePush)), asynq.MaxRetry(16))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueDomainBlockTask mirrors DomainBlockWorker.perform_async on Rails' default queue.
func (s *Server) enqueueDomainBlockTask(domainBlockID int64, update bool) bool {
	if s == nil || s.asynqClient == nil || domainBlockID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqDomainBlockPayload{DomainBlockID: domainBlockID, Update: update})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskDomainBlock, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueDomainClearMediaTask mirrors DomainClearMediaWorker.perform_async on Rails'
// pull queue.
func (s *Server) enqueueDomainClearMediaTask(domainBlockID int64) bool {
	if s == nil || s.asynqClient == nil || domainBlockID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqDomainBlockPayload{DomainBlockID: domainBlockID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskDomainClearMedia, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueAdminDomainPurgeTask mirrors Admin::DomainPurgeWorker.perform_async(domain).
func (s *Server) enqueueAdminDomainPurgeTask(domain string) bool {
	domain = normalizeDomain(domain)
	if s == nil || s.asynqClient == nil || domain == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqDomainPayload{Domain: domain})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAdminDomainPurge, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25), asynq.Unique(7*24*time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueueAccountDeletionTask mirrors AccountDeletionWorker.perform_async.
func (s *Server) enqueueAccountDeletionTask(accountID int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.enqueueAccountDeletionTaskContext(ctx, accountID) == nil
}

func (s *Server) enqueueAccountDeletionTaskContext(ctx context.Context, accountID int64) error {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return errors.New("account deletion queue is unavailable")
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(asynqTaskAccountDeletion, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25), asynq.Unique(7*24*time.Hour))
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	if asynqEnqueueAccepted(err) {
		return nil
	}
	return err
}

// enqueueAdminAccountDeletionTask mirrors Admin::AccountDeletionWorker.perform_async.
func (s *Server) enqueueAdminAccountDeletionTask(accountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAdminAccountDeletion, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25), asynq.Unique(7*24*time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueueAdminSuspensionTask mirrors Admin::SuspensionWorker.perform_async.
func (s *Server) enqueueAdminSuspensionTask(accountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAdminSuspension, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueueAdminUnsuspensionTask mirrors Admin::UnsuspensionWorker.perform_async.
func (s *Server) enqueueAdminUnsuspensionTask(accountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAdminUnsuspension, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueueBlockTask mirrors BlockWorker.perform_async on Rails' default queue.
func (s *Server) enqueueBlockTask(accountID int64, targetAccountID int64) bool {
	return s.enqueueRelationshipTask(asynqTaskBlock, accountID, targetAccountID)
}

// enqueueMuteTask mirrors MuteWorker.perform_async on Rails' default queue.
func (s *Server) enqueueMuteTask(accountID int64, targetAccountID int64) bool {
	return s.enqueueRelationshipTask(asynqTaskMute, accountID, targetAccountID)
}

// enqueueMergeTask mirrors MergeWorker.perform_async(from_account_id, into_account_id).
func (s *Server) enqueueMergeTask(fromAccountID int64, intoAccountID int64) bool {
	return s.enqueueRelationshipTask(asynqTaskMerge, fromAccountID, intoAccountID)
}

// enqueueUnmergeTask mirrors UnmergeWorker.perform_async(from_account_id, into_account_id)
// on Rails' pull queue.
func (s *Server) enqueueUnmergeTask(fromAccountID int64, intoAccountID int64) bool {
	if s == nil || s.asynqClient == nil || fromAccountID == 0 || intoAccountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqRelationshipPayload{AccountID: fromAccountID, TargetAccountID: intoAccountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskUnmerge, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

func (s *Server) enqueueRelationshipTask(taskType string, accountID int64, targetAccountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || targetAccountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqRelationshipPayload{AccountID: accountID, TargetAccountID: targetAccountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(taskType, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueDeleteMuteTask mirrors DeleteMuteWorker.perform_at.
func (s *Server) enqueueDeleteMuteTask(muteID int64, processAt time.Time) bool {
	if s == nil || s.asynqClient == nil || muteID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqMutePayload{MuteID: muteID})
	if err != nil {
		return false
	}
	opts := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25)}
	if !processAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(processAt.UTC()))
	}
	task := asynq.NewTask(asynqTaskDeleteMute, payload, opts...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueUnfavouriteTask mirrors UnfavouriteWorker.perform_async on Rails' default queue.
func (s *Server) enqueueUnfavouriteTask(accountID int64, statusID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || statusID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqStatusAccountPayload{AccountID: accountID, StatusID: statusID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskUnfavourite, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueAfterAccountDomainBlockTask mirrors AfterAccountDomainBlockWorker.perform_async.
func (s *Server) enqueueAfterAccountDomainBlockTask(accountID int64, domain string) bool {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if s == nil || s.asynqClient == nil || accountID == 0 || domain == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountDomainPayload{AccountID: accountID, Domain: domain})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAfterAccountDomainBlock, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueAfterUnallowDomainTask mirrors AfterUnallowDomainWorker.perform_async(domain).
func (s *Server) enqueueAfterUnallowDomainTask(domain string) bool {
	domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	if s == nil || s.asynqClient == nil || domain == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqDomainPayload{Domain: domain})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAfterUnallowDomain, payload, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueRemoteAccountRefreshTask mirrors RemoteAccountRefreshWorker.perform_async
// (pull queue, retry 3).
func (s *Server) enqueueRemoteAccountRefreshTask(accountID int64, requestID string) {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return
	}
	payload, err := marshalAsynqTaskPayload(asynqRemoteAccountRefreshPayload{AccountID: accountID, RequestID: requestID})
	if err != nil {
		return
	}
	task := asynq.NewTask(asynqTaskRemoteAccountRefresh, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(3))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.asynqClient.EnqueueContext(ctx, task)
}

// enqueueAccountRefreshTask mirrors Account#schedule_refresh_if_stale! /
// AccountRefreshWorker.perform_in(rand(6.hours), id). Unique prevents duplicate stale
// refresh jobs for the same account while the Rails lock would be held.
func (s *Server) enqueueAccountRefreshTask(accountID int64) {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountRefreshPayload{AccountID: accountID})
	if err != nil {
		return
	}
	task := asynq.NewTask(asynqTaskAccountRefresh, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(3), asynq.ProcessIn(accountRefreshDelay()), asynq.Unique(24*time.Hour))
}

// enqueueAccountMergingTask mirrors AccountMergingWorker.perform_async(account_id).
func (s *Server) enqueueAccountMergingTask(accountID int64) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAccountMerging, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

// enqueueResolveAccountTask mirrors ResolveAccountWorker.perform_async(uri).
func (s *Server) enqueueResolveAccountTask(uri string) bool {
	uri = strings.TrimSpace(uri)
	if s == nil || s.asynqClient == nil || uri == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqResolveAccountPayload{URI: uri})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskResolveAccount, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(25), asynq.Unique(24*time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// enqueuePollUpdateTask mirrors ActivityPub::DistributePollUpdateWorker. VoteService
// schedules it after 3 minutes, while poll-expiration can enqueue it immediately.
func (s *Server) enqueuePollUpdateTask(statusID int64, delay time.Duration) bool {
	if s == nil || s.asynqClient == nil || statusID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqStatusPayload{StatusID: statusID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskPollUpdate, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	options := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueuePush)), asynq.MaxRetry(0), asynq.Unique(pollUpdateUniqueTTL())}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err = s.asynqClient.EnqueueContext(ctx, task, options...)
	return asynqEnqueueAccepted(err)
}

func pollUpdateUniqueTTL() time.Duration {
	return railsSidekiqUniqueDefaultLockTTL
}

// enqueuePollExpirationTask mirrors PollExpirationNotifyWorker.perform_at.
func (s *Server) enqueuePollExpirationTask(pollID int64, runAt time.Time) bool {
	if s == nil || s.asynqClient == nil || pollID == 0 || runAt.IsZero() {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqPollPayload{PollID: pollID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskPollExpiration, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	taskID := "poll-expiration:" + strconv.FormatInt(pollID, 10) + ":" + strconv.FormatInt(runAt.UTC().Unix(), 10)
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(25), asynq.ProcessAt(runAt.UTC()), asynq.TaskID(taskID))
	return asynqEnqueueAccepted(err)
}

func pollExpirationUniqueTTL() time.Duration {
	return pollExpirationRequeueDelay
}

// enqueueAccountUpdateTask mirrors ActivityPub::UpdateDistributionWorker on the push
// queue. Rails uses a 5-second debounce for profile/privacy/picture updates and a
// one-day lock; asynq Unique gives the same "one pending account update" boundary.
func (s *Server) enqueueAccountUpdateTask(accountID int64, delay time.Duration) bool {
	return s.enqueueAccountUpdateTaskWithSigningKey(accountID, delay, "")
}

func (s *Server) enqueueAccountUpdateTaskWithSigningKey(accountID int64, delay time.Duration, oldPrivateKey string) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	task, err := newAsynqAccountUpdateTask(accountID, oldPrivateKey)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	options := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueuePush)), asynq.MaxRetry(25), asynq.Unique(24 * time.Hour)}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err = s.asynqClient.EnqueueContext(ctx, task, options...)
	return asynqEnqueueAccepted(err)
}

func newAsynqAccountUpdateTask(accountID int64, oldPrivateKey string) (*asynq.Task, error) {
	if accountID <= 0 {
		return nil, errors.New("account update: account id is required")
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountPayload{Version: asynqPayloadVersion43, AccountID: accountID, OldPrivateKey: oldPrivateKey})
	if err != nil {
		return nil, fmt.Errorf("account update payload: %w", err)
	}
	return asynq.NewTask(asynqTaskAccountUpdate, payload), nil
}

// enqueueAccountRawDistributionTask mirrors ActivityPub::AccountRawDistributionWorker on
// the push queue. Rails does not debounce or lock this worker; each Add/Remove featured-tag
// activity should be distributed independently.
func (s *Server) enqueueAccountRawDistributionTask(sourceAccountID int64, rawJSON []byte, excludeInboxes []string) bool {
	if s == nil || s.asynqClient == nil || sourceAccountID == 0 || len(rawJSON) == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqAccountRawDistributionPayload{
		SourceAccountID: sourceAccountID,
		JSON:            string(rawJSON),
		ExcludeInboxes:  excludeInboxes,
	})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskAccountRawDistribution, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePush)))
	return err == nil
}

// enqueueRawDistributionTask mirrors ActivityPub::RawDistributionWorker on the push queue.
// Unlike AccountRawDistributionWorker, Rails RawDistributionWorker targets followers.inboxes.
func (s *Server) enqueueRawDistributionTask(sourceAccountID int64, rawJSON []byte, excludeInboxes []string) bool {
	if s == nil || s.asynqClient == nil || sourceAccountID == 0 || len(rawJSON) == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqRawDistributionPayload{
		SourceAccountID: sourceAccountID,
		JSON:            string(rawJSON),
		ExcludeInboxes:  excludeInboxes,
	})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskRawDistribution, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePush)))
	return err == nil
}

// enqueueFeaturedCollectionSyncTask mirrors ActivityPub::SynchronizeFeaturedCollectionWorker
// on the pull queue with a one-day uniqueness window.
func (s *Server) enqueueFeaturedCollectionSyncTask(accountID int64, collectionURI string, requestID string, syncTags bool) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqFeaturedCollectionPayload{AccountID: accountID, CollectionURI: collectionURI, RequestID: requestID, SyncTags: syncTags})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskFeaturedCollectionSync, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.Unique(24*time.Hour), asynq.Timeout(featuredCollectionWorkerTimeout))
	return asynqEnqueueAccepted(err)
}

// enqueueFeaturedTagsSyncTask mirrors ActivityPub::SynchronizeFeaturedTagsCollectionWorker
// on the pull queue with a one-day uniqueness window.
func (s *Server) enqueueFeaturedTagsSyncTask(accountID int64, collectionURI string) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqFeaturedTagsPayload{AccountID: accountID, CollectionURI: collectionURI})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskFeaturedTagsSync, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.Unique(24*time.Hour), asynq.Timeout(featuredCollectionWorkerTimeout))
	return asynqEnqueueAccepted(err)
}

// enqueueMoveDistributionTask mirrors ActivityPub::MoveDistributionWorker on the push queue.
func (s *Server) enqueueMoveDistributionTask(migrationID int64) bool {
	if s == nil || s.asynqClient == nil || migrationID == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqMigrationPayload{MigrationID: migrationID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskMoveDistribution, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePush)))
	return err == nil
}

// enqueuePostUpgradeTask mirrors ActivityPub::PostUpgradeWorker.perform_async(domain)
// on the pull queue.
func (s *Server) enqueuePostUpgradeTask(domain string) bool {
	if s == nil || s.asynqClient == nil || strings.TrimSpace(domain) == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqDomainPayload{Domain: domain})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskPostUpgrade, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePull)))
	return err == nil
}

// enqueueFollowersSynchronizationTask mirrors ActivityPub::FollowersSynchronizationWorker
// on the push queue with an until-executed uniqueness lock.
func (s *Server) enqueueFollowersSynchronizationTask(accountID int64, collectionURL string) bool {
	if s == nil || s.asynqClient == nil || accountID == 0 || strings.TrimSpace(collectionURL) == "" {
		return false
	}
	payload, err := marshalAsynqTaskPayload(asynqFollowersSynchronizationPayload{Version: asynqPayloadVersion43, AccountID: accountID, URL: collectionURL})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskFollowersSync, payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.Queue(s.asynqQueue(asynqQueuePush)), asynq.Unique(railsSidekiqUniqueDefaultLockTTL))
	return asynqEnqueueAccepted(err)
}

// enqueueActivityPubProcessingTask mirrors ActivityPub::ProcessingWorker.perform_async
// on Rails' ingress queue. Processing errors must remain on this Asynq path so retry
// exhaustion preserves the failed event in the archive.
func (s *Server) enqueueActivityPubProcessingTask(job activityPubInboxProcessingJob) bool {
	return s.enqueueActivityPubProcessingTaskWithContext(context.Background(), job)
}

func (s *Server) enqueueActivityPubProcessingTaskWithContext(ctx context.Context, job activityPubInboxProcessingJob) bool {
	if s == nil || s.asynqClient == nil || job.ActorID == 0 || len(job.Body) == 0 {
		return false
	}
	payload, err := marshalAsynqTaskPayload(job)
	if err != nil {
		return false
	}
	options := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueueIngress)), asynq.MaxRetry(activityPubInboxProcessingRetryLimit)}
	var task *asynq.Task
	if s.cfg.OpenTelemetryEnabled {
		task = asynq.NewTaskWithHeaders(asynqTaskActivityPubProcessing, payload, telemetry.AsynqHeaders(ctx), options...)
	} else {
		task = asynq.NewTask(asynqTaskActivityPubProcessing, payload, options...)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err == nil
}

func accountRefreshDelay() time.Duration {
	return time.Duration(rand.Int63n(int64(6 * time.Hour)))
}

func linkCrawlDelay() time.Duration {
	return time.Duration(1+rand.Int63n(59)) * time.Second
}

func railsExponentialBackoffRetryDelay(n int, err error, task *asynq.Task) time.Duration {
	if !railsExponentialBackoffAsynqTask(task) {
		return asynq.DefaultRetryDelayFunc(n, err, task)
	}
	if n < 0 {
		n = 0
	}
	pow := n * n * n * n
	seconds := 15 + (10 * pow)
	if pow > 0 {
		seconds += int(rand.Int63n(int64(10 * pow)))
	}
	return time.Duration(seconds) * time.Second
}

func railsExponentialBackoffAsynqTask(task *asynq.Task) bool {
	if task == nil {
		return false
	}
	switch task.Type() {
	case asynqTaskActivityPubProcessing,
		asynqTaskRedownloadAvatar,
		asynqTaskRedownloadHeader,
		asynqTaskRedownloadMedia,
		asynqTaskFetchReply,
		asynqTaskFetchReplies,
		asynqTaskThreadResolve,
		asynqTaskMentionResolve,
		asynqTaskRemoteAccountRefresh:
		return true
	default:
		return false
	}
}

// newAsynqServeMux wires task types to handlers, matching Rails Sidekiq workers.
func (s *Server) newAsynqServeMux() *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.Use(asynqPayloadVersionMiddleware())
	if s != nil && s.cfg.OpenTelemetryEnabled {
		mux.Use(telemetry.WorkerMiddleware())
	}
	if s != nil && s.cfg.StatsDSidekiq {
		mux.Use(asynqStatsDMiddleware(newStatsDClient(s.cfg)))
	}
	mux.HandleFunc(asynqTaskRedownloadAvatar, s.handleAsynqRedownloadAccountMedia)
	mux.HandleFunc(asynqTaskRedownloadHeader, s.handleAsynqRedownloadAccountMedia)
	mux.HandleFunc(asynqTaskRedownloadMedia, s.handleAsynqRedownloadMedia)
	mux.HandleFunc(asynqTaskRefollow, s.handleAsynqRefollow)
	mux.HandleFunc(asynqTaskFetchReply, s.handleAsynqFetchReply)
	mux.HandleFunc(asynqTaskFetchReplies, s.handleAsynqFetchReplies)
	mux.HandleFunc(asynqTaskThreadResolve, s.handleAsynqThreadResolve)
	mux.HandleFunc(asynqTaskMentionResolve, s.handleAsynqMentionResolve)
	mux.HandleFunc(asynqTaskFeedInsert, s.handleAsynqFeedInsert)
	mux.HandleFunc(asynqTaskLocalNotification, s.handleAsynqLocalNotification)
	mux.HandleFunc(asynqTaskFilteredNotificationCleanup, s.handleAsynqFilteredNotificationCleanup)
	mux.HandleFunc(asynqTaskUnfilterNotifications, s.handleAsynqUnfilterNotifications)
	mux.HandleFunc(asynqTaskGenerateAnnualReport, s.handleAsynqGenerateAnnualReport)
	mux.HandleFunc(asynqTaskNotificationMail, s.handleAsynqNotificationMail)
	mux.HandleFunc(asynqTaskConfirmationMail, s.handleAsynqConfirmationMail)
	mux.HandleFunc(asynqTaskMailerDelivery, s.handleAsynqMailerDelivery)
	mux.HandleFunc(asynqTaskBackup, railsDeadFalseAsynqHandler(asynqTaskBackup, s.handleAsynqBackup))
	mux.HandleFunc(asynqTaskBulkImport, s.handleAsynqBulkImport)
	mux.HandleFunc(asynqTaskLegacyImport, s.handleAsynqLegacyImport)
	mux.HandleFunc(asynqTaskImportRow, railsDeadFalseAsynqHandler(asynqTaskImportRow, s.handleAsynqImportRow))
	mux.HandleFunc(asynqTaskImportRelationship, railsDeadFalseAsynqHandler(asynqTaskImportRelationship, s.handleAsynqImportRelationship))
	mux.HandleFunc(asynqTaskLinkCrawl, s.handleAsynqLinkCrawl)
	mux.HandleFunc(asynqTaskPostProcessMedia, railsDeadFalseAsynqHandler(asynqTaskPostProcessMedia, s.handleAsynqPostProcessMedia))
	mux.HandleFunc(asynqTaskRemoveFeaturedTag, s.handleAsynqRemoveFeaturedTag)
	mux.HandleFunc(asynqTaskTagUnmerge, s.handleAsynqTagUnmerge)
	mux.HandleFunc(asynqTaskUnfollowFollow, s.handleAsynqUnfollowFollow)
	mux.HandleFunc(asynqTaskPublishScheduledStatus, s.handleAsynqPublishScheduledStatus)
	mux.HandleFunc(asynqTaskPublishAnnouncement, s.handleAsynqPublishAnnouncement)
	mux.HandleFunc(asynqTaskUnpublishAnnouncement, s.handleAsynqUnpublishAnnouncement)
	mux.HandleFunc(asynqTaskRemoteAccountRefresh, s.handleAsynqRemoteAccountRefresh)
	mux.HandleFunc(asynqTaskAccountRefresh, railsDeadFalseAsynqHandler(asynqTaskAccountRefresh, s.handleAsynqAccountRefresh))
	mux.HandleFunc(asynqTaskAccountMerging, s.handleAsynqAccountMerging)
	mux.HandleFunc(asynqTaskResolveAccount, s.handleAsynqResolveAccount)
	mux.HandleFunc(asynqTaskPollExpiration, s.handleAsynqPollExpiration)
	mux.HandleFunc(asynqTaskPollUpdate, s.handleAsynqPollUpdate)
	mux.HandleFunc(asynqTaskAccountUpdate, s.handleAsynqAccountUpdate)
	mux.HandleFunc(asynqTaskRawDistribution, s.handleAsynqRawDistribution)
	mux.HandleFunc(asynqTaskAccountRawDistribution, s.handleAsynqAccountRawDistribution)
	mux.HandleFunc(asynqTaskFeaturedCollectionSync, s.handleAsynqFeaturedCollectionSync)
	mux.HandleFunc(asynqTaskFeaturedTagsSync, s.handleAsynqFeaturedTagsSync)
	mux.HandleFunc(asynqTaskMoveDistribution, s.handleAsynqMoveDistribution)
	mux.HandleFunc(asynqTaskPostUpgrade, s.handleAsynqPostUpgrade)
	mux.HandleFunc(asynqTaskFollowersSync, s.handleAsynqFollowersSynchronization)
	mux.HandleFunc(asynqTaskActivityPubProcessing, s.handleAsynqActivityPubProcessing)
	mux.HandleFunc(asynqTaskActivityPubDelivery, s.handleAsynqActivityPubDelivery)
	mux.HandleFunc(asynqTaskSelfDestructDelivery, s.handleAsynqSelfDestructDelivery)
	mux.HandleFunc(asynqTaskActivityPubDistribution, s.handleAsynqActivityPubDistribution)
	mux.HandleFunc(asynqTaskStatusUpdateDistribution, s.handleAsynqStatusUpdateDistribution)
	mux.HandleFunc(asynqTaskCacheBuster, s.handleAsynqCacheBuster)
	mux.HandleFunc(asynqTaskAnnouncementReaction, s.handleAsynqAnnouncementReaction)
	mux.HandleFunc(asynqTaskRemoval, s.handleAsynqRemoval)
	mux.HandleFunc(asynqTaskPushConversation, s.handleAsynqPushConversation)
	mux.HandleFunc(asynqTaskPushUpdate, s.handleAsynqPushUpdate)
	mux.HandleFunc(asynqTaskWebPushNotification, s.handleAsynqWebPushNotification)
	mux.HandleFunc(asynqTaskAuthorizeFollow, s.handleAsynqAuthorizeFollow)
	mux.HandleFunc(asynqTaskBootstrapTimeline, s.handleAsynqBootstrapTimeline)
	mux.HandleFunc(asynqTaskRegeneration, s.handleAsynqRegeneration)
	mux.HandleFunc(asynqTaskVerifyAccountLinks, s.handleAsynqVerifyAccountLinks)
	mux.HandleFunc(asynqTaskTriggerWebhook, s.handleAsynqTriggerWebhook)
	mux.HandleFunc(asynqTaskWebhookDelivery, railsDeadFalseAsynqHandler(asynqTaskWebhookDelivery, s.handleAsynqWebhookDelivery))
	mux.HandleFunc(asynqTaskDomainBlock, s.handleAsynqDomainBlock)
	mux.HandleFunc(asynqTaskDomainClearMedia, s.handleAsynqDomainClearMedia)
	mux.HandleFunc(asynqTaskAdminDomainPurge, s.handleAsynqAdminDomainPurge)
	mux.HandleFunc(asynqTaskAccountDeletion, s.handleAsynqAccountDeletion)
	mux.HandleFunc(asynqTaskAdminAccountDeletion, s.handleAsynqAdminAccountDeletion)
	mux.HandleFunc(asynqTaskAdminSuspension, s.handleAsynqAdminSuspension)
	mux.HandleFunc(asynqTaskAdminUnsuspension, s.handleAsynqAdminUnsuspension)
	mux.HandleFunc(asynqTaskBlock, s.handleAsynqBlock)
	mux.HandleFunc(asynqTaskMute, s.handleAsynqMute)
	mux.HandleFunc(asynqTaskMerge, s.handleAsynqMerge)
	mux.HandleFunc(asynqTaskUnmerge, s.handleAsynqUnmerge)
	mux.HandleFunc(asynqTaskDeleteMute, s.handleAsynqDeleteMute)
	mux.HandleFunc(asynqTaskUnfavourite, s.handleAsynqUnfavourite)
	mux.HandleFunc(asynqTaskAfterAccountDomainBlock, s.handleAsynqAfterAccountDomainBlock)
	mux.HandleFunc(asynqTaskAfterUnallowDomain, s.handleAsynqAfterUnallowDomain)
	return mux
}

func railsDeadFalseAsynqHandler(taskType string, handler func(context.Context, *asynq.Task) error) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, task *asynq.Task) error {
		err := handler(ctx, task)
		if err == nil || !railsDeadFalseAsynqTask(taskType) || !asynqRetryExhausted(ctx) {
			return err
		}
		return fmt.Errorf("%w: %v", asynq.RevokeTask, err)
	}
}

func railsDeadFalseAsynqTask(taskType string) bool {
	switch taskType {
	case asynqTaskBackup,
		asynqTaskImportRow,
		asynqTaskImportRelationship,
		asynqTaskPostProcessMedia,
		asynqTaskAccountRefresh,
		asynqTaskWebhookDelivery:
		return true
	default:
		return false
	}
}

func asynqRetryExhausted(ctx context.Context) bool {
	retried, okRetry := asynq.GetRetryCount(ctx)
	maxRetry, okMax := asynq.GetMaxRetry(ctx)
	return okRetry && okMax && retried >= maxRetry
}

func paonGoAsynqQueueWeights() map[string]int {
	return map[string]int{
		asynqQueueDefault: 8,
		asynqQueuePush:    6,
		asynqQueueIngress: 4,
		asynqQueueMailers: 2,
		asynqQueuePull:    1,
	}
}

func paonGoAsynqQueueWeightsForConfig(cfg config.Config) map[string]int {
	weights := paonGoAsynqQueueWeights()
	selected := weights
	if len(cfg.AsynqQueues) > 0 {
		selected = make(map[string]int, len(cfg.AsynqQueues))
		for _, queue := range cfg.AsynqQueues {
			if weight, ok := weights[queue]; ok {
				selected[queue] = weight
			}
		}
	}
	out := make(map[string]int, len(selected))
	for queue, weight := range selected {
		out[asynqQueueName(cfg, queue)] = weight
	}
	return out
}

func (s *Server) asynqQueue(queue string) string {
	if s == nil {
		return queue
	}
	return asynqQueueName(s.cfg, queue)
}

func asynqQueueName(cfg config.Config, queue string) string {
	namespace := strings.TrimSuffix(strings.TrimSpace(cfg.RedisNamespace), ":")
	if namespace == "" {
		return queue
	}
	return namespace + ":" + queue
}

func paonGoAsynqConcurrency(cfg config.Config) int {
	if cfg.SidekiqConcurrency > 0 {
		return cfg.SidekiqConcurrency
	}
	return 5
}

// startAsynqWorker starts the Asynq server (Sidekiq-equivalent), reports the
// result only after handlers have been registered and the server entered its
// running state, then waits for shutdown. The readiness callback lets the
// process create MASTODON_SIDEKIQ_READY_FILENAME after queue initialization.
func (s *Server) startAsynqWorker(ctx context.Context, ready func(error)) {
	if s == nil {
		if ready != nil {
			ready(nil)
		}
		return
	}
	srv := asynq.NewServer(asynqRedisOpt(s.cfg), asynq.Config{
		Concurrency:    paonGoAsynqConcurrency(s.cfg),
		Queues:         paonGoAsynqQueueWeightsForConfig(s.cfg),
		RetryDelayFunc: railsExponentialBackoffRetryDelay,
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			s.handleAsynqTaskError(ctx, task, err)
		}),
	})
	err := srv.Start(s.newAsynqServeMux())
	if ready != nil {
		ready(err)
	}
	if err != nil {
		log.Printf("level=ERROR event=asynq_worker_stopped error=%q", activityPubErrorLogValue(err))
		return
	}
	<-ctx.Done()
	srv.Shutdown()
}

func (s *Server) handleAsynqTaskError(ctx context.Context, task *asynq.Task, err error) {
	if task == nil || err == nil {
		return
	}
	logAsynqTaskError(ctx, task, err)
	if s == nil || s.db == nil {
		return
	}
	retried, okRetry := asynq.GetRetryCount(ctx)
	maxRetry, okMax := asynq.GetMaxRetry(ctx)
	if !okRetry || !okMax || retried < maxRetry {
		return
	}
	switch task.Type() {
	case asynqTaskPostProcessMedia:
		var p asynqMediaPostProcessPayload
		if json.Unmarshal(task.Payload(), &p) != nil || p.MediaAttachmentID == 0 {
			return
		}
		s.markMediaPostProcessFailed(ctx, p.MediaAttachmentID)
	case asynqTaskBackup:
		var p asynqBackupPayload
		if json.Unmarshal(task.Payload(), &p) != nil || p.BackupID == 0 {
			return
		}
		s.markBackupWorkerExhausted(ctx, p.BackupID)
	case asynqTaskImportRow:
		var p asynqImportRowPayload
		if json.Unmarshal(task.Payload(), &p) != nil || p.BulkImportRowID == 0 {
			return
		}
		s.markImportRowWorkerExhausted(ctx, p.BulkImportRowID)
	}
}

func logAsynqTaskError(ctx context.Context, task *asynq.Task, err error) {
	taskID, _ := asynq.GetTaskID(ctx)
	queue, _ := asynq.GetQueueName(ctx)
	retried, retryKnown := asynq.GetRetryCount(ctx)
	maxRetry, maxRetryKnown := asynq.GetMaxRetry(ctx)
	if !retryKnown {
		retried = -1
	}
	if !maxRetryKnown {
		maxRetry = -1
	}
	log.Printf("level=ERROR event=asynq_task_failed task_type=%q task_id=%q queue=%q retry=%d max_retry=%d payload_bytes=%d error=%q",
		task.Type(), taskID, queue, retried, maxRetry, len(task.Payload()), activityPubErrorLogValue(err))
}

// handleAsynqRedownloadAccountMedia mirrors RedownloadAvatarWorker / RedownloadHeaderWorker
// (reset_avatar! / reset_header!): it re-downloads just the one media referenced by the task
// type, not the whole actor. Errors are returned so asynq retries (matching Rails retry: 7);
// asynq gives up after MaxRetry, like Rails' response_error_unsalvageable handling.
func (s *Server) handleAsynqRedownloadAccountMedia(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return taskTargetError("redownload account media payload", "local", serverLocalTaskTargetHost(s), err)
	}
	if err := validateAsynqPayloadVersion("redownload account media", p.Version); err != nil {
		return err
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	kind := ""
	switch t.Type() {
	case asynqTaskRedownloadAvatar:
		kind = "avatar"
	case asynqTaskRedownloadHeader:
		kind = "header"
	default:
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		if lookupErr := workerLookupError("redownload account media lookup", err); lookupErr != nil {
			return taskTargetError("redownload account media lookup", "local", serverLocalTaskTargetHost(s), lookupErr)
		}
		return nil
	}
	allowed, err := s.remoteAccountMediaRedownloadAllowed(ctx, account, kind)
	if err != nil {
		return taskTargetError("redownload account "+kind+" policy lookup", "local", serverLocalTaskTargetHost(s), err)
	}
	if !allowed {
		return nil
	}
	remoteURL := strings.TrimSpace(account.HeaderRemoteURL)
	if kind == "avatar" {
		remoteURL = strings.TrimSpace(account.AvatarRemoteURL.String)
	}
	if err := s.downloadAndStoreRemoteAccountImage(ctx, account.ID, kind, remoteURL); err != nil {
		if remoteMediaErrorUnsalvageable(err) {
			return nil
		}
		return fmt.Errorf("redownload account %s account_id=%d: %w", kind, account.ID, err)
	}
	return nil
}

func (s *Server) handleAsynqRedownloadMedia(ctx context.Context, t *asynq.Task) error {
	var p asynqMediaAttachmentPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return taskTargetError("redownload media payload", "local", serverLocalTaskTargetHost(s), err)
	}
	if err := validateAsynqPayloadVersion("redownload media", p.Version); err != nil {
		return err
	}
	return s.redownloadRemoteMediaAttachment(ctx, p.MediaAttachmentID)
}

func (s *Server) remoteAccountMediaRedownloadAllowed(ctx context.Context, account models.Account, kind string) (bool, error) {
	if s == nil || account.ID == 0 || s.cfg.DisableRemoteMediaCache || account.SuspendedAt.Valid {
		return false, nil
	}
	rejected, err := s.remoteAccountMediaRejectedByDomainBlock(ctx, account)
	if err != nil || rejected {
		return false, err
	}
	switch kind {
	case "avatar":
		return strings.TrimSpace(account.AvatarRemoteURL.String) != "" && (!account.AvatarFileName.Valid || strings.TrimSpace(account.AvatarFileName.String) == ""), nil
	case "header":
		return strings.TrimSpace(account.HeaderRemoteURL) != "" && (!account.HeaderFileName.Valid || strings.TrimSpace(account.HeaderFileName.String) == ""), nil
	default:
		return false, nil
	}
}

func (s *Server) remoteAccountMediaRejectedByDomainBlock(ctx context.Context, account models.Account) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !account.Domain.Valid || strings.TrimSpace(account.Domain.String) == "" {
		return false, nil
	}
	domains := domainControlVariants(account.Domain.String)
	if len(domains) == 0 {
		return false, nil
	}
	var block models.DomainBlock
	err := s.db.WithContext(ctx).
		Where("lower(domain) IN ?", domains).
		Order("char_length(domain) DESC").
		Limit(1).
		First(&block).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("redownload account media domain block lookup: %w", err)
	}
	return block.RejectMedia, nil
}

// handleAsynqRefollow mirrors RefollowWorker: re-establish follows from local followers of
// a remote account (used after un-suspend/un-silence). Reuses the existing key-change
// refollow path, which re-follows all local followers.
func (s *Server) handleAsynqRefollow(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("refollow: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("refollow account lookup", err)
	}
	return s.refollowLocalFollowersAfterActivityPubKeyChange(ctx, s.db, account)
}

// handleAsynqFetchReply mirrors FetchReplyWorker: resolve the child URL through
// FetchRemoteStatusService, including FetchResourceService discovery, and retry
// via asynq's MaxRetry(3) semantics like Rails retry: 3.
func (s *Server) handleAsynqFetchReply(ctx context.Context, t *asynq.Task) error {
	var p asynqFetchReplyPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("fetch reply: %w", err)
	}
	if s == nil || s.db == nil || strings.TrimSpace(p.URI) == "" {
		return nil
	}
	_, err := s.fetchRemoteStatusFromResolvableURLForRequest(p.URI, p.RequestID)
	return err
}

// handleAsynqFetchReplies mirrors ActivityPub::FetchRepliesWorker: load the
// parent status, fetch its replies collection, and enqueue per-reply
// FetchReplyWorker tasks. Missing parent statuses are ignored like Rails'
// ActiveRecord::RecordNotFound rescue.
func (s *Server) handleAsynqFetchReplies(ctx context.Context, t *asynq.Task) error {
	var p asynqFetchRepliesPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("fetch replies: %w", err)
	}
	if s == nil || s.db == nil || p.ParentStatusID == 0 || strings.TrimSpace(p.CollectionURI) == "" {
		return nil
	}
	var status models.Status
	if err := s.db.WithContext(ctx).Preload("Account").Where("id = ?", p.ParentStatusID).First(&status).Error; err != nil {
		return workerLookupError("fetch replies parent lookup", err)
	}
	parentActorURI := status.Account.URI
	uris, err := s.fetchActivityPubReplyCollectionURIsResult(parentActorURI, p.CollectionURI, paonUserAgent(s.cfg))
	if err != nil {
		if err = activityFetchWorkerError(err); err != nil {
			return fmt.Errorf("fetch replies collection: %w", err)
		}
		return nil
	}
	for _, uri := range uris {
		s.enqueueFetchReplyTask(uri, p.RequestID)
	}
	return nil
}

// handleAsynqThreadResolve mirrors ThreadResolveWorker: resolve a missing parent
// status, attach it to the child, and fan out the child update without notifications.
func (s *Server) handleAsynqThreadResolve(ctx context.Context, t *asynq.Task) error {
	var p asynqThreadResolvePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("thread resolve: %w", err)
	}
	if s == nil || s.db == nil || p.ChildStatusID == 0 || strings.TrimSpace(p.ParentURL) == "" {
		return nil
	}
	return s.resolveActivityPubThread(p.ChildStatusID, p.ParentURL, p.RequestID)
}

func (s *Server) handleAsynqMentionResolve(ctx context.Context, t *asynq.Task) error {
	var p asynqMentionResolvePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("mention resolve payload: %w: %w", err, asynq.SkipRetry)
	}
	if err := validateAsynqPayloadVersion("mention resolve", p.Version); err != nil {
		return err
	}
	if s == nil || s.db == nil || p.StatusID == 0 || activityPubHTTPURI(p.URI) == "" {
		return nil
	}
	var statusCount int64
	if err := s.db.WithContext(ctx).Model(&models.Status{}).Where("id = ? AND deleted_at IS NULL", p.StatusID).Count(&statusCount).Error; err != nil {
		return err
	}
	if statusCount == 0 {
		return nil
	}
	account, err := s.accountFromActivityURIWithDB(s.db.WithContext(ctx), p.URI)
	if err != nil {
		return mentionResolveWorkerError(err)
	}
	if account == nil {
		account, err = s.fetchActivityPubMentionAccountByHref(s.db.WithContext(ctx), p.URI)
		if err != nil {
			return mentionResolveWorkerError(err)
		}
	}
	if account == nil || account.ID == 0 {
		return nil
	}
	now := time.Now().UTC()
	mention := models.Mention{StatusID: models.MentionStatusID(p.StatusID), AccountID: models.MentionAccountID(account.ID), Silent: false, CreatedAt: now, UpdatedAt: now}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "status_id"}, {Name: "account_id"}},
		DoUpdates: clause.Assignments(map[string]any{"silent": false, "updated_at": now}),
	}).Create(&mention).Error
}

func mentionResolveWorkerError(err error) error {
	if err == nil {
		return nil
	}
	if status, ok := activityFetchStatus(err); ok {
		if activityPubDeliveryResponseErrorUnsalvageable(status) {
			return nil
		}
		return err
	}
	var networkError net.Error
	if errors.As(err, &networkError) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var databaseError interface{ SQLState() string }
	if errors.As(err, &databaseError) {
		return err
	}
	// Actor validation, malformed JSON-LD, unsupported schemes, and missing
	// public keys are permanent for this payload. Do not poison the retry set.
	return nil
}

func (s *Server) handleAsynqFilteredNotificationCleanup(ctx context.Context, t *asynq.Task) error {
	p, err := notificationPairPayload(t)
	if err != nil {
		return err
	}
	return s.processFilteredNotificationCleanup(ctx, p)
}

func (s *Server) processFilteredNotificationCleanup(ctx context.Context, p asynqNotificationPairPayload) error {
	if s == nil || s.db == nil || p.AccountID == 0 || p.FromAccountID == 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("account_id = ? AND from_account_id = ? AND filtered = ?", p.AccountID, p.FromAccountID, true).
		Delete(&models.Notification{}).Error
}

func (s *Server) handleAsynqUnfilterNotifications(ctx context.Context, t *asynq.Task) error {
	p, err := notificationPairPayload(t)
	if err != nil {
		return err
	}
	return s.processUnfilterNotifications(ctx, p, true)
}

// processUnfilterNotifications performs the durable notification/conversation
// mutations before publishing them. When completeBarrier is false the caller is
// executing the synchronous queue-unavailable fallback and owns the final
// notifications_merged event.
func (s *Server) processUnfilterNotifications(ctx context.Context, p asynqNotificationPairPayload, completeBarrier bool) error {
	if s == nil || s.db == nil || p.AccountID == 0 || p.FromAccountID == 0 {
		return nil
	}
	var notificationIDs []int64
	var conversationIDs []int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Notification{}).
			Where("account_id = ? AND from_account_id = ? AND filtered = ?", p.AccountID, p.FromAccountID, true).
			Pluck("id", &notificationIDs).Error; err != nil {
			return err
		}
		var statuses []models.Status
		if err := tx.Model(&models.Status{}).
			Joins("JOIN mentions ON mentions.status_id = statuses.id").
			Joins("JOIN notifications ON notifications.activity_type = ? AND notifications.activity_id = mentions.id", "Mention").
			Where("notifications.account_id = ? AND notifications.from_account_id = ? AND notifications.filtered = ?", p.AccountID, p.FromAccountID, true).
			Where("statuses.visibility = ? AND statuses.deleted_at IS NULL", 3).
			Distinct("statuses.*").Find(&statuses).Error; err != nil {
			return err
		}
		for _, status := range statuses {
			updated, err := s.addDirectStatusToConversations(tx, status, nil)
			if err != nil {
				return err
			}
			conversationIDs = append(conversationIDs, updated...)
		}
		return tx.Model(&models.Notification{}).
			Where("account_id = ? AND from_account_id = ? AND filtered = ?", p.AccountID, p.FromAccountID, true).
			Updates(map[string]any{"filtered": false, "updated_at": time.Now().UTC()}).Error
	})
	if err != nil {
		return err
	}
	s.publishConversationIDs(ctx, uniqueInt64s(conversationIDs))
	s.publishNotificationIDs(uniqueInt64s(notificationIDs))
	if !completeBarrier {
		return nil
	}
	key := notificationUnfilterJobsRedisKey(s.cfg, p.AccountID)
	value, decrementErr := s.redisCommand(ctx, "DECR", key)
	if decrementErr != nil {
		return fmt.Errorf("complete notification unfilter barrier: %w", decrementErr)
	}
	if redisInteger(value) <= 0 {
		_, _ = s.redisCommand(ctx, "DEL", key)
		s.publishNotificationsMerged(p.AccountID)
	}
	return nil
}

func notificationPairPayload(t *asynq.Task) (asynqNotificationPairPayload, error) {
	var p asynqNotificationPairPayload
	if t == nil {
		return p, fmt.Errorf("notification task is nil: %w", asynq.SkipRetry)
	}
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return p, fmt.Errorf("notification task payload: %w: %w", err, asynq.SkipRetry)
	}
	if err := validateAsynqPayloadVersion("notification task", p.Version); err != nil {
		return p, err
	}
	return p, nil
}

func notificationUnfilterJobsRedisKey(cfg config.Config, accountID int64) string {
	return redisConfig(cfg).prefix + "notification_unfilter_jobs:" + strconv.FormatInt(accountID, 10)
}

func (s *Server) handleAsynqGenerateAnnualReport(ctx context.Context, t *asynq.Task) error {
	var p asynqAnnualReportPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("annual report payload: %w: %w", err, asynq.SkipRetry)
	}
	if err := validateAsynqPayloadVersion("annual report", p.Version); err != nil {
		return err
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.Year < 2000 || p.Year > 9999 {
		return nil
	}
	return s.generateAnnualReport(ctx, p.AccountID, p.Year)
}

// handleAsynqFeedInsert mirrors FeedInsertWorker: check the feed filter at execution time,
// insert one status into one recipient's home or list feed, unpush filtered updates, then trim.
func (s *Server) handleAsynqFeedInsert(ctx context.Context, t *asynq.Task) error {
	var p asynqFeedInsertPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("feed insert: %w", err)
	}
	if s == nil || s.db == nil || p.StatusID == 0 || p.FeedID == 0 || p.FeedType == "" {
		return nil
	}
	var status models.Status
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", p.StatusID).First(&status).Error; err != nil {
		return workerLookupError("feed insert status lookup", err)
	}
	if status.ReblogOfID.Valid {
		var rebloggedStatusCount int64
		if err := s.db.WithContext(ctx).Model(&models.Status{}).
			Where("id = ? AND deleted_at IS NULL", status.ReblogOfID.Int64).
			Count(&rebloggedStatusCount).Error; err != nil {
			return err
		}
		if rebloggedStatusCount == 0 {
			return nil
		}
	}
	filterResult, err := s.asynqFeedInsertFilter(ctx, p, status)
	if err != nil {
		return err
	}
	if filterResult != feedInsertFilterNone {
		if p.Update {
			if _, err := s.removeStatusFromFeedContext(ctx, p.FeedType, p.FeedID, status, p.AggregateReblogs); err != nil {
				return err
			}
		}
		if filterResult == feedInsertSkipHome {
			return s.notifyFeedInsertedStatus(ctx, p, status)
		}
		return nil
	}
	if _, err := s.addStatusToFeedContext(ctx, p.FeedType, p.FeedID, status, p.AggregateReblogs); err != nil {
		return err
	}
	s.enqueuePushUpdateForFeedInsert(ctx, p)
	if err := s.notifyFeedInsertedStatus(ctx, p, status); err != nil {
		return err
	}
	return s.trimFeedContext(ctx, p.FeedType, p.FeedID)
}

func (s *Server) enqueuePushUpdateForFeedInsert(ctx context.Context, p asynqFeedInsertPayload) {
	timelineID := feedInsertTimelineID(p)
	if timelineID == "" || !s.pushUpdateRequired(ctx, timelineID) {
		return
	}
	accountID := p.FeedID
	if p.FeedType == "list" {
		var list models.List
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", p.FeedID).First(&list).Error; err != nil {
			return
		}
		accountID = list.AccountID
	}
	if !s.enqueuePushUpdateTask(accountID, p.StatusID, timelineID, p.Update) {
		s.publishStatusUpdateToTimeline(ctx, accountID, p.StatusID, timelineID, p.Update)
	}
}

func feedInsertTimelineID(p asynqFeedInsertPayload) string {
	switch p.FeedType {
	case "home", "tags":
		if p.FeedID == 0 {
			return ""
		}
		return "timeline:" + strconv.FormatInt(p.FeedID, 10)
	case "list":
		if p.FeedID == 0 {
			return ""
		}
		return "timeline:list:" + strconv.FormatInt(p.FeedID, 10)
	default:
		return ""
	}
}

func (s *Server) pushUpdateRequired(ctx context.Context, timelineID string) bool {
	if s == nil || strings.TrimSpace(timelineID) == "" {
		return false
	}
	value, err := s.redisCommand(ctx, "EXISTS", redisConfig(s.cfg).prefix+"subscribed:"+timelineID)
	return err == nil && value == int64(1)
}

func (s *Server) publishStatusUpdateToTimeline(ctx context.Context, accountID int64, statusID int64, timelineID string, update bool) {
	if s == nil || s.db == nil || accountID == 0 || statusID == 0 || strings.TrimSpace(timelineID) == "" {
		return
	}
	var status models.Status
	if err := s.statusQuery().WithContext(ctx).Where("statuses.id = ?", statusID).First(&status).Error; err != nil {
		return
	}
	event := "update"
	if update {
		event = "status.update"
	}
	viewer := &models.Account{ID: accountID}
	if err := s.hydrateStatusRelationship(&status, viewer); err != nil {
		return
	}
	payload := statusStreamPayload(event, statusWithStreamingFilterContext(s.cfg, status, viewer, s.accountFilters(viewer), "home"))
	_, _ = s.redisCommand(ctx, "PUBLISH", redisConfig(s.cfg).prefix+timelineID, payload)
}

func (s *Server) asynqFeedInsertFilter(ctx context.Context, p asynqFeedInsertPayload, status models.Status) (feedInsertFilterResult, error) {
	switch p.FeedType {
	case "home":
		if p.FeedID == status.AccountID {
			return feedInsertFilterNone, nil
		}
		excluded, err := statusAuthorInExclusiveList(ctx, s.db, p.FeedID, status.AccountID)
		if err != nil {
			return feedInsertFilterNone, fmt.Errorf("feed insert exclusive list lookup: %w", err)
		}
		if excluded {
			return feedInsertSkipHome, nil
		}
		return feedInsertFilterNone, nil
	case "list":
		var list models.List
		if err := s.db.WithContext(ctx).Where("id = ?", p.FeedID).First(&list).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return feedInsertFilterStatus, nil
			}
			return feedInsertFilterNone, fmt.Errorf("feed insert list lookup: %w", err)
		}
		if s.filterStatusFromList(ctx, s.db, status, list) {
			return feedInsertFilterStatus, nil
		}
		return feedInsertFilterNone, nil
	default:
		return feedInsertFilterNone, nil
	}
}

func (s *Server) notifyFeedInsertedStatus(ctx context.Context, p asynqFeedInsertPayload, status models.Status) error {
	if p.FeedType != "home" || status.ID == 0 || status.AccountID == 0 || status.ReblogOfID.Valid {
		return nil
	}
	if status.Reply && status.InReplyToAccountID.Valid && status.InReplyToAccountID.Int64 != status.AccountID {
		return nil
	}
	var follow models.Follow
	if err := s.db.WithContext(ctx).Select("id", "notify").Where("account_id = ? AND target_account_id = ?", p.FeedID, status.AccountID).First(&follow).Error; err != nil {
		return workerLookupError("feed insert notify follow lookup", err)
	}
	if !follow.Notify {
		return nil
	}
	payload := asynqLocalNotificationPayload{ReceiverAccountID: p.FeedID, FromAccountID: status.AccountID, ActivityID: status.ID, ActivityType: "Status", Type: "status"}
	if s.enqueueLocalNotificationTask(payload.ReceiverAccountID, payload.FromAccountID, payload.ActivityID, payload.ActivityType, payload.Type) {
		return nil
	}
	notification, err := s.createLocalNotificationFromPayload(ctx, payload)
	if err != nil || notification == nil {
		return err
	}
	return nil
}

// handleAsynqLocalNotification mirrors LocalNotificationWorker: create the notification
// with Rails duplicate/update semantics, then run the same streaming, mail, and web-push
// side effects that Rails triggers from NotifyService/Notification callbacks.
func (s *Server) handleAsynqLocalNotification(ctx context.Context, t *asynq.Task) error {
	var p asynqLocalNotificationPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("local notification: %w", err)
	}
	_, err := s.createLocalNotificationFromPayload(ctx, p)
	return err
}

func (s *Server) createLocalNotificationFromPayload(ctx context.Context, p asynqLocalNotificationPayload) (*models.Notification, error) {
	if s == nil || s.db == nil || p.ReceiverAccountID == 0 || p.ActivityID == 0 || strings.TrimSpace(p.ActivityType) == "" || strings.TrimSpace(p.Type) == "" {
		return nil, nil
	}
	var notification *models.Notification
	var err error
	if p.ActivityType == "Mention" {
		notification, err = s.createMentionLocalNotificationFromPayload(ctx, p)
	} else {
		fromAccountID := p.FromAccountID
		if fromAccountID == 0 {
			fromAccountID, err = s.localNotificationFromAccountID(ctx, p.ActivityType, p.ActivityID)
			if err != nil {
				return nil, err
			}
		}
		notification, err = s.createRelationshipNotificationRowAndEnqueue(s.db.WithContext(ctx), p.ReceiverAccountID, fromAccountID, p.ActivityID, p.ActivityType, p.Type, time.Now().UTC())
	}
	if err != nil || notification == nil {
		return notification, err
	}
	s.publishNotificationIDWithContext(ctx, notification.ID)
	return notification, nil
}

func (s *Server) createMentionLocalNotificationFromPayload(ctx context.Context, p asynqLocalNotificationPayload) (*models.Notification, error) {
	var notification *models.Notification
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		mention, err := localNotificationMentionForPayload(tx, p)
		if err != nil || mention == nil {
			return err
		}
		fromAccountID := p.FromAccountID
		if fromAccountID == 0 {
			if !mention.StatusID.Valid {
				return nil
			}
			var status models.Status
			err := tx.Select("account_id").Where("id = ?", mention.StatusID.Int64).First(&status).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			fromAccountID = status.AccountID
		}
		notification, err = s.createRelationshipNotificationRowAndEnqueue(tx, p.ReceiverAccountID, fromAccountID, p.ActivityID, p.ActivityType, p.Type, time.Now().UTC())
		return err
	})
	return notification, err
}

func localNotificationMentionForPayload(tx *gorm.DB, p asynqLocalNotificationPayload) (*models.Mention, error) {
	var mention models.Mention
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status_id", "account_id").
		Where("id = ?", p.ActivityID).
		First(&mention).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !localNotificationMentionMatchesPayload(&mention, p) {
		return nil, nil
	}
	return &mention, nil
}

func localNotificationMentionMatchesPayload(mention *models.Mention, p asynqLocalNotificationPayload) bool {
	return mention != nil && mention.ID == p.ActivityID && mention.AccountID.Valid && mention.AccountID.Int64 == p.ReceiverAccountID
}

func (s *Server) enqueueOrCreateLocalNotification(ctx context.Context, p asynqLocalNotificationPayload) (*models.Notification, error) {
	if s.enqueueLocalNotificationTask(p.ReceiverAccountID, p.FromAccountID, p.ActivityID, p.ActivityType, p.Type) {
		return nil, nil
	}
	return s.createLocalNotificationFromPayload(ctx, p)
}

func (s *Server) enqueueOrCreateLocalNotifications(ctx context.Context, payloads []asynqLocalNotificationPayload) ([]int64, error) {
	notificationIDs := make([]int64, 0, len(payloads))
	for _, payload := range payloads {
		notification, err := s.enqueueOrCreateLocalNotification(ctx, payload)
		if err != nil {
			return notificationIDs, err
		}
		if notification != nil {
			notificationIDs = append(notificationIDs, notification.ID)
		}
	}
	return notificationIDs, nil
}

func (s *Server) localNotificationFromAccountID(ctx context.Context, activityType string, activityID int64) (int64, error) {
	switch activityType {
	case "Status":
		var status models.Status
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", activityID).First(&status).Error; err != nil {
			return 0, workerLookupError("notification status lookup", err)
		}
		return status.AccountID, nil
	case "Poll":
		var poll models.Poll
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", activityID).First(&poll).Error; err != nil {
			return 0, workerLookupError("notification poll lookup", err)
		}
		if !poll.AccountID.Valid {
			return 0, nil
		}
		return poll.AccountID.Int64, nil
	case "Favourite":
		var favourite models.Favourite
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", activityID).First(&favourite).Error; err != nil {
			return 0, workerLookupError("notification favourite lookup", err)
		}
		return favourite.AccountID, nil
	case "Follow":
		var follow models.Follow
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", activityID).First(&follow).Error; err != nil {
			return 0, workerLookupError("notification follow lookup", err)
		}
		return follow.AccountID, nil
	case "FollowRequest":
		var request models.FollowRequest
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", activityID).First(&request).Error; err != nil {
			return 0, workerLookupError("notification follow request lookup", err)
		}
		return request.AccountID, nil
	case "Report":
		var report models.Report
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", activityID).First(&report).Error; err != nil {
			return 0, workerLookupError("notification report lookup", err)
		}
		return report.AccountID, nil
	case "Mention":
		var status models.Status
		err := s.db.WithContext(ctx).
			Select("statuses.account_id").
			Joins("JOIN mentions ON mentions.status_id = statuses.id").
			Where("mentions.id = ?", activityID).
			First(&status).Error
		if err != nil {
			return 0, workerLookupError("notification mention lookup", err)
		}
		return status.AccountID, nil
	case "Account":
		return activityID, nil
	default:
		return 0, nil
	}
}

// handleAsynqNotificationMail mirrors LocalNotificationWorker -> NotifyService#send_email!:
// it loads the notification (with its from-account) and the recipient user, resolves the
// related status when applicable, and sends the plain-text notification e-mail.
func (s *Server) handleAsynqNotificationMail(ctx context.Context, t *asynq.Task) error {
	var p asynqNotificationMailPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("notification mail: %w", err)
	}
	if s == nil || s.db == nil || p.NotificationID == 0 {
		return nil
	}
	var notification models.Notification
	if err := s.db.WithContext(ctx).Preload("FromAccount").Where("id = ?", p.NotificationID).First(&notification).Error; err != nil {
		return workerLookupError("notification mail notification lookup", err)
	}
	var user models.User
	if err := s.db.WithContext(ctx).Preload("Account").Where("account_id = ?", notification.AccountID).First(&user).Error; err != nil {
		return workerLookupError("notification mail user lookup", err)
	}
	status := s.notificationActivityStatus(ctx, notification)
	return s.sendNotificationMail(user, notification, status, notificationStatusURL(status))
}

func (s *Server) handleAsynqConfirmationMail(ctx context.Context, t *asynq.Task) error {
	var p asynqConfirmationMailPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("confirmation mail: %w", err)
	}
	return processAsynqConfirmationMail(ctx, p, s.loadConfirmationMailUser, s.deliverConfirmationDelivery)
}

func (s *Server) handleAsynqMailerDelivery(ctx context.Context, t *asynq.Task) error {
	var payload asynqMailerDeliveryPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("mailer delivery: %w", err)
	}
	if s == nil || s.db == nil || payload.UserID <= 0 {
		return nil
	}
	var user models.User
	if err := s.db.WithContext(ctx).Preload("Account").Where("id = ?", payload.UserID).First(&user).Error; err != nil {
		return workerLookupError("mailer delivery user lookup", err)
	}
	eligible := strings.TrimSpace(user.Email) != ""
	switch payload.Eligibility {
	case "security":
		eligible = userReceivesSecurityMail(user)
	case "functional":
		eligible = userReceivesNotificationMail(user)
	case "present":
	default:
		return nil
	}
	if !eligible || !mailerRecipientStillBelongsToUser(user, payload.Message.To) {
		return nil
	}
	return sendMail(s.cfg, payload.Message)
}

func (s *Server) loadConfirmationMailUser(ctx context.Context, userID int64) (models.User, error) {
	if s == nil || s.db == nil || userID <= 0 {
		return models.User{}, gorm.ErrRecordNotFound
	}
	var user models.User
	err := s.db.WithContext(ctx).
		Select("id", "email", "unconfirmed_email", "confirmed_at", "approved", "locale", "settings").
		Where("id = ?", userID).
		First(&user).Error
	return user, err
}

func processAsynqConfirmationMail(ctx context.Context, p asynqConfirmationMailPayload, load func(context.Context, int64) (models.User, error), deliver func(confirmationDelivery) error) error {
	if p.UserID <= 0 || strings.TrimSpace(p.Token) == "" {
		return fmt.Errorf("confirmation mail: user id and token are required")
	}
	user, err := load(ctx, p.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("confirmation mail user: %w", err)
	}
	return deliver(confirmationDeliveryForUserWithToken(&user, p.Token))
}

func (s *Server) notificationMailNeededForNotification(ctx context.Context, db *gorm.DB, notification models.Notification) (bool, error) {
	if _, ok := notificationMailKey(string(notification.Type)); !ok {
		return false, nil
	}
	if db == nil {
		db = s.db
	}
	if s == nil || db == nil || notification.AccountID == 0 {
		return false, nil
	}
	var user models.User
	if err := db.WithContext(ctx).Preload("Account").Where("account_id = ?", notification.AccountID).First(&user).Error; err != nil {
		return false, err
	}
	return s.notificationMailNeeded(ctx, user)
}

func (s *Server) notificationMailNeeded(ctx context.Context, user models.User) (bool, error) {
	if userSettingBool(user, "always_send_emails", false) {
		return true, nil
	}
	if s == nil {
		return true, nil
	}
	if s.notificationRecipientOnline(ctx, user) {
		return false, nil
	}
	subscribed, err := s.notificationRecipientHasWebPushSubscription(ctx, user)
	if err != nil {
		return false, err
	}
	return !subscribed, nil
}

func (s *Server) notificationRecipientOnline(ctx context.Context, user models.User) bool {
	if s == nil || user.AccountID == 0 {
		return false
	}
	prefix := redisConfig(s.cfg).prefix
	accountID := strconv.FormatInt(user.AccountID, 10)
	for _, key := range []string{
		prefix + "subscribed:timeline:" + accountID,
		prefix + "subscribed:timeline:" + accountID + ":notifications",
	} {
		if value, err := s.redisCommand(ctx, "EXISTS", key); err == nil && value == int64(1) {
			return true
		}
	}
	return false
}

func (s *Server) notificationRecipientHasWebPushSubscription(ctx context.Context, user models.User) (bool, error) {
	if s == nil || s.db == nil || user.ID == 0 {
		return false, nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.WebPushSubscription{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// handleAsynqBackup mirrors BackupWorker: build the archive, remove older user backups,
// and send the backup-ready user mail. Returning an error lets asynq retry like Sidekiq.
func (s *Server) handleAsynqBackup(ctx context.Context, t *asynq.Task) error {
	var p asynqBackupPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if s == nil || s.db == nil || p.BackupID == 0 {
		return nil
	}
	return s.processBackupArchive(ctx, p.BackupID)
}

// handleAsynqBulkImport mirrors BulkImportWorker: mark the import in progress and
// process its rows outside the confirmation request.
func (s *Server) handleAsynqBulkImport(ctx context.Context, t *asynq.Task) error {
	var p asynqBulkImportPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("bulk import: %w", err)
	}
	if s == nil || s.db == nil || p.BulkImportID == 0 {
		return nil
	}
	return s.processBulkImport(ctx, p.BulkImportID)
}

// handleAsynqLegacyImport mirrors deprecated ImportWorker: process an old imports row
// through the same relationship/bookmark/domain-block side effects, then destroy it.
func (s *Server) handleAsynqLegacyImport(ctx context.Context, t *asynq.Task) error {
	var p asynqLegacyImportPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("legacy import: %w", err)
	}
	if s == nil || s.db == nil || p.ImportID == 0 {
		return nil
	}
	return s.processLegacyImport(ctx, p.ImportID)
}

// handleAsynqImportRow mirrors Import::RowWorker: process one CSV row, destroy it
// when imported, then advance BulkImport.progress!.
func (s *Server) handleAsynqImportRow(ctx context.Context, t *asynq.Task) error {
	var p asynqImportRowPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("import row: %w", err)
	}
	if s == nil || s.db == nil || p.BulkImportRowID == 0 {
		return nil
	}
	return s.processBulkImportRow(ctx, p.BulkImportRowID)
}

// handleAsynqImportRelationship mirrors deprecated Import::RelationshipWorker,
// kept so in-flight upgrade jobs can still apply follow/block/mute CSV rows.
func (s *Server) handleAsynqImportRelationship(ctx context.Context, t *asynq.Task) error {
	var p asynqImportRelationshipPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("import relationship: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || strings.TrimSpace(p.TargetAccountURI) == "" || strings.TrimSpace(p.Relationship) == "" {
		return nil
	}
	return s.processLegacyImportRelationship(ctx, p.AccountID, p.TargetAccountURI, p.Relationship, p.Options)
}

// handleAsynqLinkCrawl mirrors LinkCrawlWorker: fetch and attach a preview card
// for one status, ignoring missing statuses like Rails.
func (s *Server) handleAsynqLinkCrawl(ctx context.Context, t *asynq.Task) error {
	var p asynqStatusPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("link crawl: %w", err)
	}
	if s == nil || s.db == nil || p.StatusID == 0 {
		return nil
	}
	return s.fetchLinkCardForStatus(ctx, p.StatusID)
}

// handleAsynqPostProcessMedia mirrors PostProcessMediaWorker: mark the queued
// attachment in progress, reprocess the original, and persist complete/failed state.
func (s *Server) handleAsynqPostProcessMedia(ctx context.Context, t *asynq.Task) error {
	var p asynqMediaPostProcessPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("post process media: %w", err)
	}
	if s == nil || s.db == nil || p.MediaAttachmentID == 0 {
		return nil
	}
	return s.postProcessMediaAttachmentByID(ctx, p.MediaAttachmentID)
}

// handleAsynqRemoveFeaturedTag mirrors RemoveFeaturedTagWorker.
func (s *Server) handleAsynqRemoveFeaturedTag(ctx context.Context, t *asynq.Task) error {
	var p asynqFeaturedTagPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("remove featured tag: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.FeaturedTagID == 0 {
		return nil
	}
	return s.removeFeaturedTagForAccount(ctx, p.AccountID, p.FeaturedTagID)
}

// handleAsynqTagUnmerge mirrors TagUnmergeWorker.
func (s *Server) handleAsynqTagUnmerge(ctx context.Context, t *asynq.Task) error {
	var p asynqTagUnmergePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("tag unmerge: %w", err)
	}
	if s == nil || s.db == nil || p.TagID == 0 || p.AccountID == 0 {
		return nil
	}
	return s.unmergeTagFromHome(ctx, p.TagID, p.AccountID)
}

// handleAsynqUnfollowFollow mirrors UnfollowFollowWorker: migrate one follower
// from the old moved account to the new account, preserving follow settings and
// list memberships through FollowMigrationService-equivalent helpers.
func (s *Server) handleAsynqUnfollowFollow(ctx context.Context, t *asynq.Task) error {
	var p asynqUnfollowFollowPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unfollow follow: %w", err)
	}
	if s == nil || s.db == nil || p.FollowerAccountID == 0 || p.OldTargetAccountID == 0 || p.NewTargetAccountID == 0 {
		return nil
	}
	return s.performUnfollowFollowMigration(ctx, p.FollowerAccountID, p.OldTargetAccountID, p.NewTargetAccountID, p.BypassLocked)
}

// handleAsynqPublishScheduledStatus mirrors PublishScheduledStatusWorker.
func (s *Server) handleAsynqPublishScheduledStatus(ctx context.Context, t *asynq.Task) error {
	var p asynqScheduledStatusPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("publish scheduled status: %w", err)
	}
	if s == nil || s.db == nil || p.ScheduledStatusID == 0 {
		return nil
	}
	var scheduled models.ScheduledStatus
	if err := s.db.WithContext(ctx).Where("id = ?", p.ScheduledStatusID).First(&scheduled).Error; err != nil {
		return workerLookupError("publish scheduled status lookup", err)
	}
	_, err := s.publishScheduledStatus(ctx, scheduled, time.Now().UTC())
	return err
}

// handleAsynqPublishAnnouncement mirrors PublishScheduledAnnouncementWorker.
func (s *Server) handleAsynqPublishAnnouncement(ctx context.Context, t *asynq.Task) error {
	var p asynqAnnouncementPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("publish announcement: %w", err)
	}
	if s == nil || s.db == nil || p.AnnouncementID == 0 {
		return nil
	}
	var announcement models.Announcement
	if err := s.db.WithContext(ctx).Where("id = ?", p.AnnouncementID).First(&announcement).Error; err != nil {
		return workerLookupError("publish announcement lookup", err)
	}
	published, updated := s.publishAnnouncementWorker(ctx, announcement, time.Now().UTC())
	if published {
		s.broadcastAnnouncement(updated)
	}
	return nil
}

// handleAsynqUnpublishAnnouncement mirrors UnpublishAnnouncementWorker.
func (s *Server) handleAsynqUnpublishAnnouncement(ctx context.Context, t *asynq.Task) error {
	var p asynqAnnouncementPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unpublish announcement: %w", err)
	}
	if s == nil || p.AnnouncementID == 0 {
		return nil
	}
	s.broadcastAnnouncementDelete(p.AnnouncementID)
	return nil
}

// handleAsynqRemoteAccountRefresh mirrors RemoteAccountRefreshWorker: skip missing/local
// accounts, refresh known remote actors, and return retryable errors for transient fetches.
func (s *Server) handleAsynqRemoteAccountRefresh(ctx context.Context, t *asynq.Task) error {
	var p asynqRemoteAccountRefreshPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("remote account refresh: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("remote account refresh lookup", err)
	}
	return s.refreshActivityPubRemoteAccount(ctx, &account, p.RequestID)
}

// handleAsynqAccountRefresh mirrors AccountRefreshWorker: skip missing/local/fresh accounts,
// then resolve the known acct via WebFinger like ResolveAccountService.call(account).
func (s *Server) handleAsynqAccountRefresh(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountRefreshPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("account refresh: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("account refresh lookup", err)
	}
	if account.Local() || !activityPubActorRefreshStale(&account, time.Now().UTC()) {
		return nil
	}
	_, err := s.fetchAndStoreActivityActorForAcct(account.Acct())
	if err != nil {
		if activityFetchGone(err) {
			_ = s.deleteRemoteGoneAccountNow(ctx, &account, time.Now().UTC())
			return nil
		}
		if status, ok := activityFetchStatus(err); ok && activityPubDeliveryResponseErrorUnsalvageable(status) {
			return nil
		}
		return err
	}
	return nil
}

// handleAsynqAccountMerging mirrors AccountMergingWorker: skip missing/local accounts
// and merge duplicate remote accounts that share the verified ActivityPub URI.
func (s *Server) handleAsynqAccountMerging(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("account merging: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("account merging lookup", err)
	}
	return s.mergeDuplicateRemoteActivityPubAccounts(ctx, s.db.WithContext(ctx), account)
}

// handleAsynqResolveAccount mirrors ResolveAccountWorker: run ResolveAccountService for
// one acct URI. Rails' service defaults to suppress_errors: true, so fetch failures are
// best-effort and should not repeatedly poison the pull queue.
func (s *Server) handleAsynqResolveAccount(ctx context.Context, t *asynq.Task) error {
	var p asynqResolveAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("resolve account: %w", err)
	}
	if s == nil || s.db == nil || strings.TrimSpace(p.URI) == "" {
		return nil
	}
	existing, existingErr := s.resolveAccountWorkerExistingAccount(ctx, p.URI)
	if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
		return fmt.Errorf("resolve account existing lookup: %w", existingErr)
	}
	_, err := s.fetchAndStoreActivityActorForAcctDB(s.db.WithContext(ctx), p.URI)
	if activityFetchGone(err) && existingErr == nil {
		return s.deleteRemoteGoneAccountNow(ctx, existing, time.Now().UTC())
	}
	if status, ok := activityFetchStatus(err); ok && activityPubDeliveryResponseErrorUnsalvageable(status) {
		return nil
	}
	return err
}

func (s *Server) resolveAccountWorkerExistingAccount(ctx context.Context, uri string) (*models.Account, error) {
	if s == nil || s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	username, domain := railsLookupAcctParts(uri)
	if strings.TrimSpace(username) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	if domain != "" {
		domain = normalizeDeliveryStatsHost(domain)
	}
	return s.findAccountByUsernameDomainTx(s.db.WithContext(ctx), username, domain)
}

// handleAsynqPollUpdate mirrors ActivityPub::DistributePollUpdateWorker: load the
// status and fan out the signed UpdatePoll payload to poll recipients and relays.
func (s *Server) handleAsynqPollUpdate(ctx context.Context, t *asynq.Task) error {
	var p asynqStatusPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("poll update: %w", err)
	}
	if s == nil || s.db == nil || p.StatusID == 0 {
		return nil
	}
	status, err := s.findStatus(strconv.FormatInt(p.StatusID, 10))
	if err != nil {
		return workerLookupError("poll update status lookup", err)
	}
	if status == nil {
		return nil
	}
	return s.deliverActivityPubPollUpdate(*status)
}

// handleAsynqPollExpiration mirrors PollExpirationNotifyWorker: if the poll is
// still not due, requeue for expires_at + 5 minutes; otherwise create the
// Rails poll notifications and ActivityPub poll update fan-out.
func (s *Server) handleAsynqPollExpiration(ctx context.Context, t *asynq.Task) error {
	var p asynqPollPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("poll expiration: %w", err)
	}
	if s == nil || s.db == nil || p.PollID == 0 {
		return nil
	}
	var poll models.Poll
	if err := s.db.WithContext(ctx).Where("id = ?", p.PollID).First(&poll).Error; err != nil {
		return workerLookupError("poll expiration lookup", err)
	}
	if !poll.ExpiresAt.Valid {
		return nil
	}
	now := time.Now().UTC()
	if !poll.ExpiresAt.Time.Before(now) {
		if !s.enqueuePollExpirationTask(poll.ID, poll.ExpiresAt.Time.Add(pollExpirationRequeueDelay)) {
			return errors.New("poll expiration successor enqueue failed")
		}
		return nil
	}
	return s.notifyExpiredPoll(ctx, poll, now)
}

// handleAsynqAccountUpdate mirrors ActivityPub::UpdateDistributionWorker: load a local
// account and distribute the signed actor Update to AccountReachFinder recipients.
func (s *Server) handleAsynqAccountUpdate(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("account update: %w", err)
	}
	if err := validateAsynqPayloadVersion("account update", p.Version); err != nil {
		return err
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("account update lookup", err)
	}
	return s.deliverActivityPubAccountUpdateNowWithSigningKey(account, p.OldPrivateKey)
}

// handleAsynqAccountRawDistribution mirrors ActivityPub::AccountRawDistributionWorker:
// load the source account and push the already-generated JSON to AccountReachFinder inboxes.
func (s *Server) handleAsynqAccountRawDistribution(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountRawDistributionPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("account raw distribution: %w", err)
	}
	if s == nil || s.db == nil || p.SourceAccountID == 0 || strings.TrimSpace(p.JSON) == "" {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.SourceAccountID).First(&account).Error; err != nil {
		return workerLookupError("account raw distribution lookup", err)
	}
	return s.deliverActivityPubRawToAccountReach(account, []byte(p.JSON), p.ExcludeInboxes)
}

// handleAsynqRawDistribution mirrors ActivityPub::RawDistributionWorker:
// load the source account and push the already-generated JSON to followers.inboxes.
func (s *Server) handleAsynqRawDistribution(ctx context.Context, t *asynq.Task) error {
	var p asynqRawDistributionPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("raw distribution: %w", err)
	}
	if s == nil || s.db == nil || p.SourceAccountID == 0 || strings.TrimSpace(p.JSON) == "" {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.SourceAccountID).First(&account).Error; err != nil {
		return workerLookupError("raw distribution lookup", err)
	}
	return s.deliverActivityPubRawToFollowers(account, []byte(p.JSON), p.ExcludeInboxes)
}

// handleAsynqFeaturedCollectionSync mirrors
// ActivityPub::SynchronizeFeaturedCollectionWorker: load the remote account and synchronize
// its pinned statuses and optionally hashtags from the featured collection.
func (s *Server) handleAsynqFeaturedCollectionSync(ctx context.Context, t *asynq.Task) error {
	var p asynqFeaturedCollectionPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("featured collection sync: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	workerCtx, cleanup, acquired, err := s.featuredSyncContext(ctx, asynqTaskFeaturedCollectionSync, p.AccountID)
	if err != nil || !acquired {
		return err
	}
	defer cleanup()
	var account models.Account
	if err := s.db.WithContext(workerCtx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("featured collection account lookup", err)
	}
	return activityFetchWorkerError(s.syncActivityPubFeaturedCollectionNowWithContext(workerCtx, &account, p.CollectionURI, p.RequestID, p.SyncTags))
}

// handleAsynqFeaturedTagsSync mirrors ActivityPub::SynchronizeFeaturedTagsCollectionWorker:
// load the remote account and synchronize its Hashtag collection.
func (s *Server) handleAsynqFeaturedTagsSync(ctx context.Context, t *asynq.Task) error {
	var p asynqFeaturedTagsPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("featured tags sync: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	workerCtx, cleanup, acquired, err := s.featuredSyncContext(ctx, asynqTaskFeaturedTagsSync, p.AccountID)
	if err != nil || !acquired {
		return err
	}
	defer cleanup()
	var account models.Account
	if err := s.db.WithContext(workerCtx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("featured tags account lookup", err)
	}
	return activityFetchWorkerError(s.syncActivityPubFeaturedTagsNowWithContext(workerCtx, &account, p.CollectionURI))
}

func (s *Server) featuredSyncContext(parent context.Context, taskType string, accountID int64) (context.Context, func(), bool, error) {
	ctx, cancel := context.WithTimeout(parent, featuredCollectionWorkerTimeout)
	acquired, release, err := s.acquireActivityPubRedisLock(ctx, "featured_sync:"+taskType+":"+strconv.FormatInt(accountID, 10), featuredCollectionWorkerTimeout+time.Minute)
	if err != nil {
		cancel()
		return nil, nil, false, err
	}
	if !acquired {
		cancel()
		return nil, nil, false, nil
	}
	return ctx, func() {
		release()
		cancel()
	}, true, nil
}

// handleAsynqMoveDistribution mirrors ActivityPub::MoveDistributionWorker: load the
// migration and distribute a signed Move payload to followers, blocked_by accounts, and relays.
func (s *Server) handleAsynqMoveDistribution(ctx context.Context, t *asynq.Task) error {
	var p asynqMigrationPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("move distribution: %w", err)
	}
	if s == nil || s.db == nil || p.MigrationID == 0 {
		return nil
	}
	var migration models.AccountMigration
	if err := s.db.WithContext(ctx).Preload("Account").Preload("TargetAccount").Where("id = ?", p.MigrationID).First(&migration).Error; err != nil {
		return workerLookupError("move distribution migration lookup", err)
	}
	return s.deliverActivityPubMoveNow(migration)
}

// handleAsynqPostUpgrade mirrors ActivityPub::PostUpgradeWorker: clear stale WebFinger
// timestamps for accounts on a domain that upgraded from OStatus to ActivityPub.
func (s *Server) handleAsynqPostUpgrade(ctx context.Context, t *asynq.Task) error {
	var p asynqDomainPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("post upgrade: %w", err)
	}
	if s == nil || s.db == nil {
		return nil
	}
	return s.applyActivityPubPostUpgradeNow(ctx, s.db, p.Domain)
}

// handleAsynqFollowersSynchronization mirrors ActivityPub::FollowersSynchronizationWorker:
// load the remote account and reconcile unexpected local followers from the remote partial
// followers collection. Missing accounts are ignored like Rails Account.find_by.
func (s *Server) handleAsynqFollowersSynchronization(ctx context.Context, t *asynq.Task) error {
	var p asynqFollowersSynchronizationPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("followers synchronization: %w", err)
	}
	if err := validateAsynqPayloadVersion("followers synchronization", p.Version); err != nil {
		return err
	}
	if s == nil || s.db == nil || p.AccountID == 0 || strings.TrimSpace(p.URL) == "" {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.AccountID).First(&account).Error; err != nil {
		return workerLookupError("followers synchronization account lookup", err)
	}
	return activityFetchWorkerError(s.synchronizeActivityPubFollowers(ctx, account, p.URL))
}

// handleAsynqActivityPubProcessing mirrors ActivityPub::ProcessingWorker: reload the
// verified actor, then process the incoming ActivityPub body on the ingress queue.
func (s *Server) handleAsynqActivityPubProcessing(ctx context.Context, t *asynq.Task) error {
	var job activityPubInboxProcessingJob
	if err := json.Unmarshal(t.Payload(), &job); err != nil {
		return fmt.Errorf("activitypub processing: %w", err)
	}
	if s == nil || s.db == nil {
		return errors.New("activitypub processing: database is unavailable")
	}
	if job.ActorID == 0 || len(job.Body) == 0 {
		return activityPubProcessingError(job.Body, job.ActorID, job.DeliveredToAccountID, errors.New("task is missing actor or body"))
	}
	return s.performActivityPubInboxProcessingOnce(ctx, job)
}

// handleAsynqActivityPubDelivery mirrors ActivityPub::DeliveryWorker initial delivery.
// Transient failures are transferred to the existing Rails-shaped retry ZSET.
func (s *Server) handleAsynqActivityPubDelivery(ctx context.Context, t *asynq.Task) error {
	var job activityPubDeliveryRetryJob
	if err := json.Unmarshal(t.Payload(), &job); err != nil {
		return fmt.Errorf("activitypub delivery: %w", err)
	}
	if s == nil || s.db == nil || job.SourceAccountID == 0 || strings.TrimSpace(job.InboxURL) == "" || len(job.Body) == 0 {
		return nil
	}
	return s.performActivityPubDeliveryInitial(ctx, job)
}

// handleAsynqActivityPubDistribution mirrors ActivityPub::DistributionWorker:
// load the status and distribute the ActivityPresenter payload via StatusReachFinder.
func (s *Server) handleAsynqActivityPubDistribution(ctx context.Context, t *asynq.Task) error {
	status, err := s.statusFromAsynqTask(t)
	if err != nil || status == nil {
		return err
	}
	activity, err := activityPubOutboxActivityWithError(s, *status)
	if err != nil {
		return err
	}
	return s.deliverActivityPubStatusToFollowers(*status, activity)
}

// handleAsynqStatusUpdateDistribution mirrors ActivityPub::StatusUpdateDistributionWorker:
// load the edited status and distribute an Update activity via StatusReachFinder.
func (s *Server) handleAsynqStatusUpdateDistribution(ctx context.Context, t *asynq.Task) error {
	status, err := s.statusFromAsynqTask(t)
	if err != nil || status == nil {
		return err
	}
	activity, err := activityPubUpdateWithError(s, *status)
	if err != nil {
		return err
	}
	return s.deliverActivityPubStatusToFollowers(*status, activity)
}

func (s *Server) statusFromAsynqTask(t *asynq.Task) (*models.Status, error) {
	var p asynqStatusPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return nil, fmt.Errorf("status distribution: %w", err)
	}
	if s == nil || s.db == nil || p.StatusID == 0 {
		return nil, nil
	}
	status, err := s.findStatus(strconv.FormatInt(p.StatusID, 10))
	if err != nil {
		return nil, workerLookupError("status distribution lookup", err)
	}
	return status, nil
}

// handleAsynqCacheBuster mirrors CacheBusterWorker: issue the configured HTTP request to
// the already resolved public asset URL. Returning nil preserves Rails' best-effort cache
// buster behavior; failed purge requests should not block destructive moderation paths.
func (s *Server) handleAsynqCacheBuster(ctx context.Context, t *asynq.Task) error {
	var p asynqCacheBusterPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("cache buster: %w", err)
	}
	if s == nil || strings.TrimSpace(p.URL) == "" {
		return nil
	}
	s.bustCacheURLNow(p.URL)
	return nil
}

// handleAsynqDomainBlock mirrors DomainBlockWorker: reload the current DomainBlock,
// apply account limitations, and enqueue media clearing when reject_media is enabled.
func (s *Server) handleAsynqDomainBlock(ctx context.Context, t *asynq.Task) error {
	var p asynqDomainBlockPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("domain block: %w", err)
	}
	if s == nil || s.db == nil || p.DomainBlockID == 0 {
		return nil
	}
	var block models.DomainBlock
	if err := s.db.WithContext(ctx).Where("id = ?", p.DomainBlockID).First(&block).Error; err != nil {
		return workerLookupError("domain block lookup", err)
	}
	if err := s.applyAdminDomainBlockEffects(s.db.WithContext(ctx), block, p.Update); err != nil {
		return err
	}
	if block.RejectMedia && !s.enqueueDomainClearMediaTask(block.ID) {
		return s.clearDomainMediaCache(s.db.WithContext(ctx), block.Domain)
	}
	return nil
}

// handleAsynqDomainClearMedia mirrors DomainClearMediaWorker: ignore missing blocks and
// clear only when the current DomainBlock still has reject_media enabled.
func (s *Server) handleAsynqDomainClearMedia(ctx context.Context, t *asynq.Task) error {
	var p asynqDomainBlockPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("domain clear media: %w", err)
	}
	if s == nil || s.db == nil || p.DomainBlockID == 0 {
		return nil
	}
	var block models.DomainBlock
	if err := s.db.WithContext(ctx).Where("id = ?", p.DomainBlockID).First(&block).Error; err != nil {
		return workerLookupError("domain clear media block lookup", err)
	}
	if !block.RejectMedia {
		return nil
	}
	return s.clearDomainMediaCache(s.db.WithContext(ctx), block.Domain)
}

// handleAsynqAdminDomainPurge mirrors Admin::DomainPurgeWorker: purge all remote
// accounts and custom emoji for one instance domain, then refresh the instances view.
func (s *Server) handleAsynqAdminDomainPurge(ctx context.Context, t *asynq.Task) error {
	var p asynqDomainPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("admin domain purge: %w", err)
	}
	if s == nil || s.db == nil || strings.TrimSpace(p.Domain) == "" {
		return nil
	}
	return s.runPurgeAdminInstanceDomain(ctx, p.Domain, time.Now().UTC())
}

// handleAsynqAccountDeletion mirrors AccountDeletionWorker.
func (s *Server) handleAsynqAccountDeletion(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("account deletion: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	err := s.runOwnAccountDeletionWorkerEffects(ctx, p.AccountID, time.Now().UTC())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

// handleAsynqAdminAccountDeletion mirrors Admin::AccountDeletionWorker.
func (s *Server) handleAsynqAdminAccountDeletion(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("admin account deletion: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	err := s.runAdminAccountDeletionWorkerEffects(ctx, p.AccountID, time.Now().UTC())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

// handleAsynqAdminSuspension mirrors Admin::SuspensionWorker.
func (s *Server) handleAsynqAdminSuspension(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("admin suspension: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	err := s.applyAdminSuspensionWorkerEffects(ctx, s.db, p.AccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

// handleAsynqAdminUnsuspension mirrors Admin::UnsuspensionWorker.
func (s *Server) handleAsynqAdminUnsuspension(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("admin unsuspension: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	err := s.applyAdminUnsuspensionWorkerEffects(s.db, p.AccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

// handleAsynqBlock mirrors BlockWorker -> AfterBlockService: clean home/list feeds,
// notifications, and direct conversations for one account/target pair.
func (s *Server) handleAsynqBlock(ctx context.Context, t *asynq.Task) error {
	var p asynqRelationshipPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("block: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.TargetAccountID == 0 {
		return nil
	}
	return s.runAfterBlockWorkerEffects(ctx, s.db.WithContext(ctx), p.AccountID, p.TargetAccountID)
}

// handleAsynqMute mirrors MuteWorker: clear the target account from the muting
// account's home feed. Missing accounts are effectively no-ops, matching Rails rescue.
func (s *Server) handleAsynqMute(ctx context.Context, t *asynq.Task) error {
	var p asynqRelationshipPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("mute: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.TargetAccountID == 0 {
		return nil
	}
	return s.runMuteWorkerEffects(ctx, s.db.WithContext(ctx), p.AccountID, p.TargetAccountID)
}

// handleAsynqMerge mirrors MergeWorker: load both accounts, merge the source
// account into the local recipient's home feed, and clear the regeneration marker.
func (s *Server) handleAsynqMerge(ctx context.Context, t *asynq.Task) error {
	var p asynqRelationshipPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.TargetAccountID == 0 {
		return nil
	}
	var fromAccount models.Account
	if err := s.db.WithContext(ctx).Select("id").Where("id = ?", p.AccountID).First(&fromAccount).Error; err != nil {
		return workerLookupError("merge source account lookup", err)
	}
	var intoAccount models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.TargetAccountID).First(&intoAccount).Error; err != nil {
		return workerLookupError("merge target account lookup", err)
	}
	defer func() {
		_, _ = s.redisCommand(ctx, "DEL", redisConfig(s.cfg).prefix+"account:"+strconv.FormatInt(p.TargetAccountID, 10)+":regeneration")
	}()
	return s.mergeAccountIntoHomeFeed(ctx, s.db.WithContext(ctx), p.AccountID, intoAccount)
}

// handleAsynqUnmerge mirrors UnmergeWorker on the pull queue.
func (s *Server) handleAsynqUnmerge(ctx context.Context, t *asynq.Task) error {
	var p asynqRelationshipPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unmerge: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.TargetAccountID == 0 {
		return nil
	}
	var fromAccount models.Account
	if err := s.db.WithContext(ctx).Select("id").Where("id = ?", p.AccountID).First(&fromAccount).Error; err != nil {
		return workerLookupError("unmerge source account lookup", err)
	}
	var intoAccount models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", p.TargetAccountID).First(&intoAccount).Error; err != nil {
		return workerLookupError("unmerge target account lookup", err)
	}
	return s.unmergeAccountFromHomeFeed(ctx, s.db.WithContext(ctx), p.AccountID, intoAccount)
}

// handleAsynqDeleteMute mirrors DeleteMuteWorker: only expired rows delegate to
// the UnmuteService-equivalent side effects.
func (s *Server) handleAsynqDeleteMute(ctx context.Context, t *asynq.Task) error {
	var p asynqMutePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("delete mute: %w", err)
	}
	if s == nil || s.db == nil || p.MuteID == 0 {
		return nil
	}
	_, err := s.deleteExpiredMute(ctx, p.MuteID, time.Now().UTC())
	return err
}

// handleAsynqUnfavourite mirrors UnfavouriteWorker -> UnfavouriteService: delete the
// favourite row, remove the local notification, update counters, and deliver Undo Like.
func (s *Server) handleAsynqUnfavourite(ctx context.Context, t *asynq.Task) error {
	var p asynqStatusAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unfavourite: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.StatusID == 0 {
		return nil
	}
	return s.runUnfavouriteWorkerEffects(ctx, p.AccountID, p.StatusID)
}

// handleAsynqAfterAccountDomainBlock mirrors AfterAccountDomainBlockWorker:
// cleanup relationships and notifications after an AccountDomainBlock has been created.
func (s *Server) handleAsynqAfterAccountDomainBlock(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountDomainPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("after account domain block: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || strings.TrimSpace(p.Domain) == "" {
		return nil
	}
	return s.runAfterAccountDomainBlockEffects(ctx, p.AccountID, p.Domain)
}

// handleAsynqAfterUnallowDomain mirrors AfterUnallowDomainWorker.
func (s *Server) handleAsynqAfterUnallowDomain(ctx context.Context, t *asynq.Task) error {
	var p asynqDomainPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("after unallow domain: %w", err)
	}
	if s == nil || s.db == nil || strings.TrimSpace(p.Domain) == "" {
		return nil
	}
	return s.runAfterUnallowDomainEffects(ctx, s.db.WithContext(ctx), p.Domain)
}

// handleAsynqAnnouncementReaction mirrors PublishAnnouncementReactionWorker: load the
// current reaction aggregate and publish an announcement.reaction event to subscribed
// home timelines. Missing announcements/reactions are best-effort like Rails.
func (s *Server) handleAsynqAnnouncementReaction(ctx context.Context, t *asynq.Task) error {
	var p asynqAnnouncementReactionPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("announcement reaction: %w", err)
	}
	if s == nil || s.db == nil || p.AnnouncementID == 0 || strings.TrimSpace(p.Name) == "" {
		return nil
	}
	s.broadcastAnnouncementReaction(p.AnnouncementID, p.Name)
	return nil
}

// handleAsynqRemoval mirrors RemovalWorker. For statuses already discarded by admin
// batch actions, it runs the same feed/search/cache side effects that Rails performs
// after discard. For non-discarded statuses, it deletes through deleteStatusRecord.
func (s *Server) handleAsynqRemoval(ctx context.Context, t *asynq.Task) error {
	var p asynqRemovalPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("removal: %w", err)
	}
	if s == nil || s.db == nil || p.StatusID == 0 {
		return nil
	}
	acquired, releaseDistributionLock, err := s.acquireStatusDistributionRedisLock(ctx, p.StatusID)
	if err != nil {
		return err
	}
	if !acquired {
		return errStatusDistributionLockUnavailable
	}
	defer releaseDistributionLock()
	var status models.Status
	if err := s.db.WithContext(ctx).Unscoped().Preload("Account").Preload("Reblog").Where("id = ?", p.StatusID).First(&status).Error; err != nil {
		return workerLookupError("removal status lookup", err)
	}
	if status.DeletedAt.Valid {
		s.applyDeletedStatusRemovalSideEffects(ctx, status, p)
		reblogPurgeIDs := s.discardedUnreportedReblogIDs(ctx, status)
		if p.Immediate {
			purgeIDs := uniqueInt64s(append([]int64{p.StatusID}, reblogPurgeIDs...))
			s.applyAdminDeletedStatusSideEffects(ctx, s.db, purgeIDs)
			if err := s.syncPermanentStatusRemovalCounters(ctx, purgeIDs, time.Now().UTC()); err != nil {
				return err
			}
			return s.purgeDiscardedStatuses(ctx, purgeIDs)
		}
		if len(reblogPurgeIDs) > 0 {
			if err := s.syncPermanentStatusRemovalCounters(ctx, reblogPurgeIDs, time.Now().UTC()); err != nil {
				return err
			}
			return s.purgeDiscardedStatuses(ctx, reblogPurgeIDs)
		}
		return nil
	}
	now := time.Now().UTC()
	if err := s.deleteStatusRecord(ctx, p.StatusID, now); err != nil {
		return err
	}
	if !p.SkipStreaming {
		s.publishStatusAndReblogDeletesForIDs(ctx, s.db, []int64{p.StatusID})
	}
	if status.Account.Local() && !p.OriginalRemoved {
		_ = s.deliverActivityPubRemoval(status)
	}
	return nil
}

func (s *Server) applyDeletedStatusRemovalSideEffects(ctx context.Context, status models.Status, p asynqRemovalPayload) {
	if s == nil || s.db == nil || status.ID == 0 {
		return
	}
	_ = s.removeStatusFromRailsFeeds(ctx, s.db, status)
	s.meiliDeleteStatusBestEffort(ctx, status.ID)
	s.deleteStatusQuoteBestEffort(ctx, status.ID)
	if !p.SkipStreaming {
		s.publishStatusDelete(status)
	}
	if status.Account.Local() && !p.OriginalRemoved {
		_ = s.deliverActivityPubRemoval(status)
	}
	if !status.ReblogOfID.Valid {
		s.applyDeletedReblogRemovalSideEffects(ctx, status.ID, p)
	}
}

func (s *Server) applyDeletedReblogRemovalSideEffects(ctx context.Context, statusID int64, p asynqRemovalPayload) {
	var reblogs []models.Status
	if err := s.db.WithContext(ctx).Unscoped().Preload("Account").Preload("Reblog").Where("reblog_of_id = ? AND deleted_at IS NOT NULL", statusID).Find(&reblogs).Error; err != nil {
		return
	}
	for _, reblog := range reblogs {
		reblogPayload := p
		reblogPayload.StatusID = reblog.ID
		reblogPayload.OriginalRemoved = true
		_ = s.removeStatusFromRailsFeeds(ctx, s.db, reblog)
		s.meiliDeleteStatusBestEffort(ctx, reblog.ID)
		s.deleteStatusQuoteBestEffort(ctx, reblog.ID)
		if !reblogPayload.SkipStreaming {
			s.publishStatusDelete(reblog)
		}
	}
}

func (s *Server) deliverActivityPubRemoval(status models.Status) error {
	if status.ReblogOfID.Valid {
		undo := activityPubUndoAnnounce(s, status)
		return s.deliverActivityPubStatusToFollowers(status, undo)
	}
	activity, err := activityPubDeleteWithError(s, status)
	if err != nil {
		return err
	}
	return s.deliverActivityPubStatusToFollowers(status, activity)
}

// handleAsynqPushConversation mirrors PushConversationWorker: render the account
// conversation payload and publish it to the direct timeline when subscribed.
func (s *Server) handleAsynqPushConversation(ctx context.Context, t *asynq.Task) error {
	var p asynqConversationPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("push conversation: %w", err)
	}
	if s == nil || p.ConversationAccountID == 0 {
		return nil
	}
	s.publishConversation(ctx, p.ConversationAccountID)
	return nil
}

// handleAsynqPushUpdate mirrors PushUpdateWorker: hydrate the status for one recipient
// account and publish the update/status.update payload to the requested timeline.
func (s *Server) handleAsynqPushUpdate(ctx context.Context, t *asynq.Task) error {
	var p asynqPushUpdatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("push update: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 || p.StatusID == 0 {
		return nil
	}
	timelineID := strings.TrimSpace(p.TimelineID)
	if timelineID == "" {
		timelineID = "timeline:" + strconv.FormatInt(p.AccountID, 10)
	}
	s.publishStatusUpdateToTimeline(ctx, p.AccountID, p.StatusID, timelineID, p.Update)
	return nil
}

// handleAsynqWebPushNotification mirrors Web::PushNotificationWorker: reload the
// subscription and notification, double-check pushability, then send the encrypted push.
func (s *Server) handleAsynqWebPushNotification(ctx context.Context, t *asynq.Task) error {
	var p asynqWebPushNotificationPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return taskTargetError("web push notification payload", "local", serverLocalTaskTargetHost(s), err)
	}
	if s == nil || s.db == nil || p.SubscriptionID == 0 || p.NotificationID == 0 {
		return nil
	}
	return s.performWebPushNotificationDelivery(ctx, p.SubscriptionID, p.NotificationID)
}

// handleAsynqAuthorizeFollow mirrors AuthorizeFollowWorker: accept one pending follow
// request and ignore missing rows/accounts like Rails' RecordNotFound rescue.
func (s *Server) handleAsynqAuthorizeFollow(ctx context.Context, t *asynq.Task) error {
	var p asynqAuthorizeFollowPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("authorize follow: %w", err)
	}
	if s == nil || s.db == nil || p.SourceAccountID == 0 || p.TargetAccountID == 0 {
		return nil
	}
	if err := s.authorizeFollowRequestPairNow(ctx, p.SourceAccountID, p.TargetAccountID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

// handleAsynqBootstrapTimeline mirrors BootstrapTimelineWorker: run
// BootstrapTimelineService for a newly confirmed/approved local account.
func (s *Server) handleAsynqBootstrapTimeline(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("bootstrap timeline: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	return s.bootstrapApprovedAccount(ctx, p.AccountID, time.Now().UTC())
}

// handleAsynqRegeneration mirrors RegenerationWorker + PrecomputeFeedService:
// populate a returning user's home feed and always release the regeneration marker.
func (s *Server) handleAsynqRegeneration(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("regeneration: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	key := redisConfig(s.cfg).prefix + "account:" + strconv.FormatInt(p.AccountID, 10) + ":regeneration"
	defer func() {
		_, _ = s.redisCommand(ctx, "DEL", key)
	}()
	var user models.User
	var settings sql.NullString
	if err := s.db.WithContext(ctx).Select("settings").Where("account_id = ?", p.AccountID).First(&user).Error; err == nil {
		settings = user.Settings
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.populateAccountFeeds(ctx, s.db, p.AccountID, settings)
}

// handleAsynqVerifyAccountLinks mirrors VerifyAccountLinksWorker: run rel=me checks for
// one account's unverified profile fields and save the fields only when changed.
func (s *Server) handleAsynqVerifyAccountLinks(ctx context.Context, t *asynq.Task) error {
	var p asynqAccountPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("verify account links: %w", err)
	}
	if s == nil || s.db == nil || p.AccountID == 0 {
		return nil
	}
	return s.verifyAccountLinksNow(ctx, p.AccountID, time.Now().UTC())
}

// handleAsynqTriggerWebhook mirrors TriggerWebhookWorker: reload the target object,
// serialize the event, and enqueue one delivery worker per enabled webhook.
func (s *Server) handleAsynqTriggerWebhook(ctx context.Context, t *asynq.Task) error {
	var p asynqTriggerWebhookPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("trigger webhook: %w", err)
	}
	if s == nil || s.db == nil || p.ID == 0 {
		return nil
	}
	if err := s.triggerWebhookForRecord(ctx, p.Event, p.ClassName, p.ID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

// handleAsynqWebhookDelivery mirrors Webhooks::DeliveryWorker: reload the webhook,
// render any template, sign the final body, and POST to the endpoint.
func (s *Server) handleAsynqWebhookDelivery(ctx context.Context, t *asynq.Task) error {
	var p asynqWebhookDeliveryPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return taskTargetError("webhook delivery payload", "local", serverLocalTaskTargetHost(s), err)
	}
	if s == nil || s.db == nil || p.WebhookID == 0 || len(p.Body) == 0 {
		return nil
	}
	var webhook models.Webhook
	if err := s.db.WithContext(ctx).Where("id = ?", p.WebhookID).First(&webhook).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return taskTargetError("webhook lookup", "local", serverLocalTaskTargetHost(s), err)
	}
	return s.deliverWebhook(webhook, p.Body)
}

// notificationActivityStatusURL resolves a status URL for notification types that reference a
// status (mention/favourite/reblog/poll/status). Mentions/favourites resolve through their
// status_id; reblog/status notifications reference the status directly; poll notifications
// reference the poll's status. Returns "" for follow/follow_request or when no status applies.
func (s *Server) notificationActivityStatusURL(ctx context.Context, notification models.Notification) string {
	return notificationStatusURL(s.notificationActivityStatus(ctx, notification))
}

func (s *Server) notificationActivityStatus(ctx context.Context, notification models.Notification) *models.Status {
	statusID := s.notificationStatusID(ctx, notification)
	if statusID == 0 {
		return nil
	}
	var status models.Status
	if err := s.db.WithContext(ctx).Where("id = ?", statusID).First(&status).Error; err != nil {
		return nil
	}
	return &status
}

func notificationStatusURL(status *models.Status) string {
	if status == nil {
		return ""
	}
	if status.URL.Valid && status.URL.String != "" {
		return status.URL.String
	}
	if status.URI.Valid && status.URI.String != "" {
		return status.URI.String
	}
	return ""
}

// notificationStatusID resolves the status ID a notification refers to, mirroring Rails
// NotificationMailer: reblog/status notifications reference the status directly, while
// mention/favourite/poll notifications reference their own activity whose status_id points at
// the status. Returns 0 for follow/follow_request or when the referenced row is missing.
func (s *Server) notificationStatusID(ctx context.Context, notification models.Notification) int64 {
	switch notification.ActivityType {
	case "Status":
		return notification.ActivityID
	case "Mention":
		var mention models.Mention
		if err := s.db.WithContext(ctx).Select("status_id").Where("id = ?", notification.ActivityID).First(&mention).Error; err != nil {
			return 0
		}
		if !mention.StatusID.Valid {
			return 0
		}
		return mention.StatusID.Int64
	case "Favourite":
		var favourite models.Favourite
		if err := s.db.WithContext(ctx).Select("status_id").Where("id = ?", notification.ActivityID).First(&favourite).Error; err != nil {
			return 0
		}
		return favourite.StatusID
	case "Poll":
		var poll models.Poll
		if err := s.db.WithContext(ctx).Select("status_id").Where("id = ?", notification.ActivityID).First(&poll).Error; err != nil {
			return 0
		}
		if poll.StatusID.Valid {
			return poll.StatusID.Int64
		}
		return 0
	default:
		return 0
	}
}
