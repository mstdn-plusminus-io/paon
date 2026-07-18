package api

import (
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var allowedCleanupMinStatusAges = map[int]struct{}{
	604800:   {},
	1209600:  {},
	2629746:  {},
	5259492:  {},
	7889238:  {},
	15778476: {},
	31556952: {},
	63113904: {},
}

var errStatusesCleanupParamsMissing = errors.New("account_statuses_cleanup_policy params missing")

func (s *Server) statusesCleanupPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalOrMovedAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	policy, err := s.findOrDefaultStatusesCleanupPolicy(account.ID)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, statusesCleanupHTML(*policy, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) updateStatusesCleanup(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalOrMovedAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/statuses_cleanup")
	}
	locale := s.webLocale(c, user)
	updates, err := parseStatusesCleanupPayload(c, locale)
	if err != nil {
		if errors.Is(err, errStatusesCleanupParamsMissing) {
			return c.NoContent(http.StatusNoContent)
		}
		return s.renderStatusesCleanupError(c, account.ID, user, err.Error())
	}
	now := time.Now().UTC()
	var existing models.AccountStatusesCleanupPolicy
	err = s.db.Where("account_id = ?", account.ID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		policy := defaultStatusesCleanupPolicy(account.ID, now)
		for key, value := range updates {
			applyStatusesCleanupUpdate(&policy, key, value)
		}
		if err := s.db.Create(&policy).Error; err != nil {
			return err
		}
		return c.Redirect(http.StatusFound, "/statuses_cleanup?notice="+url.QueryEscape(settingsT(locale, "generic.changes_saved_msg", "Statuses cleanup policy saved")))
	}
	if err != nil {
		return err
	}
	clearCursor := statusesCleanupUpdateWidensPolicy(existing, updates)
	updates["updated_at"] = now
	if err := s.db.Model(&models.AccountStatusesCleanupPolicy{}).Where("id = ? AND account_id = ?", existing.ID, account.ID).Updates(updates).Error; err != nil {
		return err
	}
	if clearCursor {
		s.clearStatusesCleanupLastInspected(c.Request().Context(), account.ID)
	}
	return c.Redirect(http.StatusFound, "/statuses_cleanup?notice="+url.QueryEscape(settingsT(locale, "generic.changes_saved_msg", "Statuses cleanup policy saved")))
}

func (s *Server) renderStatusesCleanupError(c *echo.Context, accountID int64, user *models.User, errorText string) error {
	policy, err := s.findOrDefaultStatusesCleanupPolicy(accountID)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, statusesCleanupHTML(*policy, "", errorText, renderArgs...))
}

func (s *Server) findOrDefaultStatusesCleanupPolicy(accountID int64) (*models.AccountStatusesCleanupPolicy, error) {
	var policy models.AccountStatusesCleanupPolicy
	err := s.db.Where("account_id = ?", accountID).First(&policy).Error
	if err == nil {
		return &policy, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		now := time.Now().UTC()
		policy = defaultStatusesCleanupPolicy(accountID, now)
		return &policy, nil
	}
	return nil, err
}

func defaultStatusesCleanupPolicy(accountID int64, now time.Time) models.AccountStatusesCleanupPolicy {
	return models.AccountStatusesCleanupPolicy{
		AccountID:        accountID,
		Enabled:          false,
		MinStatusAge:     1209600,
		KeepDirect:       true,
		KeepPinned:       true,
		KeepSelfFav:      true,
		KeepSelfBookmark: true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func parseStatusesCleanupPayload(c *echo.Context, locale ...string) (map[string]any, error) {
	loc := settingsLocaleArgOrEnglish(locale...)
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return nil, err
	}
	updates := map[string]any{}
	prefix := "account_statuses_cleanup_policy"
	if !formHasNestedPrefix(req.Form, prefix) {
		return nil, errStatusesCleanupParamsMissing
	}
	for _, key := range []string{"enabled", "keep_direct", "keep_pinned", "keep_polls", "keep_media", "keep_self_fav", "keep_self_bookmark"} {
		formKey := prefix + "[" + key + "]"
		if value, ok := nestedFormBool(req.Form, formKey); ok {
			updates[key] = value
		}
	}
	if _, ok := req.Form[prefix+"[min_status_age]"]; ok {
		value := int(railsToInt64(lastFormValue(req.Form, prefix+"[min_status_age]")))
		if _, ok := allowedCleanupMinStatusAges[value]; !ok {
			return nil, statusCleanupInputError(settingsT(loc, "statuses_cleanup.errors.min_age_invalid", "Minimum age is invalid"))
		}
		updates["min_status_age"] = value
	}
	for _, key := range []string{"min_favs", "min_reblogs"} {
		formKey := prefix + "[" + key + "]"
		if _, ok := req.Form[formKey]; !ok {
			continue
		}
		value := strings.TrimSpace(lastFormValue(req.Form, formKey))
		if value == "" {
			updates[key] = nil
			continue
		}
		number := railsToInt64(value)
		if number < 1 {
			return nil, statusCleanupInputError(settingsT(loc, "statuses_cleanup.errors.interaction_threshold_invalid", "Interaction threshold is invalid"))
		}
		updates[key] = sql.NullInt64{Int64: number, Valid: true}
	}
	return updates, nil
}

func formHasNestedPrefix(values map[string][]string, prefix string) bool {
	nested := prefix + "["
	for key := range values {
		if strings.HasPrefix(key, nested) {
			return true
		}
	}
	return false
}

func applyStatusesCleanupUpdate(policy *models.AccountStatusesCleanupPolicy, key string, value any) {
	switch key {
	case "enabled":
		policy.Enabled = value.(bool)
	case "keep_direct":
		policy.KeepDirect = value.(bool)
	case "keep_pinned":
		policy.KeepPinned = value.(bool)
	case "keep_polls":
		policy.KeepPolls = value.(bool)
	case "keep_media":
		policy.KeepMedia = value.(bool)
	case "keep_self_fav":
		policy.KeepSelfFav = value.(bool)
	case "keep_self_bookmark":
		policy.KeepSelfBookmark = value.(bool)
	case "min_status_age":
		policy.MinStatusAge = value.(int)
	case "min_favs":
		if v, ok := value.(sql.NullInt64); ok {
			policy.MinFavs = v
		}
	case "min_reblogs":
		if v, ok := value.(sql.NullInt64); ok {
			policy.MinReblogs = v
		}
	}
}

func statusesCleanupUpdateWidensPolicy(existing models.AccountStatusesCleanupPolicy, updates map[string]any) bool {
	for _, key := range []string{"keep_direct", "keep_pinned", "keep_polls", "keep_media", "keep_self_fav", "keep_self_bookmark"} {
		value, ok := updates[key]
		if !ok {
			continue
		}
		next, ok := value.(bool)
		if !ok {
			continue
		}
		if statusesCleanupPolicyBool(existing, key) && !next {
			return true
		}
	}
	for _, key := range []string{"min_favs", "min_reblogs"} {
		value, ok := updates[key]
		if !ok {
			continue
		}
		current := statusesCleanupPolicyThreshold(existing, key)
		if !current.Valid {
			continue
		}
		next, ok := value.(sql.NullInt64)
		if !ok {
			return true
		}
		if !next.Valid || next.Int64 > current.Int64 {
			return true
		}
	}
	return false
}

func statusesCleanupPolicyBool(policy models.AccountStatusesCleanupPolicy, key string) bool {
	switch key {
	case "keep_direct":
		return policy.KeepDirect
	case "keep_pinned":
		return policy.KeepPinned
	case "keep_polls":
		return policy.KeepPolls
	case "keep_media":
		return policy.KeepMedia
	case "keep_self_fav":
		return policy.KeepSelfFav
	case "keep_self_bookmark":
		return policy.KeepSelfBookmark
	default:
		return false
	}
}

func statusesCleanupPolicyThreshold(policy models.AccountStatusesCleanupPolicy, key string) sql.NullInt64 {
	switch key {
	case "min_favs":
		return policy.MinFavs
	case "min_reblogs":
		return policy.MinReblogs
	default:
		return sql.NullInt64{}
	}
}

func statusesCleanupHTML(policy models.AccountStatusesCleanupPolicy, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArg(localeAndTheme...)
	return authPageHTML(settingsT(loc, "settings.statuses_cleanup", "Automated post deletion"), notice, errorText, `
    <form class="simple_form" id="edit_policy" method="post" action="/statuses_cleanup">
      <input type="hidden" name="_method" value="put">
      <div class="fields-row">
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupCheckboxField("account_statuses_cleanup_policy[enabled]", "enabled", settingsT(loc, "statuses_cleanup.enabled", "Automatically delete old posts"), settingsT(loc, "statuses_cleanup.enabled_hint", "Automatically deletes your posts once they reach a specified age threshold, unless they match one of the exceptions below"), policy.Enabled)+`
        </div>
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupSelectField("account_statuses_cleanup_policy[min_status_age]", "min_status_age", settingsT(loc, "statuses_cleanup.min_age_label", "Age threshold"), cleanupAgeOptions(policy.MinStatusAge, loc))+`
        </div>
      </div>
      <div class="flash-message">`+html.EscapeString(settingsT(loc, "statuses_cleanup.explanation", "Old posts are deleted slowly over time when the server is not otherwise busy."))+`</div>
      <h4>`+html.EscapeString(settingsT(loc, "statuses_cleanup.exceptions", "Exceptions"))+`</h4>
      <div class="fields-row">
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupCheckboxField("account_statuses_cleanup_policy[keep_pinned]", "keep_pinned", settingsT(loc, "statuses_cleanup.keep_pinned", "Keep pinned posts"), settingsT(loc, "statuses_cleanup.keep_pinned_hint", "Does not delete pinned posts"), policy.KeepPinned)+`
        </div>
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupCheckboxField("account_statuses_cleanup_policy[keep_direct]", "keep_direct", settingsT(loc, "statuses_cleanup.keep_direct", "Keep direct messages"), settingsT(loc, "statuses_cleanup.keep_direct_hint", "Does not delete direct messages"), policy.KeepDirect)+`
        </div>
      </div>
      <div class="fields-row">
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupCheckboxField("account_statuses_cleanup_policy[keep_self_fav]", "keep_self_fav", settingsT(loc, "statuses_cleanup.keep_self_fav", "Keep posts you favorited"), settingsT(loc, "statuses_cleanup.keep_self_fav_hint", "Does not delete your own posts that you have favorited"), policy.KeepSelfFav)+`
        </div>
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupCheckboxField("account_statuses_cleanup_policy[keep_self_bookmark]", "keep_self_bookmark", settingsT(loc, "statuses_cleanup.keep_self_bookmark", "Keep posts you bookmarked"), settingsT(loc, "statuses_cleanup.keep_self_bookmark_hint", "Does not delete your own posts that you have bookmarked"), policy.KeepSelfBookmark)+`
        </div>
      </div>
      <div class="fields-row">
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupCheckboxField("account_statuses_cleanup_policy[keep_polls]", "keep_polls", settingsT(loc, "statuses_cleanup.keep_polls", "Keep polls"), settingsT(loc, "statuses_cleanup.keep_polls_hint", "Does not delete polls"), policy.KeepPolls)+`
        </div>
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupCheckboxField("account_statuses_cleanup_policy[keep_media]", "keep_media", settingsT(loc, "statuses_cleanup.keep_media", "Keep posts with media attachments"), settingsT(loc, "statuses_cleanup.keep_media_hint", "Does not delete posts with media attachments"), policy.KeepMedia)+`
        </div>
      </div>
      <h4>`+html.EscapeString(settingsT(loc, "statuses_cleanup.interaction_exceptions", "Exceptions based on interactions"))+`</h4>
      <div class="fields-row">
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupNumberField("account_statuses_cleanup_policy[min_favs]", "min_favs", settingsT(loc, "statuses_cleanup.min_favs", "Keep posts favorited at least"), settingsT(loc, "statuses_cleanup.min_favs_hint", "Does not delete posts that have received at least this number of favorites"), settingsT(loc, "statuses_cleanup.ignore_favs", "Ignore favorites"), nullIntValue(policy.MinFavs))+`
        </div>
        <div class="fields-row__column fields-row__column-6 fields-group">
          `+statusesCleanupNumberField("account_statuses_cleanup_policy[min_reblogs]", "min_reblogs", settingsT(loc, "statuses_cleanup.min_reblogs", "Keep posts boosted at least"), settingsT(loc, "statuses_cleanup.min_reblogs_hint", "Does not delete posts that have been boosted at least this number of times"), settingsT(loc, "statuses_cleanup.ignore_reblogs", "Ignore boosts"), nullIntValue(policy.MinReblogs))+`
        </div>
      </div>
      <div class="flash-message">`+html.EscapeString(settingsT(loc, "statuses_cleanup.interaction_exceptions_explanation", "There is no guarantee for posts to be deleted after going below these thresholds."))+`</div>
    </form>`, localeAndTheme...)
}

func cleanupAgeOptions(selected int, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	ages := []int{604800, 1209600, 2629746, 5259492, 7889238, 15778476, 31556952, 63113904}
	var out strings.Builder
	for _, age := range ages {
		out.WriteString(`<option value="`)
		out.WriteString(strconv.Itoa(age))
		out.WriteString(`"`)
		if age == selected {
			out.WriteString(` selected`)
		}
		out.WriteString(`>`)
		out.WriteString(html.EscapeString(settingsT(loc, "statuses_cleanup.min_age."+strconv.Itoa(age), strconv.Itoa(age))))
		out.WriteString(`</option>`)
	}
	return out.String()
}

func statusesCleanupCheckboxField(name string, idSuffix string, label string, hint string, checked bool) string {
	id := "account_statuses_cleanup_policy_" + idSuffix
	classes := `input with_label boolean optional account_statuses_cleanup_policy_` + html.EscapeString(idSuffix)
	if strings.TrimSpace(hint) != "" {
		classes += ` field_with_hint`
	}
	fieldHTML := `<div class="` + classes + `"><div class="label_input"><label class="boolean optional" for="` + html.EscapeString(id) + `">` + html.EscapeString(label) + `</label><div class="label_input__wrapper"><input type="hidden" name="` + html.EscapeString(name) + `" value="0"><label class="checkbox"><input class="boolean optional" id="` + html.EscapeString(id) + `" type="checkbox" name="` + html.EscapeString(name) + `" value="1"`
	if checked {
		fieldHTML += ` checked`
	}
	fieldHTML += `></label></div></div>`
	if strings.TrimSpace(hint) != "" {
		fieldHTML += `<span class="hint">` + html.EscapeString(hint) + `</span>`
	}
	return fieldHTML + `</div>`
}

func statusesCleanupSelectField(name string, idSuffix string, label string, options string) string {
	id := "account_statuses_cleanup_policy_" + idSuffix
	return `<div class="input with_label select optional account_statuses_cleanup_policy_` + html.EscapeString(idSuffix) + `"><div class="label_input"><label class="select optional" for="` + html.EscapeString(id) + `">` + html.EscapeString(label) + `</label><div class="label_input__wrapper"><select class="select optional" id="` + html.EscapeString(id) + `" name="` + html.EscapeString(name) + `">` + options + `</select></div></div></div>`
}

func statusesCleanupNumberField(name string, idSuffix string, label string, hint string, placeholder string, value string) string {
	id := "account_statuses_cleanup_policy_" + idSuffix
	classes := `input with_label integer optional account_statuses_cleanup_policy_` + html.EscapeString(idSuffix)
	if strings.TrimSpace(hint) != "" {
		classes += ` field_with_hint`
	}
	out := `<div class="` + classes + `"><div class="label_input"><label class="integer optional" for="` + html.EscapeString(id) + `">` + html.EscapeString(label) + `</label><div class="label_input__wrapper"><input id="` + html.EscapeString(id) + `" class="numeric integer optional" name="` + html.EscapeString(name) + `" type="number" min="1" placeholder="` + html.EscapeString(placeholder) + `" value="` + html.EscapeString(value) + `"></div></div>`
	if strings.TrimSpace(hint) != "" {
		out += `<span class="hint">` + html.EscapeString(hint) + `</span>`
	}
	return out + `</div>`
}

func nullIntValue(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	return strconv.FormatInt(value.Int64, 10)
}

type statusCleanupInputError string

func (e statusCleanupInputError) Error() string { return string(e) }
