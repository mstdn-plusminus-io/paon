package api

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	importFileSizeLimit       = 20 * 1024 * 1024
	importRowsProcessingLimit = 20000

	bulkImportStateUnconfirmed = 0
	bulkImportStateScheduled   = 1
	bulkImportStateInProgress  = 2
	bulkImportStateFinished    = 3
)

var importTypeByName = map[string]int{
	"following":       0,
	"blocking":        1,
	"muting":          2,
	"domain_blocking": 3,
	"bookmarks":       4,
	"lists":           5,
}

var importNameByType = map[int]string{
	0: "following",
	1: "blocking",
	2: "muting",
	3: "domain_blocking",
	4: "bookmarks",
	5: "lists",
}

var importDefaultHeaders = map[string][]string{
	"following":       {"Account address"},
	"blocking":        {"Account address"},
	"muting":          {"Account address"},
	"domain_blocking": {"#domain"},
	"bookmarks":       {"#uri"},
	"lists":           {"List name", "Account address"},
}

var importExpectedHeaders = map[string][]string{
	"following":       {"Account address", "Show boosts", "Notify on new posts", "Languages"},
	"blocking":        {"Account address"},
	"muting":          {"Account address", "Hide notifications"},
	"domain_blocking": {"#domain"},
	"bookmarks":       {"#uri"},
	"lists":           {"List name", "Account address"},
}

var importHeaderAttribute = map[string]string{
	"Account address":     "acct",
	"Show boosts":         "show_reblogs",
	"Notify on new posts": "notify",
	"Languages":           "languages",
	"Hide notifications":  "hide_notifications",
	"#domain":             "domain",
	"#uri":                "uri",
	"List name":           "list_name",
}

var importFailureFilename = map[string]string{
	"following":       "following_accounts_failures.csv",
	"blocking":        "blocked_accounts_failures.csv",
	"muting":          "muted_accounts_failures.csv",
	"domain_blocking": "blocked_domains_failures.csv",
	"bookmarks":       "bookmarks_failures.csv",
	"lists":           "lists_failures.csv",
}

func (s *Server) settingsImportsPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	imports, err := s.recentBulkImports(account.ID)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, importsHTML(imports, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) createSettingsImport(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	importRootPresent, err := requestHasNestedFormOrFilePrefix(c.Request(), "form_import", importFileSizeLimit)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if !importRootPresent {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/settings/imports?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
	}
	importType := strings.TrimSpace(c.FormValue("form_import[type]"))
	typeValue, ok := importTypeByName[importType]
	if !ok {
		return s.renderSettingsImportsError(c, account.ID, user, settingsT(locale, "imports.errors.invalid_type", "Import type is invalid"))
	}
	overwrite := strings.EqualFold(c.FormValue("form_import[mode]"), "overwrite")
	fileHeader, err := c.FormFile("form_import[data]")
	if err != nil {
		return s.renderSettingsImportsError(c, account.ID, user, settingsT(locale, "imports.errors.required", "CSV file is required"))
	}
	rows, err := parseImportCSV(fileHeader, importType)
	if err != nil {
		return s.renderSettingsImportsError(c, account.ID, user, importErrorText(locale, err))
	}
	if importType == "following" {
		overLimit, limit, err := s.followingImportRowsOverLimit(c.Request().Context(), s.db, *account, len(rows), overwrite)
		if err != nil {
			return err
		}
		if overLimit {
			message := webT(locale, "users.follow_limit_reached", map[string]string{"limit": strconv.FormatInt(limit, 10)})
			return s.renderSettingsImportsError(c, account.ID, user, message)
		}
	}
	now := time.Now().UTC()
	guessedType := guessedImportType(fileHeader.Filename, rows, importType)
	bulkImport := models.BulkImport{
		Type:             typeValue,
		State:            bulkImportStateUnconfirmed,
		TotalItems:       len(rows),
		ImportedItems:    0,
		ProcessedItems:   0,
		Overwrite:        overwrite,
		LikelyMismatched: guessedType != "" && guessedType != importType,
		OriginalFilename: fileHeader.Filename,
		AccountID:        account.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&bulkImport).Error; err != nil {
			return err
		}
		importRows := make([]models.BulkImportRow, 0, len(rows))
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				return err
			}
			importRows = append(importRows, models.BulkImportRow{
				BulkImportID: bulkImport.ID,
				Data:         models.JSONValue(encoded),
				CreatedAt:    now,
				UpdatedAt:    now,
			})
		}
		if len(importRows) > 0 {
			if err := tx.CreateInBatches(importRows, 1000).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/settings/imports/"+strconv.FormatInt(bulkImport.ID, 10))
}

func (s *Server) followingImportRowsOverLimit(ctx context.Context, db *gorm.DB, account models.Account, rowCount int, overwrite bool) (bool, int64, error) {
	stat := account.AccountStat
	if stat.AccountID == 0 && db != nil && account.ID != 0 {
		err := db.WithContext(ctx).Select("account_id", "following_count", "followers_count").Where("account_id = ?", account.ID).First(&stat).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, 0, err
		}
		if stat.AccountID == 0 {
			stat.AccountID = account.ID
		}
	}
	baseLimit := followLimitForAccountStat(stat, s.maxFollowsThreshold(), s.maxFollowsRatio())
	remaining := baseLimit
	if !overwrite {
		remaining -= stat.FollowingCount
	}
	return int64(rowCount) > remaining, baseLimit, nil
}

func (s *Server) showSettingsImport(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	bulkImport, err := s.findBulkImport(account.ID, c.Param("id"), bulkImportStateUnconfirmed)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, importConfirmHTML(*bulkImport, renderArgs...))
}

func (s *Server) confirmSettingsImport(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/settings/imports?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
	}
	bulkImport, err := s.findBulkImport(account.ID, c.Param("id"), bulkImportStateUnconfirmed)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	now := time.Now().UTC()
	if err := s.db.Model(&models.BulkImport{}).
		Where("id = ? AND account_id = ? AND state = ?", c.Param("id"), account.ID, bulkImportStateUnconfirmed).
		Updates(map[string]any{"state": bulkImportStateScheduled, "updated_at": now}).Error; err != nil {
		return err
	}
	if !s.enqueueBulkImportTask(bulkImport.ID) {
		if err := s.processBulkImport(c.Request().Context(), bulkImport.ID); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/settings/imports?notice="+url.QueryEscape(settingsT(locale, "imports.success", "Your data was successfully uploaded and will be processed in due time")))
}

func (s *Server) renderSettingsImportsError(c *echo.Context, accountID int64, user *models.User, errorText string) error {
	imports, err := s.recentBulkImports(accountID)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, importsHTML(imports, "", errorText, renderArgs...))
}

func requestHasNestedFormOrFilePrefix(req *http.Request, prefix string, maxMemory int64) (bool, error) {
	if req == nil {
		return false, nil
	}
	if err := req.ParseMultipartForm(maxMemory); err != nil {
		if err != http.ErrNotMultipart {
			return false, err
		}
		if err := req.ParseForm(); err != nil {
			return false, err
		}
	}
	if formHasNestedPrefix(req.Form, prefix) {
		return true, nil
	}
	if req.MultipartForm == nil {
		return false, nil
	}
	filePrefix := prefix + "["
	for key := range req.MultipartForm.File {
		if strings.HasPrefix(key, filePrefix) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) processBulkImport(ctx context.Context, bulkImportID int64) error {
	if s == nil || s.db == nil || bulkImportID == 0 {
		return nil
	}
	var bulkImport models.BulkImport
	if err := s.db.WithContext(ctx).Where("id = ?", bulkImportID).First(&bulkImport).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if bulkImport.State == bulkImportStateFinished {
		return nil
	}
	var err error
	switch bulkImport.Type {
	case importTypeByName["following"], importTypeByName["blocking"], importTypeByName["muting"]:
		err = s.processRelationshipImport(bulkImport)
	case importTypeByName["domain_blocking"]:
		err = s.processDomainBlockImport(bulkImport)
	case importTypeByName["bookmarks"]:
		err = s.processBookmarkImport(bulkImport)
	case importTypeByName["lists"]:
		err = s.processListImport(bulkImport)
	default:
		err = fmt.Errorf("unknown import type: %d", bulkImport.Type)
	}
	if err != nil {
		now := time.Now().UTC()
		_ = s.db.WithContext(ctx).Model(&models.BulkImport{}).
			Where("id = ?", bulkImport.ID).
			Updates(map[string]any{"state": bulkImportStateFinished, "finished_at": now, "updated_at": now}).Error
	}
	if err == nil {
		_ = s.finishBulkImportIfComplete(ctx, bulkImport.ID)
	}
	return err
}

func (s *Server) processLegacyImport(ctx context.Context, importID int64) error {
	if s == nil || s.db == nil || importID == 0 {
		return nil
	}
	var table sql.NullString
	if err := s.db.WithContext(ctx).Raw(`SELECT to_regclass('public.imports')`).Scan(&table).Error; err != nil {
		return err
	}
	// Mastodon 4.4 drops imports after the bulk-import cutover. A queued legacy
	// task can outlive the contract migration, so treat it as already complete
	// instead of querying a table that no longer exists.
	if !table.Valid || strings.TrimSpace(table.String) == "" {
		return nil
	}
	var legacy models.Import
	if err := s.db.WithContext(ctx).Where("id = ?", importID).First(&legacy).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	defer func() {
		_ = s.db.WithContext(context.Background()).Delete(&models.Import{}, legacy.ID).Error
	}()
	typeName, ok := importNameByType[legacy.Type]
	if !ok || typeName == "lists" {
		return nil
	}
	rows, err := s.legacyImportRows(legacy, typeName)
	if err != nil {
		return err
	}
	switch typeName {
	case "following", "blocking", "muting":
		return s.processLegacyRelationshipImport(ctx, legacy, typeName, rows)
	case "domain_blocking":
		return s.processLegacyDomainBlockImport(ctx, legacy, rows)
	case "bookmarks":
		return s.processLegacyBookmarkImport(ctx, legacy, rows)
	default:
		return nil
	}
}

func (s *Server) legacyImportRows(legacy models.Import, typeName string) ([]map[string]any, error) {
	path := s.legacyImportDataPath(legacy)
	if path == "" {
		return nil, fmt.Errorf("legacy import data file is missing")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseLegacyImportCSVReader(file, typeName)
}

func (s *Server) legacyImportDataPath(legacy models.Import) string {
	if !legacy.DataFileName.Valid || strings.TrimSpace(legacy.DataFileName.String) == "" {
		return ""
	}
	return s.cfg.SystemAssetPath("imports", "data", mediaPaperclipIDPartition(legacy.ID), "original", filepath.Base(strings.TrimSpace(legacy.DataFileName.String)))
}

func (s *Server) processLegacyRelationshipImport(ctx context.Context, legacy models.Import, typeName string, rows []map[string]any) error {
	action, undo := legacyRelationshipActions(typeName)
	if action == "" {
		return nil
	}
	items := map[string]map[string]any{}
	for _, row := range rows {
		acct := s.legacyImportAcct(row)
		if acct == "" {
			continue
		}
		items[acct] = row
	}
	if legacy.Overwrite {
		current, err := s.legacyRelationshipAccounts(ctx, legacy.AccountID, typeName)
		if err != nil {
			return err
		}
		for _, account := range current {
			acct := account.Acct()
			if row, ok := items[acct]; ok {
				if err := s.processLegacyImportRelationship(ctx, legacy.AccountID, acct, action, legacyRelationshipOptionsFromRow(typeName, row)); err != nil {
					return err
				}
				delete(items, acct)
				continue
			}
			if err := s.processLegacyImportRelationship(ctx, legacy.AccountID, acct, undo, nil); err != nil {
				return err
			}
		}
	}
	for acct, row := range items {
		if err := s.processLegacyImportRelationship(ctx, legacy.AccountID, acct, action, legacyRelationshipOptionsFromRow(typeName, row)); err != nil {
			return err
		}
	}
	return nil
}

func legacyRelationshipActions(typeName string) (string, string) {
	switch typeName {
	case "following":
		return "follow", "unfollow"
	case "blocking":
		return "block", "unblock"
	case "muting":
		return "mute", "unmute"
	default:
		return "", ""
	}
}

func (s *Server) legacyImportAcct(row map[string]any) string {
	acct := strings.TrimPrefix(strings.TrimSpace(fmt.Sprint(row["acct"])), "@")
	if domain := strings.TrimSpace(firstNonEmpty(s.cfg.LocalDomain, s.cfg.WebDomain)); domain != "" {
		acct = strings.TrimSuffix(acct, "@"+domain)
	}
	return acct
}

func legacyRelationshipOptionsFromRow(typeName string, row map[string]any) map[string]any {
	switch typeName {
	case "following":
		return map[string]any{
			"reblogs":   row["show_reblogs"],
			"notify":    row["notify"],
			"languages": row["languages"],
		}
	case "muting":
		return map[string]any{"notifications": row["hide_notifications"]}
	default:
		return nil
	}
}

func (s *Server) legacyRelationshipAccounts(ctx context.Context, accountID int64, typeName string) ([]models.Account, error) {
	var accounts []models.Account
	query := s.db.WithContext(ctx).Model(&models.Account{})
	switch typeName {
	case "following":
		query = query.Joins("JOIN follows ON follows.target_account_id = accounts.id").Where("follows.account_id = ?", accountID)
	case "blocking":
		query = query.Joins("JOIN blocks ON blocks.target_account_id = accounts.id").Where("blocks.account_id = ?", accountID)
	case "muting":
		query = query.Joins("JOIN mutes ON mutes.target_account_id = accounts.id").Where("mutes.account_id = ?", accountID)
	default:
		return accounts, nil
	}
	return accounts, query.Find(&accounts).Error
}

func (s *Server) processLegacyDomainBlockImport(ctx context.Context, legacy models.Import, rows []map[string]any) error {
	domains := make([]string, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		domain, err := normalizeDomainBlockParam(fmt.Sprint(row["domain"]))
		if err != nil || domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	now := time.Now().UTC()
	changed := append([]string{}, domains...)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if legacy.Overwrite {
			var current []models.AccountDomainBlock
			if err := tx.Where("account_id = ?", legacy.AccountID).Find(&current).Error; err != nil {
				return err
			}
			for _, block := range current {
				domain := string(block.Domain)
				if _, ok := seen[domain]; ok {
					continue
				}
				if err := tx.Delete(&models.AccountDomainBlock{}, block.ID).Error; err != nil {
					return err
				}
				changed = append(changed, domain)
			}
		}
		for _, domain := range domains {
			row := models.AccountDomainBlock{AccountID: models.AccountDomainBlockAccountID(legacy.AccountID), Domain: models.NullSafeString(domain), CreatedAt: now, UpdatedAt: now}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if res.Error != nil {
				return res.Error
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.clearDomainBlockFeedCaches(context.Background(), legacy.AccountID, changed)
	s.invalidateAccountDomainBlockCaches(context.Background(), legacy.AccountID, changed)
	for _, domain := range domains {
		if err := s.enqueueAfterAccountDomainBlockOrRun(context.Background(), legacy.AccountID, domain); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) processLegacyBookmarkImport(ctx context.Context, legacy models.Import, rows []map[string]any) error {
	uris := map[string]struct{}{}
	ordered := make([]string, 0, len(rows))
	for _, row := range rows {
		uri := strings.TrimSpace(fmt.Sprint(row["uri"]))
		if uri == "" {
			continue
		}
		if _, ok := uris[uri]; ok {
			continue
		}
		uris[uri] = struct{}{}
		ordered = append(ordered, uri)
	}
	if legacy.Overwrite {
		var bookmarks []models.Bookmark
		if err := s.db.WithContext(ctx).Preload("Status.Account").Where("account_id = ?", legacy.AccountID).Find(&bookmarks).Error; err != nil {
			return err
		}
		for _, bookmark := range bookmarks {
			if _, ok := uris[activityPubStatusURI(s, bookmark.Status)]; ok {
				continue
			}
			if err := s.db.WithContext(ctx).Delete(&models.Bookmark{}, bookmark.ID).Error; err != nil {
				return err
			}
			s.runBookmarkDestroyedSideEffects(context.Background(), bookmark)
		}
	}
	now := time.Now().UTC()
	for _, uri := range ordered {
		status, err := s.statusFromActivityURI(uri)
		if err != nil {
			return err
		}
		if status == nil {
			status, err = s.fetchRemoteStatusFromActivityURI(uri)
			if err != nil {
				return err
			}
		}
		if status == nil {
			continue
		}
		target, err := s.visibleBookmarkImportTargetStatus(ctx, legacy.AccountID, status)
		if err != nil {
			return err
		}
		if target == nil {
			continue
		}
		bookmark := models.Bookmark{AccountID: legacy.AccountID, StatusID: target.ID, CreatedAt: now, UpdatedAt: now}
		res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&bookmark)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			s.addBookmarkToFeedCache(context.Background(), bookmark)
		}
	}
	return nil
}

func (s *Server) enqueueOrProcessImportRows(ctx context.Context, rows []models.BulkImportRow) error {
	for _, row := range rows {
		if row.ID == 0 {
			continue
		}
		if s.enqueueImportRowTask(row.ID) {
			continue
		}
		if err := s.processBulkImportRow(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) processBulkImportRow(ctx context.Context, rowID int64) error {
	if s == nil || s.db == nil || rowID == 0 {
		return nil
	}
	var row models.BulkImportRow
	if err := s.db.WithContext(ctx).Where("id = ?", rowID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var bulkImport models.BulkImport
	if err := s.db.WithContext(ctx).Where("id = ?", row.BulkImportID).First(&bulkImport).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if bulkImport.State == bulkImportStateFinished {
		return nil
	}
	switch bulkImport.Type {
	case importTypeByName["following"], importTypeByName["blocking"], importTypeByName["muting"]:
		return s.processRelationshipImportRow(ctx, bulkImport, row)
	case importTypeByName["bookmarks"]:
		return s.processBookmarkImportRow(ctx, bulkImport, row)
	case importTypeByName["lists"]:
		return s.processListImportRow(ctx, bulkImport, row)
	default:
		return s.progressBulkImport(ctx, bulkImport.ID, false)
	}
}

func (s *Server) processRelationshipImportRow(ctx context.Context, bulkImport models.BulkImport, row models.BulkImportRow) error {
	values := map[string]any{}
	if err := json.Unmarshal(row.Data, &values); err != nil {
		return s.progressBulkImport(ctx, bulkImport.ID, false)
	}
	importType := importNameByType[bulkImport.Type]
	now := time.Now().UTC()
	var targetID int64
	var hideNotifications bool
	var notificationPayloads []asynqLocalNotificationPayload
	imported := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target, err := s.findOrResolveImportAccountTx(tx, fmt.Sprint(values["acct"]))
		if err != nil {
			return err
		}
		if target == nil || target.ID == bulkImport.AccountID || target.SuspendedAt.Valid {
			return s.progressBulkImportTx(tx, bulkImport.ID, 1, 0, now)
		}
		hideNotifications = importType == "muting" && importBool(values, "hide_notifications", true)
		notificationPayload := (*asynqLocalNotificationPayload)(nil)
		imported, notificationPayload, err = s.applyRelationshipImportRow(tx, bulkImport.AccountID, target, importType, values, now)
		if err != nil {
			return err
		}
		notificationPayloads = appendRelationshipBatchNotificationPayload(notificationPayloads, notificationPayload)
		targetID = target.ID
		if imported {
			if err := tx.Where("id = ?", row.ID).Delete(&models.BulkImportRow{}).Error; err != nil {
				return err
			}
			return s.progressBulkImportTx(tx, bulkImport.ID, 1, 1, now)
		}
		return s.progressBulkImportTx(tx, bulkImport.ID, 1, 0, now)
	})
	if err != nil {
		return err
	}
	if !imported || targetID == 0 {
		return nil
	}
	notificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)
	if err != nil {
		return err
	}
	s.publishNotificationIDs(notificationIDs)
	switch importType {
	case "following":
		_ = s.clearHomeFeedCacheContext(context.Background(), bulkImport.AccountID)
		s.invalidateFollowRelationshipCaches(context.Background(), models.Account{ID: bulkImport.AccountID}, targetID)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), targetID)
	case "blocking":
		s.clearAfterBlockFeedCaches(context.Background(), bulkImport.AccountID, targetID)
		s.invalidateBlockRelationshipCaches(context.Background(), bulkImport.AccountID, targetID)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), bulkImport.AccountID, targetID)
	case "muting":
		if hideNotifications {
			s.clearAfterBlockFeedCaches(context.Background(), bulkImport.AccountID, targetID)
		} else {
			s.clearAfterMuteFeedCache(context.Background(), bulkImport.AccountID, targetID)
		}
		s.invalidateMuteRelationshipCaches(context.Background(), bulkImport.AccountID, targetID)
	}
	return nil
}

func (s *Server) processLegacyImportRelationship(ctx context.Context, accountID int64, targetAccountURI string, relationship string, options map[string]any) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	relationship = strings.TrimSpace(relationship)
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", accountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	now := time.Now().UTC()
	var targetID int64
	var affectedListIDs []int64
	var hideNotifications bool
	var notificationPayloads []asynqLocalNotificationPayload
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target, err := s.findOrResolveImportAccountTx(tx, targetAccountURI)
		if err != nil {
			return err
		}
		if target == nil || target.ID == accountID || target.SuspendedAt.Valid {
			return nil
		}
		targetID = target.ID
		switch relationship {
		case "follow":
			values := legacyImportRelationshipOptions(options, "following")
			_, notificationPayload, err := s.applyRelationshipImportRow(tx, accountID, target, "following", values, now)
			notificationPayloads = appendRelationshipBatchNotificationPayload(notificationPayloads, notificationPayload)
			return err
		case "unfollow":
			_, affectedListIDs, _, err = deleteFollowEdgeReturningFollow(tx, accountID, target.ID)
			if err != nil {
				return err
			}
			return deleteFollowRequestEdge(tx, accountID, target.ID)
		case "block":
			_, _, err := s.applyRelationshipImportRow(tx, accountID, target, "blocking", nil, now)
			return err
		case "unblock":
			return tx.Where("account_id = ? AND target_account_id = ?", accountID, target.ID).Delete(&models.Block{}).Error
		case "mute":
			values := legacyImportRelationshipOptions(options, "muting")
			hideNotifications = importBool(values, "hide_notifications", true)
			_, _, err := s.applyRelationshipImportRow(tx, accountID, target, "muting", values, now)
			return err
		case "unmute":
			return tx.Where("account_id = ? AND target_account_id = ?", accountID, target.ID).Delete(&models.Mute{}).Error
		default:
			return nil
		}
	})
	if err != nil {
		return err
	}
	if targetID == 0 {
		return nil
	}
	notificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)
	if err != nil {
		return err
	}
	s.publishNotificationIDs(notificationIDs)
	switch relationship {
	case "follow":
		_ = s.clearHomeFeedCacheContext(context.Background(), accountID)
		s.invalidateFollowRelationshipCaches(context.Background(), account, targetID)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), targetID)
	case "unfollow":
		s.invalidateFollowRelationshipCaches(context.Background(), account, targetID)
		s.unmergeAfterUnfollowBestEffort(context.Background(), targetID, account)
		s.unmergeListFeedsAfterUnfollowBestEffort(context.Background(), targetID, affectedListIDs)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), targetID)
	case "block":
		s.clearAfterBlockFeedCaches(context.Background(), accountID, targetID)
		s.invalidateBlockRelationshipCaches(context.Background(), accountID, targetID)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), accountID, targetID)
	case "unblock":
		s.invalidateBlockRelationshipCaches(context.Background(), accountID, targetID)
	case "mute":
		if hideNotifications {
			s.clearAfterBlockFeedCaches(context.Background(), accountID, targetID)
		} else {
			s.clearAfterMuteFeedCache(context.Background(), accountID, targetID)
		}
		s.invalidateMuteRelationshipCaches(context.Background(), accountID, targetID)
	case "unmute":
		s.restoreAfterUnmuteFeedCache(context.Background(), accountID, targetID)
		s.invalidateMuteRelationshipCaches(context.Background(), accountID, targetID)
	}
	return nil
}

func legacyImportRelationshipOptions(options map[string]any, importType string) map[string]any {
	values := map[string]any{}
	switch importType {
	case "following":
		values["show_reblogs"] = legacyOptionValue(options, "reblogs", true)
		values["notify"] = legacyOptionValue(options, "notify", false)
		values["languages"] = legacyOptionValue(options, "languages", nil)
	case "muting":
		values["hide_notifications"] = legacyOptionValue(options, "notifications", true)
	}
	return values
}

func legacyOptionValue(options map[string]any, key string, fallback any) any {
	if options == nil {
		return fallback
	}
	if value, ok := options[key]; ok {
		return value
	}
	return fallback
}

func deleteFollowRequestEdge(tx *gorm.DB, sourceID int64, targetID int64) error {
	var req models.FollowRequest
	err := tx.Where("account_id = ? AND target_account_id = ?", sourceID, targetID).First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", req.ID).Delete(&models.Notification{}).Error; err != nil {
		return err
	}
	if _, err := deleteListAccountsForRejectedFollowRequest(tx, req.ID); err != nil {
		return err
	}
	return tx.Delete(&req).Error
}

func (s *Server) processBookmarkImportRow(ctx context.Context, bulkImport models.BulkImport, row models.BulkImportRow) error {
	values := map[string]any{}
	if err := json.Unmarshal(row.Data, &values); err != nil {
		return s.progressBulkImport(ctx, bulkImport.ID, false)
	}
	uri := strings.TrimSpace(fmt.Sprint(values["uri"]))
	status, err := s.statusFromActivityURI(uri)
	if err != nil {
		return err
	}
	if status == nil {
		status, err = s.fetchRemoteStatusFromActivityURI(uri)
		if err != nil {
			return err
		}
		if status == nil {
			return s.progressBulkImport(ctx, bulkImport.ID, false)
		}
	}
	now := time.Now().UTC()
	target, err := s.visibleBookmarkImportTargetStatus(ctx, bulkImport.AccountID, status)
	if err != nil {
		return err
	}
	if target == nil {
		return s.progressBulkImport(ctx, bulkImport.ID, false)
	}
	bookmark := models.Bookmark{AccountID: bulkImport.AccountID, StatusID: target.ID, CreatedAt: now, UpdatedAt: now}
	added := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&bookmark)
		if res.Error != nil {
			return res.Error
		}
		added = res.RowsAffected > 0
		if err := tx.Where("id = ?", row.ID).Delete(&models.BulkImportRow{}).Error; err != nil {
			return err
		}
		return s.progressBulkImportTx(tx, bulkImport.ID, 1, 1, now)
	}); err != nil {
		return err
	}
	if added {
		s.addBookmarkToFeedCache(context.Background(), bookmark)
	}
	return nil
}

func (s *Server) processListImportRow(ctx context.Context, bulkImport models.BulkImport, row models.BulkImportRow) error {
	values := map[string]any{}
	if err := json.Unmarshal(row.Data, &values); err != nil {
		return s.progressBulkImport(ctx, bulkImport.ID, false)
	}
	title := strings.TrimSpace(fmt.Sprint(values["list_name"]))
	acct := strings.TrimSpace(fmt.Sprint(values["acct"]))
	if title == "" || acct == "" {
		return s.progressBulkImport(ctx, bulkImport.ID, false)
	}
	now := time.Now().UTC()
	var affectedFollowTargetID int64
	var affectedListID int64
	var notificationPayloads []asynqLocalNotificationPayload
	imported := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target, err := s.findOrResolveImportAccountTx(tx, acct)
		if err != nil {
			return err
		}
		if target == nil || target.SuspendedAt.Valid {
			return s.progressBulkImportTx(tx, bulkImport.ID, 1, 0, now)
		}
		list, err := findOrCreateImportList(tx, bulkImport.AccountID, title, now)
		if err != nil {
			return err
		}
		item, err := listAccountRow(tx, *list, target.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) && target.ID != bulkImport.AccountID {
			followed, notificationPayload, err := s.applyRelationshipImportRow(tx, bulkImport.AccountID, target, "following", map[string]any{"show_reblogs": true, "notify": false}, now)
			if err != nil {
				return err
			}
			notificationPayloads = appendRelationshipBatchNotificationPayload(notificationPayloads, notificationPayload)
			if followed {
				affectedFollowTargetID = target.ID
			}
			item, err = listAccountRow(tx, *list, target.ID)
		}
		if err != nil {
			return s.progressBulkImportTx(tx, bulkImport.ID, 1, 0, now)
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(item).Error; err != nil {
			return err
		}
		affectedListID = list.ID
		imported = true
		if err := tx.Where("id = ?", row.ID).Delete(&models.BulkImportRow{}).Error; err != nil {
			return err
		}
		return s.progressBulkImportTx(tx, bulkImport.ID, 1, 1, now)
	})
	if err != nil {
		return err
	}
	if affectedListID != 0 {
		_ = s.clearListFeedCacheContext(context.Background(), affectedListID)
	}
	if imported && affectedFollowTargetID != 0 {
		notificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)
		if err != nil {
			return err
		}
		s.publishNotificationIDs(notificationIDs)
		_ = s.clearHomeFeedCacheContext(context.Background(), bulkImport.AccountID)
		s.invalidateFollowRelationshipCaches(context.Background(), models.Account{ID: bulkImport.AccountID}, affectedFollowTargetID)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), affectedFollowTargetID)
	}
	return nil
}

func (s *Server) progressBulkImport(ctx context.Context, bulkImportID int64, imported bool) error {
	if s == nil || s.db == nil || bulkImportID == 0 {
		return nil
	}
	importedCount := 0
	if imported {
		importedCount = 1
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.progressBulkImportTx(tx, bulkImportID, 1, importedCount, time.Now().UTC())
	})
}

func (s *Server) markImportRowWorkerExhausted(ctx context.Context, rowID int64) {
	if s == nil || s.db == nil || rowID == 0 {
		return
	}
	var row models.BulkImportRow
	if err := s.db.WithContext(ctx).Select("id", "bulk_import_id").Where("id = ?", rowID).First(&row).Error; err != nil || row.BulkImportID == 0 {
		return
	}
	_ = s.progressBulkImport(ctx, row.BulkImportID, false)
}

func (s *Server) progressBulkImportTx(tx *gorm.DB, bulkImportID int64, processedDelta int, importedDelta int, now time.Time) error {
	if processedDelta <= 0 && importedDelta <= 0 {
		return nil
	}
	updates := map[string]any{"updated_at": now}
	if processedDelta > 0 {
		updates["processed_items"] = gorm.Expr("processed_items + ?", processedDelta)
	}
	if importedDelta > 0 {
		updates["imported_items"] = gorm.Expr("imported_items + ?", importedDelta)
	}
	if err := tx.Model(&models.BulkImport{}).Where("id = ?", bulkImportID).Updates(updates).Error; err != nil {
		return err
	}
	var bulkImport models.BulkImport
	if err := tx.Select("id", "processed_items", "total_items").Where("id = ?", bulkImportID).First(&bulkImport).Error; err != nil {
		return err
	}
	if bulkImport.ProcessedItems >= bulkImport.TotalItems {
		return tx.Model(&models.BulkImport{}).
			Where("id = ?", bulkImportID).
			Updates(map[string]any{"state": bulkImportStateFinished, "finished_at": now, "updated_at": now}).Error
	}
	return nil
}

func (s *Server) finishBulkImportIfComplete(ctx context.Context, bulkImportID int64) error {
	var bulkImport models.BulkImport
	if err := s.db.WithContext(ctx).Select("id", "processed_items", "total_items", "state").Where("id = ?", bulkImportID).First(&bulkImport).Error; err != nil {
		return err
	}
	if bulkImport.State != bulkImportStateFinished && bulkImport.ProcessedItems >= bulkImport.TotalItems {
		now := time.Now().UTC()
		return s.db.WithContext(ctx).Model(&models.BulkImport{}).
			Where("id = ?", bulkImportID).
			Updates(map[string]any{"state": bulkImportStateFinished, "finished_at": now, "updated_at": now}).Error
	}
	return nil
}

func (s *Server) processRelationshipImport(bulkImport models.BulkImport) error {
	now := time.Now().UTC()
	affectedRelationshipTargets := []int64{}
	affectedListIDs := map[int64][]int64{}
	importType := importNameByType[bulkImport.Type]
	var rows []models.BulkImportRow
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("bulk_import_id = ?", bulkImport.ID).Order("id ASC").Find(&rows).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.BulkImport{}).
			Where("id = ? AND state IN ?", bulkImport.ID, []int{bulkImportStateUnconfirmed, bulkImportStateScheduled}).
			Updates(map[string]any{"state": bulkImportStateInProgress, "updated_at": now}).Error; err != nil {
			return err
		}
		if bulkImport.Overwrite {
			removedIDs, removedListIDs, err := s.applyRelationshipImportOverwrite(tx, bulkImport.AccountID, importType, rows)
			if err != nil {
				return err
			}
			affectedRelationshipTargets = append(affectedRelationshipTargets, removedIDs...)
			for targetID, listIDs := range removedListIDs {
				affectedListIDs[targetID] = append(affectedListIDs[targetID], listIDs...)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	affectedTargets := uniqueInt64s(affectedRelationshipTargets)
	if importType == "following" && len(affectedTargets) > 0 {
		_ = s.clearHomeFeedCacheContext(context.Background(), bulkImport.AccountID)
	}
	for _, targetID := range affectedTargets {
		switch importType {
		case "following":
			s.unmergeListFeedsAfterUnfollowBestEffort(context.Background(), targetID, affectedListIDs[targetID])
			s.invalidateFollowRelationshipCaches(context.Background(), models.Account{ID: bulkImport.AccountID}, targetID)
			s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), targetID)
		case "blocking":
			s.clearAfterBlockFeedCaches(context.Background(), bulkImport.AccountID, targetID)
			s.invalidateBlockRelationshipCaches(context.Background(), bulkImport.AccountID, targetID)
			s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), bulkImport.AccountID, targetID)
		case "muting":
			s.clearAfterMuteFeedCache(context.Background(), bulkImport.AccountID, targetID)
			s.invalidateMuteRelationshipCaches(context.Background(), bulkImport.AccountID, targetID)
		}
	}
	return s.enqueueOrProcessImportRows(context.Background(), rows)
}

func (s *Server) processBookmarkImport(bulkImport models.BulkImport) error {
	now := time.Now().UTC()
	var removedBookmarks []models.Bookmark
	var rows []models.BulkImportRow
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("bulk_import_id = ?", bulkImport.ID).Order("id ASC").Find(&rows).Error; err != nil {
			return err
		}
		uris := bookmarkImportURIs(rows)
		successfulRowIDs := make([]int64, 0, len(rows))
		if err := tx.Model(&models.BulkImport{}).
			Where("id = ? AND state IN ?", bulkImport.ID, []int{bulkImportStateUnconfirmed, bulkImportStateScheduled}).
			Updates(map[string]any{"state": bulkImportStateInProgress, "updated_at": now}).Error; err != nil {
			return err
		}
		if bulkImport.Overwrite {
			if err := s.applyBookmarkImportOverwrite(tx, bulkImport.AccountID, uris, &successfulRowIDs, &removedBookmarks); err != nil {
				return err
			}
		}
		if len(successfulRowIDs) > 0 {
			if err := tx.Where("id IN ?", successfulRowIDs).Delete(&models.BulkImportRow{}).Error; err != nil {
				return err
			}
			if err := s.progressBulkImportTx(tx, bulkImport.ID, len(successfulRowIDs), len(successfulRowIDs), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, bookmark := range removedBookmarks {
		s.runBookmarkDestroyedSideEffects(context.Background(), bookmark)
	}
	return s.enqueueOrProcessImportRows(context.Background(), rows)
}

func (s *Server) applyBookmarkImportOverwrite(tx *gorm.DB, accountID int64, uris map[string]int64, successfulRowIDs *[]int64, removedBookmarks *[]models.Bookmark) error {
	var bookmarks []models.Bookmark
	if err := tx.Preload("Status.Account").
		Where("account_id = ?", accountID).
		Find(&bookmarks).Error; err != nil {
		return err
	}
	for _, bookmark := range bookmarks {
		uri := activityPubStatusURI(s, bookmark.Status)
		if rowID, ok := uris[uri]; ok {
			*successfulRowIDs = append(*successfulRowIDs, rowID)
			continue
		}
		if err := tx.Delete(&models.Bookmark{}, bookmark.ID).Error; err != nil {
			return err
		}
		*removedBookmarks = append(*removedBookmarks, bookmark)
	}
	return nil
}

func bookmarkImportURIs(rows []models.BulkImportRow) map[string]int64 {
	uris := map[string]int64{}
	for _, row := range rows {
		values := map[string]any{}
		if err := json.Unmarshal(row.Data, &values); err != nil {
			continue
		}
		uri := strings.TrimSpace(fmt.Sprint(values["uri"]))
		if uri == "" {
			continue
		}
		if _, ok := uris[uri]; !ok {
			uris[uri] = row.ID
		}
	}
	return uris
}

func (s *Server) bookmarkImportTargetStatus(ctx context.Context, status *models.Status) (*models.Status, error) {
	if status == nil {
		return nil, nil
	}
	if status.ReblogOfID.Valid && (status.Reblog == nil || status.Reblog.ID == 0) && s != nil && s.db != nil {
		var reblog models.Status
		err := s.db.WithContext(ctx).
			Preload("Account.AccountStat").
			Where("id = ? AND deleted_at IS NULL", status.ReblogOfID.Int64).
			First(&reblog).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		status.Reblog = &reblog
	}
	return statusJoinTarget(status), nil
}

func (s *Server) visibleBookmarkImportTargetStatus(ctx context.Context, accountID int64, status *models.Status) (*models.Status, error) {
	if status == nil || s == nil || s.db == nil || accountID == 0 {
		return nil, nil
	}
	account, err := s.userAccount(accountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var visible models.Status
	err = s.visibleStatusQuery(account).
		Where("statuses.id = ?", status.ID).
		First(&visible).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.bookmarkImportTargetStatus(ctx, &visible)
}

func (s *Server) processListImport(bulkImport models.BulkImport) error {
	now := time.Now().UTC()
	affectedListIDs := []int64{}
	var rows []models.BulkImportRow
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("bulk_import_id = ?", bulkImport.ID).Order("id ASC").Find(&rows).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.BulkImport{}).
			Where("id = ? AND state IN ?", bulkImport.ID, []int{bulkImportStateUnconfirmed, bulkImportStateScheduled}).
			Updates(map[string]any{"state": bulkImportStateInProgress, "updated_at": now}).Error; err != nil {
			return err
		}
		if bulkImport.Overwrite {
			listIDs, err := applyListImportOverwrite(tx, bulkImport.AccountID, listImportTitles(rows))
			if err != nil {
				return err
			}
			affectedListIDs = append(affectedListIDs, listIDs...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, listID := range uniqueInt64s(affectedListIDs) {
		_ = s.clearListFeedCacheContext(context.Background(), listID)
	}
	return s.enqueueOrProcessImportRows(context.Background(), rows)
}

func applyListImportOverwrite(tx *gorm.DB, accountID int64, titles []string) ([]int64, error) {
	var lists []models.List
	if err := tx.Where("account_id = ?", accountID).Find(&lists).Error; err != nil {
		return nil, err
	}
	titleSet := map[string]struct{}{}
	for _, title := range titles {
		titleSet[title] = struct{}{}
	}
	removedIDs := make([]int64, 0, len(lists))
	clearedIDs := make([]int64, 0, len(lists))
	for _, list := range lists {
		if _, ok := titleSet[list.Title]; ok {
			clearedIDs = append(clearedIDs, list.ID)
		} else {
			removedIDs = append(removedIDs, list.ID)
		}
	}
	if len(removedIDs) > 0 {
		if err := tx.Where("list_id IN ?", removedIDs).Delete(&models.ListAccount{}).Error; err != nil {
			return nil, err
		}
		if err := tx.Where("id IN ?", removedIDs).Delete(&models.List{}).Error; err != nil {
			return nil, err
		}
	}
	if len(clearedIDs) > 0 {
		if err := tx.Where("list_id IN ?", clearedIDs).Delete(&models.ListAccount{}).Error; err != nil {
			return nil, err
		}
	}
	return append(removedIDs, clearedIDs...), nil
}

func listImportTitles(rows []models.BulkImportRow) []string {
	titles := make([]string, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		values := map[string]any{}
		if err := json.Unmarshal(row.Data, &values); err != nil {
			continue
		}
		title := strings.TrimSpace(fmt.Sprint(values["list_name"]))
		if title == "" {
			continue
		}
		if _, ok := seen[title]; ok {
			continue
		}
		seen[title] = struct{}{}
		titles = append(titles, title)
	}
	return titles
}

func findOrCreateImportList(tx *gorm.DB, accountID int64, title string, now time.Time) (*models.List, error) {
	var list models.List
	err := tx.Where("account_id = ? AND title = ?", accountID, title).First(&list).Error
	if err == nil {
		return &list, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	list = models.List{AccountID: accountID, Title: title, RepliesPolicy: 0, Exclusive: false, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&list).Error; err != nil {
		return nil, err
	}
	return &list, nil
}

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Server) applyRelationshipImportOverwrite(tx *gorm.DB, accountID int64, importType string, rows []models.BulkImportRow) ([]int64, map[int64][]int64, error) {
	accts := s.relationshipImportAccts(rows)
	switch importType {
	case "following":
		var follows []models.Follow
		if err := tx.Preload("TargetAccount").Where("account_id = ?", accountID).Find(&follows).Error; err != nil {
			return nil, nil, err
		}
		removed := make([]int64, 0, len(follows))
		removedListIDs := map[int64][]int64{}
		for _, follow := range follows {
			if relationshipImportHasAcct(accts, follow.TargetAccount.Acct()) {
				continue
			}
			removed = append(removed, follow.TargetAccountID)
			listIDs, err := deleteFollowWithAffectedListIDs(tx, follow)
			if err != nil {
				return nil, nil, err
			}
			removedListIDs[follow.TargetAccountID] = append(removedListIDs[follow.TargetAccountID], listIDs...)
		}
		return removed, removedListIDs, nil
	case "blocking":
		var rows []models.Block
		if err := tx.Preload("TargetAccount").Where("account_id = ?", accountID).Find(&rows).Error; err != nil {
			return nil, nil, err
		}
		removed := make([]int64, 0, len(rows))
		removedRowIDs := make([]int64, 0, len(rows))
		for _, row := range rows {
			if relationshipImportHasAcct(accts, row.TargetAccount.Acct()) {
				continue
			}
			removed = append(removed, row.TargetAccountID)
			removedRowIDs = append(removedRowIDs, row.ID)
		}
		if len(removedRowIDs) == 0 {
			return removed, nil, nil
		}
		return removed, nil, tx.Where("id IN ?", removedRowIDs).Delete(&models.Block{}).Error
	case "muting":
		var rows []models.Mute
		if err := tx.Preload("TargetAccount").Where("account_id = ?", accountID).Find(&rows).Error; err != nil {
			return nil, nil, err
		}
		removed := make([]int64, 0, len(rows))
		removedRowIDs := make([]int64, 0, len(rows))
		for _, row := range rows {
			if relationshipImportHasAcct(accts, row.TargetAccount.Acct()) {
				continue
			}
			removed = append(removed, row.TargetAccountID)
			removedRowIDs = append(removedRowIDs, row.ID)
		}
		if len(removedRowIDs) == 0 {
			return removed, nil, nil
		}
		return removed, nil, tx.Where("id IN ?", removedRowIDs).Delete(&models.Mute{}).Error
	}
	return nil, nil, nil
}

func (s *Server) relationshipImportAccts(rows []models.BulkImportRow) map[string]struct{} {
	out := map[string]struct{}{}
	for _, row := range rows {
		values := map[string]any{}
		if err := json.Unmarshal(row.Data, &values); err != nil {
			continue
		}
		acct := s.relationshipImportAcct(fmt.Sprint(values["acct"]))
		if acct == "" {
			continue
		}
		out[strings.ToLower(acct)] = struct{}{}
	}
	return out
}

func (s *Server) relationshipImportAcct(raw string) string {
	acct := strings.TrimPrefix(strings.TrimSpace(normalizeAcctInput(raw)), "@")
	if acct == "" {
		return ""
	}
	for _, domain := range []string{s.cfg.LocalDomain, s.cfg.WebDomain} {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		suffix := "@" + domain
		if strings.HasSuffix(strings.ToLower(acct), strings.ToLower(suffix)) {
			return acct[:len(acct)-len(suffix)]
		}
	}
	if username, domain, ok := strings.Cut(acct, "@"); ok {
		return username + "@" + strings.ToLower(strings.TrimSpace(domain))
	}
	return acct
}

func relationshipImportHasAcct(accts map[string]struct{}, acct string) bool {
	_, ok := accts[strings.ToLower(strings.TrimSpace(acct))]
	return ok
}

func (s *Server) findOrResolveImportAccountTx(tx *gorm.DB, acct string) (*models.Account, error) {
	target, err := s.findAccountByAcctTx(tx, acct)
	if err == nil {
		return target, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	remoteAcct, ok := s.importRemoteAcct(acct)
	if !ok {
		return nil, nil
	}
	target, err = s.fetchAndStoreActivityActorForAcctDB(tx, remoteAcct)
	if err != nil {
		return nil, nil
	}
	if !accountMatchesImportAcct(target, remoteAcct) {
		return nil, nil
	}
	return target, nil
}

func (s *Server) importRemoteAcct(acct string) (string, bool) {
	username, domain, ok := strings.Cut(normalizeAcctInput(acct), "@")
	username = strings.TrimSpace(username)
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !ok || username == "" || domain == "" {
		return "", false
	}
	if strings.EqualFold(domain, s.cfg.LocalDomain) || strings.EqualFold(domain, s.cfg.WebDomain) {
		return "", false
	}
	return username + "@" + domain, true
}

func accountMatchesImportAcct(account *models.Account, acct string) bool {
	if account == nil {
		return false
	}
	username, domain, ok := strings.Cut(normalizeAcctInput(acct), "@")
	if !ok || username == "" || domain == "" || !account.Domain.Valid {
		return false
	}
	return strings.EqualFold(account.Username, username) && strings.EqualFold(account.Domain.String, domain)
}

func (s *Server) applyRelationshipImportRow(tx *gorm.DB, accountID int64, target *models.Account, importType string, values map[string]any, now time.Time) (bool, *asynqLocalNotificationPayload, error) {
	switch importType {
	case "following":
		showReblogs := importBool(values, "show_reblogs", true)
		notify := importBool(values, "notify", false)
		languages := importStringArray(values["languages"])
		needsCreate, limit, err := s.relationshipImportFollowCreateAllowed(tx, accountID, target.ID)
		if err != nil {
			return false, nil, err
		}
		if !needsCreate {
			if target.Locked {
				return true, nil, nil
			}
		} else if limit > 0 {
			return false, nil, errors.New(followLimitReachedMessage(limit))
		}
		if target.Locked {
			req := models.FollowRequest{CreatedAt: now, UpdatedAt: now, AccountID: accountID, TargetAccountID: target.ID, ShowReblogs: showReblogs, Notify: notify, Languages: models.StringArray(languages), URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&req)
			if res.Error != nil {
				return false, nil, res.Error
			}
			if res.RowsAffected == 0 {
				return true, nil, nil
			}
			return true, &asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: accountID, ActivityID: req.ID, ActivityType: "FollowRequest", Type: "follow_request"}, nil
		}
		follow := models.Follow{CreatedAt: now, UpdatedAt: now, AccountID: accountID, TargetAccountID: target.ID, ShowReblogs: showReblogs, Notify: notify, Languages: models.StringArray(languages), URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
		if res.Error != nil {
			return false, nil, res.Error
		}
		if res.RowsAffected == 0 {
			return true, nil, tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, target.ID).Updates(map[string]any{"show_reblogs": showReblogs, "notify": notify, "languages": models.StringArray(languages), "updated_at": now}).Error
		}
		if err := incrementAccountStatCounter(tx, accountID, accountStatCounterFollowing, 1); err != nil {
			return false, nil, err
		}
		if err := incrementAccountStatCounter(tx, target.ID, accountStatCounterFollowers, 1); err != nil {
			return false, nil, err
		}
		return true, &asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: accountID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"}, nil
	case "blocking":
		if _, err := s.createAccountBlock(tx, accountID, target.ID, now); err != nil {
			return false, nil, err
		}
		if err := afterBlockServiceCleanup(tx, accountID, target.ID); err != nil {
			return false, nil, err
		}
		return true, nil, nil
	case "muting":
		hideNotifications := importBool(values, "hide_notifications", true)
		if err := upsertAccountMute(tx, accountID, target.ID, hideNotifications, now); err != nil {
			return false, nil, err
		}
		if hideNotifications {
			if err := afterBlockServiceCleanup(tx, accountID, target.ID); err != nil {
				return false, nil, err
			}
		}
		return true, nil, nil
	}
	return false, nil, nil
}

func (s *Server) relationshipImportFollowCreateAllowed(tx *gorm.DB, accountID int64, targetAccountID int64) (bool, int64, error) {
	if tx == nil || accountID == 0 || targetAccountID == 0 {
		return false, 0, nil
	}
	var existingFollow int64
	if err := tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&existingFollow).Error; err != nil {
		return false, 0, err
	}
	if existingFollow > 0 {
		return false, 0, nil
	}
	var existingRequest int64
	if err := tx.Model(&models.FollowRequest{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&existingRequest).Error; err != nil {
		return false, 0, err
	}
	if existingRequest > 0 {
		return false, 0, nil
	}
	var account models.Account
	if err := tx.Preload("AccountStat").Select("id", "domain").Where("id = ?", accountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, 0, nil
		}
		return false, 0, err
	}
	reached, limit, err := s.followLimitReachedInDB(context.Background(), tx, account)
	if err != nil {
		return false, 0, err
	}
	if reached {
		return true, limit, nil
	}
	return true, 0, nil
}

func (s *Server) processDomainBlockImport(bulkImport models.BulkImport) error {
	now := time.Now().UTC()
	invalidateDomains := []string{}
	afterBlockDomains := []string{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rows []models.BulkImportRow
		if err := tx.Where("bulk_import_id = ?", bulkImport.ID).Order("id ASC").Find(&rows).Error; err != nil {
			return err
		}
		domains, successfulRowIDs := domainBlockImportDomains(rows)
		invalidateDomains = append(invalidateDomains, domains...)
		afterBlockDomains = append(afterBlockDomains, domains...)
		if err := tx.Model(&models.BulkImport{}).
			Where("id = ? AND state IN ?", bulkImport.ID, []int{bulkImportStateUnconfirmed, bulkImportStateScheduled}).
			Updates(map[string]any{"state": bulkImportStateInProgress, "updated_at": now}).Error; err != nil {
			return err
		}
		if bulkImport.Overwrite {
			query := tx.Where("account_id = ?", bulkImport.AccountID)
			var deleted []models.AccountDomainBlock
			if err := query.Find(&deleted).Error; err != nil {
				return err
			}
			for _, row := range deleted {
				invalidateDomains = append(invalidateDomains, string(row.Domain))
			}
			if err := query.Delete(&models.AccountDomainBlock{}).Error; err != nil {
				return err
			}
		}
		for _, domain := range domains {
			if _, err := createImportedDomainBlock(tx, bulkImport.AccountID, domain, now); err != nil {
				return err
			}
		}
		if len(successfulRowIDs) > 0 {
			if err := tx.Where("id IN ?", successfulRowIDs).Delete(&models.BulkImportRow{}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&models.BulkImport{}).
			Where("id = ?", bulkImport.ID).
			Updates(map[string]any{
				"state":           bulkImportStateFinished,
				"processed_items": bulkImport.TotalItems,
				"imported_items":  len(successfulRowIDs),
				"finished_at":     now,
				"updated_at":      now,
			}).Error
	})
	if err != nil {
		return err
	}
	s.clearDomainBlockFeedCaches(context.Background(), bulkImport.AccountID, invalidateDomains)
	s.invalidateAccountDomainBlockCaches(context.Background(), bulkImport.AccountID, invalidateDomains)
	for _, domain := range afterBlockDomains {
		if err := s.enqueueAfterAccountDomainBlockOrRun(context.Background(), bulkImport.AccountID, domain); err != nil {
			return err
		}
	}
	return nil
}

func createImportedDomainBlock(tx *gorm.DB, accountID int64, domain string, now time.Time) (bool, error) {
	var count int64
	if err := tx.Model(&models.AccountDomainBlock{}).Where("account_id = ? AND lower(domain) = ?", accountID, domain).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	block := models.AccountDomainBlock{AccountID: models.AccountDomainBlockAccountID(accountID), Domain: models.NullSafeString(domain), CreatedAt: now, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&block).Error; err != nil {
		return false, err
	}
	return true, nil
}

func domainBlockImportDomains(rows []models.BulkImportRow) ([]string, []int64) {
	domains := make([]string, 0, len(rows))
	successfulRowIDs := make([]int64, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		values := map[string]any{}
		if err := json.Unmarshal(row.Data, &values); err != nil {
			continue
		}
		domain, err := normalizeDomainBlockParam(fmt.Sprint(values["domain"]))
		if err != nil {
			continue
		}
		successfulRowIDs = append(successfulRowIDs, row.ID)
		if _, ok := seen[domain]; !ok {
			seen[domain] = struct{}{}
			domains = append(domains, domain)
		}
	}
	return domains, successfulRowIDs
}

func importBool(values map[string]any, key string, fallback bool) bool {
	value, ok := values[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return formBoolValue(typed)
	default:
		return formBoolValue(fmt.Sprint(typed))
	}
}

func importStringArray(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		if text == "" {
			return nil
		}
		parts := strings.Split(text, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if item := strings.TrimSpace(part); item != "" {
				out = append(out, item)
			}
		}
		return out
	}
}

func (s *Server) destroySettingsImport(c *echo.Context) error {
	account, _, _, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	if strings.EqualFold(c.FormValue("_method"), "delete") || c.Request().Method == http.MethodDelete {
		bulkImport, err := s.findBulkImport(account.ID, c.Param("id"), bulkImportStateUnconfirmed)
		if err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("bulk_import_id = ?", bulkImport.ID).Delete(&models.BulkImportRow{}).Error; err != nil {
				return err
			}
			return tx.Delete(&models.BulkImport{}, bulkImport.ID).Error
		}); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/settings/imports")
}

func (s *Server) settingsImportFailuresCSV(c *echo.Context) error {
	account, _, _, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	if !settingsImportFailuresCSVRequested(c) {
		return noContentError(http.StatusNotAcceptable)
	}
	bulkImport, err := s.findBulkImport(account.ID, c.Param("id"), bulkImportStateFinished)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	typeName := importNameByType[bulkImport.Type]
	headers := importFailureHeaders(typeName)
	var rows []models.BulkImportRow
	if err := s.db.Where("bulk_import_id = ?", bulkImport.ID).Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", csvBytes(importFailureFilename[typeName], headers, func(w *csv.Writer) error {
		for _, row := range rows {
			values := map[string]any{}
			_ = json.Unmarshal(row.Data, &values)
			if err := w.Write(importFailureRow(typeName, values)); err != nil {
				return err
			}
		}
		return nil
	}, c))
}

func settingsImportFailuresCSVRequested(c *echo.Context) bool {
	return settingsImportFailuresCSVRequestedFor(c.Request().URL.Path, c.Param("format"), c.Request().Header.Get("Accept"))
}

func settingsImportFailuresCSVRequestedFor(path string, format string, accept string) bool {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "csv" {
		return true
	}
	if strings.HasSuffix(path, ".csv") {
		return true
	}
	for _, part := range strings.Split(accept, ",") {
		mediaType, q, _ := parseAcceptEntry(part)
		if q <= 0 {
			continue
		}
		switch mediaType {
		case "text/csv", "application/csv", "text/comma-separated-values":
			return true
		}
	}
	return false
}

func (s *Server) recentBulkImports(accountID int64) ([]models.BulkImport, error) {
	if s.db == nil {
		return []models.BulkImport{}, nil
	}
	var imports []models.BulkImport
	err := s.db.Where("account_id = ?", accountID).Order("id DESC").Limit(10).Find(&imports).Error
	return imports, err
}

func (s *Server) findBulkImport(accountID int64, id string, state int) (*models.BulkImport, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var bulkImport models.BulkImport
	err := s.db.Where("id = ? AND account_id = ? AND state = ?", id, accountID, state).First(&bulkImport).Error
	return &bulkImport, err
}

func parseImportCSV(fileHeader *multipart.FileHeader, importType string) ([]map[string]any, error) {
	if fileHeader.Size > importFileSizeLimit {
		return nil, fmt.Errorf("CSV file is too large")
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseImportCSVReader(file, importType)
}

func parseImportCSVReader(reader io.Reader, importType string) ([]map[string]any, error) {
	return parseImportCSVReaderWithHeaderMode(reader, importType, false)
}

func parseLegacyImportCSVReader(reader io.Reader, importType string) ([]map[string]any, error) {
	return parseImportCSVReaderWithHeaderMode(reader, importType, true)
}

func parseImportCSVReaderWithHeaderMode(reader io.Reader, importType string, legacyHeaderHeuristic bool) ([]map[string]any, error) {
	expected, ok := importExpectedHeaders[importType]
	if !ok {
		return nil, fmt.Errorf("Import type is invalid")
	}
	csvReader := csv.NewReader(reader)
	csvReader.FieldsPerRecord = -1
	first, err := readNextImportCSVRecord(csvReader)
	if err == io.EOF {
		return nil, fmt.Errorf("CSV file is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("CSV file is invalid: %w", err)
	}

	headers := first
	pendingRecord := []string(nil)
	if !importCSVLooksHeadered(headers, legacyHeaderHeuristic) {
		headers = importDefaultHeaders[importType]
		pendingRecord = first
	}
	if !legacyHeaderHeuristic && !containsAll(headers, importDefaultHeaders[importType]) {
		return nil, fmt.Errorf("CSV file is incompatible with the selected import type")
	}

	out := []map[string]any{}
	if pendingRecord != nil {
		out = append(out, importRecordToRow(headers, expected, pendingRecord))
	}
	for {
		record, err := readNextImportCSVRecord(csvReader)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("CSV file is invalid: %w", err)
		}
		out = append(out, importRecordToRow(headers, expected, record))
		if len(out) > importRowsProcessingLimit {
			return nil, fmt.Errorf("CSV file has more than %d rows", importRowsProcessingLimit)
		}
	}
	return out, nil
}

func readNextImportCSVRecord(reader *csv.Reader) ([]string, error) {
	for {
		record, err := reader.Read()
		if err != nil {
			return nil, err
		}
		blank := true
		for _, field := range record {
			if strings.TrimSpace(field) != "" {
				blank = false
				break
			}
		}
		if !blank {
			return record, nil
		}
	}
}

func importRecordToRow(headers []string, expected []string, record []string) map[string]any {
	raw := map[string]string{}
	for i, header := range headers {
		if i < len(record) {
			raw[header] = record[i]
		}
	}
	row := map[string]any{}
	for _, header := range expected {
		attr := importHeaderAttribute[header]
		row[attr] = convertImportCSVField(header, raw[header])
	}
	return row
}

func convertImportCSVField(header string, value string) any {
	value = strings.TrimSpace(value)
	switch header {
	case "Show boosts", "Notify on new posts", "Hide notifications":
		return formBoolValue(value)
	case "Languages":
		if value == "" {
			return nil
		}
		parts := strings.Split(value, ",")
		languages := make([]string, 0, len(parts))
		for _, part := range parts {
			if item := strings.TrimSpace(part); item != "" {
				languages = append(languages, item)
			}
		}
		if len(languages) == 0 {
			return nil
		}
		return languages
	case "Account address":
		return strings.TrimPrefix(value, "@")
	default:
		return value
	}
}

func guessedImportType(filename string, rows []map[string]any, selected string) string {
	lower := strings.ToLower(filepath.Base(filename))
	switch {
	case strings.HasPrefix(lower, "follows") || strings.HasPrefix(lower, "following_accounts"):
		return "following"
	case strings.HasPrefix(lower, "blocks") || strings.HasPrefix(lower, "blocked_accounts"):
		return "blocking"
	case strings.HasPrefix(lower, "mutes") || strings.HasPrefix(lower, "muted_accounts"):
		return "muting"
	case strings.HasPrefix(lower, "domain_blocks") || strings.HasPrefix(lower, "blocked_domains"):
		return "domain_blocking"
	case strings.HasPrefix(lower, "bookmarks"):
		return "bookmarks"
	case strings.HasPrefix(lower, "lists"):
		return "lists"
	}
	if selected == "following" || selected == "muting" {
		for _, row := range rows {
			if _, ok := row["hide_notifications"]; ok {
				return "muting"
			}
			if _, ok := row["show_reblogs"]; ok {
				return "following"
			}
			if _, ok := row["notify"]; ok {
				return "following"
			}
			if _, ok := row["languages"]; ok {
				return "following"
			}
		}
	}
	return ""
}

func importCSVLooksHeadered(headers []string, legacyHeaderHeuristic bool) bool {
	if len(headers) == 0 {
		return false
	}
	if legacyHeaderHeuristic {
		return strings.Contains(strings.TrimSpace(headers[0]), " ")
	}
	return importKnownFirstHeader(headers[0])
}

func importKnownFirstHeader(header string) bool {
	for _, expected := range importExpectedHeaders {
		if len(expected) > 0 && expected[0] == header {
			return true
		}
	}
	return false
}

func containsAll(headers []string, required []string) bool {
	seen := map[string]bool{}
	for _, header := range headers {
		seen[header] = true
	}
	for _, header := range required {
		if !seen[header] {
			return false
		}
	}
	return true
}

func importFailureHeaders(typeName string) [][]string {
	switch typeName {
	case "following":
		return [][]string{{"Account address", "Show boosts", "Notify on new posts", "Languages"}}
	case "muting":
		return [][]string{{"Account address", "Hide notifications"}}
	default:
		return nil
	}
}

func importFailureRow(typeName string, values map[string]any) []string {
	stringValue := func(key string) string {
		if value, ok := values[key].(string); ok {
			return value
		}
		return ""
	}
	boolValue := func(key string, fallback bool) string {
		value, ok := values[key]
		if !ok {
			return strconv.FormatBool(fallback)
		}
		if boolValue, ok := value.(bool); ok {
			return strconv.FormatBool(boolValue)
		}
		return strconv.FormatBool(formBoolValue(fmt.Sprint(value)))
	}
	languageValue := func() string {
		value, ok := values["languages"]
		if !ok || value == nil {
			return ""
		}
		items, ok := value.([]any)
		if !ok {
			return fmt.Sprint(value)
		}
		languages := make([]string, 0, len(items))
		for _, item := range items {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				languages = append(languages, text)
			}
		}
		return strings.Join(languages, ", ")
	}
	switch typeName {
	case "following":
		return []string{stringValue("acct"), boolValue("show_reblogs", true), boolValue("notify", false), languageValue()}
	case "blocking":
		return []string{stringValue("acct")}
	case "muting":
		return []string{stringValue("acct"), boolValue("hide_notifications", true)}
	case "domain_blocking":
		return []string{stringValue("domain")}
	case "bookmarks":
		return []string{stringValue("uri")}
	case "lists":
		return []string{stringValue("list_name"), stringValue("acct")}
	default:
		return nil
	}
}

func importsHTML(imports []models.BulkImport, noticeText string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArg(localeAndTheme...)
	applicationName := settingsApplicationNameArg(localeAndTheme)
	var rows strings.Builder
	for _, bulkImport := range imports {
		rows.WriteString(`<tr><td>`)
		rows.WriteString(html.EscapeString(importTypeLabel(loc, importNameByType[bulkImport.Type])))
		rows.WriteString(`</td><td>`)
		state := html.EscapeString(importStateName(bulkImport.State, loc))
		if bulkImport.State == bulkImportStateUnconfirmed {
			rows.WriteString(`<a href="/settings/imports/` + strconv.FormatInt(bulkImport.ID, 10) + `">` + state + `</a>`)
		} else {
			rows.WriteString(state)
		}
		rows.WriteString(`</td><td>`)
		rows.WriteString(strconv.Itoa(bulkImport.ImportedItems))
		rows.WriteString(`/`)
		rows.WriteString(strconv.Itoa(bulkImport.TotalItems))
		rows.WriteString(`</td><td>`)
		stamp := bulkImport.CreatedAt.UTC().Format(time.RFC3339)
		rows.WriteString(`<time class="formatted" datetime="` + html.EscapeString(stamp) + `">` + html.EscapeString(bulkImport.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")) + `</time>`)
		rows.WriteString(`</td><td>`)
		failed := bulkImport.ProcessedItems - bulkImport.ImportedItems
		if failed > 0 {
			failedText := strconv.Itoa(failed)
			if bulkImport.State == bulkImportStateFinished {
				rows.WriteString(`<a href="/settings/imports/` + strconv.FormatInt(bulkImport.ID, 10) + `/failures.csv">` + failedText + `</a>`)
			} else {
				rows.WriteString(failedText)
			}
		}
		rows.WriteString(`</td></tr>`)
	}
	recentImportsHTML := ""
	if len(imports) > 0 {
		recentImportsHTML = `
    <hr class="spacer">
    <h3>` + html.EscapeString(settingsT(loc, "imports.recent_imports", "Recent imports")) + `</h3>
    <div class="table-wrapper"><table class="table">
      <thead><tr><th>` + html.EscapeString(settingsT(loc, "imports.type", "Import type")) + `</th><th>` + html.EscapeString(settingsT(loc, "imports.status", "Status")) + `</th><th>` + html.EscapeString(settingsT(loc, "imports.imported", "Imported")) + `</th><th>` + html.EscapeString(settingsT(loc, "imports.time_started", "Started")) + `</th><th>` + html.EscapeString(settingsT(loc, "imports.failures", "Failures")) + `</th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table></div>`
	}
	required := filterRequiredMarker(loc)
	dataHint := strings.ReplaceAll(settingsT(loc, "simple_form.hints.imports.data", "CSV file exported from another Mastodon server"), "Mastodon", applicationName)
	return authPageHTML(settingsT(loc, "settings.import", "Import"), noticeText, errorText, `
	<form class="simple_form new_form_import" id="new_form_import" novalidate="novalidate" method="post" action="/settings/imports" enctype="multipart/form-data">
	  <div class="field-group"><div class="input with_block_label grouped_select required form_import_type field_with_hint"><label class="grouped_select required" for="form_import_type">`+html.EscapeString(settingsT(loc, "imports.type", "Import type"))+required+`</label><span class="hint">`+html.EscapeString(settingsT(loc, "imports.preface", "You can import data that you have exported from another server."))+`</span><div class="label_input"><select class="grouped_select required" id="form_import_type" name="form_import[type]"><optgroup label="`+html.EscapeString(settingsT(loc, "imports.type_groups.constructive", "Import"))+`"><option value="following">`+html.EscapeString(importTypeLabel(loc, "following"))+`</option><option value="bookmarks">`+html.EscapeString(importTypeLabel(loc, "bookmarks"))+`</option><option value="lists">`+html.EscapeString(importTypeLabel(loc, "lists"))+`</option></optgroup><optgroup label="`+html.EscapeString(settingsT(loc, "imports.type_groups.destructive", "Block and mute"))+`"><option value="muting">`+html.EscapeString(importTypeLabel(loc, "muting"))+`</option><option value="blocking">`+html.EscapeString(importTypeLabel(loc, "blocking"))+`</option><option value="domain_blocking">`+html.EscapeString(importTypeLabel(loc, "domain_blocking"))+`</option></optgroup></select></div></div></div>
	  <div class="fields-row"><div class="fields-group fields-row__column fields-row__column-6"><div class="input with_block_label file required form_import_data field_with_hint"><label class="file required" for="form_import_data">`+html.EscapeString(settingsT(loc, "simple_form.labels.defaults.data", "Data"))+required+`</label><span class="hint">`+html.EscapeString(dataHint)+`</span><div class="label_input"><input class="file required" type="file" name="form_import[data]" id="form_import_data"></div></div></div>
	  <div class="fields-group fields-row__column fields-row__column-6"><div class="input radio_buttons optional form_import_mode"><ul><input type="hidden" name="form_import[mode]" value=""><li class="radio"><label for="form_import_mode_merge"><input class="radio_buttons optional" type="radio" value="merge" checked name="form_import[mode]" id="form_import_mode_merge">`+html.EscapeString(settingsT(loc, "imports.modes.merge", "Merge"))+`<span class="hint">`+html.EscapeString(settingsT(loc, "imports.modes.merge_long", "Keep existing records and add new ones"))+`</span></label></li><li class="radio"><label for="form_import_mode_overwrite"><input class="radio_buttons optional" type="radio" value="overwrite" name="form_import[mode]" id="form_import_mode_overwrite">`+html.EscapeString(settingsT(loc, "imports.modes.overwrite", "Overwrite"))+`<span class="hint">`+html.EscapeString(settingsT(loc, "imports.modes.overwrite_long", "Replace existing records"))+`</span></label></li></ul></div></div></div>
	  <div class="actions"><button name="button" type="submit" class="button">`+html.EscapeString(settingsT(loc, "imports.upload", "Upload"))+`</button></div>
    </form>`+recentImportsHTML, localeAndTheme...)
}

func importConfirmHTML(bulkImport models.BulkImport, localeAndTheme ...string) string {
	loc := settingsLocaleArg(localeAndTheme...)
	warning := ""
	if bulkImport.LikelyMismatched {
		warning = `<div class="flash-message warning">` + html.EscapeString(settingsT(loc, "imports.mismatched_types_warning", "This CSV looks like a different import type.")) + `</div>`
	}
	typeName := importNameByType[bulkImport.Type]
	return authPageHTML(settingsT(loc, "imports.titles."+typeName, "Confirm import"), "", "", `
    `+warning+`
    <p class="hint">`+importPreambleHTML(loc, typeName, bulkImport)+`</p>
	<div class="simple_form"><div class="actions"><a class="button button-tertiary" href="/settings/imports/`+strconv.FormatInt(bulkImport.ID, 10)+`" data-method="delete">`+html.EscapeString(settingsT(loc, "generic.cancel", "Cancel"))+`</a><a class="button" href="/settings/imports/`+strconv.FormatInt(bulkImport.ID, 10)+`/confirm" data-method="post">`+html.EscapeString(settingsT(loc, "generic.confirm", "Confirm"))+`</a></div></div>`, localeAndTheme...)
}

func importStateName(state int, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	switch state {
	case 0:
		return settingsT(loc, "imports.states.unconfirmed", "Unconfirmed")
	case 1:
		return settingsT(loc, "imports.states.scheduled", "Scheduled")
	case 2:
		return settingsT(loc, "imports.states.in_progress", "In progress")
	case 3:
		return settingsT(loc, "imports.states.finished", "Finished")
	default:
		return strconv.Itoa(state)
	}
}

func importModeName(overwrite bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	if overwrite {
		return settingsT(loc, "imports.modes.overwrite", "Overwrite")
	}
	return settingsT(loc, "imports.modes.merge", "Merge")
}

func importTypeLabel(locale string, typeName string) string {
	return settingsT(locale, "imports.types."+typeName, typeName)
}

func importPreambleHTML(locale string, typeName string, bulkImport models.BulkImport) string {
	group := "preambles"
	if bulkImport.Overwrite {
		group = "overwrite_preambles"
	}
	key := "imports." + group + "." + typeName + "_html"
	value := webT(locale, key, map[string]string{
		"filename":    html.EscapeString(firstNonEmpty(bulkImport.OriginalFilename, "CSV")),
		"total_items": strconv.Itoa(bulkImport.TotalItems),
	})
	if value == key || strings.TrimSpace(value) == "" {
		return html.EscapeString(settingsT(locale, "imports.preface", "You can import data that you have exported from another server."))
	}
	return value
}

func importErrorText(locale string, err error) string {
	text := err.Error()
	switch {
	case text == "CSV file is too large":
		return settingsT(locale, "imports.errors.too_large", "File is too large")
	case text == "Import type is invalid":
		return settingsT(locale, "imports.errors.invalid_type", "Import type is invalid")
	case text == "CSV file is empty":
		return settingsT(locale, "imports.errors.empty", "Empty CSV file")
	case text == "CSV file is incompatible with the selected import type":
		return settingsT(locale, "imports.errors.incompatible_type", "Incompatible with the selected import type")
	case strings.HasPrefix(text, "CSV file has more than "):
		return webT(locale, "imports.errors.over_rows_processing_limit", map[string]string{"count": strconv.Itoa(importRowsProcessingLimit)})
	case strings.HasPrefix(text, "CSV file is invalid: "):
		return webT(locale, "imports.errors.invalid_csv_file", map[string]string{"error": strings.TrimPrefix(text, "CSV file is invalid: ")})
	default:
		return text
	}
}
