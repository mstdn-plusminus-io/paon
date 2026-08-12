package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const asynqTaskDistributeTermsOfService = "admin:distribute_terms_of_service_notification"

type asynqTermsOfServiceDistributionPayload struct {
	TermsOfServiceID int64 `json:"terms_of_service_id"`
}

func (s *Server) enqueueTermsOfServiceDistributionTask(termsOfServiceID int64) error {
	if s == nil || s.asynqClient == nil {
		return fmt.Errorf("terms of service distribution: asynq client is not configured")
	}
	if termsOfServiceID <= 0 {
		return fmt.Errorf("terms of service distribution: terms of service id is required")
	}
	payload, err := marshalAsynqTaskPayload(asynqTermsOfServiceDistributionPayload{TermsOfServiceID: termsOfServiceID})
	if err != nil {
		return fmt.Errorf("terms of service distribution payload: %w", err)
	}
	task := asynq.NewTask(asynqTaskDistributeTermsOfService, payload,
		asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(10))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.asynqClient.EnqueueContext(ctx, task); !asynqEnqueueAccepted(err) {
		return fmt.Errorf("enqueue terms of service distribution: %w", err)
	}
	return nil
}

func (s *Server) handleAsynqDistributeTermsOfService(ctx context.Context, task *asynq.Task) error {
	var payload asynqTermsOfServiceDistributionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("terms of service distribution: %w", err)
	}
	if s == nil || s.db == nil || payload.TermsOfServiceID <= 0 {
		return nil
	}
	var terms models.TermsOfService
	if err := s.db.WithContext(ctx).Where("id = ? AND published_at IS NOT NULL", payload.TermsOfServiceID).First(&terms).Error; err != nil {
		return workerLookupError("terms of service distribution lookup", err)
	}
	if !terms.PublishedAt.Valid {
		return nil
	}
	if _, err := markTermsOfServiceInterstitialUsers(ctx, s.db, terms); err != nil {
		return err
	}

	var users []models.User
	query := termsOfServiceNotificationUsersQuery(s.db.WithContext(ctx), terms).
		Select("users.id", "users.email", "users.unconfirmed_email", "users.locale", "users.settings", "users.disabled", "users.approved", "users.confirmed_at").
		Order("users.id ASC")
	if err := query.FindInBatches(&users, 500, func(_ *gorm.DB, _ int) error {
		for _, user := range users {
			if err := s.enqueueTermsOfServiceChangedMail(user, terms); err != nil {
				return fmt.Errorf("terms of service mail for user %d: %w", user.ID, err)
			}
		}
		return nil
	}).Error; err != nil {
		return fmt.Errorf("terms of service notification users: %w", err)
	}
	return nil
}

func markTermsOfServiceInterstitialUsers(ctx context.Context, db *gorm.DB, terms models.TermsOfService) (int64, error) {
	if db == nil || !terms.PublishedAt.Valid {
		return 0, nil
	}
	cutoff := termsOfServiceNotificationCutoff(terms)
	result := db.WithContext(ctx).Model(&models.User{}).
		Where("confirmed_at IS NOT NULL AND created_at <= ?", terms.PublishedAt.Time).
		Where(`current_sign_in_at IS NULL OR current_sign_in_at < ? OR EXISTS (
			SELECT 1 FROM accounts WHERE accounts.id = users.account_id AND accounts.suspended_at IS NOT NULL
		)`, cutoff).
		Where("require_tos_interstitial = ?", false).
		UpdateColumn("require_tos_interstitial", true)
	if result.Error != nil {
		return 0, fmt.Errorf("terms of service interstitial update: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Server) enqueueTermsOfServiceChangedMail(user models.User, terms models.TermsOfService) error {
	message := termsOfServiceChangedMailMessage(s.cfg, user, terms)
	message.Bulk = true
	if s.asynqClient == nil {
		return sendMail(s.cfg, message)
	}
	payload, err := marshalAsynqTaskPayload(asynqMailerDeliveryPayload{UserID: user.ID, Eligibility: "bulk_terms_of_service", Message: message})
	if err != nil {
		return fmt.Errorf("terms of service mail payload: %w", err)
	}
	taskID := termsOfServiceDistributionMailTaskID(terms.ID, user.ID)
	task := asynq.NewTask(asynqTaskMailerDelivery, payload,
		asynq.Queue(s.asynqQueue(asynqQueueMailers)), asynq.MaxRetry(25),
		asynq.TaskID(taskID), asynq.Retention(7*24*time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.asynqClient.EnqueueContext(ctx, task); !asynqEnqueueAccepted(err) {
		return fmt.Errorf("enqueue terms of service mail: %w", err)
	}
	return nil
}

func termsOfServiceDistributionMailTaskID(termsID int64, userID int64) string {
	return fmt.Sprintf("terms-of-service-%d-user-%d", termsID, userID)
}

func termsOfServiceChangedMailMessage(cfg config.Config, user models.User, terms models.TermsOfService) mailMessage {
	locale := mailLocale(cfg, user)
	effectiveAt := termsOfServiceUTCDay(time.Now().UTC())
	path := cfg.BaseURL() + "/terms-of-service"
	if terms.EffectiveDate.Valid {
		effectiveAt = terms.EffectiveDate.Time
		path += "/" + effectiveAt.Format("2006-01-02")
	}
	subject := localizedMailText(locale, "user_mailer.terms_of_service_changed.subject", "Updates to our terms of service", nil)
	title := localizedMailText(locale, "user_mailer.terms_of_service_changed.title", "Important update", nil)
	description := localizedMailText(locale, "user_mailer.terms_of_service_changed.description", "You are receiving this email because we are making changes to our terms of service at %{domain}. These updates will become effective on %{date}.", map[string]string{
		"domain": cfg.LocalDomain,
		"date":   formatOptionalDate(locale, effectiveAt),
	})
	changelogLabel := localizedMailText(locale, "user_mailer.terms_of_service_changed.changelog", "At a glance, here is what this update means for you:", nil)
	agreement := localizedMailText(locale, "user_mailer.terms_of_service_changed.agreement", "By continuing to use %{domain}, you agree to these terms.", map[string]string{"domain": cfg.LocalDomain})
	signOff := localizedMailText(locale, "user_mailer.terms_of_service_changed.sign_off", "The %{domain} team", map[string]string{"domain": cfg.LocalDomain})
	body := title + "\n\n===\n\n" + description + "\n\n=> " + path + "\n\n" + changelogLabel + "\n\n" + strings.TrimSpace(terms.Changelog) + "\n\n" + agreement + "\n\n" + signOff + "\n"
	return mailMessage{To: user.Email, Subject: subject, Body: body}
}

func localizedMailText(locale, key, fallback string, vars map[string]string) string {
	value := webT(locale, key, vars)
	if value == key || strings.TrimSpace(value) == "" {
		value = fallback
		for name, replacement := range vars {
			value = strings.ReplaceAll(value, "%{"+name+"}", replacement)
		}
	}
	return value
}
