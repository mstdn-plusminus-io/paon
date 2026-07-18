package api

import (
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

type adminIPBlockForm struct {
	IP        string
	Severity  string
	Comment   string
	ExpiresIn string
}

var errAdminIPBlockParamsMissing = errors.New("admin IP block root parameter is missing")

func (s *Server) adminIPBlocksPage(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	rows, err := s.adminIPBlockModels(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminIPBlocksHTML(rows, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) newAdminIPBlockPage(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminIPBlockFormHTML(adminIPBlockForm{Severity: "no_access", ExpiresIn: "31536000"}, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminIPBlockWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminIPBlockForm(c)
	if err != nil {
		if errors.Is(err, errAdminIPBlockParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		form = adminIPBlockForm{Severity: "no_access", ExpiresIn: "31536000"}
		return c.HTML(http.StatusOK, adminIPBlockFormHTML(form, adminIPBlockMessage(locale, "errors.invalid", "IP block is invalid"), locale))
	}
	ip := normalizeIPBlock(form.IP)
	if ip == "" {
		return c.HTML(http.StatusOK, adminIPBlockFormHTML(form, adminIPBlockMessage(locale, "errors.ip_invalid", "IP is invalid"), locale))
	}
	severity, ok := ipBlockSeverityValue(form.Severity)
	if !ok {
		return c.HTML(http.StatusOK, adminIPBlockFormHTML(form, adminIPBlockMessage(locale, "errors.severity_invalid", "IP block severity is invalid"), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminIPBlockFormHTML(form, adminIPBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	now := time.Now().UTC()
	row := models.IPBlock{
		IP:        ip,
		Severity:  severity,
		Comment:   form.Comment,
		ExpiresAt: expiresAt(form.ExpiresIn),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return c.HTML(http.StatusOK, adminIPBlockFormHTML(form, adminIPBlockMessage(locale, "errors.taken", "IP has already been taken"), locale))
		}
		return err
	}
	s.invalidateIPBlockCache(c.Request().Context())
	if err := logAdminAction(s.db, user.AccountID, "create", ipBlockAuditLogTarget(row), now); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/ip_blocks?notice="+url.QueryEscape(adminIPBlockMessage(locale, "created_msg", "Successfully added new IP rule")))
}

func (s *Server) batchAdminIPBlocks(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if !adminBatchFormRootPresent(c, "form_ip_block_batch") {
		return c.Redirect(http.StatusFound, "/admin/ip_blocks?error="+url.QueryEscape(adminIPBlockMessage(locale, "no_ip_block_selected", "No IP rules were changed as none were selected")))
	}
	if !adminBatchFormParamExists(c, "delete") {
		return c.Redirect(http.StatusFound, "/admin/ip_blocks")
	}
	ids := parseAdminIPBlockIDs(c.Request().PostForm, c)
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, "/admin/ip_blocks?error="+url.QueryEscape(adminIPBlockMessage(locale, "no_ip_block_selected", "No IP rules were changed as none were selected")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/ip_blocks?error="+url.QueryEscape(adminIPBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	var rows []models.IPBlock
	if err := s.db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return err
	}
	if err := s.db.Where("id IN ?", ids).Delete(&models.IPBlock{}).Error; err != nil {
		return err
	}
	s.invalidateIPBlockCache(c.Request().Context())
	now := time.Now().UTC()
	for _, row := range rows {
		if err := logAdminAction(s.db, user.AccountID, "destroy", ipBlockAuditLogTarget(row), now); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/admin/ip_blocks")
}

func adminBatchFormParamExists(c *echo.Context, key string) bool {
	_ = c.Request().ParseForm()
	_, ok := c.Request().Form[key]
	return ok
}

func adminBatchFormRootPresent(c *echo.Context, root string) bool {
	_ = c.Request().ParseForm()
	return formHasNestedPrefix(c.Request().Form, root)
}

func adminIPBlockMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.ip_blocks."+key, fallback)
}

func (s *Server) requireAdminBlocksWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageBlocks) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.ip_blocks.title", "Admin blocks"), "", adminT(locale, "admin.ip_blocks.not_permitted", "You are not allowed to manage blocks."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminIPBlockModels(c *echo.Context) ([]models.IPBlock, error) {
	if s.db == nil {
		return []models.IPBlock{}, nil
	}
	var rows []models.IPBlock
	err := s.db.Order("ip ASC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&rows).Error
	return rows, err
}

func parseAdminIPBlockForm(c *echo.Context) (adminIPBlockForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminIPBlockForm{}, err
	}
	prefix := "ip_block"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminIPBlockForm{}, errAdminIPBlockParamsMissing
	}
	return adminIPBlockForm{
		IP:        strings.TrimSpace(lastFormValue(req.Form, "ip_block[ip]")),
		Severity:  strings.TrimSpace(lastFormValue(req.Form, "ip_block[severity]")),
		Comment:   lastFormValue(req.Form, "ip_block[comment]"),
		ExpiresIn: strings.TrimSpace(lastFormValue(req.Form, "ip_block[expires_in]")),
	}, nil
}

func parseAdminIPBlockIDs(_ url.Values, c *echo.Context) []int64 {
	_ = c.Request().ParseForm()
	values := c.Request().PostForm["form_ip_block_batch[ip_block_ids][]"]
	out := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func adminIPBlockSeverityAllowed(value string) bool {
	switch value {
	case "sign_up_requires_approval", "sign_up_block", "no_access":
		return true
	default:
		return false
	}
}

func adminIPBlockSeverityLabel(value int) string {
	switch value {
	case 5000:
		return "sign_up_requires_approval"
	case 9999:
		return "no_access"
	default:
		return "sign_up_block"
	}
}

func adminIPBlocksHTML(rows []models.IPBlock, notice string, errorText string, page string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><a class="button" href="/admin/ip_blocks/new">` + html.EscapeString(adminT(loc, "admin.ip_blocks.add_new", "Add IP block")) + `</a></div><form method="post" action="/admin/ip_blocks/batch" class="new_form_ip_block_batch"><input type="hidden" name="page" value="` + html.EscapeString(firstNonEmpty(page, "1")) + `"><div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button class="table-action-link" type="submit" name="delete" value="1" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.ip_blocks.delete", "Delete selected")) + `</button></div></div><div class="batch-table__body">`)
	if len(rows) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, row := range rows {
			comment := ""
			if strings.TrimSpace(row.Comment) != "" {
				comment = ` &middot; ` + html.EscapeString(row.Comment)
			}
			body.WriteString(`<div class="batch-table__row"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="form_ip_block_batch[ip_block_ids][]" value="` + strconv.FormatInt(row.ID, 10) + `"></label><div class="batch-table__row__content pending-account"><div class="pending-account__header"><samp>` + adminIPBlockAccountFilterLink(row.IP) + `</samp>` + comment + `<br>` + html.EscapeString(adminT(loc, "simple_form.labels.ip_block.severities."+adminIPBlockSeverityLabel(row.Severity), adminIPBlockSeverityLabel(row.Severity))) + `</div></div></div>`)
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminIPBlocksPaginationHTML(page, len(rows) == adminRailsDefaultPageSize, loc))
	return authPageHTML(adminT(loc, "admin.ip_blocks.title", "Admin IP blocks"), notice, errorText, body.String(), loc)
}

func adminIPBlockAccountFilterLink(ip string) string {
	label := normalizeIPBlock(ip)
	if label == "" {
		label = strings.TrimSpace(ip)
	}
	escapedLabel := html.EscapeString(label)
	if label == "" {
		return escapedLabel
	}
	return `<a href="/admin/accounts?ip=` + url.QueryEscape(label) + `">` + escapedLabel + `</a>`
}

func adminIPBlocksPaginationHTML(page string, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := url.Values{"page": []string{strconv.Itoa(pageNum - 1)}}
		links = append(links, `<a href="/admin/ip_blocks?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.previous", "Previous"))+`</a>`)
	}
	if hasNext {
		params := url.Values{"page": []string{strconv.Itoa(pageNum + 1)}}
		links = append(links, `<a href="/admin/ip_blocks?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminIPBlockFormHTML(form adminIPBlockForm, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	option := func(value string, label string) string {
		selected := ""
		if form.Severity == value {
			selected = ` selected`
		}
		return `<option value="` + value + `"` + selected + `>` + label + `</option>`
	}
	body := simpleFormOpen("/admin/ip_blocks", "post") +
		simpleTextInput(adminT(loc, "simple_form.labels.ip_block.ip", "IP"), "ip_block[ip]", form.IP, "text", `placeholder="192.0.2.0/24" required`) +
		`<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.ip_block.expires_in", "Expires in")) + `</label><select name="ip_block[expires_in]"><option value="">` + html.EscapeString(adminT(loc, "admin.ip_blocks.expires_in.never", "Never")) + `</option><option value="86400">1 day</option><option value="1209600">2 weeks</option><option value="2592000">1 month</option><option value="15552000">6 months</option><option value="31536000" selected>1 year</option><option value="94608000">3 years</option></select></div></div></div>` +
		`<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.ip_block.severity", "Severity")) + `</label><select name="ip_block[severity]">` +
		option("sign_up_requires_approval", adminT(loc, "admin.ip_blocks.severities.sign_up_requires_approval", "Require sign-up approval")) +
		option("sign_up_block", adminT(loc, "admin.ip_blocks.severities.sign_up_block", "Block sign-ups")) +
		option("no_access", adminT(loc, "admin.ip_blocks.severities.no_access", "No access")) +
		`</select></div></div></div>` +
		simpleTextInput(adminT(loc, "simple_form.labels.ip_block.comment", "Comment"), "ip_block[comment]", form.Comment, "text", "") +
		simpleSubmit(adminT(loc, "admin.ip_blocks.add_new", "Add IP block")) +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.ip_blocks.add_new", "Add IP block"), "", errorText, body, loc)
}

func adminIPBlockExpiresLabel(row models.IPBlock) string {
	if !row.ExpiresAt.Valid {
		return "never"
	}
	return row.ExpiresAt.Time.Format(time.RFC3339)
}
