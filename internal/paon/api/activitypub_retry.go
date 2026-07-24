package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const activityPubDeliveryRetryKey = "paon:activitypub:delivery:retry"
const activityPubDeliveryRetryFailureThreshold = 16

// Inbound activity processing mirrors Rails ActivityPub::ProcessingWorker (Sidekiq
// queue "ingress", retry 8). The inbox enqueues a job after signature verification and
// returns 202 once a backend accepts it; a background worker re-fetches the actor and runs
// processActivityPubInboxForTarget, re-enqueuing on failure up to the retry limit.
const activityPubInboxProcessingRetryKey = "paon:activitypub:ingress:retry"
const activityPubInboxProcessingRetryLimit = 8

type activityPubInboxProcessingJob struct {
	ActorID              int64           `json:"actor_id"`
	DeliveredToAccountID int64           `json:"delivered_to_account_id,omitempty"`
	ActorType            string          `json:"actor_type,omitempty"`
	Body                 json.RawMessage `json:"body"`
	Attempts             int             `json:"attempts"`
	CreatedAt            int64           `json:"created_at"`
}

func (s *Server) enqueueActivityPubInboxProcessingJob(actorID, deliveredTo int64, actorType string, body []byte) error {
	if s == nil || s.db == nil || actorID == 0 || len(body) == 0 {
		return nil
	}
	job := activityPubInboxProcessingJob{
		ActorID:              actorID,
		DeliveredToAccountID: deliveredTo,
		ActorType:            actorType,
		Body:                 append(json.RawMessage(nil), body...),
		Attempts:             0,
		CreatedAt:            time.Now().UTC().Unix(),
	}
	return enqueueActivityPubInboxProcessingJobWithBackends(
		context.Background(),
		job,
		s.enqueueActivityPubProcessingTask,
		s.enqueueActivityPubInboxProcessingJobRetry,
	)
}

func enqueueActivityPubInboxProcessingJobWithBackends(
	ctx context.Context,
	job activityPubInboxProcessingJob,
	enqueueAsynq func(activityPubInboxProcessingJob) bool,
	enqueueFallback func(context.Context, activityPubInboxProcessingJob) error,
) error {
	if job.ActorID == 0 || len(job.Body) == 0 {
		return nil
	}
	if enqueueAsynq != nil && enqueueAsynq(job) {
		return nil
	}
	if enqueueFallback == nil {
		return errors.New("activitypub inbox processing fallback enqueue unavailable")
	}
	if err := enqueueFallback(ctx, job); err != nil {
		return fmt.Errorf("enqueue activitypub inbox processing fallback: %w", err)
	}
	return nil
}

func (s *Server) enqueueActivityPubInboxProcessingJobRetry(ctx context.Context, job activityPubInboxProcessingJob) error {
	if job.ActorID == 0 || len(job.Body) == 0 {
		return nil
	}
	encoded, runAt, err := nextActivityPubInboxRetry(job, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = s.redisCommand(ctx, "ZADD", redisConfig(s.cfg).prefix+activityPubInboxProcessingRetryKey, strconv.FormatInt(runAt.Unix(), 10), encoded)
	return err
}

func nextActivityPubInboxRetry(job activityPubInboxProcessingJob, now time.Time) (string, time.Time, error) {
	job.Attempts++
	runAt := now.UTC().Add(activityPubDeliveryRetryDelay(job.Attempts))
	encoded, err := json.Marshal(job)
	return string(encoded), runAt, err
}

func (s *Server) runActivityPubInboxProcessingWorker(ctx context.Context) {
	s.processDueActivityPubInboxProcessingJobs(ctx, 25)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDueActivityPubInboxProcessingJobs(ctx, 25)
		}
	}
}

func (s *Server) processDueActivityPubInboxProcessingJobs(ctx context.Context, limit int) {
	if s.db == nil || limit <= 0 {
		return
	}
	key := redisConfig(s.cfg).prefix + activityPubInboxProcessingRetryKey
	now := time.Now().UTC()
	claims, err := s.claimRedisRetryJobs(ctx, key, limit, now)
	if err != nil {
		return
	}
	for _, claim := range claims {
		var job activityPubInboxProcessingJob
		if err := json.Unmarshal([]byte(claim.Member), &job); err != nil {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		if err := s.performActivityPubInboxProcessingOnce(ctx, job); err == nil || job.Attempts >= activityPubInboxProcessingRetryLimit {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		successor, runAt, err := nextActivityPubInboxRetry(job, now)
		if err != nil {
			continue
		}
		_ = s.replaceRedisRetryJob(ctx, key, claim, successor, runAt)
	}
}

func (s *Server) performActivityPubInboxProcessing(ctx context.Context, job activityPubInboxProcessingJob) {
	if err := s.performActivityPubInboxProcessingOnce(ctx, job); err != nil {
		s.reenqueueActivityPubInboxProcessing(ctx, job)
	}
}

func (s *Server) performActivityPubInboxProcessingOnce(ctx context.Context, job activityPubInboxProcessingJob) error {
	if job.ActorType != "" && job.ActorType != "Account" {
		return nil
	}
	if s.db == nil {
		return nil
	}
	var actor models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", job.ActorID).First(&actor).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	err := s.processActivityPubInboxForDeliveredToWithContext(ctx, job.Body, &actor, nil, job.DeliveredToAccountID)
	if activityPubInboxPermanentValidationError(err) {
		logActivityPubProcessingIssue("rejected", "permanent_validation_error", job.Body, actor.ID, job.DeliveredToAccountID, err)
		return nil
	}
	return activityPubProcessingError(job.Body, actor.ID, job.DeliveredToAccountID, err)
}

func activityPubInboxPermanentValidationError(err error) bool {
	return errors.Is(err, errFeaturedTagInvalidName) || errors.Is(err, errFeaturedTagLimit) || errors.Is(err, errFeaturedTagDuplicate)
}

func (s *Server) reenqueueActivityPubInboxProcessing(ctx context.Context, job activityPubInboxProcessingJob) {
	if job.Attempts >= activityPubInboxProcessingRetryLimit {
		return
	}
	_ = s.enqueueActivityPubInboxProcessingJobRetry(ctx, job)
}

type activityPubDeliveryRetryJob struct {
	SourceAccountID                  int64           `json:"source_account_id"`
	InboxURL                         string          `json:"inbox_url"`
	Body                             json.RawMessage `json:"body"`
	Attempts                         int             `json:"attempts"`
	CreatedAt                        int64           `json:"created_at"`
	RetryLimit                       int             `json:"retry_limit,omitempty"`
	SynchronizeFollowers             bool            `json:"synchronize_followers,omitempty"`
	BypassAvailability               bool            `json:"bypass_availability,omitempty"`
	MigratedFollowOldTargetAccountID int64           `json:"migrated_follow_old_target_account_id,omitempty"`
}

func (s *Server) enqueueActivityPubDeliveryRetry(local models.Account, inboxURL string, body []byte) {
	s.enqueueActivityPubDeliveryRetryConfigured(local, inboxURL, body, nil)
}

func (s *Server) enqueueActivityPubDeliveryRetryConfigured(local models.Account, inboxURL string, body []byte, configure func(*activityPubDeliveryRetryJob)) {
	if s.db == nil || local.ID == 0 || inboxURL == "" || len(body) == 0 {
		return
	}
	job := activityPubDeliveryRetryJob{
		SourceAccountID: local.ID,
		InboxURL:        inboxURL,
		Body:            append(json.RawMessage(nil), body...),
		Attempts:        0,
		CreatedAt:       time.Now().UTC().Unix(),
	}
	if configure != nil {
		configure(&job)
	}
	_ = s.enqueueActivityPubDeliveryRetryJob(context.Background(), job)
}

func (s *Server) enqueueActivityPubDeliveryRetryJob(ctx context.Context, job activityPubDeliveryRetryJob) error {
	if job.SourceAccountID == 0 || job.InboxURL == "" || len(job.Body) == 0 {
		return nil
	}
	encoded, runAt, err := nextActivityPubDeliveryRetry(job, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = s.redisCommand(ctx, "ZADD", redisConfig(s.cfg).prefix+activityPubDeliveryRetryKey, strconv.FormatInt(runAt.Unix(), 10), encoded)
	return err
}

func nextActivityPubDeliveryRetry(job activityPubDeliveryRetryJob, now time.Time) (string, time.Time, error) {
	job.Attempts++
	runAt := now.UTC().Add(activityPubDeliveryRetryDelay(job.Attempts))
	encoded, err := json.Marshal(job)
	return string(encoded), runAt, err
}

func activityPubDeliveryRetryDelay(attempts int) time.Duration {
	return activityPubDeliveryRetryDelayWithRand(attempts, rand.Int63n)
}

func activityPubDeliveryRetryDelayWithRand(attempts int, int63n func(int64) int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	count := attempts - 1
	if count > activityPubDeliveryRetryFailureThreshold-1 {
		count = activityPubDeliveryRetryFailureThreshold - 1
	}
	seconds := count*count*count*count + 15
	delay := time.Duration(seconds) * time.Second
	maxJitter := time.Duration(count*count*count*count) * time.Second / 2
	if maxJitter <= 0 || int63n == nil {
		return delay
	}
	return delay + time.Duration(int63n(int64(maxJitter)))
}

func (s *Server) StartBackgroundWorkers(ctx context.Context) *BackgroundWorkers {
	workers := newBackgroundWorkers()
	workers.Go(ctx, s.runPaonGoWorkerHeartbeat)
	workers.Go(ctx, func(ctx context.Context) {
		s.runSchedulerWithRedisLock(ctx, "meili_index_definition_scheduler", 30*time.Minute, func() {
			s.syncMeiliIndexesBestEffort(ctx)
		})
	})
	workers.Go(ctx, s.runStatsDInformantWorker)
	workers.Go(ctx, s.runActivityPubDeliveryRetryWorker)
	workers.Go(ctx, s.runActivityPubInboxProcessingWorker)
	workers.Go(ctx, s.startAsynqWorker)
	workers.Go(ctx, s.runWebhookDeliveryRetryWorker)
	workers.Go(ctx, s.runWebPushDeliveryRetryWorker)
	workers.Go(ctx, s.runScheduledStatusPublishWorker)
	workers.Go(ctx, s.runStatusesCleanupWorker)
	workers.Go(ctx, s.runPollExpirationWorker)
	workers.Go(ctx, s.runMuteExpirationWorker)
	workers.Go(ctx, s.runAccountDeletionWorker)
	workers.Go(ctx, s.runBackupVacuumWorker)
	workers.Go(ctx, s.runOAuthVacuumWorker)
	workers.Go(ctx, s.runImportVacuumWorker)
	workers.Go(ctx, s.runFeedVacuumWorker)
	workers.Go(ctx, s.runPreviewCardVacuumWorker)
	workers.Go(ctx, s.runMediaVacuumWorker)
	workers.Go(ctx, s.runMediaPostProcessWorker)
	workers.Go(ctx, s.runRemoteMediaRedownloadWorker)
	workers.Go(ctx, s.runStatusVacuumWorker)
	workers.Go(ctx, s.runFollowRecommendationsWorker)
	workers.Go(ctx, s.runIPCleanupWorker)
	workers.Go(ctx, s.runUserCleanupWorker)
	workers.Go(ctx, s.runAutoCloseRegistrationsWorker)
	workers.Go(ctx, s.runPgHeroSpaceStatsWorker)
	workers.Go(ctx, s.runAdminMetricsPrewarmWorker)
	workers.Go(ctx, s.runInstanceRefreshWorker)
	workers.Go(ctx, s.runIndexingWorker)
	workers.Go(ctx, s.runSoftwareUpdateCheckWorker)
	workers.Go(ctx, s.runTrendsRefreshWorker)
	workers.Go(ctx, s.runFeaturedTagRefreshWorker)
	workers.Go(ctx, s.runProfileVerificationWorker)
	workers.Seal()
	return workers
}

func (s *Server) runActivityPubDeliveryRetryWorker(ctx context.Context) {
	s.processDueActivityPubDeliveryRetries(ctx, 25)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDueActivityPubDeliveryRetries(ctx, 25)
		}
	}
}

func (s *Server) processDueActivityPubDeliveryRetries(ctx context.Context, limit int) {
	if s.db == nil || limit <= 0 {
		return
	}
	key := redisConfig(s.cfg).prefix + activityPubDeliveryRetryKey
	now := time.Now().UTC()
	claims, err := s.claimRedisRetryJobs(ctx, key, limit, now)
	if err != nil {
		return
	}
	for _, claim := range claims {
		var job activityPubDeliveryRetryJob
		if err := json.Unmarshal([]byte(claim.Member), &job); err != nil {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		if err := s.performActivityPubDeliveryRetryOnce(ctx, job); err == nil || job.Attempts >= job.activityPubDeliveryRetryLimit() {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		successor, runAt, err := nextActivityPubDeliveryRetry(job, now)
		if err != nil {
			continue
		}
		_ = s.replaceRedisRetryJob(ctx, key, claim, successor, runAt)
	}
}

func (s *Server) performActivityPubDeliveryRetry(ctx context.Context, job activityPubDeliveryRetryJob) {
	if err := s.performActivityPubDeliveryRetryOnce(ctx, job); err != nil && job.Attempts < job.activityPubDeliveryRetryLimit() {
		_ = s.enqueueActivityPubDeliveryRetryJob(ctx, job)
	}
}

func (s *Server) performActivityPubDeliveryRetryOnce(ctx context.Context, job activityPubDeliveryRetryJob) error {
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", job.SourceAccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	host := activityPubDeliveryHost(job.InboxURL)
	if host == "" {
		return nil
	}
	if !job.BypassAvailability && !s.activityPubDeliveryAvailable(host) {
		return nil
	}
	if err := s.deliverActivityPubOnce(account, job.InboxURL, job.Body, host, job.SynchronizeFollowers); err != nil {
		s.trackActivityPubDeliveryFailure(host)
		return err
	}
	return s.afterActivityPubDeliveryRetrySuccess(ctx, job)
}

func (s *Server) performActivityPubDeliveryInitial(ctx context.Context, job activityPubDeliveryRetryJob) error {
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", job.SourceAccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	host := activityPubDeliveryHost(job.InboxURL)
	if host == "" {
		return nil
	}
	if !job.BypassAvailability && !s.activityPubDeliveryAvailable(host) {
		return nil
	}
	if err := s.deliverActivityPubOnce(account, job.InboxURL, job.Body, host, job.SynchronizeFollowers); err != nil {
		s.trackActivityPubDeliveryFailure(host)
		return err
	}
	return s.afterActivityPubDeliveryRetrySuccess(ctx, job)
}

func (s *Server) afterActivityPubDeliveryRetrySuccess(ctx context.Context, job activityPubDeliveryRetryJob) error {
	if job.MigratedFollowOldTargetAccountID == 0 {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := deleteFollowEdge(tx, job.SourceAccountID, job.MigratedFollowOldTargetAccountID); err != nil {
			return err
		}
		var request models.FollowRequest
		err := tx.Where("account_id = ? AND target_account_id = ?", job.SourceAccountID, job.MigratedFollowOldTargetAccountID).First(&request).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if _, err := deleteListAccountsForRejectedFollowRequest(tx, request.ID); err != nil {
			return err
		}
		return tx.Delete(&request).Error
	})
}

func (job activityPubDeliveryRetryJob) activityPubDeliveryRetryLimit() int {
	if job.RetryLimit > 0 {
		return job.RetryLimit
	}
	return activityPubDeliveryRetryFailureThreshold
}

func activityPubDeliveryHost(inboxURL string) string {
	parsed, err := url.Parse(inboxURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return normalizeDeliveryStatsHost(parsed.Hostname())
}

func redisStringArray(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			out = append(out, v)
		default:
			return nil, false
		}
	}
	return out, true
}

func (job activityPubDeliveryRetryJob) String() string {
	return fmt.Sprintf("source=%d inbox=%s attempts=%d", job.SourceAccountID, job.InboxURL, job.Attempts)
}
