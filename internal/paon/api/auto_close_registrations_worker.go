package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	autoCloseRegistrationsWorkerInterval           = time.Hour
	autoCloseRegistrationsSignInUpdateFrequency    = 24 * time.Hour
	autoCloseRegistrationsModeratorActiveThreshold = 7*24*time.Hour + autoCloseRegistrationsSignInUpdateFrequency
)

func (s *Server) runAutoCloseRegistrationsWorker(ctx context.Context) {
	ticker := time.NewTicker(autoCloseRegistrationsWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "auto_close_registrations_scheduler", autoCloseRegistrationsWorkerInterval, func() {
				s.autoCloseRegistrations(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) autoCloseRegistrations(ctx context.Context, now time.Time) bool {
	if s == nil || s.db == nil || s.autoCloseRegistrationsDisabledByConfig() {
		return false
	}
	active, err := s.hasActiveRegistrationModerator(ctx, now.Add(-autoCloseRegistrationsModeratorActiveThreshold))
	if err != nil || active {
		return false
	}
	transitioned := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "paon:auto_close_registrations").Error; err != nil {
			return err
		}
		var setting models.Setting
		err := tx.Where("var = ?", "registrations_mode").First(&setting).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if normalizeRegistrationsMode(setting.Value.String) != "open" {
			return nil
		}
		if err := tx.Model(&models.Setting{}).Where("id = ?", setting.ID).Updates(map[string]any{
			"value":      "approved",
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		transitioned = true
		return nil
	}); err != nil {
		return false
	}
	if !transitioned {
		return false
	}
	_ = s.sendAutoCloseRegistrationsMails()
	return true
}

func (s *Server) autoCloseRegistrationsDisabledByConfig() bool {
	if strings.TrimSpace(railsEnvOrLegacyEnv("EMAIL_DOMAIN_ALLOWLIST", "EMAIL_DOMAIN_WHITELIST")) != "" {
		return true
	}
	return s != nil && s.cfg.DisableAutoSwitchingRegistrations
}

func (s *Server) hasActiveRegistrationModerator(ctx context.Context, cutoff time.Time) (bool, error) {
	roleIDs, err := s.roleIDsWithPermission(s.db.WithContext(ctx), rolePermissionManageReports)
	if err != nil || len(roleIDs) == 0 {
		return false, err
	}
	query := s.db.WithContext(ctx).Model(&models.User{}).Where("current_sign_in_at >= ?", cutoff)
	if roleIDsIncludeEveryone(roleIDs) {
		filtered := roleIDsWithoutEveryone(roleIDs)
		if len(filtered) > 0 {
			query = query.Where("role_id IN ? OR role_id IS NULL", filtered)
		} else {
			query = query.Where("role_id IS NULL")
		}
	} else {
		query = query.Where("role_id IN ?", roleIDs)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
