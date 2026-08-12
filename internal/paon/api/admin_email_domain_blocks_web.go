package api

import (
	"database/sql"
	"errors"
	"html"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type adminEmailDomainBlockForm struct {
	Domain            string
	OtherDomains      []string
	Resolved          bool
	AllowWithApproval bool
}

var lookupEmailDomainBlockMX = net.LookupMX

var errAdminEmailDomainBlockParamsMissing = errors.New("admin e-mail domain block root parameter is missing")

func (s *Server) adminEmailDomainBlocksPage(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	rows, err := s.adminEmailDomainBlockModels(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminEmailDomainBlocksHTML(rows, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) newAdminEmailDomainBlockPage(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminEmailDomainBlockFormHTML(adminEmailDomainBlockForm{Domain: c.QueryParam("_domain")}, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminEmailDomainBlockWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminEmailDomainBlockForm(c)
	if err != nil {
		if errors.Is(err, errAdminEmailDomainBlockParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminEmailDomainBlockFormHTML(form, adminEmailDomainBlockMessage(locale, "errors.invalid", "E-mail domain block is invalid"), locale))
	}
	domain := normalizeDomain(form.Domain)
	if domain == "" {
		return c.HTML(http.StatusOK, adminEmailDomainBlockFormHTML(form, adminEmailDomainBlockMessage(locale, "errors.domain_invalid", "Domain is invalid"), locale))
	}
	if !adminBatchFormParamExists(c, "save") {
		form.Domain = domain
		form.OtherDomains = resolveEmailDomainBlockMXDomains(domain)
		form.Resolved = true
		return c.HTML(http.StatusOK, adminEmailDomainBlockFormHTML(form, "", locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminEmailDomainBlockFormHTML(form, adminEmailDomainBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	rows, err := s.insertAdminEmailDomainBlocks(domain, form.OtherDomains, form.AllowWithApproval)
	if err != nil {
		if isUniqueConstraintError(err) {
			return c.HTML(http.StatusOK, adminEmailDomainBlockFormHTML(form, adminEmailDomainBlockMessage(locale, "errors.taken", "Domain has already been taken"), locale))
		}
		return err
	}
	for _, row := range rows {
		if err := logAdminAction(s.db, user.AccountID, "create", emailDomainBlockAuditLogTarget(row), row.CreatedAt); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/admin/email_domain_blocks?notice="+url.QueryEscape(adminEmailDomainBlockMessage(locale, "created_msg", "Successfully blocked e-mail domain")))
}

func (s *Server) batchAdminEmailDomainBlocks(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if !adminBatchFormRootPresent(c, "form_email_domain_block_batch") {
		return c.Redirect(http.StatusFound, "/admin/email_domain_blocks?error="+url.QueryEscape(adminEmailDomainBlockMessage(locale, "no_email_domain_block_selected", "No e-mail domain blocks were changed as none were selected")))
	}
	if !adminBatchFormParamExists(c, "delete") {
		return c.Redirect(http.StatusFound, "/admin/email_domain_blocks")
	}
	ids := parseAdminEmailDomainBlockIDs(c)
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, "/admin/email_domain_blocks?error="+url.QueryEscape(adminEmailDomainBlockMessage(locale, "no_email_domain_block_selected", "No e-mail domain blocks were changed as none were selected")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/email_domain_blocks?error="+url.QueryEscape(adminEmailDomainBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	var rows []models.EmailDomainBlock
	if err := s.db.Where("id IN ? OR parent_id IN ?", ids, ids).Find(&rows).Error; err != nil {
		return err
	}
	if err := s.db.Where("id IN ? OR parent_id IN ?", ids, ids).Delete(&models.EmailDomainBlock{}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, row := range rows {
		if err := logAdminAction(s.db, user.AccountID, "destroy", emailDomainBlockAuditLogTarget(row), now); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/admin/email_domain_blocks")
}

func adminEmailDomainBlockMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.email_domain_blocks."+key, fallback)
}

func (s *Server) adminEmailDomainBlockModels(c *echo.Context) ([]models.EmailDomainBlock, error) {
	if s.db == nil {
		return []models.EmailDomainBlock{}, nil
	}
	var parents []models.EmailDomainBlock
	if err := s.db.Where("parent_id IS NULL").
		Order("id DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&parents).Error; err != nil {
		return nil, err
	}
	if len(parents) == 0 {
		return parents, nil
	}
	parentIDs := make([]int64, 0, len(parents))
	for _, parent := range parents {
		parentIDs = append(parentIDs, parent.ID)
	}
	var children []models.EmailDomainBlock
	if err := s.db.Where("parent_id IN ?", parentIDs).Order("parent_id DESC, id ASC").Find(&children).Error; err != nil {
		return nil, err
	}
	return adminEmailDomainBlockRowsWithChildren(parents, children), nil
}

func adminEmailDomainBlockRowsWithChildren(parents []models.EmailDomainBlock, children []models.EmailDomainBlock) []models.EmailDomainBlock {
	childrenByParent := make(map[int64][]models.EmailDomainBlock, len(parents))
	for _, child := range children {
		if !child.ParentID.Valid {
			continue
		}
		childrenByParent[child.ParentID.Int64] = append(childrenByParent[child.ParentID.Int64], child)
	}
	rows := make([]models.EmailDomainBlock, 0, len(parents)+len(children))
	for _, parent := range parents {
		rows = append(rows, parent)
		rows = append(rows, childrenByParent[parent.ID]...)
	}
	return rows
}

func parseAdminEmailDomainBlockForm(c *echo.Context) (adminEmailDomainBlockForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminEmailDomainBlockForm{}, err
	}
	prefix := "email_domain_block"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminEmailDomainBlockForm{}, errAdminEmailDomainBlockParamsMissing
	}
	return adminEmailDomainBlockForm{
		Domain:            strings.TrimSpace(lastFormValue(req.Form, "email_domain_block[domain]")),
		OtherDomains:      normalizeAdminEmailDomainBlockDomains(req.Form["email_domain_block[other_domains][]"]),
		AllowWithApproval: truthy(lastFormValue(req.Form, "email_domain_block[allow_with_approval]")),
	}, nil
}

func parseAdminEmailDomainBlockIDs(c *echo.Context) []int64 {
	_ = c.Request().ParseForm()
	values := c.Request().PostForm["form_email_domain_block_batch[email_domain_block_ids][]"]
	out := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func normalizeAdminEmailDomainBlockDomains(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == ';' }) {
			domain := normalizeDomain(part)
			if domain == "" {
				continue
			}
			if _, ok := seen[domain]; ok {
				continue
			}
			seen[domain] = struct{}{}
			out = append(out, domain)
		}
	}
	return out
}

func resolveEmailDomainBlockMXDomains(domain string) []string {
	records, err := lookupEmailDomainBlockMX(domain)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(records))
	for _, record := range records {
		values = append(values, strings.TrimSuffix(record.Host, "."))
	}
	values = normalizeAdminEmailDomainBlockDomains(values)
	sort.Strings(values)
	return values
}

func (s *Server) insertAdminEmailDomainBlocks(domain string, otherDomains []string, allowWithApproval ...bool) ([]models.EmailDomainBlock, error) {
	now := time.Now().UTC()
	approval := len(allowWithApproval) > 0 && allowWithApproval[0]
	created := make([]models.EmailDomainBlock, 0, 1+len(otherDomains))
	err := s.db.Transaction(func(tx *gorm.DB) error {
		parent := models.EmailDomainBlock{Domain: domain, AllowWithApproval: approval, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&parent).Error; err != nil {
			return err
		}
		created = append(created, parent)
		for _, other := range otherDomains {
			if other == domain {
				continue
			}
			child := models.EmailDomainBlock{
				Domain:            other,
				ParentID:          sql.NullInt64{Int64: parent.ID, Valid: true},
				AllowWithApproval: approval,
				CreatedAt:         now,
				UpdatedAt:         now,
			}
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "domain"}}, DoNothing: true}).Create(&child)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			created = append(created, child)
		}
		return nil
	})
	return created, err
}

func adminEmailDomainBlocksHTML(rows []models.EmailDomainBlock, notice string, errorText string, page string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><a class="button" href="/admin/email_domain_blocks/new">` + html.EscapeString(adminT(loc, "admin.email_domain_blocks.add_new", "Add e-mail domain block")) + `</a></div><form method="post" action="/admin/email_domain_blocks/batch" class="new_form_email_domain_block_batch"><input type="hidden" name="page" value="` + html.EscapeString(firstNonEmpty(page, "1")) + `"><div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button class="table-action-link" type="submit" name="delete" value="1" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.email_domain_blocks.delete", "Delete selected")) + `</button></div></div><div class="batch-table__body">`)
	if len(rows) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, row := range rows {
			parent := ""
			if row.ParentID.Valid {
				parent = `<br>` + html.EscapeString(adminTVars(loc, "admin.email_domain_blocks.resolved_through_html", "Resolved through %{domain}", map[string]string{"domain": "#" + strconv.FormatInt(row.ParentID.Int64, 10)}))
			}
			emailQuery := url.Values{"email": []string{"%@" + row.Domain}}
			approval := ""
			if row.AllowWithApproval {
				approval = ` <span class="positive">` + html.EscapeString(adminT(loc, "admin.email_domain_blocks.allow_with_approval", "Allow with approval")) + `</span>`
			}
			body.WriteString(`<div class="batch-table__row"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="form_email_domain_block_batch[email_domain_block_ids][]" value="` + strconv.FormatInt(row.ID, 10) + `"></label><div class="batch-table__row__content pending-account"><div class="pending-account__header"><samp><a href="/admin/accounts?` + html.EscapeString(emailQuery.Encode()) + `">` + html.EscapeString(row.Domain) + `</a></samp>` + approval + parent + `</div></div></div>`)
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminEmailDomainBlocksPaginationHTML(page, adminEmailDomainBlockParentCount(rows) == adminRailsDefaultPageSize, loc))
	return authPageHTML(adminT(loc, "admin.email_domain_blocks.title", "Admin e-mail domain blocks"), notice, errorText, body.String(), loc)
}

func adminEmailDomainBlockParentCount(rows []models.EmailDomainBlock) int {
	count := 0
	for _, row := range rows {
		if !row.ParentID.Valid {
			count++
		}
	}
	return count
}

func adminEmailDomainBlocksPaginationHTML(page string, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := url.Values{"page": []string{strconv.Itoa(pageNum - 1)}}
		links = append(links, `<a href="/admin/email_domain_blocks?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.previous", "Previous"))+`</a>`)
	}
	if hasNext {
		params := url.Values{"page": []string{strconv.Itoa(pageNum + 1)}}
		links = append(links, `<a href="/admin/email_domain_blocks?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminEmailDomainBlockFormHTML(form adminEmailDomainBlockForm, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	readonly := ""
	buttonName := "resolve"
	buttonLabel := adminT(loc, "admin.email_domain_blocks.resolve", "Resolve MX records")
	if form.Resolved {
		readonly = ` readonly`
		buttonName = "save"
		buttonLabel = adminT(loc, "admin.email_domain_blocks.create_block", "Create block")
	}
	body := simpleFormOpen("/admin/email_domain_blocks", "post") +
		simpleTextInput(adminT(loc, "simple_form.labels.email_domain_block.domain", "Domain"), "email_domain_block[domain]", form.Domain, "text", readonly+` required`)
	checked := ""
	if form.AllowWithApproval {
		checked = ` checked`
	}
	body += `<div class="fields-group"><label class="boolean"><input type="checkbox" name="email_domain_block[allow_with_approval]" value="1"` + checked + `> ` + html.EscapeString(adminT(loc, "simple_form.labels.email_domain_block.allow_with_approval", "Allow registrations but require manual approval")) + `</label></div>`
	if form.Resolved {
		if len(form.OtherDomains) == 0 {
			body += `<p class="lead">` + html.EscapeString(adminT(loc, "admin.email_domain_blocks.no_mx_records", "No MX records were resolved for this domain. You can still create the parent block.")) + `</p>`
		} else {
			body += `<fieldset><legend>` + html.EscapeString(adminT(loc, "admin.email_domain_blocks.resolved_mx_records", "Resolved MX records")) + `</legend>`
			for _, domain := range form.OtherDomains {
				escaped := html.EscapeString(domain)
				body += `<div class="fields-group"><div class="input boolean with_label"><label class="boolean"><input type="checkbox" name="email_domain_block[other_domains][]" value="` + escaped + `" checked> ` + escaped + `</label></div></div>`
			}
			body += `</fieldset>`
		}
	} else {
		body += `<div class="fields-group"><div class="input with_block_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.email_domain_block.other_domains", "Other domains")) + `</label><textarea name="email_domain_block[other_domains][]" rows="4">` + html.EscapeString(strings.Join(form.OtherDomains, "\n")) + `</textarea></div></div></div>`
	}
	body += `<div class="actions"><button type="submit" name="` + buttonName + `" value="1" class="button">` + html.EscapeString(buttonLabel) + `</button></div>` +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.email_domain_blocks.add_new", "Add e-mail domain block"), "", errorText, body, loc)
}
