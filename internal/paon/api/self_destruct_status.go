package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const selfDestructMaxEnqueued = 10_000

type selfDestructQueueStats struct {
	Pending   int
	Active    int
	Retry     int
	Scheduled int
	Archived  int
}

func (stats selfDestructQueueStats) unfinished() int {
	return stats.Pending + stats.Active + stats.Retry + stats.Scheduled + stats.Archived
}

type selfDestructLoad struct {
	Queue      selfDestructQueueStats
	UsedMemory int64
	MaxMemory  int64
}

func (load selfDestructLoad) pauseReason() string {
	if load.Queue.Pending > selfDestructMaxEnqueued {
		return fmt.Sprintf("enqueued backlog %d exceeds %d", load.Queue.Pending, selfDestructMaxEnqueued)
	}
	if load.MaxMemory > 0 && load.UsedMemory > load.MaxMemory/2 {
		return fmt.Sprintf("Redis memory %d exceeds 50%% of %d", load.UsedMemory, load.MaxMemory)
	}
	return ""
}

func selfDestructPositiveMemoryLimit(configuredMaxMemory int64, totalSystemMemory int64) int64 {
	limit := int64(0)
	for _, value := range []int64{configuredMaxMemory, totalSystemMemory} {
		if value > 0 && (limit == 0 || value < limit) {
			limit = value
		}
	}
	return limit
}

func (s *Server) currentSelfDestructLoad(ctx context.Context) (selfDestructLoad, error) {
	var load selfDestructLoad
	queue, err := s.currentSelfDestructQueueStats()
	if err != nil {
		return load, fmt.Errorf("inspect self-destruct queues: %w", err)
	}
	load.Queue = queue
	value, err := redisCommandWithConfig(ctx, sidekiqRedisConfig(s.cfg), "INFO", "memory")
	if err != nil {
		return load, fmt.Errorf("inspect self-destruct Redis memory: %w", err)
	}
	info, ok := redisStringValue(value)
	if !ok {
		return load, errors.New("inspect self-destruct Redis memory: unexpected INFO response")
	}
	used, err := parseSelfDestructRedisMemory(info, "used_memory")
	if err != nil {
		return load, err
	}
	configured, _ := parseSelfDestructRedisMemory(info, "maxmemory")
	total, _ := parseSelfDestructRedisMemory(info, "total_system_memory")
	load.UsedMemory = used
	load.MaxMemory = selfDestructPositiveMemoryLimit(configured, total)
	if load.MaxMemory <= 0 {
		return load, errors.New("inspect self-destruct Redis memory: no positive maxmemory or total_system_memory")
	}
	return load, nil
}

func redisStringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func parseSelfDestructRedisMemory(info string, key string) (int64, error) {
	raw := redisInfoValue(info, key)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("inspect self-destruct Redis memory: invalid %s", key)
	}
	return value, nil
}

func (s *Server) currentSelfDestructQueueStats() (selfDestructQueueStats, error) {
	var stats selfDestructQueueStats
	if s == nil || s.asynqInspector == nil {
		return stats, errors.New("Asynq inspector is unavailable")
	}
	queues, err := s.asynqInspector.Queues()
	if err != nil {
		return stats, err
	}
	for _, queue := range queues {
		info, err := s.asynqInspector.GetQueueInfo(queue)
		if err != nil {
			return stats, err
		}
		stats.Pending += info.Pending
		stats.Active += info.Active
		stats.Retry += info.Retry
		stats.Scheduled += info.Scheduled
		stats.Archived += info.Archived
	}
	return stats, nil
}

type SelfDestructStatus struct {
	Configured                bool   `json:"configured"`
	Enabled                   bool   `json:"enabled"`
	LocalDomain               string `json:"local_domain"`
	PendingUnsuspended        int64  `json:"pending_unsuspended"`
	PendingDeletionRequested  int64  `json:"pending_deletion_requested"`
	QueuePending              int    `json:"queue_pending"`
	QueueActive               int    `json:"queue_active"`
	QueueRetry                int    `json:"queue_retry"`
	QueueScheduled            int    `json:"queue_scheduled"`
	QueueArchived             int    `json:"queue_archived"`
	RedisUsedMemory           int64  `json:"redis_used_memory"`
	RedisMemoryReferenceLimit int64  `json:"redis_memory_reference_limit"`
	Paused                    bool   `json:"paused"`
	PauseReason               string `json:"pause_reason,omitempty"`
	Complete                  bool   `json:"complete"`
}

type SelfDestructInventory struct {
	Unsuspended               int64  `json:"unsuspended"`
	DeletionRequested         int64  `json:"deletion_requested"`
	KnownInboxes              int    `json:"known_inboxes"`
	WouldProcessUnsuspended   int64  `json:"would_process_unsuspended"`
	WouldProcessRequested     int64  `json:"would_process_requested"`
	DeliveryBatchesPerAccount int    `json:"delivery_batches_per_account"`
	QueuePending              int    `json:"queue_pending"`
	RedisUsedMemory           int64  `json:"redis_used_memory"`
	RedisMemoryReferenceLimit int64  `json:"redis_memory_reference_limit"`
	Paused                    bool   `json:"paused"`
	PauseReason               string `json:"pause_reason,omitempty"`
}

func (operations *Operations) SelfDestructInventory(ctx context.Context) (SelfDestructInventory, error) {
	var inventory SelfDestructInventory
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return inventory, errors.New("operations database is not configured")
	}
	s := operations.server
	if err := s.db.WithContext(ctx).Model(&models.Account{}).
		Where("domain IS NULL AND suspended_at IS NULL").Count(&inventory.Unsuspended).Error; err != nil {
		return inventory, err
	}
	if err := s.db.WithContext(ctx).Model(&models.Account{}).
		Joins("JOIN account_deletion_requests ON account_deletion_requests.account_id = accounts.id").
		Where("accounts.domain IS NULL AND accounts.suspended_at IS NOT NULL").Count(&inventory.DeletionRequested).Error; err != nil {
		return inventory, err
	}
	inboxes, err := s.activityPubAllRemoteAccountInboxes()
	if err != nil {
		return inventory, err
	}
	inventory.KnownInboxes = len(inboxes)
	inventory.DeliveryBatchesPerAccount = (len(inboxes) + selfDestructMaxDeliveryBatch - 1) / selfDestructMaxDeliveryBatch
	inventory.WouldProcessUnsuspended = min(inventory.Unsuspended, int64(selfDestructMaxAccountsPerGroup))
	inventory.WouldProcessRequested = min(inventory.DeletionRequested, int64(selfDestructMaxAccountsPerGroup))
	load, err := s.currentSelfDestructLoad(ctx)
	if err != nil {
		return inventory, err
	}
	inventory.QueuePending = load.Queue.Pending
	inventory.RedisUsedMemory = load.UsedMemory
	inventory.RedisMemoryReferenceLimit = load.MaxMemory
	inventory.PauseReason = load.pauseReason()
	inventory.Paused = inventory.PauseReason != ""
	return inventory, nil
}

func (operations *Operations) SelfDestructStatus(ctx context.Context) (SelfDestructStatus, error) {
	var status SelfDestructStatus
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return status, errors.New("operations database is not configured")
	}
	s := operations.server
	status.Configured = strings.TrimSpace(s.cfg.SelfDestruct) != ""
	status.Enabled = s.selfDestructEnabled()
	status.LocalDomain = strings.TrimSpace(s.cfg.LocalDomain)
	if err := s.db.WithContext(ctx).Model(&models.Account{}).
		Where("domain IS NULL AND suspended_at IS NULL").Count(&status.PendingUnsuspended).Error; err != nil {
		return status, err
	}
	if err := s.db.WithContext(ctx).Model(&models.Account{}).
		Joins("JOIN account_deletion_requests ON account_deletion_requests.account_id = accounts.id").
		Where("accounts.domain IS NULL AND accounts.suspended_at IS NOT NULL").Count(&status.PendingDeletionRequested).Error; err != nil {
		return status, err
	}
	load, err := s.currentSelfDestructLoad(ctx)
	if err != nil {
		return status, err
	}
	status.QueuePending = load.Queue.Pending
	status.QueueActive = load.Queue.Active
	status.QueueRetry = load.Queue.Retry
	status.QueueScheduled = load.Queue.Scheduled
	status.QueueArchived = load.Queue.Archived
	status.RedisUsedMemory = load.UsedMemory
	status.RedisMemoryReferenceLimit = load.MaxMemory
	status.PauseReason = load.pauseReason()
	status.Paused = status.PauseReason != ""
	status.Complete = status.Enabled && status.PendingUnsuspended == 0 && status.PendingDeletionRequested == 0 && load.Queue.unfinished() == 0
	return status, nil
}
