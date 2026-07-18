package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

func (s *Server) instanceExtendedDescription(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	setting, err := s.findSetting("site_extended_description")
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return c.JSON(http.StatusOK, serializer.ExtendedDescriptionFromSetting(setting))
}

func (s *Server) instancePrivacyPolicy(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	setting, err := s.findSetting("site_terms")
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return c.JSON(http.StatusOK, serializer.PrivacyPolicyFromSetting(s.cfg, setting))
}

func (s *Server) customCSS(c *echo.Context) error {
	c.Response().Header().Set("Content-Type", "text/css; charset=utf-8")
	c.Response().Header().Set("Cache-Control", "max-age=180, public")
	return c.String(http.StatusOK, s.customCSSBody())
}

func (s *Server) customCSSBody() string {
	var body strings.Builder
	if custom := s.settingValue("custom_css", ""); strings.TrimSpace(custom) != "" {
		body.WriteString(custom)
		body.WriteString("\n\n")
	}
	if s == nil || s.db == nil {
		return body.String()
	}
	var roles []models.UserRole
	if err := s.db.
		Where("highlighted = ? AND color <> ?", true, "").
		Order("id ASC").
		Find(&roles).Error; err != nil {
		return body.String()
	}
	body.WriteString(highlightedUserRoleCSS(roles))
	return body.String()
}

func highlightedUserRoleCSS(roles []models.UserRole) string {
	var body strings.Builder
	for _, role := range roles {
		if !role.Highlighted {
			continue
		}
		if strings.TrimSpace(role.Color) == "" {
			continue
		}
		body.WriteString(".user-role-")
		body.WriteString(strconv.FormatInt(role.ID, 10))
		body.WriteString(" {\n  --user-role-accent: ")
		body.WriteString(role.Color)
		body.WriteString(";\n}\n\n")
	}
	return body.String()
}

func (s *Server) instanceDomainBlocks(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	showBlocks := s.settingValue("show_domain_blocks", "disabled")
	showRationale := s.settingValue("show_domain_blocks_rationale", "disabled")
	functionalOrMoved, err := s.currentUserFunctionalOrMoved(c)
	if err != nil {
		return err
	}

	if showBlocks != "all" && !(showBlocks == "users" && functionalOrMoved) {
		return c.NoContent(http.StatusNotFound)
	}
	if showBlocks == "all" {
		c.Response().Header().Set("Vary", "")
		s.publicRESTCacheEvenIfAuthenticated(c, 300)
	}
	withComment := showRationale == "all" || (showRationale == "users" && functionalOrMoved)

	var blocks []models.DomainBlock
	if s.db != nil {
		err := s.db.
			Where("severity IN ?", []int{0, 1}).
			Order("(CASE severity WHEN 0 THEN 1 WHEN 1 THEN 2 WHEN 2 THEN 0 END), domain").
			Find(&blocks).Error
		if err != nil {
			return err
		}
	}

	out := make([]serializer.InstanceDomainBlock, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, serializer.InstanceDomainBlockFromModel(block, withComment))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) currentUserFunctionalOrMoved(c *echo.Context) (bool, error) {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return false, nil
	}
	return s.userFunctionalOrMoved(*user), nil
}

func (s *Server) userFunctionalOrMoved(user models.User) bool {
	return userCanUseAuthenticatedAPI(user) && !s.userAccountSuspendedOrMemorial(user)
}

func (s *Server) userAccountSuspendedOrMemorial(user models.User) bool {
	account := user.Account
	if account != nil && account.ID != 0 {
		return account.SuspendedAt.Valid || account.Memorial
	}
	if s == nil || s.db == nil || user.AccountID == 0 {
		return false
	}
	var loaded models.Account
	if err := s.db.Select("id", "suspended_at", "memorial").Where("id = ?", user.AccountID).First(&loaded).Error; err != nil {
		return false
	}
	return loaded.SuspendedAt.Valid || loaded.Memorial
}

func (s *Server) findSetting(name string) (*models.Setting, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var setting models.Setting
	err := s.db.
		Where("var = ?", name).
		Where("thing_type IS NULL").
		Where("thing_id IS NULL").
		First(&setting).Error
	return &setting, err
}

func (s *Server) settingValue(name string, fallback string) string {
	setting, err := s.findSetting(name)
	if err != nil || setting == nil || !setting.Value.Valid || setting.Value.String == "" {
		return railsSettingDefaultValue(name, fallback)
	}
	return railsSettingStoredValue(setting.Value.String)
}

func (s *Server) settingRawValue(name string, fallback string) string {
	setting, err := s.findSetting(name)
	if err != nil || setting == nil || !setting.Value.Valid {
		return railsSettingDefaultValue(name, fallback)
	}
	return railsSettingStoredValue(setting.Value.String)
}

func (s *Server) settingStringValue(name string, fallback string) string {
	value := normalizeSettingScalar(s.settingValue(name, ""))
	if value == "" {
		return fallback
	}
	return value
}

func (s *Server) settingBoolValue(name string, fallback bool) bool {
	value := strings.ToLower(normalizeSettingScalar(s.settingValue(name, "")))
	switch value {
	case "1", "t", "true", "yes", "on":
		return true
	case "0", "f", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func (s *Server) settingOptionalBoolValue(name string) any {
	setting, err := s.findSetting(name)
	if err != nil || setting == nil || !setting.Value.Valid {
		return nil
	}
	value := strings.ToLower(normalizeSettingScalar(setting.Value.String))
	switch value {
	case "1", "t", "true", "yes", "on":
		return true
	case "0", "f", "false", "no", "off":
		return false
	default:
		return nil
	}
}

func (s *Server) settingOptionalStringValue(name string) any {
	setting, err := s.findSetting(name)
	if err != nil || setting == nil || !setting.Value.Valid {
		return nil
	}
	return normalizeSettingScalar(setting.Value.String)
}

func (s *Server) requireAuthenticatedAPIIfDisallowed(c *echo.Context) error {
	if !s.disallowUnauthenticatedAPIAccess() {
		return nil
	}
	c.Response().Header().Set("Vary", "Authorization")
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return apiError(c, http.StatusUnauthorized, "This method requires an authenticated user")
	}
	return s.requireFunctionalUser(c, *user)
}

func (s *Server) requireAuthenticatedAPIInLimitedFederation(c *echo.Context) error {
	if !s.cfg.LimitedFederationMode {
		return nil
	}
	return s.requireAuthenticatedAPIIfDisallowed(c)
}

func (s *Server) disallowUnauthenticatedAPIAccess() bool {
	return s.cfg.LimitedFederationMode || s.cfg.DisallowUnauthenticatedAPIAccess
}

func (s *Server) authorizedFetchMode() bool {
	if s.cfg.LimitedFederationMode || s.cfg.AuthorizedFetch {
		return true
	}
	if s.cfg.AuthorizedFetchEnvSet {
		return false
	}
	return s.settingBoolValue("authorized_fetch", false)
}

func (s *Server) authorizedFetchOverridden() bool {
	return s.cfg.LimitedFederationMode || s.cfg.AuthorizedFetch || s.cfg.AuthorizedFetchEnvSet
}

func normalizeSettingScalar(value string) string {
	if decoded, ok := decodeRailsSettingScalar(value); ok {
		return decoded
	}
	return strings.Trim(strings.TrimSpace(value), `"`)
}

func railsSettingStoredValue(value string) string {
	if decoded, ok := decodeRailsSettingScalar(value); ok {
		return decoded
	}
	return value
}

func decodeRailsSettingScalar(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "---") {
		return "", false
	}

	var decoded any
	if err := yaml.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return "", false
	}
	switch scalar := decoded.(type) {
	case nil:
		return "", true
	case string:
		return scalar, true
	case bool:
		return strconv.FormatBool(scalar), true
	case int:
		return strconv.Itoa(scalar), true
	case int64:
		return strconv.FormatInt(scalar, 10), true
	case uint64:
		return strconv.FormatUint(scalar, 10), true
	case float64:
		return strconv.FormatFloat(scalar, 'g', -1, 64), true
	default:
		return "", false
	}
}

func railsSettingDefaultValue(name string, fallback string) string {
	if value, ok := railsSettingDefaults[name]; ok {
		return value
	}
	return fallback
}

var railsSettingDefaults = map[string]string{
	"site_title":                   "Mastodon",
	"site_short_description":       "",
	"site_description":             "",
	"site_extended_description":    "",
	"site_terms":                   "",
	"site_contact_username":        "",
	"site_contact_email":           "",
	"registrations_mode":           "none",
	"profile_directory":            "true",
	"closed_registrations_message": "",
	"timeline_preview":             "true",
	"show_staff_badge":             "true",
	"preview_sensitive_media":      "false",
	"noindex":                      "false",
	"theme":                        "default",
	"trends":                       "true",
	"trends_as_landing_page":       "true",
	"trendable_by_default":         "false",
	"reserved_usernames":           "admin\nsupport\nhelp\nroot\nwebmaster\nadministrator\nmod\nmoderator",
	"disallowed_hashtags":          "",
	"bootstrap_timeline_accounts":  "",
	"activity_api_enabled":         "true",
	"peers_api_enabled":            "true",
	"show_domain_blocks":           "disabled",
	"show_domain_blocks_rationale": "disabled",
	"require_invite_text":          "false",
	"backups_retention_period":     "7",
	"captcha_enabled":              "false",
}
