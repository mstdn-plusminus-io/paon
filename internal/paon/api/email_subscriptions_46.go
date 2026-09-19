package api

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) createAccountEmailSubscription(c *echo.Context) error {
	if !s.emailSubscriptionsEnabled() {
		return c.NoContent(http.StatusNotFound)
	}
	account, err := s.findAccountByID(c.Param("id"))
	if err != nil || account == nil || !account.Local() || account.SuspendedAt.Valid || !s.emailSubscriptionsAllowedForAccount(c.Request().Context(), account.ID) {
		return c.NoContent(http.StatusNotFound)
	}
	email := strings.ToLower(strings.TrimSpace(requestRawParamValue(c, "email")))
	if email == "" {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"error": "Validation failed: Email can't be blank, Email is invalid",
			"details": map[string]any{"email": []map[string]string{
				{"error": "ERR_BLANK", "description": "can't be blank"},
				{"error": "ERR_INVALID", "description": "is invalid"},
			}},
		})
	}
	if !railsEmailAddressValid(email) {
		return emailSubscriptionValidationError(c, "invalid", "is invalid")
	}
	if len([]rune(email)) > 320 {
		return emailSubscriptionValidationError(c, "too_long", "is too long (maximum is 320 characters)")
	}
	if err := s.ensureEmailDomainAllowed(c.Request().Context(), email, "", true, false); err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "unreachable"):
			return emailSubscriptionValidationError(c, "unreachable", "is unreachable")
		case strings.Contains(message, "blocked"):
			return emailSubscriptionValidationError(c, "blocked", "is blocked")
		default:
			return err
		}
	}
	now := time.Now().UTC()
	subscription := models.EmailSubscription{
		AccountID: account.ID, Email: email, Locale: s.webLocale(c, nil),
		ConfirmationToken: sql.NullString{String: emailSubscriptionConfirmationToken(), Valid: true},
		CreatedAt:         now, UpdatedAt: now,
	}
	if err := s.db.Create(&subscription).Error; err != nil {
		if isUniqueConstraintError(err) {
			return emailSubscriptionValidationError(c, "taken", "has already been taken")
		}
		return err
	}
	if err := s.enqueueEmailSubscriptionConfirmation(subscription.ID); err != nil {
		return err
	}
	return renderEmpty(c)
}

func emailSubscriptionValidationError(c *echo.Context, code string, description string) error {
	return c.JSON(http.StatusUnprocessableEntity, map[string]any{
		"error": "Validation failed: Email " + description,
		"details": map[string]any{"email": []map[string]string{{
			"error": "ERR_" + strings.ToUpper(code), "description": description,
		}}},
	})
}

func (s *Server) emailSubscriptionsEnabled() bool {
	return s.cfg.EmailSubscriptionsEnabled && s.settingBoolValue("email_subscriptions", false)
}
