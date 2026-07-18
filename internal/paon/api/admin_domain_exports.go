package api

import (
	"encoding/csv"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) newAdminExportDomainAllowsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminDomainExportFormHTML(adminT(locale, "admin.export_domain_allows.new.title", "Import domain allows"), "/admin/export_domain_allows/import", c.QueryParam("error"), locale))
}

func (s *Server) newAdminExportDomainBlocksPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminDomainExportFormHTML(adminT(locale, "admin.export_domain_blocks.new.title", "Import domain blocks"), "/admin/export_domain_blocks/import", c.QueryParam("error"), locale))
}

func (s *Server) exportAdminDomainAllowsCSV(c *echo.Context) error {
	if _, handled, err := s.requireAdminFederationWebUser(c); handled || err != nil {
		return err
	}
	var rows []models.DomainAllow
	if s.db != nil {
		if err := s.db.Order("domain ASC").Find(&rows).Error; err != nil {
			return err
		}
	}
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", csvBytes("domain_allows.csv", [][]string{{"#domain"}}, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{row.Domain}); err != nil {
				return err
			}
		}
		return nil
	}, c))
}

func (s *Server) exportAdminDomainBlocksCSV(c *echo.Context) error {
	if _, handled, err := s.requireAdminFederationWebUser(c); handled || err != nil {
		return err
	}
	var rows []models.DomainBlock
	if s.db != nil {
		if err := s.db.Where("severity IN ? OR reject_media = ?", []int{domainBlockSeverityCode("silence"), domainBlockSeverityCode("suspend")}, true).Order("domain ASC").Find(&rows).Error; err != nil {
			return err
		}
	}
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", csvBytes("domain_blocks.csv", [][]string{{"#domain", "#severity", "#reject_media", "#reject_reports", "#public_comment", "#obfuscate"}}, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{row.Domain, adminDomainBlockSeverityLabel(row.Severity), boolCSVValue(row.RejectMedia), boolCSVValue(row.RejectReports), row.PublicComment.String, boolCSVValue(row.Obfuscate)}); err != nil {
				return err
			}
		}
		return nil
	}, c))
}

func (s *Server) importAdminDomainAllowsCSV(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	rows, err := parseAdminDomainImportUpload(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/admin/instances?error="+url.QueryEscape(adminDomainExportAllowMessage(locale, "no_file", "No file selected")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/instances?error="+url.QueryEscape(adminDomainExportAllowMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	for _, row := range rows {
		domain := normalizeDomain(row["#domain"])
		if domain == "" {
			continue
		}
		now := time.Now().UTC()
		var allow models.DomainAllow
		err := s.db.Where("domain = ?", domain).First(&allow).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		allow = models.DomainAllow{Domain: domain, CreatedAt: now, UpdatedAt: now}
		if err := s.db.Create(&allow).Error; err != nil {
			if isUniqueConstraintError(err) {
				continue
			}
			return err
		}
		if err := logAdminAction(s.db, user.AccountID, "create", domainAllowAuditLogTarget(allow), now); err != nil {
			return err
		}
		if err := s.materializeDomainControlMutation(c.Request().Context(), allow.Domain); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/admin/instances?notice="+url.QueryEscape(adminT(locale, "admin.domain_allows.created_msg", "Domain allow created")))
}

func (s *Server) importAdminDomainBlocksCSV(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	rows, filename, err := parseAdminDomainImportUploadWithName(c)
	if err != nil {
		errorText := err.Error()
		if errorText == "No file selected" {
			errorText = adminDomainExportBlockMessage(locale, "no_file", "No file selected")
		}
		return c.HTML(http.StatusOK, adminDomainExportFormHTML(adminT(locale, "admin.export_domain_blocks.new.title", "Import domain blocks"), "/admin/export_domain_blocks/import", errorText, locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminDomainExportFormHTML(adminT(locale, "admin.export_domain_blocks.new.title", "Import domain blocks"), "/admin/export_domain_blocks/import", adminDomainExportBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	privateComment := adminTVars(locale, "admin.export_domain_blocks.import.private_comment_template", "Imported from %{source} on %{date}", map[string]string{
		"source": filename,
		"date":   time.Now().UTC().Format(time.RFC3339),
	})
	forms := make([]adminDomainBlockForm, 0, len(rows))
	invalids := make([]string, 0)
	for _, row := range rows {
		domain := normalizeDomain(row["#domain"])
		if domain == "" {
			continue
		}
		severity := strings.TrimSpace(row["#severity"])
		if severity == "" {
			severity = "suspend"
		}
		if !adminDomainBlockSeverityAllowed(severity) {
			invalids = append(invalids, domain+": invalid severity")
			continue
		}
		existing, err := s.existingDomainBlockForDomain(domain)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		forms = append(forms, adminDomainBlockForm{
			Domain:         domain,
			Severity:       severity,
			RejectMedia:    adminCSVBool(row["#reject_media"]),
			RejectReports:  adminCSVBool(row["#reject_reports"]),
			PrivateComment: privateComment,
			PublicComment:  strings.TrimSpace(row["#public_comment"]),
			Obfuscate:      adminCSVBool(row["#obfuscate"]),
		})
	}
	warningDomains, err := s.adminDomainBlockImportWarningDomains(forms)
	if err != nil {
		return err
	}
	errorText := ""
	if len(invalids) > 0 {
		errorText = adminTVars(locale, "admin.export_domain_blocks.invalid_domain_block", "One or more domain blocks were skipped because of the following error(s): %{error}", map[string]string{"error": strings.Join(invalids, ", ")})
	}
	return c.HTML(http.StatusOK, adminDomainBlockImportPreviewHTML(forms, warningDomains, privateComment, errorText, locale))
}

func parseAdminDomainImportUpload(c *echo.Context) ([]map[string]string, error) {
	rows, _, err := parseAdminDomainImportUploadWithName(c)
	return rows, err
}

func adminDomainExportAllowMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.export_domain_allows."+key, fallback)
}

func adminDomainExportBlockMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.export_domain_blocks."+key, fallback)
}

func parseAdminDomainImportUploadWithName(c *echo.Context) ([]map[string]string, string, error) {
	file, err := c.FormFile("admin_import[data]")
	if err != nil {
		return nil, "", errors.New("No file selected")
	}
	opened, err := file.Open()
	if err != nil {
		return nil, "", err
	}
	defer opened.Close()
	rows, err := parseAdminDomainCSVRows(opened)
	return rows, firstNonEmpty(file.Filename, "domain_blocks.csv"), err
}

func parseAdminDomainCSVRows(r io.Reader) ([]map[string]string, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	first, err := readAdminDomainCSVRecord(reader)
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	headers := []string{"#domain"}
	rows := make([]map[string]string, 0)
	if len(first) > 0 && strings.TrimSpace(first[0]) == "#domain" {
		headers = trimCSVRow(first)
	} else {
		rows = append(rows, adminDomainCSVRow(headers, first))
	}
	for len(rows) <= 20000 {
		record, err := readAdminDomainCSVRecord(reader)
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, adminDomainCSVRow(headers, record))
	}
	return nil, errors.New("CSV contains too many rows")
}

func readAdminDomainCSVRecord(reader *csv.Reader) ([]string, error) {
	for {
		record, err := reader.Read()
		if err != nil {
			return nil, err
		}
		if !adminDomainCSVBlankRecord(record) {
			return record, nil
		}
	}
}

func adminDomainCSVBlankRecord(record []string) bool {
	return len(record) == 1 && strings.TrimSpace(record[0]) == ""
}

func trimCSVRow(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

func adminDomainCSVRow(headers []string, values []string) map[string]string {
	row := map[string]string{}
	for index, header := range headers {
		if index < len(values) {
			row[header] = strings.TrimSpace(values[index])
		}
	}
	return row
}

func boolCSVValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func adminCSVBool(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "0", "f", "false", "off":
		return false
	default:
		return true
	}
}

func adminDomainExportFormHTML(title string, action string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	body := `<form method="post" action="` + html.EscapeString(action) + `" enctype="multipart/form-data" class="simple_form new_admin_import">
	<div class="fields-row"><div class="fields-group fields-row__column fields-row__column-6"><div class="input file optional admin_import_data"><div class="label_input"><label for="admin_import_data">CSV</label><input class="file optional" id="admin_import_data" type="file" name="admin_import[data]" accept=".csv,text/csv"><span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.imports.data", "CSV file exported from another server")) + `</span></div></div></div></div>
	<div class="actions"><button class="button" type="submit">` + html.EscapeString(settingsT(loc, "imports.upload", "Upload")) + `</button></div>
</form>`
	return authPageHTML(title, "", errorText, body, loc)
}

func (s *Server) adminDomainBlockImportWarningDomains(forms []adminDomainBlockForm) (map[string]bool, error) {
	out := map[string]bool{}
	if s == nil || s.db == nil || len(forms) == 0 {
		return out, nil
	}
	domains := make([]string, 0, len(forms))
	for _, form := range forms {
		if form.Domain != "" {
			domains = append(domains, form.Domain)
		}
	}
	if len(domains) == 0 {
		return out, nil
	}
	var rows []string
	err := s.db.Model(&models.Instance{}).
		Where("instances.domain IN ?", domains).
		Where("EXISTS (SELECT 1 FROM follows JOIN accounts ON follows.account_id = accounts.id OR follows.target_account_id = accounts.id WHERE accounts.domain = instances.domain)").
		Pluck("instances.domain", &rows).Error
	if err != nil {
		return nil, err
	}
	for _, domain := range rows {
		out[domain] = true
	}
	return out, nil
}

func adminDomainBlockImportPreviewHTML(forms []adminDomainBlockForm, warningDomains map[string]bool, privateComment string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<p>` + adminT(loc, "admin.export_domain_blocks.import.description_html", "You are about to import a list of domain blocks. Please review this list very carefully, especially if you have not authored this list yourself.") + `</p>`)
	if strings.TrimSpace(privateComment) != "" {
		body.WriteString(`<p>` + adminTVars(loc, "admin.export_domain_blocks.import.private_comment_description_html", "To help you track where imported blocks come from, imported blocks will be created with the following private comment: <q>%{comment}</q>", map[string]string{"comment": html.EscapeString(privateComment)}) + `</p>`)
	}
	body.WriteString(`<form method="post" action="/admin/domain_blocks/batch" class="new_form_domain_block_batch"><div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button type="submit" name="save" value="1" class="table-action-link" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-copy"></i> ` + html.EscapeString(adminT(loc, "admin.domain_blocks.import", "Import")) + `</button></div></div><div class="batch-table__body">`)
	if len(forms) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for index, form := range forms {
			body.WriteString(adminDomainBlockImportPreviewRowHTML(index, form, warningDomains[form.Domain], loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	return authPageHTML(adminT(loc, "admin.export_domain_blocks.import.title", "Import domain blocks"), "", errorText, body.String(), loc)
}

func adminDomainBlockImportPreviewRowHTML(index int, form adminDomainBlockForm, existingRelationships bool, locale string) string {
	prefix := `form_domain_block_batch[domain_blocks_attributes][` + strconv.Itoa(index) + `]`
	checked := ` checked`
	rowClass := `batch-table__row`
	if existingRelationships {
		checked = ``
		rowClass += ` batch-table__row--attention`
	}
	var body strings.Builder
	body.WriteString(`<div class="` + rowClass + `"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="` + prefix + `[enabled]" value="1"` + checked + `></label><div class="batch-table__row__content pending-account"><div class="pending-account__header"><strong>` + html.EscapeString(form.Domain) + `</strong>`)
	body.WriteString(adminDomainBlockImportHidden(prefix, "domain", form.Domain))
	body.WriteString(adminDomainBlockImportHidden(prefix, "severity", form.Severity))
	body.WriteString(adminDomainBlockImportHidden(prefix, "reject_media", boolCSVValue(form.RejectMedia)))
	body.WriteString(adminDomainBlockImportHidden(prefix, "reject_reports", boolCSVValue(form.RejectReports)))
	body.WriteString(adminDomainBlockImportHidden(prefix, "obfuscate", boolCSVValue(form.Obfuscate)))
	body.WriteString(adminDomainBlockImportHidden(prefix, "private_comment", form.PrivateComment))
	body.WriteString(adminDomainBlockImportHidden(prefix, "public_comment", form.PublicComment))
	body.WriteString(`<br>` + html.EscapeString(adminDomainBlockPolicySummary(form, locale)))
	if strings.TrimSpace(form.PublicComment) != "" {
		body.WriteString(` &middot; ` + html.EscapeString(form.PublicComment))
	}
	if existingRelationships {
		body.WriteString(` &middot; ` + html.EscapeString(adminT(locale, "admin.export_domain_blocks.import.existing_relationships_warning", "Existing follow relationships")))
	}
	body.WriteString(`</div></div></div>`)
	return body.String()
}

func adminDomainBlockImportHidden(prefix string, key string, value string) string {
	return `<input type="hidden" name="` + prefix + `[` + key + `]" value="` + html.EscapeString(value) + `">`
}

func adminDomainBlockPolicySummary(form adminDomainBlockForm, locale string) string {
	if form.Severity == "suspend" {
		return adminT(locale, "admin.instances.content_policies.policies.suspend", "Suspend")
	}
	parts := make([]string, 0, 3)
	if form.Severity == "silence" {
		parts = append(parts, adminT(locale, "admin.instances.content_policies.policies.silence", "Limit"))
	}
	if form.RejectMedia {
		parts = append(parts, adminT(locale, "admin.instances.content_policies.policies.reject_media", "Reject media files"))
	}
	if form.RejectReports {
		parts = append(parts, adminT(locale, "admin.instances.content_policies.policies.reject_reports", "Reject reports"))
	}
	if len(parts) == 0 {
		return adminT(locale, "admin.instances.content_policies.policies.noop", "None")
	}
	return strings.Join(parts, " · ")
}
