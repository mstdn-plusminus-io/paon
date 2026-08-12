package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) updateSettingsPreferences(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, settingsPreferencesRedirectPath(c))
	}
	userUpdates, settings, err := parseSettingsPreferencesPayload(c)
	if err != nil {
		if errors.Is(err, errSettingsPreferencesParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return s.renderSettingsPreferencesError(c, *account, user, err.Error())
	}
	if len(userUpdates) > 0 {
		userUpdates["updated_at"] = time.Now().UTC()
		if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(userUpdates).Error; err != nil {
			return err
		}
	}
	if len(settings) > 0 {
		if err := s.updateUserSettingsAttributes(user.ID, settings); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, settingsPreferencesRedirectPath(c)+"?notice="+url.QueryEscape(settingsChangeSavedMessage(locale)))
}

func (s *Server) renderSettingsPreferencesError(c *echo.Context, account models.Account, user *models.User, errorText string) error {
	settings := decodeUserSettings(user.Settings.String)
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(settings)
	switch settingsPreferencesRedirectPath(c) {
	case "/settings/preferences/notifications":
		return c.HTML(http.StatusOK, settingsPreferencesNotificationsHTMLWithMessages(settings, "", errorText, locale, theme))
	case "/settings/preferences/other":
		return c.HTML(http.StatusOK, settingsPreferencesOtherHTMLWithMessages(*user, account, settings, "", errorText, locale, theme))
	default:
		return c.HTML(http.StatusOK, settingsPreferencesAppearanceHTMLWithMessages(*user, settings, "", errorText, locale, theme))
	}
}

func parseSettingsPreferencesPayload(c *echo.Context) (map[string]any, map[string]any, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return nil, nil, err
	}
	prefix := "user"
	if !formHasNestedPrefix(req.Form, prefix) {
		return nil, nil, errSettingsPreferencesParamsMissing
	}
	userUpdates := map[string]any{}
	if _, ok := req.Form[prefix+"[locale]"]; ok {
		userUpdates["locale"] = railsUserLocaleValue(lastFormValue(req.Form, prefix+"[locale]"))
	}
	if _, ok := req.Form[prefix+"[time_zone]"]; ok {
		userUpdates["time_zone"] = railsUserTimeZoneValue(lastFormValue(req.Form, prefix+"[time_zone]"))
	}
	if raw, ok := req.Form[prefix+"[chosen_languages][]"]; ok {
		userUpdates["chosen_languages"] = chosenLanguagesUpdateValue(raw)
	} else if raw, ok := req.Form[prefix+"[chosen_languages]"]; ok {
		userUpdates["chosen_languages"] = chosenLanguagesUpdateValue(raw)
	}

	settings, err := preferencesSettingsFromForm(req.Form)
	if err != nil {
		return nil, nil, err
	}
	return userUpdates, settings, nil
}

var errSettingsPreferencesParamsMissing = errors.New("settings preferences user root parameter is missing")

func preferencesSettingsFromForm(values map[string][]string) (map[string]any, error) {
	settings := map[string]any{}
	for key := range preferencesBoolSettingKeys() {
		for _, formKey := range preferenceSettingFormKeys(key) {
			if value, ok := nestedFormBool(values, formKey); ok {
				settings[key] = value
				break
			}
		}
	}
	for key, allowed := range preferencesStringSettingKeys() {
		formKey, ok := firstPresentPreferenceSettingFormKey(values, key)
		if !ok {
			continue
		}
		value := strings.TrimSpace(lastFormValue(values, formKey))
		if value == "" && key == "default_language" {
			settings[key] = nil
			continue
		}
		if len(allowed) > 0 && !stringAllowed(value, allowed) {
			return nil, errors.New("Preference value is invalid")
		}
		settings[key] = value
	}
	return settings, nil
}

func preferenceSettingFormKeys(key string) []string {
	return []string{
		"user[settings][" + key + "]",
		"user[settings_attributes][" + key + "]",
	}
}

func firstPresentPreferenceSettingFormKey(values map[string][]string, key string) (string, bool) {
	for _, formKey := range preferenceSettingFormKeys(key) {
		if _, ok := values[formKey]; ok {
			return formKey, true
		}
	}
	return "", false
}

func preferencesBoolSettingKeys() map[string]struct{} {
	return map[string]struct{}{
		"always_send_emails":                  {},
		"aggregate_reblogs":                   {},
		"web.advanced_layout":                 {},
		"web.use_pending_items":               {},
		"web.auto_play":                       {},
		"web.reduce_motion":                   {},
		"web.disable_swiping":                 {},
		"web.use_system_font":                 {},
		"web.crop_images":                     {},
		"web.trends":                          {},
		"web.disable_hover_cards":             {},
		"web.reblog_modal":                    {},
		"web.delete_modal":                    {},
		"web.use_blurhash":                    {},
		"web.expand_content_warnings":         {},
		"notification_emails.follow":          {},
		"notification_emails.follow_request":  {},
		"notification_emails.reblog":          {},
		"notification_emails.favourite":       {},
		"notification_emails.mention":         {},
		"notification_emails.report":          {},
		"notification_emails.appeal":          {},
		"notification_emails.pending_account": {},
		"notification_emails.trends":          {},
		"interactions.must_be_follower":       {},
		"interactions.must_be_following":      {},
		"interactions.must_be_following_dm":   {},
		"interactions.must_be_human":          {},
		"default_sensitive":                   {},
	}
}

func preferencesStringSettingKeys() map[string][]string {
	return map[string][]string{
		"theme":                                nil,
		"web.display_media":                    {"default", "show_all", "hide_all"},
		"notification_emails.software_updates": {"none", "critical", "patch", "all"},
		"default_privacy":                      {"public", "unlisted", "private"},
		"default_language":                     nil,
	}
}

func nullStringFromForm(values map[string][]string, key string) sql.NullString {
	value := strings.TrimSpace(lastFormValue(values, key))
	return sql.NullString{String: value, Valid: value != ""}
}

func railsUserLocaleValue(value string) sql.NullString {
	if !railsLocaleAvailable(value) {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func railsLocaleAvailable(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, locale := range railsI18nAvailableLocales {
		if value == locale {
			return true
		}
	}
	return false
}

func railsUserTimeZoneValue(value string) sql.NullString {
	if strings.TrimSpace(value) == "" {
		return sql.NullString{}
	}
	if !railsTimeZoneAvailable(value) {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nonEmptyFormValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func chosenLanguagesUpdateValue(values []string) any {
	languages := nonEmptyFormValues(values)
	if len(languages) == 0 {
		return nil
	}
	return models.StringArray(languages)
}

func stringAllowed(value string, allowed []string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func settingsPreferencesRedirectPath(c *echo.Context) string {
	path := c.Request().URL.Path
	switch path {
	case "/settings/preferences/notifications", "/settings/preferences/other":
		return path
	default:
		return "/settings/preferences/appearance"
	}
}
