package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	statusesCleanupWorkerInterval = time.Minute
	statusesCleanupMaxBudget      = 300
	statusesCleanupPerAccount     = 5
	statusesCleanupPerThread      = 5
	statusesCleanupEarlyCutoff    = 5000
	statusesCleanupCursorTTL      = 14 * 24 * time.Hour
	statusesCleanupSchedulerKey   = "account_statuses_cleanup_scheduler:last_policy_id"
	statusesCleanupDefaultLatency = 5 * time.Second
	statusesCleanupPushLatency    = 10 * time.Second
	statusesCleanupPullLatency    = 5 * time.Minute
)

type statusesCleanupQueueLatencyLimit struct {
	name       string
	maxLatency time.Duration
}

var statusesCleanupQueueLatencyLimits = []statusesCleanupQueueLatencyLimit{
	{name: "default", maxLatency: statusesCleanupDefaultLatency},
	{name: "push", maxLatency: statusesCleanupPushLatency},
	{name: "pull", maxLatency: statusesCleanupPullLatency},
}

func (s *Server) runStatusesCleanupWorker(ctx context.Context) {
	ticker := time.NewTicker(statusesCleanupWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "accounts_statuses_cleanup_scheduler", 24*time.Hour, func() {
				s.processAccountStatusesCleanup(ctx, s.statusesCleanupBudget(ctx))
			})
		}
	}
}

func (s *Server) statusesCleanupBudget(ctx context.Context) int {
	if s == nil {
		return 0
	}
	threads := s.statusesCleanupPushConcurrency(ctx)
	if threads <= 0 {
		threads = s.statusesCleanupPaonGoPushConcurrency()
	}
	budget := statusesCleanupPerThread * threads
	if budget > statusesCleanupMaxBudget {
		return statusesCleanupMaxBudget
	}
	if budget < 0 {
		return 0
	}
	return budget
}

func (s *Server) statusesCleanupPaonGoPushConcurrency() int {
	weight := paonGoAsynqQueueWeights()[asynqQueuePush]
	if weight <= 0 {
		return 0
	}
	return weight
}

func (s *Server) statusesCleanupPushConcurrency(ctx context.Context) int {
	if s == nil {
		return 0
	}
	value, err := s.redisCommand(ctx, "SMEMBERS", redisConfig(s.cfg).prefix+"processes")
	if err != nil {
		return 0
	}
	identities, ok := redisStringArray(value)
	if !ok {
		return 0
	}
	threads := 0
	for _, identity := range identities {
		threads += s.statusesCleanupPushConcurrencyForIdentity(ctx, identity)
	}
	return threads
}

func (s *Server) statusesCleanupPushConcurrencyForIdentity(ctx context.Context, identity string) int {
	if strings.TrimSpace(identity) == "" {
		return 0
	}
	queuesValue, err := s.redisCommand(ctx, "HGET", redisConfig(s.cfg).prefix+identity, "queues")
	if err != nil {
		return 0
	}
	queuesRaw, _ := queuesValue.(string)
	queues := sidekiqProcessQueuesFromRedis(queuesRaw)
	hasPush := false
	for _, queue := range queues {
		if queue == "push" {
			hasPush = true
			break
		}
	}
	if !hasPush {
		return 0
	}
	concurrencyValue, err := s.redisCommand(ctx, "HGET", redisConfig(s.cfg).prefix+identity, "concurrency")
	if err != nil {
		return 0
	}
	if concurrency := redisInt(concurrencyValue); concurrency > 0 {
		return int(concurrency)
	}
	return 0
}

func (s *Server) processAccountStatusesCleanup(ctx context.Context, maxBudget int) int {
	if s == nil || s.db == nil || maxBudget <= 0 {
		return 0
	}
	if s.statusesCleanupUnderLoad(ctx, time.Now().UTC()) {
		return 0
	}
	budget := maxBudget
	deleted := 0
	firstPolicyID := s.statusesCleanupSchedulerLastPolicyID(ctx)
	firstIteration := true
	fullIteration := true
	affectedPolicyIDs := make([]int64, 0)
	processedPolicyID := int64(0)

	baseScope := func() *gorm.DB {
		return s.db.WithContext(ctx).
			Model(&models.AccountStatusesCleanupPolicy{}).
			Joins("JOIN accounts ON accounts.id = account_statuses_cleanup_policies.account_id").
			Where("account_statuses_cleanup_policies.enabled = ? AND (accounts.domain IS NULL OR accounts.domain = '') AND accounts.suspended_at IS NULL", true)
	}

	processScope := func(query *gorm.DB) int {
		if budget <= 0 {
			return 0
		}
		processedAccounts := 0
		var policies []models.AccountStatusesCleanupPolicy
		if err := query.
			Preload("Account").
			Order("account_statuses_cleanup_policies.id ASC").
			Find(&policies).Error; err != nil {
			return 0
		}
		for _, policy := range policies {
			if budget <= 0 {
				break
			}
			processedPolicyID = policy.ID
			limit := statusesCleanupPerAccount
			if budget < limit {
				limit = budget
			}
			count, err := s.cleanupStatusesForPolicy(ctx, policy, limit)
			if err != nil {
				continue
			}
			deleted += count
			budget -= count
			if count != 0 {
				processedAccounts++
				if fullIteration {
					affectedPolicyIDs = appendUniqueInt64(affectedPolicyIDs, policy.ID)
				}
			}
			if !firstIteration && policy.ID >= firstPolicyID {
				fullIteration = false
			}
		}
		return processedAccounts
	}

	for {
		query := statusesCleanupPolicyScope(baseScope(), firstPolicyID, affectedPolicyIDs, firstIteration, fullIteration)
		processedAccounts := processScope(query)
		if budget <= 0 || (processedAccounts == 0 && !fullIteration) {
			break
		}
		if !firstIteration {
			fullIteration = false
		}
		firstIteration = false
	}

	if budget <= 0 && processedPolicyID > 0 {
		s.saveStatusesCleanupSchedulerLastPolicyID(ctx, processedPolicyID)
	}
	return deleted
}

func statusesCleanupPolicyScope(base *gorm.DB, firstPolicyID int64, affectedPolicyIDs []int64, firstIteration bool, fullIteration bool) *gorm.DB {
	if fullIteration {
		if firstIteration {
			return base.Where("account_statuses_cleanup_policies.id >= ?", firstPolicyID)
		}
		if len(affectedPolicyIDs) == 0 {
			return base.Where("account_statuses_cleanup_policies.id <= ?", firstPolicyID)
		}
		return base.Where("account_statuses_cleanup_policies.id <= ? OR account_statuses_cleanup_policies.id IN ?", firstPolicyID, affectedPolicyIDs)
	}
	if len(affectedPolicyIDs) == 0 {
		return base.Where("1 = 0")
	}
	return base.Where("account_statuses_cleanup_policies.id IN ?", affectedPolicyIDs)
}

func appendUniqueInt64(values []int64, value int64) []int64 {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (s *Server) cleanupStatusesForPolicy(ctx context.Context, policy models.AccountStatusesCleanupPolicy, budget int) (int, error) {
	if !policy.Enabled || budget <= 0 {
		return 0, nil
	}
	cutoffID, ok, err := s.statusesCleanupCutoffID(ctx, policy, time.Now().UTC())
	if err != nil || !ok {
		return 0, err
	}
	lastInspected := s.statusesCleanupLastInspected(ctx, policy.AccountID)
	statuses, err := s.statusesCleanupCandidateStatuses(ctx, policy, budget, cutoffID, lastInspected)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	deleted := 0
	lastDeleted := int64(0)
	for _, status := range statuses {
		discardedRows, err := s.discardStatusRowsForRemoval(ctx, status.ID, now)
		if err != nil {
			return deleted, err
		}
		status.DeletedAt = sql.NullTime{Time: now, Valid: true}
		if !s.enqueueRemovalTask(asynqRemovalPayload{StatusID: status.ID}) {
			s.applyDiscardedStatusRowSideEffects(ctx, discardedRows)
			s.applyDeletedStatusRemovalSideEffects(ctx, status, asynqRemovalPayload{StatusID: status.ID})
		}
		deleted++
		lastDeleted = status.ID
	}
	if lastDeleted == 0 {
		lastDeleted = cutoffID
	}
	s.recordStatusesCleanupLastInspected(ctx, policy.AccountID, lastDeleted)
	return deleted, nil
}

func (s *Server) statusesCleanupUnderLoad(ctx context.Context, now time.Time) bool {
	if s == nil {
		return false
	}
	for _, queue := range statusesCleanupQueueLatencyLimits {
		if s.sidekiqQueueLatencyOver(ctx, queue.name, queue.maxLatency, now) {
			return true
		}
	}
	return false
}

func (s *Server) sidekiqQueueLatencyOver(ctx context.Context, queueName string, maxLatency time.Duration, now time.Time) bool {
	if strings.TrimSpace(queueName) == "" || maxLatency <= 0 {
		return false
	}
	value, err := s.redisCommand(ctx, "LINDEX", redisConfig(s.cfg).prefix+"queue:"+queueName, "-1")
	if err != nil {
		return false
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return false
	}
	enqueuedAt, ok := sidekiqJobEnqueuedAt(raw)
	if !ok {
		return false
	}
	return now.Sub(enqueuedAt) > maxLatency
}

func sidekiqJobEnqueuedAt(raw string) (time.Time, bool) {
	var payload struct {
		EnqueuedAt float64 `json:"enqueued_at"`
		CreatedAt  float64 `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return time.Time{}, false
	}
	seconds := payload.EnqueuedAt
	if seconds <= 0 {
		seconds = payload.CreatedAt
	}
	if seconds <= 0 {
		return time.Time{}, false
	}
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * 1_000_000_000)
	return time.Unix(whole, nanos).UTC(), true
}

func (s *Server) statusesCleanupCutoffID(ctx context.Context, policy models.AccountStatusesCleanupPolicy, now time.Time) (int64, bool, error) {
	maxID := mastodonSnowflakeIDAt(now.Add(-time.Duration(policy.MinStatusAge)*time.Second), false)
	minID := s.statusesCleanupLastInspected(ctx, policy.AccountID)
	var ids []int64
	err := s.db.WithContext(ctx).
		Model(&models.Status{}).
		Where("account_id = ? AND deleted_at IS NULL AND id >= ? AND id <= ?", policy.AccountID, minID, maxID).
		Order("id ASC").
		Limit(statusesCleanupEarlyCutoff).
		Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return 0, false, err
	}
	return ids[len(ids)-1], true, nil
}

func (s *Server) statusesCleanupCandidateStatuses(ctx context.Context, policy models.AccountStatusesCleanupPolicy, limit int, cutoffID int64, minID int64) ([]models.Status, error) {
	query := s.db.WithContext(ctx).
		Model(&models.Status{}).
		Preload("Account").
		Preload("Mentions.Account").
		Preload("MediaAttachments").
		Preload("Tags").
		Where("statuses.account_id = ? AND statuses.deleted_at IS NULL AND statuses.id >= ? AND statuses.id <= ?", policy.AccountID, minID, cutoffID)
	if policy.KeepDirect {
		query = query.Where("statuses.visibility <> ?", 3)
	}
	if policy.KeepPinned {
		query = query.Where("NOT EXISTS (SELECT 1 FROM status_pins pin WHERE pin.account_id = statuses.account_id AND pin.status_id = statuses.id)")
	}
	if policy.KeepPolls {
		query = query.Where("statuses.poll_id IS NULL")
	}
	if policy.KeepMedia {
		query = query.Where("NOT EXISTS (SELECT 1 FROM media_attachments media WHERE media.status_id = statuses.id)")
	}
	if policy.KeepSelfFav {
		query = query.Where("NOT EXISTS (SELECT 1 FROM favourites fav WHERE fav.account_id = statuses.account_id AND fav.status_id = statuses.id)")
	}
	if policy.KeepSelfBookmark {
		query = query.Where("NOT EXISTS (SELECT 1 FROM bookmarks bookmark WHERE bookmark.account_id = statuses.account_id AND bookmark.status_id = statuses.id)")
	}
	if policy.MinFavs.Valid || policy.MinReblogs.Valid {
		query = query.Joins("LEFT JOIN status_stats ON status_stats.status_id = statuses.id")
	}
	if policy.MinFavs.Valid {
		query = query.Where("COALESCE(status_stats.favourites_count, 0) < ?", policy.MinFavs.Int64)
	}
	if policy.MinReblogs.Valid {
		query = query.Where("COALESCE(status_stats.reblogs_count, 0) < ?", policy.MinReblogs.Int64)
	}
	var statuses []models.Status
	err := query.Order("statuses.id ASC").Limit(limit).Find(&statuses).Error
	return statuses, err
}

func mastodonSnowflakeIDAt(timestamp time.Time, withRandom bool) int64 {
	id := timestamp.UTC().Unix() * 1000
	id = id << 16
	if withRandom {
		id += time.Now().UnixNano() & 0xffff
	}
	return id
}

func statusesCleanupPolicyRedisKey(accountID int64) string {
	return "account_cleanup:" + strconv.FormatInt(accountID, 10)
}

func (s *Server) statusesCleanupLastInspected(ctx context.Context, accountID int64) int64 {
	value, err := s.redisCommand(ctx, "GET", redisConfig(s.cfg).prefix+statusesCleanupPolicyRedisKey(accountID))
	if err != nil {
		return 0
	}
	text, ok := value.(string)
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

func (s *Server) recordStatusesCleanupLastInspected(ctx context.Context, accountID int64, statusID int64) {
	if statusID <= 0 {
		return
	}
	_, _ = s.redisCommand(ctx, "SET", redisConfig(s.cfg).prefix+statusesCleanupPolicyRedisKey(accountID), strconv.FormatInt(statusID, 10), "EX", strconv.FormatInt(int64(statusesCleanupCursorTTL/time.Second), 10))
}

func (s *Server) invalidateStatusesCleanupLastInspected(ctx context.Context, accountID int64, statusID int64, action string) {
	if s == nil || s.db == nil || accountID == 0 || statusID == 0 {
		return
	}
	lastValue := s.statusesCleanupLastInspected(ctx, accountID)
	if lastValue == 0 || statusID > lastValue {
		return
	}
	var policy models.AccountStatusesCleanupPolicy
	if err := s.db.Where("account_id = ?", accountID).First(&policy).Error; err != nil {
		return
	}
	switch action {
	case "unbookmark":
		if !policy.KeepSelfBookmark {
			return
		}
	case "unfav":
		if !policy.KeepSelfFav {
			return
		}
	case "unpin":
		if !policy.KeepPinned {
			return
		}
	default:
		return
	}
	s.recordStatusesCleanupLastInspected(ctx, accountID, statusID)
}

func (s *Server) runStatusPinDestroyedSideEffects(ctx context.Context, pin models.StatusPin) {
	if s == nil || s.db == nil || pin.AccountID == 0 || pin.StatusID == 0 {
		return
	}
	var status models.Status
	if err := s.db.WithContext(ctx).Select("id, account_id").Where("id = ?", pin.StatusID).First(&status).Error; err != nil {
		return
	}
	if status.AccountID != pin.AccountID {
		return
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Select("id, domain").Where("id = ?", pin.AccountID).First(&account).Error; err != nil {
		return
	}
	if !account.Local() {
		return
	}
	s.invalidateStatusesCleanupLastInspected(ctx, pin.AccountID, pin.StatusID, "unpin")
}

func (s *Server) runFavouriteDestroyedSideEffects(ctx context.Context, favourite models.Favourite) {
	if s == nil || s.db == nil || favourite.AccountID == 0 || favourite.StatusID == 0 {
		return
	}
	var status models.Status
	if err := s.db.WithContext(ctx).Select("id, account_id").Where("id = ?", favourite.StatusID).First(&status).Error; err != nil {
		return
	}
	if status.AccountID != favourite.AccountID {
		return
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Select("id, domain").Where("id = ?", favourite.AccountID).First(&account).Error; err != nil {
		return
	}
	if !account.Local() {
		return
	}
	s.invalidateStatusesCleanupLastInspected(ctx, favourite.AccountID, favourite.StatusID, "unfav")
}

func (s *Server) clearStatusesCleanupLastInspected(ctx context.Context, accountID int64) {
	_, _ = s.redisCommand(ctx, "DEL", redisConfig(s.cfg).prefix+statusesCleanupPolicyRedisKey(accountID))
}

func (s *Server) statusesCleanupSchedulerLastPolicyID(ctx context.Context) int64 {
	value, err := s.redisCommand(ctx, "GET", redisConfig(s.cfg).prefix+statusesCleanupSchedulerKey)
	if err != nil {
		return 0
	}
	text, ok := value.(string)
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

func (s *Server) saveStatusesCleanupSchedulerLastPolicyID(ctx context.Context, policyID int64) {
	if policyID <= 0 {
		return
	}
	_, _ = s.redisCommand(ctx, "SET", redisConfig(s.cfg).prefix+statusesCleanupSchedulerKey, strconv.FormatInt(policyID, 10), "EX", "3600")
}
