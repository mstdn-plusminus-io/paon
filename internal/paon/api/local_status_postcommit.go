package api

import (
	"context"
	"log"
	"runtime/debug"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type localStatusCreatePostCommit struct {
	RequestID            string
	Status               models.Status
	Account              models.Account
	ReplyTo              *models.Status
	NotificationIDs      []int64
	NotificationPayloads []asynqLocalNotificationPayload
	ConversationIDs      []int64
	IndexedTagIDs        []int64
	IdempotencyKey       string
	CreatedAt            time.Time
}

func (s *Server) startLocalStatusCreatePostCommit(effects localStatusCreatePostCommit) {
	if s == nil || effects.Status.ID == 0 {
		return
	}
	s.postCommitMu.Lock()
	if s.postCommitClosed {
		s.postCommitMu.Unlock()
		log.Printf("level=ERROR event=status_postcommit_rejected request_id=%q status_id=%d reason=%q", effects.RequestID, effects.Status.ID, "server is shutting down")
		return
	}
	s.postCommitWG.Add(1)
	s.postCommitMu.Unlock()

	go func() {
		defer s.postCommitWG.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("level=ERROR event=status_postcommit_panic request_id=%q status_id=%d panic=%q stack=%q", effects.RequestID, effects.Status.ID, recovered, debug.Stack())
			}
		}()
		s.runLocalStatusCreatePostCommit(effects)
	}()
}

func (s *Server) runLocalStatusCreatePostCommit(effects localStatusCreatePostCommit) {
	startedAt := time.Now()
	log.Printf("level=INFO event=status_postcommit_started request_id=%q status_id=%d", effects.RequestID, effects.Status.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	created := effects.Status
	s.rememberStatusIdempotency(ctx, effects.Account.ID, effects.IdempotencyKey, created.ID)
	if err := s.runLocalStatusAfterCreateCommitEffects(s.db.WithContext(ctx), &created, effects.Account, nil, effects.ReplyTo, func() {
		if statusCountsTowardLocalActivity(created.Visibility) {
			s.activityTrackerIncrementBasic(ctx, "activity:statuses:local", created.CreatedAt, 1)
		}
	}); err != nil {
		s.logLocalStatusPostCommitError(effects, "after_create_commit", err)
	}
	if refreshed, err := s.findStatusWithContext(ctx, created.ID); err != nil {
		s.logLocalStatusPostCommitError(effects, "reload_status", err)
	} else if refreshed != nil {
		created = *refreshed
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, effects.NotificationPayloads)
	if err != nil {
		s.logLocalStatusPostCommitError(effects, "notifications", err)
	}
	notificationIDs := append(append([]int64{}, effects.NotificationIDs...), createdNotificationIDs...)
	s.schedulePollExpirationNotifyWorker(created.Poll)
	s.meiliIndexStatusBestEffort(ctx, created.ID)
	s.meiliIndexTagsBestEffort(ctx, effects.IndexedTagIDs)
	s.recordStatusTrendUse(ctx, created.ID, created.CreatedAt)
	if err := s.enqueueFASPContentLifecycle(ctx, created, "new"); err != nil {
		s.logLocalStatusPostCommitError(effects, "fasp_content_lifecycle", err)
	}
	if created.InReplyToID.Valid {
		if err := s.enqueueFASPTrendForStatus(ctx, created, "reply"); err != nil {
			s.logLocalStatusPostCommitError(effects, "fasp_reply_trend", err)
		}
	}
	if created.InReplyToAccountID.Valid && created.InReplyToAccountID.Int64 != effects.Account.ID {
		s.activityTrackerIncrementBasic(ctx, "activity:interactions", created.CreatedAt, 1)
		s.recordPotentialFriendship(ctx, effects.Account.ID, created.InReplyToAccountID.Int64, "reply")
	}
	s.recordTagTrendUse(ctx, effects.Account.ID, created.Visibility, effects.IndexedTagIDs, effects.CreatedAt)
	s.publishStatusUpdateEvent("update", created)
	s.publishConversationIDs(ctx, effects.ConversationIDs)
	s.publishNotificationIDs(notificationIDs)
	if err := s.fanOutStatusToLocalRecipientsSkipNotifications(ctx, s.db.WithContext(ctx), created); err != nil {
		s.logLocalStatusPostCommitError(effects, "local_distribution", err)
	}
	s.fetchLinkCardForStatusAsync(created.ID)
	if err := s.enqueueOrDeliverActivityPubDistribution(created); err != nil {
		s.logLocalStatusPostCommitError(effects, "activitypub_distribution", err)
	}
	if err := s.deliverLocalQuoteRequest(ctx, created); err != nil {
		s.logLocalStatusPostCommitError(effects, "activitypub_quote_request", err)
	}
	log.Printf("level=INFO event=status_postcommit_completed request_id=%q status_id=%d duration_ms=%.2f", effects.RequestID, effects.Status.ID, time.Since(startedAt).Seconds()*1000)
}

func (s *Server) findStatusWithContext(ctx context.Context, statusID int64) (*models.Status, error) {
	if s == nil || s.db == nil || statusID == 0 {
		return nil, nil
	}
	var status models.Status
	err := s.statusQuery().WithContext(ctx).Where("statuses.id = ? AND statuses.deleted_at IS NULL", statusID).First(&status).Error
	if err == nil {
		err = s.hydrateStatusCustomEmojis(&status)
	}
	if err == nil {
		s.hydrateStatusQuote(&status)
	}
	return &status, err
}

func (s *Server) logLocalStatusPostCommitError(effects localStatusCreatePostCommit, effect string, err error) {
	if err == nil {
		return
	}
	log.Printf("level=ERROR event=status_postcommit_effect_failed request_id=%q status_id=%d effect=%q error=%q", effects.RequestID, effects.Status.ID, effect, err)
}

func (s *Server) closePostCommitWorkers(timeout time.Duration) {
	if s == nil {
		return
	}
	s.postCommitMu.Lock()
	s.postCommitClosed = true
	s.postCommitMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.postCommitWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Printf("level=ERROR event=status_postcommit_drain_timeout timeout=%q", timeout)
	}
}
