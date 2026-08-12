package api

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type adminRuleForm struct {
	Text            string
	Hint            string
	Priority        int
	PriorityPresent bool
	Translations    []adminRuleTranslationForm
}

type adminRuleTranslationForm struct {
	ID       int64
	Language string
	Text     string
	Hint     string
	Destroy  bool
}

var errAdminRuleParamsMissing = errors.New("admin rule root parameter is missing")

var (
	errAdminRuleTranslationLanguageBlank = errors.New("rule translation language can't be blank")
	errAdminRuleTranslationLanguageBad   = errors.New("rule translation language is invalid")
	errAdminRuleTranslationTextBlank     = errors.New("rule translation text can't be blank")
	errAdminRuleTranslationTextTooLong   = errors.New("rule translation text is too long")
	errAdminRuleTranslationDuplicate     = errors.New("rule translation language has already been taken")
)

func (s *Server) adminRulesPage(c *echo.Context) error {
	user, handled, err := s.requireAdminRulesWebUser(c)
	if handled || err != nil {
		return err
	}
	rules, err := s.adminRuleModels()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRulesIndexHTML(rules, adminRuleForm{}, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) newAdminRulePage(c *echo.Context) error {
	user, handled, err := s.requireAdminRulesWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRuleNewHTML(adminRuleForm{}, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminRule(c *echo.Context) error {
	user, handled, err := s.requireAdminRulesWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminRuleForm(c)
	if err != nil {
		if errors.Is(err, errAdminRuleParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminRuleNewHTML(adminRuleForm{}, "", adminRuleMessage(locale, "errors.invalid", "Rule is invalid"), locale))
	}
	if err := validateAdminRuleForm(form); err != nil {
		return c.HTML(http.StatusOK, adminRuleNewHTML(form, "", adminRuleErrorText(locale, err), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminRuleNewHTML(form, "", adminRuleMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	if err := s.insertAdminRule(form); err != nil {
		if isAdminRuleValidationError(err) {
			return c.HTML(http.StatusOK, adminRuleNewHTML(form, "", adminRuleErrorText(locale, err), locale))
		}
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/rules")
}

func (s *Server) editAdminRulePage(c *echo.Context) error {
	user, handled, err := s.requireAdminRulesWebUser(c)
	if handled || err != nil {
		return err
	}
	rule, err := s.findAdminRule(c.Param("id"))
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRuleEditHTML(rule, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) updateAdminRule(c *echo.Context) error {
	user, handled, err := s.requireAdminRulesWebUser(c)
	if handled || err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/rules")
	}
	rule, err := s.findAdminRule(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminRuleForm(c)
	if err != nil {
		if errors.Is(err, errAdminRuleParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminRuleEditHTML(rule, "", adminRuleMessage(locale, "errors.invalid", "Rule is invalid"), locale))
	}
	if err := validateAdminRuleForm(form); err != nil {
		return c.HTML(http.StatusOK, adminRuleEditHTML(adminRuleWithForm(rule, form), "", adminRuleErrorText(locale, err), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminRuleEditHTML(adminRuleWithForm(rule, form), "", adminRuleMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	if err := s.updateAdminRuleModel(rule.ID, form); err != nil {
		if isAdminRuleValidationError(err) {
			return c.HTML(http.StatusOK, adminRuleEditHTML(adminRuleWithForm(rule, form), "", adminRuleErrorText(locale, err), locale))
		}
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/rules")
}

func (s *Server) adminRuleMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminRule(c)
	}
	if methodOverrideIs(c, "put", "patch") {
		return s.updateAdminRule(c)
	}
	return c.Redirect(http.StatusFound, "/admin/rules")
}

func (s *Server) destroyAdminRule(c *echo.Context) error {
	user, handled, err := s.requireAdminRulesWebUser(c)
	if handled || err != nil {
		return err
	}
	rule, err := s.findAdminRule(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/rules?error="+url.QueryEscape(adminRuleMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	now := time.Now().UTC()
	if err := s.db.Model(&models.Rule{}).
		Where("id = ?", rule.ID).
		Where("deleted_at IS NULL").
		Updates(map[string]any{
			"deleted_at": sql.NullTime{Time: now, Valid: true},
			"updated_at": now,
		}).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/rules")
}

func (s *Server) moveAdminRuleUp(c *echo.Context) error {
	return s.moveAdminRule(c, -1)
}

func (s *Server) moveAdminRuleDown(c *echo.Context) error {
	return s.moveAdminRule(c, 1)
}

func (s *Server) moveAdminRule(c *echo.Context, offset int) error {
	_, handled, err := s.requireAdminRulesWebUser(c)
	if handled || err != nil {
		return err
	}
	rule, err := s.findAdminRule(c.Param("id"))
	if err != nil {
		return err
	}
	if s.db == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	err = s.moveAdminRuleModel(c.Request().Context(), rule.ID, offset)
	if err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/rules")
}

func (s *Server) moveAdminRuleModel(ctx context.Context, ruleID int64, offset int) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rules []models.Rule
		if err := tx.Where("deleted_at IS NULL").Order("priority ASC, id ASC").Find(&rules).Error; err != nil {
			return err
		}
		position := -1
		for index := range rules {
			if rules[index].ID == ruleID {
				position = index
				break
			}
		}
		if position < 0 {
			return gorm.ErrRecordNotFound
		}
		target := position + offset
		if target < 0 || target >= len(rules) {
			return nil
		}
		moved := rules[position]
		rules = append(rules[:position], rules[position+1:]...)
		rules = append(rules, models.Rule{})
		copy(rules[target+1:], rules[target:len(rules)-1])
		rules[target] = moved
		now := time.Now().UTC()
		for index := range rules {
			if err := tx.Model(&models.Rule{}).Where("id = ?", rules[index].ID).Updates(map[string]any{"priority": index, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func adminRuleMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.rules."+key, fallback)
}

func adminRuleErrorText(locale string, err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	keys := map[string]string{
		"Rule text can't be blank":                         "text_blank",
		"Rule text is too long":                            "text_too_long",
		"rule translation language can't be blank":         "translation_language_blank",
		"rule translation language is invalid":             "translation_language_invalid",
		"rule translation text can't be blank":             "translation_text_blank",
		"rule translation text is too long":                "translation_text_too_long",
		"rule translation language has already been taken": "translation_language_taken",
	}
	if key := keys[text]; key != "" {
		return adminRuleMessage(locale, "errors."+key, text)
	}
	return text
}

func isAdminRuleValidationError(err error) bool {
	return errors.Is(err, errAdminRuleTranslationLanguageBlank) ||
		errors.Is(err, errAdminRuleTranslationLanguageBad) ||
		errors.Is(err, errAdminRuleTranslationTextBlank) ||
		errors.Is(err, errAdminRuleTranslationTextTooLong) ||
		errors.Is(err, errAdminRuleTranslationDuplicate)
}

func (s *Server) requireAdminRulesWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageRules) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.rules.title", "Admin rules"), "", adminT(locale, "admin.rules.not_permitted", "You are not allowed to manage rules."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminRuleModels() ([]models.Rule, error) {
	if s.db == nil {
		return []models.Rule{}, nil
	}
	var rules []models.Rule
	err := s.db.Preload("Translations", func(db *gorm.DB) *gorm.DB { return db.Order("language ASC") }).
		Where("deleted_at IS NULL").Order("priority ASC, id ASC").Find(&rules).Error
	return rules, err
}

func (s *Server) findAdminRule(rawID string) (models.Rule, error) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return models.Rule{}, echo.NewHTTPError(http.StatusNotFound, "rule not found")
	}
	var rule models.Rule
	if s.db == nil {
		return rule, echo.NewHTTPError(http.StatusNotFound, "rule not found")
	}
	if err := s.db.Preload("Translations", func(db *gorm.DB) *gorm.DB { return db.Order("language ASC") }).
		Where("id = ?", id).Where("deleted_at IS NULL").First(&rule).Error; err != nil {
		return rule, echo.NewHTTPError(http.StatusNotFound, "rule not found")
	}
	return rule, nil
}

func (s *Server) insertAdminRule(form adminRuleForm) error {
	now := time.Now().UTC()
	rule := models.Rule{
		Text:      form.Text,
		Hint:      form.Hint,
		Priority:  form.Priority,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&rule).Error; err != nil {
			return err
		}
		return syncAdminRuleTranslations(tx, rule.ID, form.Translations, now)
	})
}

func (s *Server) updateAdminRuleModel(id int64, form adminRuleForm) error {
	updates := map[string]any{
		"text":       form.Text,
		"hint":       form.Hint,
		"updated_at": time.Now().UTC(),
	}
	if form.PriorityPresent {
		updates["priority"] = form.Priority
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Rule{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return syncAdminRuleTranslations(tx, id, form.Translations, time.Now().UTC())
	})
}

func adminRuleWithForm(rule models.Rule, form adminRuleForm) models.Rule {
	rule.Text = form.Text
	rule.Hint = form.Hint
	if form.PriorityPresent {
		rule.Priority = form.Priority
	}
	rule.Translations = adminRuleTranslationsFromForm(form.Translations)
	return rule
}

func parseAdminRuleForm(c *echo.Context) (adminRuleForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminRuleForm{}, err
	}
	prefix := "rule"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminRuleForm{}, errAdminRuleParamsMissing
	}
	form := adminRuleForm{
		Text: lastFormValue(req.Form, "rule[text]"),
		Hint: lastFormValue(req.Form, "rule[hint]"),
	}
	priority := strings.TrimSpace(lastFormValue(req.Form, "rule[priority]"))
	if priority != "" {
		parsed, err := strconv.Atoi(priority)
		if err != nil {
			return adminRuleForm{}, err
		}
		form.Priority = parsed
		form.PriorityPresent = true
	}
	translations, err := parseAdminRuleTranslationForms(req.Form)
	if err != nil {
		return adminRuleForm{}, err
	}
	form.Translations = translations
	return form, nil
}

func validateAdminRuleForm(form adminRuleForm) error {
	if strings.TrimSpace(form.Text) == "" {
		return errAdminSetting("Rule text can't be blank")
	}
	if len([]rune(form.Text)) > 300 {
		return errAdminSetting("Rule text is too long")
	}
	seen := make(map[string]struct{}, len(form.Translations))
	for _, translation := range form.Translations {
		if translation.Destroy {
			continue
		}
		if translation.ID == 0 && strings.TrimSpace(translation.Text) == "" && strings.TrimSpace(translation.Language) == "" && strings.TrimSpace(translation.Hint) == "" {
			continue
		}
		language, ok := canonicalRuleTranslationLanguage(translation.Language)
		if strings.TrimSpace(translation.Language) == "" {
			return errAdminRuleTranslationLanguageBlank
		}
		if !ok {
			return errAdminRuleTranslationLanguageBad
		}
		if strings.TrimSpace(translation.Text) == "" {
			return errAdminRuleTranslationTextBlank
		}
		if len([]rune(translation.Text)) > 300 {
			return errAdminRuleTranslationTextTooLong
		}
		key := strings.ToLower(language)
		if _, duplicate := seen[key]; duplicate {
			return errAdminRuleTranslationDuplicate
		}
		seen[key] = struct{}{}
	}
	return nil
}

func parseAdminRuleTranslationForms(values map[string][]string) ([]adminRuleTranslationForm, error) {
	const prefix = "rule[translations_attributes]["
	byIndex := map[string]*adminRuleTranslationForm{}
	for key := range values {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(key, prefix)
		end := strings.Index(remainder, "]")
		if end <= 0 {
			continue
		}
		index := remainder[:end]
		field := strings.TrimPrefix(remainder[end+1:], "[")
		field = strings.TrimSuffix(field, "]")
		if field == "" {
			continue
		}
		entry := byIndex[index]
		if entry == nil {
			entry = &adminRuleTranslationForm{}
			byIndex[index] = entry
		}
		value := lastFormValue(values, key)
		switch field {
		case "id":
			if strings.TrimSpace(value) != "" {
				id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if err != nil || id <= 0 {
					return nil, errAdminRuleTranslationLanguageBad
				}
				entry.ID = id
			}
		case "language":
			entry.Language = strings.TrimSpace(value)
		case "text":
			entry.Text = value
		case "hint":
			entry.Hint = value
		case "_destroy":
			entry.Destroy = formBoolValue(value)
		}
	}
	indexes := make([]string, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		left, leftErr := strconv.Atoi(indexes[i])
		right, rightErr := strconv.Atoi(indexes[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return indexes[i] < indexes[j]
	})
	out := make([]adminRuleTranslationForm, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, *byIndex[index])
	}
	return out, nil
}

func canonicalRuleTranslationLanguage(raw string) (string, bool) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")
	if raw == "" {
		return "", false
	}
	for _, language := range railsI18nAvailableLocales {
		if strings.EqualFold(language, raw) {
			return language, true
		}
	}
	return "", false
}

func syncAdminRuleTranslations(tx *gorm.DB, ruleID int64, forms []adminRuleTranslationForm, now time.Time) error {
	for _, form := range forms {
		if form.Destroy {
			if form.ID > 0 {
				if err := tx.Where("id = ? AND rule_id = ?", form.ID, ruleID).Delete(&models.RuleTranslation{}).Error; err != nil {
					return err
				}
			}
			continue
		}
		if form.ID == 0 && strings.TrimSpace(form.Text) == "" && strings.TrimSpace(form.Language) == "" && strings.TrimSpace(form.Hint) == "" {
			continue
		}
		language, ok := canonicalRuleTranslationLanguage(form.Language)
		if !ok {
			return errAdminRuleTranslationLanguageBad
		}
		if form.ID > 0 {
			result := tx.Model(&models.RuleTranslation{}).Where("id = ? AND rule_id = ?", form.ID, ruleID).Updates(map[string]any{
				"language":   language,
				"text":       form.Text,
				"hint":       form.Hint,
				"updated_at": now,
			})
			if result.Error != nil {
				return adminRuleTranslationDatabaseError(result.Error)
			}
			if result.RowsAffected == 0 {
				return errAdminRuleTranslationLanguageBad
			}
			continue
		}
		translation := models.RuleTranslation{RuleID: ruleID, Language: language, Text: form.Text, Hint: form.Hint, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&translation).Error; err != nil {
			return adminRuleTranslationDatabaseError(err)
		}
	}
	return nil
}

func adminRuleTranslationDatabaseError(err error) error {
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "rule_id_and_language") || (strings.Contains(text, "unique") && strings.Contains(text, "language")) {
		return errAdminRuleTranslationDuplicate
	}
	return err
}

func adminRuleTranslationsFromForm(forms []adminRuleTranslationForm) []models.RuleTranslation {
	out := make([]models.RuleTranslation, 0, len(forms))
	for _, form := range forms {
		if form.Destroy || (form.ID == 0 && strings.TrimSpace(form.Text) == "" && strings.TrimSpace(form.Language) == "") {
			continue
		}
		language, _ := canonicalRuleTranslationLanguage(form.Language)
		out = append(out, models.RuleTranslation{ID: form.ID, Language: language, Text: form.Text, Hint: form.Hint})
	}
	return out
}

func adminRulesIndexHTML(rules []models.Rule, form adminRuleForm, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	var rows strings.Builder
	if len(rules) == 0 {
		rows.WriteString(`<div class="muted-hint center-text">` + html.EscapeString(adminT(loc, "admin.rules.empty", "No server rules have been configured.")) + `</div>`)
	} else {
		rows.WriteString(`<div class="announcements-list">`)
		for index, rule := range rules {
			id := strconv.FormatInt(rule.ID, 10)
			text, hint := localizedRuleContent(rule, loc)
			rows.WriteString(`<div class="announcements-list__item"><a class="announcements-list__item__title" href="/admin/rules/` + id + `/edit">` + strconv.Itoa(index+1) + `. ` + html.EscapeString(text) + `</a><div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta">` + html.EscapeString(hint) + `</div><div class="rule-actions">`)
			if index > 0 {
				rows.WriteString(`<a class="table-action-link" data-method="post" href="/admin/rules/` + id + `/move_up"><i class="fa fa-arrow-up fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.rules.move_up", "Move up")) + `</a> `)
			}
			rows.WriteString(`<a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/rules/` + id + `"><i class="fa fa-trash fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.rules.delete", "Delete")) + `</a>`)
			if index+1 < len(rules) {
				rows.WriteString(` <a class="table-action-link" data-method="post" href="/admin/rules/` + id + `/move_down"><i class="fa fa-arrow-down fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.rules.move_down", "Move down")) + `</a>`)
			}
			rows.WriteString(`</div></div></div>`)
		}
		rows.WriteString(`</div>`)
	}
	body := `<div class="content__heading__actions"><a class="button" href="/admin/rules/new">` + html.EscapeString(adminT(loc, "admin.rules.add_new", "Add rule")) + `</a></div><p>` + adminT(loc, "admin.rules.description_html", "Define the rules that apply on this server.") + `</p><hr class="spacer">` +
		simpleFormOpen("/admin/rules", "post") +
		`<div class="fields-group">` + adminRuleTextFieldHTML(form.Text, loc) + `</div>` +
		`<div class="fields-group">` + adminRuleHintFieldHTML(form.Hint, loc) + `</div>` +
		simpleSubmit(adminT(loc, "admin.rules.add_new", "Add rule")) +
		simpleFormClose() + `<hr class="spacer">` +
		rows.String()
	return authPageHTML(adminT(loc, "admin.rules.title", "Admin rules"), notice, errorText, body, loc)
}

func adminRuleNewHTML(form adminRuleForm, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	body := `<p>` + adminT(loc, "admin.rules.description_html", "Define the rules that apply on this server.") + `</p><hr class="spacer">` +
		simpleFormOpen("/admin/rules", "post") +
		`<div class="fields-group">` + adminRuleTextFieldHTML(form.Text, loc) + `</div>` +
		`<div class="fields-group">` + adminRuleHintFieldHTML(form.Hint, loc) + `</div>` +
		simpleSubmit(adminT(loc, "admin.rules.add_new", "Add rule")) +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.rules.add_new", "Add rule"), notice, errorText, body, loc)
}

func adminRuleEditHTML(rule models.Rule, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	id := strconv.FormatInt(rule.ID, 10)
	text, hint := localizedRuleContent(rule, loc)
	var translations strings.Builder
	translations.WriteString(`<hr class="spacer"><h4>` + html.EscapeString(adminT(loc, "admin.rules.translations", "Translations")) + `</h4><p class="hint">` + html.EscapeString(adminT(loc, "admin.rules.translations_explanation", "Add optional translations. The default value is used when no translation matches.")) + `</p><div class="table-wrapper"><table class="table keywords-table"><thead><tr><th>` + html.EscapeString(adminT(loc, "admin.rules.translation", "Translation")) + `</th><th></th></tr></thead><tbody>`)
	for index, translation := range rule.Translations {
		translations.WriteString(adminRuleTranslationRowHTML(translation, index, loc))
	}
	translations.WriteString(adminRuleTranslationRowHTML(models.RuleTranslation{}, len(rule.Translations), loc))
	translations.WriteString(`</tbody></table></div><hr class="spacer"><h4>` + html.EscapeString(adminT(loc, "admin.rules.preview", "Preview")) + `</h4><ol class="rules-list"><li><div class="rules-list__text">` + html.EscapeString(text) + `</div>`)
	if strings.TrimSpace(hint) != "" {
		translations.WriteString(`<div class="rules-list__hint">` + html.EscapeString(hint) + `</div>`)
	}
	translations.WriteString(`</li></ol>`)
	body := simpleFormOpen("/admin/rules/"+id, "patch") +
		`<div class="fields-group">` + adminRuleTextFieldHTML(rule.Text, loc) + `</div>` +
		`<div class="fields-group">` + adminRuleHintFieldHTML(rule.Hint, loc) + `</div>` +
		translations.String() +
		simpleSubmit(adminT(loc, "generic.save_changes", "Save changes")) +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.rules.edit", "Edit rule"), notice, errorText, body, loc)
}

func adminRuleTextFieldHTML(value string, locale string) string {
	return `<div class="input with_block_label text required rule_text field_with_hint"><label class="text required" for="rule_text">` + html.EscapeString(adminT(locale, "simple_form.labels.rule.text", "Rule")) + filterRequiredMarker(locale) + `</label><span class="hint">` + html.EscapeString(adminT(locale, "simple_form.hints.rule.text", "Describe a rule or requirement for users. Keep it short and simple.")) + `</span><div class="label_input"><textarea class="text required" name="rule[text]" id="rule_text" maxlength="300" required="required" aria-required="true">` + html.EscapeString(value) + `</textarea></div></div>`
}

func adminRuleHintFieldHTML(value string, locale string) string {
	return `<div class="input with_block_label text optional rule_hint field_with_hint"><label class="text optional" for="rule_hint">` + html.EscapeString(adminT(locale, "simple_form.labels.rule.hint", "Additional information")) + `</label><span class="hint">` + html.EscapeString(adminT(locale, "simple_form.hints.rule.hint", "Optional. Provide more details about the rule")) + `</span><div class="label_input"><textarea class="text optional" name="rule[hint]" id="rule_hint">` + html.EscapeString(value) + `</textarea></div></div>`
}

func adminRuleTranslationRowHTML(translation models.RuleTranslation, index int, locale string) string {
	prefix := "rule[translations_attributes][" + strconv.Itoa(index) + "]"
	idPrefix := "rule_translations_attributes_" + strconv.Itoa(index)
	var options strings.Builder
	options.WriteString(`<option value=""></option>`)
	for _, language := range railsI18nAvailableLocales {
		selected := ""
		if strings.EqualFold(language, translation.Language) {
			selected = ` selected="selected"`
		}
		label := settingsNativeLocaleName(language)
		if strings.TrimSpace(label) == "" {
			label = railsStandardLocaleName(language)
		}
		standardName := railsStandardLocaleName(language)
		options.WriteString(`<option value="` + html.EscapeString(language) + `"` + selected + `>` + html.EscapeString(label+" ("+standardName+")") + `</option>`)
	}
	var persisted strings.Builder
	if translation.ID > 0 {
		persisted.WriteString(`<input type="hidden" name="` + prefix + `[id]" value="` + strconv.FormatInt(translation.ID, 10) + `">`)
		persisted.WriteString(`<input type="hidden" name="` + prefix + `[_destroy]" value="0"><label class="checkbox"><input type="checkbox" name="` + prefix + `[_destroy]" value="1"> ` + html.EscapeString(adminT(locale, "admin.rules.delete_translation", "Delete translation")) + `</label>`)
	}
	return `<tr class="nested-fields"><td><div class="fields-row"><div class="fields-row__column fields-group"><label for="` + idPrefix + `_language">` + html.EscapeString(adminT(locale, "admin.rules.translation_language", "Language")) + `</label><select id="` + idPrefix + `_language" name="` + prefix + `[language]">` + options.String() + `</select></div><div class="fields-row__column fields-group">` + persisted.String() + `</div></div><div class="fields-group"><label for="` + idPrefix + `_text">` + html.EscapeString(adminT(locale, "simple_form.labels.rule.text", "Rule")) + `</label><textarea maxlength="300" id="` + idPrefix + `_text" name="` + prefix + `[text]" lang="` + html.EscapeString(translation.Language) + `">` + html.EscapeString(translation.Text) + `</textarea></div><div class="fields-group"><label for="` + idPrefix + `_hint">` + html.EscapeString(adminT(locale, "simple_form.labels.rule.hint", "Additional information")) + `</label><textarea id="` + idPrefix + `_hint" name="` + prefix + `[hint]" lang="` + html.EscapeString(translation.Language) + `">` + html.EscapeString(translation.Hint) + `</textarea></div></td><td></td></tr>`
}

func localizedRuleContent(rule models.Rule, locale string) (string, string) {
	normalized := strings.ReplaceAll(strings.TrimSpace(locale), "_", "-")
	candidates := []string{normalized}
	if separator := strings.Index(normalized, "-"); separator > 0 {
		candidates = append(candidates, normalized[:separator])
	}
	for _, candidate := range candidates {
		for _, translation := range rule.Translations {
			if strings.EqualFold(translation.Language, candidate) {
				return translation.Text, translation.Hint
			}
		}
	}
	return rule.Text, rule.Hint
}
