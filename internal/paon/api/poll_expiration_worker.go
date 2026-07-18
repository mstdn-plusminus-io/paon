package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	pollExpirationWorkerInterval = time.Minute
	pollExpirationBatchSize      = 100
	pollExpirationRequeueDelay   = 5 * time.Minute
)

const expiredPollNeedsNotificationSQL = `(
	EXISTS (
		SELECT 1 FROM accounts owner
		WHERE owner.id = polls.account_id
			AND (owner.domain IS NULL OR owner.domain = '')
			AND owner.suspended_at IS NULL
			AND NOT EXISTS (
				SELECT 1 FROM notifications existing_owner_notification
				WHERE existing_owner_notification.account_id = owner.id
					AND existing_owner_notification.activity_type = 'Poll'
					AND existing_owner_notification.activity_id = polls.id
			)
	)
	OR EXISTS (
		SELECT 1 FROM poll_votes
		JOIN accounts voter ON voter.id = poll_votes.account_id
		WHERE poll_votes.poll_id = polls.id
			AND (voter.domain IS NULL OR voter.domain = '')
			AND voter.suspended_at IS NULL
			AND NOT EXISTS (
				SELECT 1 FROM notifications existing_voter_notification
				WHERE existing_voter_notification.account_id = voter.id
					AND existing_voter_notification.activity_type = 'Poll'
					AND existing_voter_notification.activity_id = polls.id
			)
	)
)`

func (s *Server) runPollExpirationWorker(ctx context.Context) {
	s.processExpiredPolls(ctx, time.Now().UTC(), pollExpirationBatchSize)
	ticker := time.NewTicker(pollExpirationWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.processExpiredPolls(ctx, now.UTC(), pollExpirationBatchSize)
		}
	}
}

func (s *Server) processExpiredPolls(ctx context.Context, now time.Time, limit int) {
	if s == nil || s.db == nil || limit <= 0 {
		return
	}
	var polls []models.Poll
	if err := s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at < ? AND status_id IS NOT NULL", now.UTC()).
		Where(expiredPollNeedsNotificationSQL).
		Order("expires_at ASC, id ASC").
		Limit(limit).
		Find(&polls).Error; err != nil {
		return
	}
	for _, poll := range polls {
		s.schedulePollExpirationFinalCheck(&poll)
	}
}

func (s *Server) schedulePollExpirationNotifyWorker(poll *models.Poll) bool {
	if poll == nil || poll.ID == 0 || !poll.ExpiresAt.Valid {
		return false
	}
	return s.schedulePollExpirationNotifyWorkerAt(poll, poll.ExpiresAt.Time.UTC())
}

func (s *Server) schedulePollExpirationFinalCheck(poll *models.Poll) bool {
	if poll == nil || poll.ID == 0 || !poll.ExpiresAt.Valid {
		return false
	}
	return s.schedulePollExpirationNotifyWorkerAt(poll, poll.ExpiresAt.Time.Add(pollExpirationRequeueDelay))
}

func (s *Server) schedulePollExpirationNotifyWorkerAt(poll *models.Poll, runAt time.Time) bool {
	if poll == nil || poll.ID == 0 || runAt.IsZero() {
		return false
	}
	return s.enqueuePollExpirationTask(poll.ID, runAt.UTC())
}

func (s *Server) removeScheduledPollExpirationTasks(pollID int64) bool {
	if s == nil || s.asynqClient == nil || pollID == 0 {
		return false
	}
	inspector := asynq.NewInspector(asynqRedisOpt(s.cfg))
	defer inspector.Close()
	removed := false
	const pageSize = 100
	for {
		queue := s.asynqQueue(asynqQueueDefault)
		tasks, err := inspector.ListScheduledTasks(queue, asynq.PageSize(pageSize), asynq.Page(1))
		if err != nil {
			return removed
		}
		removedInPage := false
		for _, task := range tasks {
			if task == nil || task.Type != asynqTaskPollExpiration {
				continue
			}
			var payload asynqPollPayload
			if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.PollID != pollID {
				continue
			}
			if err := inspector.DeleteTask(queue, task.ID); err == nil {
				removed = true
				removedInPage = true
			}
		}
		if removedInPage {
			continue
		}
		if len(tasks) < pageSize {
			break
		}
	}
	return removed
}

func (s *Server) notifyExpiredPoll(ctx context.Context, poll models.Poll, now time.Time) error {
	if poll.ID == 0 || !poll.AccountID.Valid || poll.AccountID.Int64 == 0 {
		return nil
	}
	ownerID := poll.AccountID.Int64
	ownerLocal, err := s.pollOwnerLocal(ctx, ownerID)
	if err != nil {
		return err
	}
	voterIDs, err := s.localPollVoterIDs(ctx, poll.ID)
	if err != nil {
		return err
	}
	recipients := pollNotificationRecipientIDs(ownerID, ownerLocal, voterIDs)
	if len(recipients) == 0 {
		return nil
	}

	for _, accountID := range recipients {
		payload := asynqLocalNotificationPayload{ReceiverAccountID: accountID, FromAccountID: ownerID, ActivityID: poll.ID, ActivityType: "Poll", Type: "poll"}
		if !s.enqueueLocalNotificationTask(payload.ReceiverAccountID, payload.FromAccountID, payload.ActivityID, payload.ActivityType, payload.Type) {
			return errors.New("poll notification enqueue failed")
		}
	}
	if ownerLocal && poll.StatusID.Valid {
		status, err := s.findStatus(strconv.FormatInt(poll.StatusID.Int64, 10))
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if status != nil {
			s.publishStatusUpdateEvent("status.update", *status)
		}
		if !s.enqueuePollUpdateTask(poll.StatusID.Int64, 0) {
			return errors.New("poll update enqueue failed")
		}
	}
	return nil
}

func (s *Server) pollOwnerLocal(ctx context.Context, accountID int64) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&models.Account{}).
		Where("id = ? AND (domain IS NULL OR domain = '') AND suspended_at IS NULL", accountID).
		Count(&count).Error
	return count > 0, err
}

func (s *Server) localPollVoterIDs(ctx context.Context, pollID int64) ([]int64, error) {
	var ids []int64
	err := s.db.WithContext(ctx).
		Model(&models.PollVote{}).
		Distinct("poll_votes.account_id").
		Joins("JOIN accounts ON accounts.id = poll_votes.account_id").
		Where("poll_votes.poll_id = ? AND (accounts.domain IS NULL OR accounts.domain = '') AND accounts.suspended_at IS NULL", pollID).
		Order("poll_votes.account_id ASC").
		Pluck("poll_votes.account_id", &ids).Error
	return ids, err
}

func pollNotificationRecipientIDs(ownerID int64, ownerLocal bool, voterIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(voterIDs)+1)
	recipients := make([]int64, 0, len(voterIDs)+1)
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		recipients = append(recipients, id)
	}
	if ownerLocal {
		add(ownerID)
	}
	for _, id := range voterIDs {
		add(id)
	}
	return recipients
}

func createPollNotificationIfMissing(tx *gorm.DB, accountID int64, fromAccountID int64, pollID int64, at time.Time) (*models.Notification, error) {
	var existing int64
	if err := tx.Model(&models.Notification{}).
		Where("account_id = ? AND activity_type = ? AND activity_id = ?", accountID, "Poll", pollID).
		Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, nil
	}
	return createRelationshipNotificationRow(tx, accountID, fromAccountID, pollID, "Poll", "poll", at)
}
