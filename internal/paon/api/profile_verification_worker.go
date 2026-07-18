package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	profileVerificationWorkerInterval = time.Hour
	profileVerificationBatchSize      = 100
)

func (s *Server) runProfileVerificationWorker(ctx context.Context) {
	ticker := time.NewTicker(profileVerificationWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "profile_verification_scheduler", profileVerificationWorkerInterval, func() {
				_ = s.verifyPendingProfileLinks(ctx, now.UTC(), profileVerificationBatchSize)
			})
		}
	}
}

func (s *Server) verifyPendingProfileLinks(ctx context.Context, now time.Time, limit int) error {
	if s.db == nil || limit <= 0 {
		return nil
	}
	var accounts []models.Account
	if err := s.db.WithContext(ctx).
		Select("id", "username", "domain", "fields").
		Where("domain IS NULL").
		Where("suspended_at IS NULL").
		Where("fields IS NOT NULL").
		Where("fields::text LIKE ?", "%\"verified_at\":null%").
		Order("id ASC").
		Limit(limit).
		Find(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		if err := s.verifyAccountLinksForAccount(ctx, account, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) verifyAccountLinksNow(ctx context.Context, accountID int64, now time.Time) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Select("id", "username", "domain", "uri", "url", "fields", "suspended_at").Where("id = ?", accountID).First(&account).Error; err != nil {
		return err
	}
	return s.verifyAccountLinksForAccount(ctx, account, now)
}

func (s *Server) verifyAccountLinksForAccount(ctx context.Context, account models.Account, now time.Time) error {
	encoded, changed, err := s.verifiedProfileFieldsForAccount(account, now)
	if err != nil || !changed {
		return err
	}
	return s.db.WithContext(ctx).
		Model(&models.Account{}).
		Where("id = ?", account.ID).
		Updates(map[string]any{"fields": models.JSONValue(encoded), "updated_at": now.UTC()}).
		Error
}

func (s *Server) enqueueVerifyAccountLinksIfNeeded(ctx context.Context, account models.Account, now time.Time) {
	if !accountProfileFieldsRequireVerification(account) {
		return
	}
	if s.enqueueVerifyAccountLinksTask(account.ID) {
		return
	}
	_ = s.verifyAccountLinksForAccount(ctx, account, now)
}

func accountProfileFieldsRequireVerification(account models.Account) bool {
	if account.SuspendedAt.Valid {
		return false
	}
	fields := profileFieldsFromRaw(account.Fields)
	for _, field := range fields {
		if field.VerifiedAt != nil && strings.TrimSpace(*field.VerifiedAt) != "" {
			continue
		}
		if account.Local() {
			if _, ok := profileFieldVerificationURL(field.Value); ok {
				return true
			}
		} else if _, ok := remoteProfileFieldVerificationURL(field.Value); ok {
			return true
		}
	}
	return false
}

func (s *Server) verifiedProfileFieldsForAccount(account models.Account, now time.Time) ([]byte, bool, error) {
	fields := profileFieldsFromRaw(account.Fields)
	if account.SuspendedAt.Valid || len(fields) == 0 {
		return account.Fields, false, nil
	}
	if account.Local() {
		fields = verifyProfileFieldLinksBestEffort(fields, s.cfg.BaseURL()+accountWebPath(account), now.UTC())
	} else {
		profileURL := strings.TrimSpace(account.URL.String)
		if profileURL == "" {
			profileURL = strings.TrimSpace(account.URI)
		}
		if profileURL == "" {
			return account.Fields, false, nil
		}
		fields = verifyRemoteProfileFieldLinksBestEffort(fields, profileURL, now.UTC())
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, false, err
	}
	return encoded, string(encoded) != string(account.Fields), nil
}
