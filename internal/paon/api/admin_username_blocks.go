package api

import (
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

type adminUsernameBlockForm struct {
	Username          string
	Comparison        string
	AllowWithApproval bool
}

var (
	errAdminUsernameBlockParamsMissing = errors.New("admin username block root parameter is missing")
	adminUsernameBlockPattern          = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

func (s *Server) adminUsernameBlocksPage(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	rows := []models.UsernameBlock{}
	if s.db != nil {
		err = s.db.Order("username ASC").Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&rows).Error
		if err != nil {
			return err
		}
	}
	return c.HTML(http.StatusOK, adminUsernameBlocksHTML(rows, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) newAdminUsernameBlockPage(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminUsernameBlockFormHTML(0, adminUsernameBlockForm{Comparison: "equals"}, "", s.webLocale(c, user)))
}

func (s *Server) editAdminUsernameBlockPage(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	var row models.UsernameBlock
	if s.db == nil || s.db.Where("id = ?", c.Param("id")).First(&row).Error != nil {
		return s.notFound(c)
	}
	comparison := "contains"
	if row.Exact {
		comparison = "equals"
	}
	return c.HTML(http.StatusOK, adminUsernameBlockFormHTML(row.ID, adminUsernameBlockForm{Username: row.Username, Comparison: comparison, AllowWithApproval: row.AllowWithApproval}, "", s.webLocale(c, user)))
}

func parseAdminUsernameBlockForm(c *echo.Context) (adminUsernameBlockForm, error) {
	if err := c.Request().ParseForm(); err != nil {
		return adminUsernameBlockForm{}, err
	}
	if !formHasNestedPrefix(c.Request().Form, "username_block") {
		return adminUsernameBlockForm{}, errAdminUsernameBlockParamsMissing
	}
	return adminUsernameBlockForm{
		Username:          strings.TrimSpace(lastFormValue(c.Request().Form, "username_block[username]")),
		Comparison:        strings.TrimSpace(lastFormValue(c.Request().Form, "username_block[comparison]")),
		AllowWithApproval: adminSettingsCheckbox(c.Request().Form, "username_block[allow_with_approval]"),
	}, nil
}

func validateAdminUsernameBlockForm(form adminUsernameBlockForm) error {
	if form.Username == "" || len(form.Username) > 30 || !adminUsernameBlockPattern.MatchString(form.Username) {
		return errAdminSetting("Username is invalid")
	}
	if form.Comparison != "equals" && form.Comparison != "contains" {
		return errAdminSetting("Comparison is invalid")
	}
	return nil
}

func (s *Server) createAdminUsernameBlockWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminUsernameBlockForm(c)
	if err != nil {
		if errors.Is(err, errAdminUsernameBlockParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return err
	}
	if err := validateAdminUsernameBlockForm(form); err != nil {
		return c.HTML(http.StatusOK, adminUsernameBlockFormHTML(0, form, err.Error(), locale))
	}
	now := time.Now().UTC()
	row := models.UsernameBlock{Username: form.Username, NormalizedUsername: normalizeBlockedUsername(form.Username), Exact: form.Comparison == "equals", AllowWithApproval: form.AllowWithApproval, CreatedAt: now, UpdatedAt: now}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "create", usernameBlockAuditLogTarget(row), now)
	})
	if err != nil {
		if isUniqueConstraintError(err) {
			return c.HTML(http.StatusOK, adminUsernameBlockFormHTML(0, form, adminUsernameBlockMessage(locale, "errors.taken", "Username has already been taken"), locale))
		}
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/username_blocks?notice="+url.QueryEscape(adminUsernameBlockMessage(locale, "created_msg", "Successfully added username rule")))
}

func (s *Server) updateAdminUsernameBlockWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminUsernameBlockForm(c)
	if err != nil {
		if errors.Is(err, errAdminUsernameBlockParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return err
	}
	id := railsToInt64(c.Param("id"))
	if err := validateAdminUsernameBlockForm(form); err != nil {
		return c.HTML(http.StatusOK, adminUsernameBlockFormHTML(id, form, err.Error(), locale))
	}
	var row models.UsernameBlock
	if id == 0 || s.db.Where("id = ?", id).First(&row).Error != nil {
		return s.notFound(c)
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&row).Updates(map[string]any{"username": form.Username, "normalized_username": normalizeBlockedUsername(form.Username), "exact": form.Comparison == "equals", "allow_with_approval": form.AllowWithApproval, "updated_at": now}).Error; err != nil {
			return err
		}
		row.Username, row.NormalizedUsername, row.Exact, row.AllowWithApproval = form.Username, normalizeBlockedUsername(form.Username), form.Comparison == "equals", form.AllowWithApproval
		return logAdminAction(tx, user.AccountID, "update", usernameBlockAuditLogTarget(row), now)
	})
	if err != nil {
		if isUniqueConstraintError(err) {
			return c.HTML(http.StatusOK, adminUsernameBlockFormHTML(id, form, adminUsernameBlockMessage(locale, "errors.taken", "Username has already been taken"), locale))
		}
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/username_blocks?notice="+url.QueryEscape(adminUsernameBlockMessage(locale, "updated_msg", "Successfully updated username rule")))
}

func (s *Server) batchAdminUsernameBlocks(c *echo.Context) error {
	user, handled, err := s.requireAdminBlocksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	_ = c.Request().ParseForm()
	values, rootPresent := c.Request().PostForm["form_username_block_batch[username_block_ids][]"]
	if !rootPresent || !adminBatchFormParamExists(c, "delete") {
		return c.Redirect(http.StatusFound, "/admin/username_blocks?error="+url.QueryEscape(adminUsernameBlockMessage(locale, "no_username_block_selected", "No username rules were selected")))
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		if id := railsToInt64(value); id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, "/admin/username_blocks?error="+url.QueryEscape(adminUsernameBlockMessage(locale, "no_username_block_selected", "No username rules were selected")))
	}
	var rows []models.UsernameBlock
	if err := s.db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id IN ?", ids).Delete(&models.UsernameBlock{}).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := logAdminAction(tx, user.AccountID, "destroy", usernameBlockAuditLogTarget(row), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/username_blocks")
}

func adminUsernameBlockMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.username_blocks."+key, fallback)
}

func adminUsernameBlocksHTML(rows []models.UsernameBlock, notice, errorText, page string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><a class="button" href="/admin/username_blocks/new">` + html.EscapeString(adminUsernameBlockMessage(loc, "add_new", "Add username rule")) + `</a></div><form method="post" action="/admin/username_blocks/batch"><div class="batch-table"><div class="batch-table__toolbar"><button class="table-action-link" type="submit" name="delete" value="1">` + html.EscapeString(adminUsernameBlockMessage(loc, "delete", "Delete selected")) + `</button></div><div class="batch-table__body">`)
	if len(rows) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	}
	for _, row := range rows {
		comparison := adminUsernameBlockMessage(loc, "comparison.contains", "contains")
		if row.Exact {
			comparison = adminUsernameBlockMessage(loc, "comparison.equals", "equals")
		}
		approval := ""
		if row.AllowWithApproval {
			approval = ` &middot; ` + html.EscapeString(adminUsernameBlockMessage(loc, "allow_with_approval", "Allow with approval"))
		}
		body.WriteString(`<div class="batch-table__row"><label class="batch-table__row__select"><input type="checkbox" name="form_username_block_batch[username_block_ids][]" value="` + strconv.FormatInt(row.ID, 10) + `"></label><div class="batch-table__row__content"><a href="/admin/username_blocks/` + strconv.FormatInt(row.ID, 10) + `/edit"><strong>` + html.EscapeString(row.Username) + `</strong></a> &middot; ` + html.EscapeString(comparison) + approval + `</div></div>`)
	}
	body.WriteString(`</div></div></form>` + adminIPBlocksPaginationHTML(page, len(rows) == adminRailsDefaultPageSize, loc))
	return authPageHTML(adminUsernameBlockMessage(loc, "title", "Username blocks"), notice, errorText, body.String(), loc)
}

func adminUsernameBlockFormHTML(id int64, form adminUsernameBlockForm, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	action := "/admin/username_blocks"
	method := ""
	if id > 0 {
		action += "/" + strconv.FormatInt(id, 10)
		method = `<input type="hidden" name="_method" value="patch">`
	}
	containsSelected, equalsSelected := "", ""
	if form.Comparison == "contains" {
		containsSelected = " selected"
	} else {
		equalsSelected = " selected"
	}
	checked := ""
	if form.AllowWithApproval {
		checked = " checked"
	}
	body := `<form method="post" action="` + action + `" class="simple_form">` + method + `<div class="fields-group"><label>` + html.EscapeString(adminUsernameBlockMessage(loc, "username", "Username")) + ` <input name="username_block[username]" value="` + html.EscapeString(form.Username) + `" pattern="[a-zA-Z0-9_]+" maxlength="30" autocomplete="new-password"></label></div><div class="fields-group"><label>` + html.EscapeString(adminUsernameBlockMessage(loc, "comparison.title", "Comparison")) + ` <select name="username_block[comparison]"><option value="equals"` + equalsSelected + `>` + html.EscapeString(adminUsernameBlockMessage(loc, "comparison.equals", "equals")) + `</option><option value="contains"` + containsSelected + `>` + html.EscapeString(adminUsernameBlockMessage(loc, "comparison.contains", "contains")) + `</option></select></label></div><div class="fields-group"><input type="hidden" name="username_block[allow_with_approval]" value="0"><label><input type="checkbox" name="username_block[allow_with_approval]" value="1"` + checked + `> ` + html.EscapeString(adminUsernameBlockMessage(loc, "allow_with_approval", "Allow registrations with approval")) + `</label></div><button type="submit">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></form>`
	return authPageHTML(adminUsernameBlockMessage(loc, "title", "Username blocks"), "", errorText, body, loc)
}
