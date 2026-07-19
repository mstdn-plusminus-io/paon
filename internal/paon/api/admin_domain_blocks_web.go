package api

import (
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type adminDomainBlockForm struct {
	Domain         string
	Severity       string
	RejectMedia    bool
	RejectReports  bool
	PrivateComment string
	PublicComment  string
	Obfuscate      bool
}

var adminDomainBlockBatchFieldPattern = regexp.MustCompile(`^form_domain_block_batch\[domain_blocks_attributes\]\[([^\]]+)\]\[([^\]]+)\]$`)

var errAdminDomainBlockParamsMissing = errors.New("admin domain block root parameter is missing")

func (s *Server) newAdminDomainBlockPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.add_new", "New domain block"), "/admin/domain_blocks", "", adminDomainBlockForm{Domain: c.QueryParam("_domain"), Severity: "silence"}, false, adminT(locale, "admin.domain_blocks.new.create", "Create block"), c.QueryParam("error"), locale))
}

func (s *Server) editAdminDomainBlockPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	block, err := s.findAdminDomainBlock(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.edit", "Edit domain block"), "/admin/domain_blocks/"+strconv.FormatInt(block.ID, 10), "put", adminDomainBlockFormFromModel(block), true, adminT(locale, "generic.save_changes", "Save changes"), c.QueryParam("error"), locale))
}

func (s *Server) createAdminDomainBlockWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminDomainBlockForm(c, true)
	if err != nil {
		if errors.Is(err, errAdminDomainBlockParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		form = adminDomainBlockForm{Severity: "silence"}
		return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.add_new", "New domain block"), "/admin/domain_blocks", "", form, false, adminT(locale, "admin.domain_blocks.new.create", "Create block"), adminDomainBlockMessage(locale, "errors.invalid", "Domain block is invalid"), locale))
	}
	domain := normalizeDomain(form.Domain)
	if domain == "" {
		return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.add_new", "New domain block"), "/admin/domain_blocks", "", form, false, adminT(locale, "admin.domain_blocks.new.create", "Create block"), adminDomainBlockMessage(locale, "errors.domain_invalid", "Domain is invalid"), locale))
	}
	if !adminDomainBlockSeverityAllowed(form.Severity) {
		return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.add_new", "New domain block"), "/admin/domain_blocks", "", form, false, adminT(locale, "admin.domain_blocks.new.create", "Create block"), adminDomainBlockMessage(locale, "errors.severity_invalid", "Domain block severity is invalid"), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.add_new", "New domain block"), "/admin/domain_blocks", "", form, false, adminT(locale, "admin.domain_blocks.new.create", "Create block"), adminDomainBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	now := time.Now().UTC()
	block := adminDomainBlockModelFromForm(form, domain, now)
	existing, err := s.existingDomainBlockForDomain(domain)
	if err != nil {
		return err
	}
	upgraded := false
	if existing != nil {
		if !domainBlockStricterThan(block, *existing) {
			return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.add_new", "New domain block"), "/admin/domain_blocks", "", form, false, adminT(locale, "admin.domain_blocks.new.create", "Create block"), existingDomainBlockError(*existing, locale).Error, locale))
		}
		if existing.Domain == domain {
			upgraded = true
			if adminDomainBlockCreateRequiresConfirmation(form, existing, block, adminBatchFormParamExists(c, "confirm")) {
				return c.HTML(http.StatusOK, adminDomainBlockConfirmHTML(form, "/admin/domain_blocks", locale))
			}
			if err := s.db.Model(&models.DomainBlock{}).Where("id = ?", existing.ID).Updates(adminDomainBlockUpdates(form)).Error; err != nil {
				return err
			}
			if err := s.db.First(&block, existing.ID).Error; err != nil {
				return err
			}
		}
	}
	if !upgraded && adminDomainBlockCreateRequiresConfirmation(form, existing, block, adminBatchFormParamExists(c, "confirm")) {
		return c.HTML(http.StatusOK, adminDomainBlockConfirmHTML(form, "/admin/domain_blocks", locale))
	}
	if !upgraded {
		if err := s.db.Create(&block).Error; err != nil {
			if isUniqueConstraintError(err) {
				return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.add_new", "New domain block"), "/admin/domain_blocks", "", form, false, adminT(locale, "admin.domain_blocks.new.create", "Create block"), adminDomainBlockMessage(locale, "errors.taken", "Domain has already been taken"), locale))
			}
			return err
		}
	}
	if err := logAdminAction(s.db, user.AccountID, "create", domainBlockAuditLogTarget(block), now); err != nil {
		return err
	}
	if err := s.enqueueAdminDomainBlockEffectsOrApply(s.db, block, false); err != nil {
		return err
	}
	if err := s.materializeDomainControlMutation(c.Request().Context(), block.Domain); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances?limited=1&notice="+url.QueryEscape(adminDomainBlockMessage(locale, "created_msg", "Domain block created")))
}

func (s *Server) updateAdminDomainBlockWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/instances?limited=1")
	}
	block, err := s.findAdminDomainBlock(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminDomainBlockForm(c, false)
	if err != nil {
		if errors.Is(err, errAdminDomainBlockParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.edit", "Edit domain block"), "/admin/domain_blocks/"+strconv.FormatInt(block.ID, 10), "put", adminDomainBlockFormFromModel(block), true, adminT(locale, "generic.save_changes", "Save changes"), adminDomainBlockMessage(locale, "errors.invalid", "Domain block is invalid"), locale))
	}
	form.Domain = block.Domain
	if !adminDomainBlockSeverityAllowed(form.Severity) {
		return c.HTML(http.StatusOK, adminDomainBlockFormHTML(adminT(locale, "admin.domain_blocks.edit", "Edit domain block"), "/admin/domain_blocks/"+strconv.FormatInt(block.ID, 10), "put", form, true, adminT(locale, "generic.save_changes", "Save changes"), adminDomainBlockMessage(locale, "errors.severity_invalid", "Domain block severity is invalid"), locale))
	}
	if form.Severity == "suspend" && !domainBlockSeverityIs(block, "suspend") && !adminBatchFormParamExists(c, "confirm") {
		return c.HTML(http.StatusOK, adminDomainBlockConfirmHTML(form, "/admin/domain_blocks/"+strconv.FormatInt(block.ID, 10), locale))
	}
	if err := s.db.Model(&models.DomainBlock{}).Where("id = ?", block.ID).Updates(adminDomainBlockUpdates(form)).Error; err != nil {
		return err
	}
	block = models.DomainBlock{}
	if err := s.db.First(&block, c.Param("id")).Error; err != nil {
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "update", domainBlockAuditLogTarget(block), time.Now().UTC()); err != nil {
		return err
	}
	if err := s.enqueueAdminDomainBlockEffectsOrApply(s.db, block, true); err != nil {
		return err
	}
	if err := s.refreshDomainControlMutation(c.Request().Context(), block.Domain); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances?limited=1&notice="+url.QueryEscape(adminDomainBlockMessage(locale, "created_msg", "Domain block created")))
}

func (s *Server) destroyAdminDomainBlockWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	block, err := s.findAdminDomainBlock(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if err := s.applyAdminDomainUnblockEffects(s.db, block); err != nil {
		return err
	}
	if err := s.db.Delete(&models.DomainBlock{}, block.ID).Error; err != nil {
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "destroy", domainBlockAuditLogTarget(block), time.Now().UTC()); err != nil {
		return err
	}
	if err := s.refreshDomainControlMutation(c.Request().Context(), block.Domain); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances?limited=1&notice="+url.QueryEscape(adminDomainBlockMessage(locale, "destroyed_msg", "Domain block removed")))
}

func (s *Server) batchAdminDomainBlocks(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	forms, err := parseAdminDomainBlockBatchForms(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/admin/export_domain_blocks/new?error="+url.QueryEscape(adminDomainBlockMessage(locale, "errors.import_invalid", "Domain block import is invalid")))
	}
	rowsPresent := adminDomainBlockBatchRowsPresent(c)
	if !adminBatchFormParamExists(c, "save") {
		if rowsPresent {
			return c.Redirect(http.StatusFound, "/admin/instances?limited=1&notice="+url.QueryEscape(adminDomainBlockMessage(locale, "created_msg", "Domain block created")))
		}
		return c.Redirect(http.StatusFound, "/admin/export_domain_blocks/new?error="+url.QueryEscape(adminDomainBlockMessage(locale, "no_domain_block_selected", "No domain blocks were changed as none were selected")))
	}
	if len(forms) == 0 {
		if rowsPresent {
			return c.Redirect(http.StatusFound, "/admin/instances?limited=1&notice="+url.QueryEscape(adminDomainBlockMessage(locale, "created_msg", "Domain block created")))
		}
		return c.Redirect(http.StatusFound, "/admin/export_domain_blocks/new?error="+url.QueryEscape(adminDomainBlockMessage(locale, "no_domain_block_selected", "No domain blocks were changed as none were selected")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/export_domain_blocks/new?error="+url.QueryEscape(adminDomainBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	createdDomains := make(map[string]struct{}, len(forms))
	for _, form := range forms {
		domain, err := s.createAdminDomainBlockFromForm(c, user, form)
		if err != nil {
			return err
		}
		if domain != "" {
			createdDomains[domain] = struct{}{}
		}
	}
	for domain := range createdDomains {
		if err := s.materializeDomainControlMutation(c.Request().Context(), domain); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/admin/instances?limited=1&notice="+url.QueryEscape(adminDomainBlockMessage(locale, "created_msg", "Domain block created")))
}

func adminDomainBlockMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.domain_blocks."+key, fallback)
}

func adminDomainBlockCreateRequiresConfirmation(form adminDomainBlockForm, existing *models.DomainBlock, block models.DomainBlock, confirmed bool) bool {
	if confirmed || form.Severity != "suspend" {
		return false
	}
	return existing == nil || existing.Domain != block.Domain || existing.Severity != block.Severity
}

func (s *Server) createAdminDomainBlockFromForm(c *echo.Context, user *models.User, form adminDomainBlockForm) (string, error) {
	domain := normalizeDomain(form.Domain)
	if domain == "" {
		return "", nil
	}
	if form.Severity == "" {
		form.Severity = "suspend"
	}
	if !adminDomainBlockSeverityAllowed(form.Severity) {
		return "", echo.NewHTTPError(http.StatusBadRequest, "domain block severity is invalid")
	}
	existing, err := s.existingDomainBlockForDomain(domain)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return "", nil
	}
	now := time.Now().UTC()
	block := adminDomainBlockModelFromForm(form, domain, now)
	if err := s.db.Create(&block).Error; err != nil {
		if isUniqueConstraintError(err) {
			return "", nil
		}
		return "", err
	}
	if err := logAdminAction(s.db, user.AccountID, "create", domainBlockAuditLogTarget(block), now); err != nil {
		return "", err
	}
	if err := s.enqueueAdminDomainBlockEffectsOrApply(s.db, block, false); err != nil {
		return "", err
	}
	return block.Domain, nil
}

func adminDomainBlockModelFromForm(form adminDomainBlockForm, domain string, now time.Time) models.DomainBlock {
	return models.DomainBlock{
		Domain:         domain,
		Severity:       domainBlockSeverityValue(form.Severity),
		RejectMedia:    form.RejectMedia,
		RejectReports:  form.RejectReports,
		PrivateComment: sql.NullString{String: form.PrivateComment, Valid: form.PrivateComment != ""},
		PublicComment:  sql.NullString{String: form.PublicComment, Valid: form.PublicComment != ""},
		Obfuscate:      form.Obfuscate,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func (s *Server) findAdminDomainBlock(rawID string) (models.DomainBlock, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 {
		return models.DomainBlock{}, echo.NewHTTPError(http.StatusNotFound, "domain block not found")
	}
	var block models.DomainBlock
	if s.db == nil {
		return block, echo.NewHTTPError(http.StatusNotFound, "domain block not found")
	}
	if err := s.db.First(&block, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return block, echo.NewHTTPError(http.StatusNotFound, "domain block not found")
		}
		return block, err
	}
	return block, nil
}

func parseAdminDomainBlockForm(c *echo.Context, includeDomain bool) (adminDomainBlockForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminDomainBlockForm{}, err
	}
	prefix := "domain_block"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminDomainBlockForm{}, errAdminDomainBlockParamsMissing
	}
	form := adminDomainBlockForm{
		Severity:       strings.TrimSpace(lastFormValue(req.Form, "domain_block[severity]")),
		RejectMedia:    adminSettingsCheckbox(req.Form, "domain_block[reject_media]"),
		RejectReports:  adminSettingsCheckbox(req.Form, "domain_block[reject_reports]"),
		PrivateComment: lastFormValue(req.Form, "domain_block[private_comment]"),
		PublicComment:  lastFormValue(req.Form, "domain_block[public_comment]"),
		Obfuscate:      adminSettingsCheckbox(req.Form, "domain_block[obfuscate]"),
	}
	if includeDomain {
		form.Domain = strings.TrimSpace(lastFormValue(req.Form, "domain_block[domain]"))
	}
	return form, nil
}

func parseAdminDomainBlockBatchForms(c *echo.Context) ([]adminDomainBlockForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return nil, err
	}
	type batchRow struct {
		enabled bool
		values  map[string]string
	}
	rows := map[string]*batchRow{}
	for key, values := range req.Form {
		match := adminDomainBlockBatchFieldPattern.FindStringSubmatch(key)
		if len(match) != 3 {
			continue
		}
		index, field := match[1], match[2]
		row := rows[index]
		if row == nil {
			row = &batchRow{values: map[string]string{}}
			rows[index] = row
		}
		value := values[len(values)-1]
		if field == "enabled" {
			row.enabled = truthy(strings.TrimSpace(value))
			continue
		}
		if field != "private_comment" && field != "public_comment" {
			value = strings.TrimSpace(value)
		}
		row.values[field] = value
	}
	forms := make([]adminDomainBlockForm, 0, len(rows))
	for _, row := range rows {
		if !row.enabled {
			continue
		}
		form := adminDomainBlockForm{
			Domain:         row.values["domain"],
			Severity:       firstNonEmpty(row.values["severity"], "suspend"),
			RejectMedia:    truthy(row.values["reject_media"]),
			RejectReports:  truthy(row.values["reject_reports"]),
			PrivateComment: row.values["private_comment"],
			PublicComment:  row.values["public_comment"],
			Obfuscate:      truthy(row.values["obfuscate"]),
		}
		if strings.TrimSpace(form.Domain) != "" {
			forms = append(forms, form)
		}
	}
	return forms, nil
}

func adminDomainBlockBatchRowsPresent(c *echo.Context) bool {
	req := c.Request()
	_ = req.ParseForm()
	for key := range req.Form {
		if adminDomainBlockBatchFieldPattern.MatchString(key) {
			return true
		}
	}
	return false
}

func adminDomainBlockUpdates(form adminDomainBlockForm) map[string]any {
	return map[string]any{
		"severity":        domainBlockSeverityValue(form.Severity),
		"reject_media":    form.RejectMedia,
		"reject_reports":  form.RejectReports,
		"private_comment": sql.NullString{String: form.PrivateComment, Valid: true},
		"public_comment":  sql.NullString{String: form.PublicComment, Valid: true},
		"obfuscate":       form.Obfuscate,
		"updated_at":      time.Now().UTC(),
	}
}

func adminDomainBlockSeverityAllowed(value string) bool {
	switch value {
	case "silence", "suspend", "noop":
		return true
	default:
		return false
	}
}

func adminDomainBlockSeverityLabel(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	switch value.Int64 {
	case 1:
		return "suspend"
	case 2:
		return "noop"
	default:
		return "silence"
	}
}

func adminDomainBlockFormFromModel(block models.DomainBlock) adminDomainBlockForm {
	return adminDomainBlockForm{
		Domain:         block.Domain,
		Severity:       adminDomainBlockSeverityLabel(block.Severity),
		RejectMedia:    block.RejectMedia,
		RejectReports:  block.RejectReports,
		PrivateComment: block.PrivateComment.String,
		PublicComment:  block.PublicComment.String,
		Obfuscate:      block.Obfuscate,
	}
}

func adminDomainBlockFormHTML(title string, action string, methodOverride string, form adminDomainBlockForm, readOnlyDomain bool, submitLabel string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	if strings.TrimSpace(submitLabel) == "" {
		submitLabel = adminT(loc, "generic.save_changes", "Save changes")
	}
	readonly := ""
	if readOnlyDomain {
		readonly = " readonly"
	}
	body := simpleFormOpen(action, methodOverride) +
		simpleTextInput(adminT(loc, "simple_form.labels.domain_block.domain", "Domain"), "domain_block[domain]", form.Domain, "text", readonly+` required`) +
		`<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.domain_block.severity", "Severity")) + `</label>` + adminDomainBlockSeveritySelect(form.Severity, loc) + `</div></div></div>` +
		simpleCheckbox(adminT(loc, "simple_form.labels.domain_block.reject_media", "Reject media"), "domain_block[reject_media]", form.RejectMedia) +
		simpleCheckbox(adminT(loc, "simple_form.labels.domain_block.reject_reports", "Reject reports"), "domain_block[reject_reports]", form.RejectReports) +
		simpleCheckbox(adminT(loc, "simple_form.labels.domain_block.obfuscate", "Obfuscate"), "domain_block[obfuscate]", form.Obfuscate) +
		simpleTextInput(adminT(loc, "simple_form.labels.domain_block.private_comment", "Private comment"), "domain_block[private_comment]", form.PrivateComment, "text", "") +
		simpleTextInput(adminT(loc, "simple_form.labels.domain_block.public_comment", "Public comment"), "domain_block[public_comment]", form.PublicComment, "text", "") +
		simpleSubmit(submitLabel) +
		simpleFormClose()
	return authPageHTML(title, "", errorText, body, loc)
}

func adminDomainBlockSeveritySelect(current string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	option := func(value string, label string) string {
		selected := ""
		if current == value {
			selected = ` selected`
		}
		return `<option value="` + value + `"` + selected + `>` + label + `</option>`
	}
	return `<select name="domain_block[severity]">` +
		option("silence", adminT(loc, "admin.domain_blocks.severity.silence", "Silence")) +
		option("suspend", adminT(loc, "admin.domain_blocks.severity.suspend", "Suspend")) +
		option("noop", adminT(loc, "admin.domain_blocks.severity.noop", "Noop")) +
		`</select>`
}

func adminDomainBlockConfirmHTML(form adminDomainBlockForm, action string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	hidden := func(name string, value string) string {
		return `<input type="hidden" name="domain_block[` + name + `]" value="` + html.EscapeString(value) + `">`
	}
	boolHidden := func(name string, value bool) string {
		if value {
			return hidden(name, "1")
		}
		return ""
	}
	body := `<form method="post" action="` + html.EscapeString(action) + `" class="simple_form new_domain_block">
	<p class="hint">` + adminTVars(loc, "admin.domain_blocks.confirm_suspension.preamble_html", "Suspending %{domain} is destructive and should be confirmed explicitly.", map[string]string{"domain": html.EscapeString(form.Domain)}) + `</p>
	<ul class="hint"><li>` + html.EscapeString(adminT(loc, "admin.domain_blocks.confirm_suspension.stop_communication", "Communication with this domain will stop.")) + `</li><li>` + html.EscapeString(adminT(loc, "admin.domain_blocks.confirm_suspension.remove_all_data", "All cached data from this domain will be removed.")) + `</li><li>` + html.EscapeString(adminT(loc, "admin.domain_blocks.confirm_suspension.undo_relationships", "Existing relationships will be removed.")) + `</li><li class="negative-hint">` + html.EscapeString(adminT(loc, "admin.domain_blocks.confirm_suspension.permanent_action", "This action cannot be fully undone.")) + `</li></ul>
  ` + hidden("domain", form.Domain) + hidden("severity", form.Severity) + boolHidden("reject_media", form.RejectMedia) + boolHidden("reject_reports", form.RejectReports) + boolHidden("obfuscate", form.Obfuscate) + hidden("private_comment", form.PrivateComment) + hidden("public_comment", form.PublicComment) + `
	<hr class="spacer">` + adminDashboardReactComponent("ImpactReport", map[string]any{"domain": form.Domain}) + `<div class="actions"><a class="button button-tertiary" href="/admin/instances">` + html.EscapeString(adminT(loc, "admin.domain_blocks.confirm_suspension.cancel", "Cancel")) + `</a><button class="button negative" type="submit" name="confirm" value="1">` + html.EscapeString(adminT(loc, "admin.domain_blocks.confirm_suspension.confirm", "Confirm suspension")) + `</button></div>
</form>`
	return authPageHTML(adminTVars(loc, "admin.domain_blocks.confirm_suspension.title", "Confirm domain suspension", map[string]string{"domain": form.Domain}), "", "", body, loc)
}
