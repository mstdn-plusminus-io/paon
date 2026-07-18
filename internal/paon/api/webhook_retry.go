package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const webhookDeliveryRetryKey = "paon:webhooks:delivery:retry"
const webhookDeliveryRetryFailureThreshold = 7

type webhookDeliveryRetryJob struct {
	WebhookID int64           `json:"webhook_id"`
	Body      json.RawMessage `json:"body"`
	Attempts  int             `json:"attempts"`
	CreatedAt int64           `json:"created_at"`
}

func (s *Server) enqueueWebhookDeliveryRetry(webhookID int64, body []byte) {
	if s == nil || s.db == nil || webhookID == 0 || len(body) == 0 {
		return
	}
	job := webhookDeliveryRetryJob{
		WebhookID: webhookID,
		Body:      append(json.RawMessage(nil), body...),
		Attempts:  0,
		CreatedAt: time.Now().UTC().Unix(),
	}
	_ = s.enqueueWebhookDeliveryRetryJob(context.Background(), job)
}

func (s *Server) enqueueWebhookDeliveryRetryJob(ctx context.Context, job webhookDeliveryRetryJob) error {
	if job.WebhookID == 0 || len(job.Body) == 0 {
		return nil
	}
	encoded, runAt, err := nextWebhookDeliveryRetry(job, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = s.redisCommand(ctx, "ZADD", redisConfig(s.cfg).prefix+webhookDeliveryRetryKey, strconv.FormatInt(runAt.Unix(), 10), encoded)
	return err
}

func nextWebhookDeliveryRetry(job webhookDeliveryRetryJob, now time.Time) (string, time.Time, error) {
	job.Attempts++
	runAt := now.UTC().Add(webhookDeliveryRetryDelay(job.Attempts))
	encoded, err := json.Marshal(job)
	return string(encoded), runAt, err
}

func webhookDeliveryRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 8 {
		attempts = 8
	}
	seconds := attempts * attempts * 15
	return time.Duration(seconds) * time.Second
}

func (s *Server) runWebhookDeliveryRetryWorker(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDueWebhookDeliveryRetries(ctx, 25)
		}
	}
}

func (s *Server) processDueWebhookDeliveryRetries(ctx context.Context, limit int) {
	if s == nil || s.db == nil || limit <= 0 {
		return
	}
	key := redisConfig(s.cfg).prefix + webhookDeliveryRetryKey
	now := time.Now().UTC()
	claims, err := s.claimRedisRetryJobs(ctx, key, limit, now)
	if err != nil {
		return
	}
	for _, claim := range claims {
		var job webhookDeliveryRetryJob
		if err := json.Unmarshal([]byte(claim.Member), &job); err != nil {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		if err := s.performWebhookDeliveryRetryOnce(ctx, job); err == nil || job.Attempts >= webhookDeliveryRetryFailureThreshold {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		successor, runAt, err := nextWebhookDeliveryRetry(job, now)
		if err != nil {
			continue
		}
		_ = s.replaceRedisRetryJob(ctx, key, claim, successor, runAt)
	}
}

func (s *Server) performWebhookDeliveryRetry(ctx context.Context, job webhookDeliveryRetryJob) {
	if err := s.performWebhookDeliveryRetryOnce(ctx, job); err != nil && job.Attempts < webhookDeliveryRetryFailureThreshold {
		_ = s.enqueueWebhookDeliveryRetryJob(ctx, job)
	}
}

func (s *Server) performWebhookDeliveryRetryOnce(ctx context.Context, job webhookDeliveryRetryJob) error {
	var webhook models.Webhook
	if err := s.db.WithContext(ctx).Where("id = ?", job.WebhookID).First(&webhook).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	return s.deliverWebhook(webhook, job.Body)
}
