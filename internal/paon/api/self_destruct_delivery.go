package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	selfDestructMaxDeliveryBatch      = 1_000
	selfDestructDeliveryMarkerHashKey = "self_destruct:delivered"
	asynqTaskSelfDestructDelivery     = "self_destruct:delivery"
)

func (s *Server) enqueueSelfDestructAccountDelete(ctx context.Context, account models.Account, inboxes []string) error {
	if !s.selfDestructEnabled() {
		return errors.New("refusing self-destruct delivery without a valid SELF_DESTRUCT token")
	}
	if len(inboxes) == 0 {
		return nil
	}
	activity, err := s.signActivityPubLinkedDataSignaturePayload(account, activityPubDeleteActor(s, account))
	if err != nil {
		return err
	}
	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	for _, batch := range selfDestructInboxBatches(inboxes, selfDestructMaxDeliveryBatch) {
		if err := s.enqueueSelfDestructDeliveryBatch(ctx, account.ID, body, batch); err != nil {
			return err
		}
	}
	return nil
}

func selfDestructInboxBatches(inboxes []string, limit int) [][]string {
	if limit <= 0 || len(inboxes) == 0 {
		return nil
	}
	batches := make([][]string, 0, (len(inboxes)+limit-1)/limit)
	for start := 0; start < len(inboxes); start += limit {
		end := start + limit
		if end > len(inboxes) {
			end = len(inboxes)
		}
		batches = append(batches, inboxes[start:end])
	}
	return batches
}

func (s *Server) enqueueSelfDestructDeliveryBatch(ctx context.Context, accountID int64, body []byte, inboxes []string) error {
	if !s.selfDestructEnabled() {
		return errors.New("refusing self-destruct batch without a valid SELF_DESTRUCT token")
	}
	if s.asynqClient == nil {
		return errors.New("self-destruct Asynq client is unavailable")
	}
	if accountID == 0 || len(body) == 0 || len(inboxes) == 0 || len(inboxes) > selfDestructMaxDeliveryBatch {
		return errors.New("invalid self-destruct delivery batch")
	}
	for _, inboxURL := range inboxes {
		job := activityPubDeliveryRetryJob{
			SourceAccountID: accountID,
			InboxURL:        strings.TrimSpace(inboxURL),
			Body:            append(json.RawMessage(nil), body...),
			CreatedAt:       time.Now().UTC().Unix(),
		}
		if job.InboxURL == "" {
			continue
		}
		payload, err := marshalAsynqTaskPayload(job)
		if err != nil {
			return err
		}
		task := asynq.NewTask(
			asynqTaskSelfDestructDelivery,
			payload,
			asynq.Queue(s.asynqQueue(asynqQueuePush)),
			asynq.MaxRetry(activityPubDeliveryRetryFailureThreshold),
			asynq.TaskID(selfDestructDeliveryTaskID(accountID, job.InboxURL)),
		)
		if _, err := s.asynqClient.EnqueueContext(ctx, task); err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
			return err
		}
	}
	return nil
}

func selfDestructDeliveryTaskID(accountID int64, inboxURL string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(inboxURL)))
	return fmt.Sprintf("self-destruct-%d-%s", accountID, hex.EncodeToString(digest[:16]))
}

// handleAsynqSelfDestructDelivery uses a compact durable marker in worker Redis.
// The deterministic task ID suppresses duplicates while queued; the marker
// suppresses delivery after a scheduler retry or process restart.
func (s *Server) handleAsynqSelfDestructDelivery(ctx context.Context, task *asynq.Task) error {
	var job activityPubDeliveryRetryJob
	if err := json.Unmarshal(task.Payload(), &job); err != nil {
		return fmt.Errorf("self-destruct delivery: %w", err)
	}
	if s == nil || s.db == nil || job.SourceAccountID == 0 || strings.TrimSpace(job.InboxURL) == "" || len(job.Body) == 0 {
		return nil
	}
	marker := selfDestructDeliveryMarker(job.SourceAccountID, job.InboxURL)
	redisCfg := sidekiqRedisConfig(s.cfg)
	key := selfDestructDeliveryMarkerKey(redisCfg.prefix)
	alreadyDelivered, err := redisCommandWithConfig(ctx, redisCfg, "HEXISTS", key, marker)
	if err != nil {
		return fmt.Errorf("self-destruct delivery marker lookup: %w", err)
	}
	if redisInteger(alreadyDelivered) == 1 {
		return nil
	}
	if err := s.performActivityPubDeliveryInitial(ctx, job); err != nil {
		return err
	}
	if _, err := redisCommandWithConfig(ctx, redisCfg, "HSET", key, marker, "1"); err != nil {
		return fmt.Errorf("record self-destruct delivery marker: %w", err)
	}
	return nil
}

func selfDestructDeliveryMarker(accountID int64, inboxURL string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(inboxURL)))
	return strconv.FormatInt(accountID, 10) + ":" + hex.EncodeToString(digest[:])
}

func selfDestructDeliveryMarkerKey(namespace string) string {
	namespace = strings.TrimSuffix(strings.TrimSpace(namespace), ":")
	if namespace == "" {
		return selfDestructDeliveryMarkerHashKey
	}
	return namespace + ":" + selfDestructDeliveryMarkerHashKey
}
