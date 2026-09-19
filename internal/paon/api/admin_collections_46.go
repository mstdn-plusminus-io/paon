package api

import (
	"database/sql"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const adminCollectionsPageSize = 20

func (s *Server) requireAdminCollectionsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCanAny(user, rolePermissionManageReports, rolePermissionManageUsers) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.collections.title", "Collections"), "", adminT(locale, "admin.collections.not_permitted", "You are not allowed to view collections."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminCollectionAccount(rawID string) (*models.Account, error) {
	if s == nil || s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var account models.Account
	if err := s.db.Where("id = ?", id).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *Server) adminAccountCollectionsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminCollectionsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.adminCollectionAccount(c.Param("account_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	pageText := adminTrendsPageValue(c)
	page, _ := strconv.Atoi(pageText)
	if page <= 0 {
		page = 1
	}
	var collections []models.Collection
	if err := s.db.Where("account_id = ?", account.ID).Order("id DESC").Offset(adminPageOffset(c, adminCollectionsPageSize)).Limit(adminCollectionsPageSize).Find(&collections).Error; err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminCollectionsIndexHTML(*account, collections, page, c.QueryParam("report_id"), c.QueryParam("error"), s.webLocale(c, user)))
}

func adminCollectionsIndexHTML(account models.Account, collections []models.Collection, page int, reportID string, errorText string, locale string) string {
	accountID := strconv.FormatInt(account.ID, 10)
	var body strings.Builder
	if errorText != "" {
		body.WriteString(`<p class="flash-message alert">` + html.EscapeString(errorText) + `</p>`)
	}
	back := "/admin/accounts/" + accountID
	if id, err := strconv.ParseInt(reportID, 10, 64); err == nil && id > 0 {
		back = "/admin/reports/" + strconv.FormatInt(id, 10)
	}
	body.WriteString(`<p class="back-link"><a href="` + html.EscapeString(back) + `">` + html.EscapeString(adminT(locale, "admin.collections.back_to_account", "Back")) + `</a></p>`)
	body.WriteString(`<form method="post" action="/admin/accounts/` + accountID + `/collections/batch"><input type="hidden" name="page" value="` + strconv.Itoa(page) + `"><input type="hidden" name="report_id" value="` + html.EscapeString(reportID) + `"><div class="batch-table"><div class="batch-table__toolbar"><button class="table-action-link" type="submit" name="report" value="1">` + html.EscapeString(adminT(locale, "admin.collections.batch.report", "Report")) + `</button></div><div class="batch-table__body">`)
	if len(collections) == 0 {
		body.WriteString(`<div class="empty-state">` + html.EscapeString(adminT(locale, "admin.collections.empty", "No collections found")) + `</div>`)
	}
	for _, collection := range collections {
		id := strconv.FormatInt(collection.ID, 10)
		body.WriteString(`<div class="batch-table__row"><label class="batch-table__row__select"><input type="checkbox" name="admin_collection_batch_action[collection_ids][]" value="` + id + `"></label><div class="batch-table__row__content"><a href="/admin/accounts/` + accountID + `/collections/` + id + `"><strong>` + html.EscapeString(collection.Name) + `</strong></a><br>` + html.EscapeString(collection.Description.String) + `</div></div>`)
	}
	body.WriteString(`</div></div></form>`)
	filters := url.Values{}
	if reportID != "" {
		filters.Set("report_id", reportID)
	}
	links := []string{}
	if page > 1 {
		links = append(links, `<a rel="prev" href="`+html.EscapeString(adminRailsPaginationURL("/admin/accounts/"+accountID+"/collections", filters, page-1))+`">`+html.EscapeString(adminT(locale, "pagination.prev", "Previous"))+`</a>`)
	}
	if len(collections) == adminCollectionsPageSize {
		links = append(links, `<a rel="next" href="`+html.EscapeString(adminRailsPaginationURL("/admin/accounts/"+accountID+"/collections", filters, page+1))+`">`+html.EscapeString(adminT(locale, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) > 0 {
		body.WriteString(`<nav class="pagination">` + strings.Join(links, " ") + `</nav>`)
	}
	title := adminTVars(locale, "admin.collections.title", "Collections for %{name}", map[string]string{"name": account.Acct()})
	return authPageHTML(title, "", "", body.String(), locale)
}

func (s *Server) adminAccountCollectionPage(c *echo.Context) error {
	user, handled, err := s.requireAdminCollectionsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.adminCollectionAccount(c.Param("account_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	collectionID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || collectionID <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var collection models.Collection
	if err := s.db.Where("id = ? AND account_id = ?", collectionID, account.ID).First(&collection).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var items []models.CollectionItem
	if err := s.db.Where("collection_id = ? AND state = ? AND account_id IS NOT NULL", collection.ID, 1).Order("position ASC, id ASC").Find(&items).Error; err != nil {
		return err
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.AccountID.Int64)
	}
	accounts := []models.Account{}
	if len(ids) > 0 {
		if err := s.db.Where("id IN ?", ids).Find(&accounts).Error; err != nil {
			return err
		}
	}
	return c.HTML(http.StatusOK, adminCollectionShowHTML(*account, collection, accounts, s.webLocale(c, user)))
}

func adminCollectionShowHTML(owner models.Account, collection models.Collection, accounts []models.Account, locale string) string {
	var body strings.Builder
	body.WriteString(`<p class="back-link"><a href="/admin/accounts/` + strconv.FormatInt(owner.ID, 10) + `/collections">` + html.EscapeString(adminT(locale, "admin.collections.back_to_account", "Back to collections")) + `</a></p>`)
	body.WriteString(`<p><a class="button" target="_blank" rel="noopener" href="/collections/` + strconv.FormatInt(collection.ID, 10) + `">` + html.EscapeString(adminT(locale, "admin.collections.open", "Open collection")) + `</a></p>`)
	body.WriteString(`<h3>` + html.EscapeString(collection.Name) + `</h3><p>` + html.EscapeString(collection.Description.String) + `</p><h3>` + html.EscapeString(adminT(locale, "admin.collections.accounts", "Accounts")) + `</h3><div class="batch-table__body">`)
	if len(accounts) == 0 {
		body.WriteString(`<div class="empty-state">` + html.EscapeString(adminT(locale, "admin.collections.empty", "Nothing here")) + `</div>`)
	}
	for _, account := range accounts {
		body.WriteString(`<div class="batch-table__row"><a href="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `">` + html.EscapeString(account.Acct()) + `</a></div>`)
	}
	body.WriteString(`</div>`)
	return authPageHTML(adminTVars(locale, "admin.collections.collection_title", "Collection by %{name}", map[string]string{"name": owner.Acct()}), "", "", body.String(), locale)
}

func (s *Server) batchAdminAccountCollections(c *echo.Context) error {
	user, handled, err := s.requireAdminCollectionsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.adminCollectionAccount(c.Param("account_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := c.Request().ParseForm(); err != nil {
		return err
	}
	values, present := c.Request().PostForm["admin_collection_batch_action[collection_ids][]"]
	if !present {
		return s.redirectAdminCollectionBatch(c, account.ID, 0, adminT(s.webLocale(c, user), "admin.collections.no_collection_selected", "No collection selected"))
	}
	ids := parseAdminCollectionIDs(values)
	if len(ids) == 0 {
		return s.redirectAdminCollectionBatch(c, account.ID, 0, adminT(s.webLocale(c, user), "admin.collections.no_collection_selected", "No collection selected"))
	}
	var selected []models.Collection
	if err := s.db.Where("account_id = ? AND id IN ?", account.ID, ids).Find(&selected).Error; err != nil {
		return err
	}
	if len(selected) == 0 {
		return s.redirectAdminCollectionBatch(c, account.ID, 0, adminT(s.webLocale(c, user), "admin.collections.no_collection_selected", "No collection selected"))
	}
	reportID, _ := strconv.ParseInt(strings.TrimSpace(c.Request().PostFormValue("report_id")), 10, 64)
	remove := strings.TrimSpace(c.Request().PostFormValue("remove_from_report")) != ""
	report := strings.TrimSpace(c.Request().PostFormValue("report")) != ""
	if !remove && !report {
		return s.redirectAdminCollectionBatch(c, account.ID, reportID, "")
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if reportID > 0 {
			var existing models.Report
			if err := tx.Where("id = ? AND target_account_id = ?", reportID, account.ID).First(&existing).Error; err != nil {
				return err
			}
		} else if report {
			now := time.Now().UTC()
			created := models.Report{AccountID: user.AccountID, TargetAccountID: account.ID, Category: reportCategoryValue("other"), Forwarded: sql.NullBool{Bool: false, Valid: true}, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&created).Error; err != nil {
				return err
			}
			reportID = created.ID
		}
		if reportID <= 0 {
			return nil
		}
		for _, collection := range selected {
			if remove {
				if err := tx.Where("report_id = ? AND collection_id = ?", reportID, collection.ID).Delete(&models.CollectionReport{}).Error; err != nil {
					return err
				}
				continue
			}
			row := models.CollectionReport{ReportID: reportID, CollectionID: collection.ID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.redirectAdminCollectionBatch(c, account.ID, reportID, "")
}

func parseAdminCollectionIDs(values []string) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *Server) redirectAdminCollectionBatch(c *echo.Context, accountID int64, reportID int64, errorText string) error {
	if reportID > 0 {
		return c.Redirect(http.StatusFound, "/admin/reports/"+strconv.FormatInt(reportID, 10))
	}
	values := url.Values{}
	if page := strings.TrimSpace(c.FormValue("page")); page != "" {
		values.Set("page", page)
	}
	if errorText != "" {
		values.Set("error", errorText)
	}
	target := "/admin/accounts/" + strconv.FormatInt(accountID, 10) + "/collections"
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	return c.Redirect(http.StatusFound, target)
}
