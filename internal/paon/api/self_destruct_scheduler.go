package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	selfDestructMaxAccountsPerGroup = 50
	selfDestructSchedulerInterval   = time.Minute
	selfDestructSchedulerLockTTL    = 24 * time.Hour
)

type selfDestructRunResult struct {
	Unsuspended int
	Suspended   int
}

func (s *Server) runSelfDestructScheduler(ctx context.Context) {
	if !s.selfDestructEnabled() {
		return
	}
	run := func() {
		if _, err := s.runSelfDestructSchedulerOnce(ctx); err != nil {
			log.Printf("self-destruct scheduler paused after error: %v", err)
		}
	}
	s.runSchedulerWithRedisLock(ctx, "self_destruct_scheduler", selfDestructSchedulerLockTTL, run)
	ticker := time.NewTicker(selfDestructSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "self_destruct_scheduler", selfDestructSchedulerLockTTL, run)
		}
	}
}

func (s *Server) runSelfDestructSchedulerOnce(ctx context.Context) (selfDestructRunResult, error) {
	var result selfDestructRunResult
	if !s.selfDestructEnabled() {
		return result, nil
	}
	load, err := s.currentSelfDestructLoad(ctx)
	if err != nil {
		return result, err
	}
	if load.pauseReason() != "" {
		return result, nil
	}
	inboxes, err := s.activityPubAllRemoteAccountInboxes()
	if err != nil {
		return result, fmt.Errorf("load self-destruct inboxes: %w", err)
	}
	sort.Strings(inboxes)
	result.Unsuspended, err = s.processSelfDestructAccountGroup(ctx, false, inboxes, nil)
	if err != nil {
		return result, err
	}
	load, err = s.currentSelfDestructLoad(ctx)
	if err != nil {
		return result, err
	}
	if load.pauseReason() != "" {
		return result, nil
	}
	result.Suspended, err = s.processSelfDestructAccountGroup(ctx, true, inboxes, nil)
	return result, err
}

type selfDestructAccountEnqueuer func(context.Context, models.Account, []string) error

// processSelfDestructAccountGroup only changes account state after every Delete
// task for that account has been accepted by Asynq. A failed enqueue leaves the
// account eligible for the next minute; deterministic task IDs and delivery
// markers make that restart path idempotent.
func (s *Server) processSelfDestructAccountGroup(ctx context.Context, deletionRequested bool, inboxes []string, enqueue selfDestructAccountEnqueuer) (int, error) {
	if !s.selfDestructEnabled() {
		return 0, errors.New("refusing account transition without a valid SELF_DESTRUCT token")
	}
	if s.db == nil {
		return 0, errors.New("self-destruct database is unavailable")
	}
	if enqueue == nil {
		enqueue = s.enqueueSelfDestructAccountDelete
	}
	query := s.db.WithContext(ctx).Model(&models.Account{}).Where("accounts.domain IS NULL")
	if deletionRequested {
		query = query.Joins("JOIN account_deletion_requests ON account_deletion_requests.account_id = accounts.id").
			Where("accounts.suspended_at IS NOT NULL")
	} else {
		query = query.Where("accounts.suspended_at IS NULL")
	}
	var accounts []models.Account
	if err := query.Order("accounts.id ASC").Limit(selfDestructMaxAccountsPerGroup).Find(&accounts).Error; err != nil {
		return 0, err
	}
	processed := 0
	for _, account := range accounts {
		if err := enqueue(ctx, account, inboxes); err != nil {
			return processed, fmt.Errorf("enqueue self-destruct for account %d: %w", account.ID, err)
		}
		now := time.Now().UTC()
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			updates := map[string]any{
				"suspended_at":      sql.NullTime{Time: now, Valid: true},
				"suspension_origin": sql.NullInt64{Int64: 0, Valid: true},
				"updated_at":        now,
			}
			if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
				return err
			}
			if deletionRequested {
				return tx.Where("account_id = ?", account.ID).Delete(&models.AccountDeletionRequest{}).Error
			}
			return nil
		}); err != nil {
			return processed, fmt.Errorf("mark self-destruct account %d suspended: %w", account.ID, err)
		}
		processed++
	}
	return processed, nil
}
