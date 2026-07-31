package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) updateSettingsPrivacy(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/settings/privacy")
	}
	payload, userSettings, err := parseSettingsPrivacyPayload(c)
	if err != nil {
		if errors.Is(err, errSettingsPrivacyMissingAccountRoot) {
			return railsWebBadRequest(c)
		}
		return s.renderSettingsPrivacyError(c, *account, user, settingsPrivacyInvalidMessage(locale))
	}
	updates, err := accountUpdateMap(payload)
	if err != nil {
		return s.renderSettingsPrivacyError(c, *account, user, err.Error())
	}
	accountChanged := len(updates) > 0
	settingsChanged := len(userSettings) > 0
	var reloaded *models.Account
	if accountChanged {
		now := time.Now().UTC()
		updates["updated_at"] = now
		if err := s.updateAccountRowsAndTags(*account, updates, nil, now); err != nil {
			return err
		}
		reloaded, err = s.accountForUser(user)
		if err != nil {
			return err
		}
		s.triggerAccountWebhook("account.updated", reloaded.ID)
		if payload.Locked != nil && account.Locked && !*payload.Locked {
			if err := s.authorizePendingFollowRequestsForUnlockedAccount(c.Request().Context(), *reloaded); err != nil {
				return err
			}
		}
	} else if settingsChanged {
		if err := s.syncAccountTagsForAccount(*account, nil, time.Now().UTC()); err != nil {
			return err
		}
	}
	if settingsChanged {
		if err := s.updateUserSettingsAttributes(user.ID, userSettings); err != nil {
			return err
		}
	}
	if accountChanged || settingsChanged {
		if reloaded == nil {
			reloaded, err = s.accountForUser(user)
			if err != nil {
				return err
			}
		}
		_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)
	}
	return c.Redirect(http.StatusFound, "/settings/privacy?notice="+url.QueryEscape(settingsChangeSavedMessage(locale)))
}

func (s *Server) renderSettingsPrivacyError(c *echo.Context, account models.Account, user *models.User, errorText string) error {
	settings := decodeUserSettings(user.Settings.String)
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, settingsPrivacyHTMLWithMessages(account, settings, "", errorText, locale, settingsWebTheme(settings)))
}

var errSettingsPrivacyMissingAccountRoot = errors.New("settings privacy account root parameter is missing")

func parseSettingsPrivacyPayload(c *echo.Context) (accountUpdatePayload, map[string]any, error) {
	var payload accountUpdatePayload
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return payload, nil, err
	}
	if !settingsProfileAccountRootPresent(req) {
		return payload, nil, errSettingsPrivacyMissingAccountRoot
	}
	payload.Discoverable = boolPtrFromForm(c, "account[discoverable]")
	if unlocked := boolPtrFromForm(c, "account[unlocked]"); unlocked != nil {
		locked := !*unlocked
		payload.Locked = &locked
	}
	payload.Indexable = boolPtrFromForm(c, "account[indexable]")
	if showCollections := boolPtrFromForm(c, "account[show_collections]"); showCollections != nil {
		hideCollections := !*showCollections
		payload.HideCollections = &hideCollections
	}
	settings := map[string]any{}
	if value, ok := nestedFormBool(req.Form, "account[settings][indexable]"); ok {
		settings["noindex"] = !value
	}
	if value, ok := nestedFormBool(req.Form, "account[settings][show_application]"); ok {
		settings["show_application"] = value
	}
	return payload, settings, nil
}

func nestedFormBool(values map[string][]string, key string) (bool, bool) {
	if _, ok := values[key]; !ok {
		return false, false
	}
	return truthy(lastFormValue(values, key)), true
}

func (s *Server) updateUserSettingsAttributes(userID int64, attrs map[string]any) error {
	var user models.User
	if err := s.db.Select("id, settings").Where("id = ?", userID).First(&user).Error; err != nil {
		return err
	}
	settings := map[string]any{}
	if user.Settings.Valid && strings.TrimSpace(user.Settings.String) != "" {
		_ = json.Unmarshal([]byte(user.Settings.String), &settings)
	}
	applyUserSettingsAttributes(settings, attrs)
	encoded, _ := json.Marshal(settings)
	return s.db.Model(&models.User{}).Where("id = ?", userID).Update("settings", string(encoded)).Error
}

func applyUserSettingsAttributes(settings map[string]any, attrs map[string]any) {
	for key, value := range attrs {
		if value == nil {
			delete(settings, key)
			continue
		}
		settings[key] = value
	}
}

func settingsChangeSavedMessage(locale string) string {
	return settingsT(locale, "generic.changes_saved_msg", "Changes successfully saved!")
}

func settingsDatabaseUnavailableMessage(locale string) string {
	return settingsT(locale, "errors.database_unavailable", "DATABASE_URL is not set")
}

func settingsProfileInvalidMessage(locale string) string {
	return settingsT(locale, "edit_profile.errors.invalid", "Profile update is invalid")
}

func settingsPrivacyInvalidMessage(locale string) string {
	return settingsT(locale, "privacy.errors.invalid", "Privacy update is invalid")
}
