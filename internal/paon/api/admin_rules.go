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
)

type adminRuleForm struct {
	Text            string
	Hint            string
	Priority        int
	PriorityPresent bool
}

var errAdminRuleParamsMissing = errors.New("admin rule root parameter is missing")

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
		return s.renderAdminRulesIndexError(c, adminRuleForm{}, adminRuleMessage(locale, "errors.invalid", "Rule is invalid"), locale)
	}
	if err := validateAdminRuleForm(form); err != nil {
		return s.renderAdminRulesIndexError(c, form, adminRuleErrorText(locale, err), locale)
	}
	if s.db == nil {
		return s.renderAdminRulesIndexError(c, form, adminRuleMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale)
	}
	if err := s.insertAdminRule(form); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/rules")
}

func (s *Server) renderAdminRulesIndexError(c *echo.Context, form adminRuleForm, errorText string, locale string) error {
	rules, err := s.adminRuleModels()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRulesIndexHTML(rules, form, "", errorText, locale))
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

func adminRuleMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.rules."+key, fallback)
}

func adminRuleErrorText(locale string, err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	keys := map[string]string{
		"Rule text can't be blank": "text_blank",
		"Rule text is too long":    "text_too_long",
	}
	if key := keys[text]; key != "" {
		return adminRuleMessage(locale, "errors."+key, text)
	}
	return text
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
	err := s.db.Where("deleted_at IS NULL").Order("priority ASC, id ASC").Find(&rules).Error
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
	if err := s.db.Where("id = ?", id).Where("deleted_at IS NULL").First(&rule).Error; err != nil {
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
	return s.db.Create(&rule).Error
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
	return s.db.Model(&models.Rule{}).Where("id = ?", id).Updates(updates).Error
}

func adminRuleWithForm(rule models.Rule, form adminRuleForm) models.Rule {
	rule.Text = form.Text
	rule.Hint = form.Hint
	if form.PriorityPresent {
		rule.Priority = form.Priority
	}
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
	return form, nil
}

func validateAdminRuleForm(form adminRuleForm) error {
	if strings.TrimSpace(form.Text) == "" {
		return errAdminSetting("Rule text can't be blank")
	}
	if len([]rune(form.Text)) > 300 {
		return errAdminSetting("Rule text is too long")
	}
	return nil
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
			rows.WriteString(`<div class="announcements-list__item"><a class="announcements-list__item__title" href="/admin/rules/` + id + `/edit">` + strconv.Itoa(index+1) + `. ` + html.EscapeString(rule.Text) + `</a><div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta">` + html.EscapeString(rule.Hint) + `</div><div><a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/rules/` + id + `"><i class="fa fa-trash fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.rules.delete", "Delete")) + `</a></div></div></div>`)
		}
		rows.WriteString(`</div>`)
	}
	body := `<p>` + adminT(loc, "admin.rules.description_html", "Define the rules that apply on this server.") + `</p><hr class="spacer">` +
		simpleFormOpen("/admin/rules", "post") +
		`<div class="fields-group">` + adminRuleTextFieldHTML(form.Text, loc) + `</div>` +
		`<div class="fields-group">` + adminRuleHintFieldHTML(form.Hint, loc) + `</div>` +
		simpleSubmit(adminT(loc, "admin.rules.add_new", "Add rule")) +
		simpleFormClose() + `<hr class="spacer">` +
		rows.String()
	return authPageHTML(adminT(loc, "admin.rules.title", "Admin rules"), notice, errorText, body, loc)
}

func adminRuleEditHTML(rule models.Rule, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	id := strconv.FormatInt(rule.ID, 10)
	body := simpleFormOpen("/admin/rules/"+id, "patch") +
		`<div class="fields-group">` + adminRuleTextFieldHTML(rule.Text, loc) + `</div>` +
		`<div class="fields-group">` + adminRuleHintFieldHTML(rule.Hint, loc) + `</div>` +
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
