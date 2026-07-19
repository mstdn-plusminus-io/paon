package api

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var customEmojiShortcodePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{2,}$`)

const customEmojiMaxBytes = 256 * 1024

var (
	errCustomEmojiImageSizeInvalid = errors.New("custom emoji image size is invalid")
	errCustomEmojiImageTypeInvalid = errors.New("custom emoji image type is invalid")
	errCustomEmojiImageUnreadable  = errors.New("custom emoji image is unreadable")
)

func (s *Server) adminCustomEmojisPage(c *echo.Context) error {
	user, handled, err := s.requireAdminCustomEmojisWebUser(c)
	if handled || err != nil {
		return err
	}
	emojis, err := s.adminCustomEmojiModels(c)
	if err != nil {
		return err
	}
	categories, err := s.adminCustomEmojiCategories()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminCustomEmojisHTMLWithConfig(s.cfg, emojis, categories, c.QueryParam("notice"), c.QueryParam("error"), adminCustomEmojiFilters{
		Page:      adminTrendsPageValue(c),
		Local:     c.QueryParam("local"),
		Remote:    c.QueryParam("remote"),
		Shortcode: c.QueryParam("shortcode"),
		ByDomain:  c.QueryParam("by_domain"),
	}, s.webLocale(c, user)))
}

func (s *Server) newAdminCustomEmojiPage(c *echo.Context) error {
	user, handled, err := s.requireAdminCustomEmojisWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminCustomEmojiFormHTML(c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminCustomEmojiWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminCustomEmojisWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	rootPresent, err := requestHasNestedFormOrFilePrefix(c.Request(), "custom_emoji", 8<<20)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if !rootPresent {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	shortcode := c.FormValue("custom_emoji[shortcode]")
	if !customEmojiShortcodePattern.MatchString(shortcode) {
		return c.HTML(http.StatusOK, adminCustomEmojiFormHTMLWithShortcode(shortcode, adminCustomEmojiMessage(locale, "errors.shortcode_invalid", "Shortcode is invalid"), locale))
	}
	file, err := c.FormFile("custom_emoji[image]")
	if err != nil {
		return c.HTML(http.StatusOK, adminCustomEmojiFormHTMLWithShortcode(shortcode, adminCustomEmojiMessage(locale, "errors.image_required", "Image is required"), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminCustomEmojiFormHTMLWithShortcode(shortcode, adminCustomEmojiMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	emoji, err := s.createLocalCustomEmoji(shortcode, file, adminCustomEmojiVisibleInPickerFromForm(c.Request()))
	if err != nil {
		if isUniqueConstraintError(err) {
			return c.HTML(http.StatusOK, adminCustomEmojiFormHTMLWithShortcode(shortcode, adminCustomEmojiMessage(locale, "errors.shortcode_taken", "Shortcode has already been taken"), locale))
		}
		if message, ok := adminCustomEmojiImageValidationMessage(locale, err); ok {
			return c.HTML(http.StatusOK, adminCustomEmojiFormHTMLWithShortcode(shortcode, message, locale))
		}
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "create", customEmojiAuditLogTarget(emoji), time.Now().UTC()); err != nil {
		return err
	}
	s.invalidateCustomEmojiEntityCaches(c.Request().Context(), []models.CustomEmoji{emoji})
	return c.Redirect(http.StatusFound, "/admin/custom_emojis?notice="+url.QueryEscape(adminCustomEmojiMessage(locale, "created_msg", "Emoji successfully created!")))
}

func adminCustomEmojiVisibleInPickerFromForm(req *http.Request) bool {
	if req == nil {
		return false
	}
	if len(req.Form) == 0 {
		_ = req.ParseMultipartForm(8 << 20)
		if len(req.Form) == 0 {
			_ = req.ParseForm()
		}
	}
	return truthy(lastFormValue(req.Form, "custom_emoji[visible_in_picker]"))
}

func (s *Server) batchAdminCustomEmojisWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminCustomEmojisWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if !adminBatchFormRootPresent(c, "form_custom_emoji_batch") {
		return c.Redirect(http.StatusFound, adminCustomEmojiBatchRedirectURL(c, "error", adminCustomEmojiMessage(locale, "no_emoji_selected", "No emojis were changed as none were selected")))
	}
	action := adminCustomEmojiBatchAction(c)
	if action == "" {
		return c.Redirect(http.StatusFound, adminCustomEmojiBatchRedirectURL(c, "", ""))
	}
	ids := parseAdminCustomEmojiIDs(c)
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, adminCustomEmojiBatchRedirectURL(c, "error", adminCustomEmojiMessage(locale, "no_emoji_selected", "No emojis were changed as none were selected")))
	}
	if err := s.applyAdminCustomEmojiBatch(c, ids, action, user.AccountID); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, adminCustomEmojiBatchRedirectURL(c, "", ""))
}

func (s *Server) requireAdminCustomEmojisWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageCustomEmojis) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.custom_emojis.title", "Custom emojis"), "", adminT(locale, "admin.custom_emojis.not_permitted", "You are not allowed to manage custom emojis."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminCustomEmojiModels(c *echo.Context) ([]models.CustomEmoji, error) {
	if s.db == nil {
		return []models.CustomEmoji{}, nil
	}
	query := s.db.Preload("Category").Model(&models.CustomEmoji{})
	if adminCustomEmojiFilterPresent(c, "local") {
		query = query.Where("domain IS NULL").Joins("LEFT JOIN custom_emoji_categories ON custom_emoji_categories.id = custom_emojis.category_id").Order("custom_emoji_categories.name ASC NULLS FIRST, custom_emojis.shortcode ASC")
		if adminCustomEmojiFilterPresent(c, "remote") {
			query = query.Where("domain IS NOT NULL")
		}
	} else if adminCustomEmojiFilterPresent(c, "remote") {
		query = query.Where("domain IS NOT NULL").Order("domain ASC, shortcode ASC")
	} else {
		query = query.Order("domain ASC, shortcode ASC")
	}
	if domain := strings.TrimSpace(c.QueryParam("by_domain")); domain != "" {
		query = query.Where("lower(domain) = ?", strings.ToLower(domain))
	}
	if shortcode := strings.TrimSpace(c.QueryParam("shortcode")); shortcode != "" {
		query = query.Where("shortcode ILIKE ?", "%"+shortcode+"%")
	}
	var emojis []models.CustomEmoji
	err := query.Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&emojis).Error
	return emojis, err
}

func adminCustomEmojiFilterPresent(c *echo.Context, key string) bool {
	return strings.TrimSpace(c.QueryParam(key)) != ""
}

func (s *Server) adminCustomEmojiCategories() ([]models.CustomEmojiCategory, error) {
	if s.db == nil {
		return []models.CustomEmojiCategory{}, nil
	}
	var categories []models.CustomEmojiCategory
	err := s.db.Order("name ASC").Find(&categories).Error
	return categories, err
}

func (s *Server) createLocalCustomEmoji(shortcode string, file *multipart.FileHeader, visible bool) (models.CustomEmoji, error) {
	filename := paperclipObfuscatedFilename(file.Filename)
	contentType := mediaContentType(filename, file.Header.Get("Content-Type"))
	if file.Size <= 0 || file.Size >= customEmojiMaxBytes {
		return models.CustomEmoji{}, errCustomEmojiImageSizeInvalid
	}
	if !customEmojiContentTypeAllowed(contentType) {
		return models.CustomEmoji{}, errCustomEmojiImageTypeInvalid
	}
	if _, err := imageConfigFromHeader(file); err != nil {
		return models.CustomEmoji{}, errCustomEmojiImageUnreadable
	}
	now := time.Now().UTC()
	emoji := models.CustomEmoji{
		Shortcode:                 shortcode,
		VisibleInPicker:           visible,
		CreatedAt:                 now,
		UpdatedAt:                 now,
		ImageFileName:             sql.NullString{String: filename, Valid: true},
		ImageContentType:          sql.NullString{String: contentType, Valid: true},
		ImageFileSize:             sql.NullInt64{Int64: file.Size, Valid: true},
		ImageUpdatedAt:            sql.NullTime{Time: now, Valid: true},
		ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}
	if err := s.db.Create(&emoji).Error; err != nil {
		return emoji, err
	}
	if err := s.storeCustomEmojiFile(emoji, file); err != nil {
		_ = s.db.Delete(&emoji).Error
		return emoji, err
	}
	return emoji, nil
}

func adminCustomEmojiImageValidationMessage(locale string, err error) (string, bool) {
	if errors.Is(err, errCustomEmojiImageSizeInvalid) || errors.Is(err, errCustomEmojiImageTypeInvalid) || errors.Is(err, errCustomEmojiImageUnreadable) {
		return adminCustomEmojiMessage(locale, "errors.image_invalid", "Image is invalid"), true
	}
	return "", false
}

func customEmojiContentTypeAllowed(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func (s *Server) storeCustomEmojiFile(emoji models.CustomEmoji, file *multipart.FileHeader) error {
	if !emoji.ImageFileName.Valid || emoji.ImageFileName.String == "" {
		return errors.New("custom emoji image filename is blank")
	}
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dir := s.cfg.SystemAssetPath("custom_emojis", "images", adminPaperclipIDPartition(emoji.ID), "original")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	original := filepath.Join(dir, filepath.Base(emoji.ImageFileName.String))
	dst, err := os.Create(original)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	if err := s.uploadPaperclipObject(context.Background(), customEmojiObjectKey(emoji, "original", emoji.ImageFileName.String), original, emoji.ImageContentType.String); err != nil {
		return err
	}
	return s.generateCustomEmojiStaticFile(emoji.ID, original, emoji.ImageFileName.String)
}

func (s *Server) generateCustomEmojiStaticFile(id int64, original string, filename string) error {
	target := s.customEmojiImagePath(id, "static", customEmojiStaticFilename(filename))
	if err := writeVIPSStaticPNG(original, target); err != nil {
		return err
	}
	emoji := models.CustomEmoji{
		ID:                        id,
		Domain:                    sql.NullString{},
		ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}
	return s.uploadPaperclipObject(context.Background(), customEmojiObjectKey(emoji, "static", customEmojiStaticFilename(filename)), target, "image/png")
}

func (s *Server) customEmojiImagePath(id int64, style string, filename string) string {
	return s.cfg.SystemAssetPath("custom_emojis", "images", adminPaperclipIDPartition(id), style, filename)
}

func customEmojiStaticFilename(filename string) string {
	base := filename
	if index := strings.LastIndex(base, "."); index > 0 {
		base = base[:index]
	}
	return base + ".png"
}

func (s *Server) applyAdminCustomEmojiBatch(c *echo.Context, ids []int64, action string, actorAccountID int64) error {
	if s.db == nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	var changed []models.CustomEmoji
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if action == "copy" {
			copied, err := s.copyAdminCustomEmojis(tx, ids, actorAccountID, now)
			if err != nil {
				return err
			}
			changed = append(changed, copied...)
			return nil
		}
		var emojis []models.CustomEmoji
		if err := tx.Where("id IN ?", ids).Find(&emojis).Error; err != nil {
			return err
		}
		categoryIDsBefore := customEmojiCategoryIDs(emojis)
		switch action {
		case "list":
			return updateAdminCustomEmojisWithAudit(tx, emojis, actorAccountID, "update", map[string]any{"visible_in_picker": true, "updated_at": now}, now, &changed)
		case "unlist":
			return updateAdminCustomEmojisWithAudit(tx, emojis, actorAccountID, "update", map[string]any{"visible_in_picker": false, "updated_at": now}, now, &changed)
		case "enable":
			return updateAdminCustomEmojisWithAudit(tx, emojis, actorAccountID, "enable", map[string]any{"disabled": false, "updated_at": now}, now, &changed)
		case "disable":
			return updateAdminCustomEmojisWithAudit(tx, emojis, actorAccountID, "disable", map[string]any{"disabled": true, "updated_at": now}, now, &changed)
		case "delete":
			for _, emoji := range emojis {
				s.removeCustomEmojiLocalFiles(emoji)
				if err := tx.Delete(&emoji).Error; err != nil {
					return err
				}
				if err := logAdminAction(tx, actorAccountID, "destroy", customEmojiAuditLogTarget(emoji), now); err != nil {
					return err
				}
				changed = append(changed, emoji)
			}
			return cleanupUnusedCustomEmojiCategories(tx, categoryIDsBefore)
		case "update":
			categoryID, err := s.adminCustomEmojiBatchCategory(tx, c)
			if err != nil {
				return err
			}
			if err := updateAdminCustomEmojisWithAudit(tx, emojis, actorAccountID, "update", map[string]any{"category_id": categoryID, "updated_at": now}, now, &changed); err != nil {
				return err
			}
			return cleanupUnusedCustomEmojiCategories(tx, categoryIDsBefore)
		default:
			return nil
		}
	}); err != nil {
		return err
	}
	s.invalidateCustomEmojiEntityCaches(c.Request().Context(), changed)
	return nil
}

func updateAdminCustomEmojisWithAudit(tx *gorm.DB, emojis []models.CustomEmoji, actorAccountID int64, action string, updates map[string]any, at time.Time, changed *[]models.CustomEmoji) error {
	for _, emoji := range emojis {
		if err := tx.Model(&models.CustomEmoji{}).Where("id = ?", emoji.ID).Updates(updates).Error; err != nil {
			return err
		}
		if err := logAdminAction(tx, actorAccountID, action, customEmojiAuditLogTarget(emoji), at); err != nil {
			return err
		}
		if changed != nil {
			*changed = append(*changed, emoji)
		}
	}
	return nil
}

func customEmojiCategoryIDs(emojis []models.CustomEmoji) []int64 {
	seen := map[int64]struct{}{}
	ids := make([]int64, 0, len(emojis))
	for _, emoji := range emojis {
		if !emoji.CategoryID.Valid || emoji.CategoryID.Int64 <= 0 {
			continue
		}
		if _, ok := seen[emoji.CategoryID.Int64]; ok {
			continue
		}
		seen[emoji.CategoryID.Int64] = struct{}{}
		ids = append(ids, emoji.CategoryID.Int64)
	}
	return ids
}

func cleanupUnusedCustomEmojiCategories(tx *gorm.DB, categoryIDs []int64) error {
	if len(categoryIDs) == 0 {
		return nil
	}
	return tx.Exec(`
DELETE FROM custom_emoji_categories
WHERE id IN ?
  AND NOT EXISTS (
    SELECT 1
    FROM custom_emojis
    WHERE custom_emojis.category_id = custom_emoji_categories.id
  )
`, categoryIDs).Error
}

func (s *Server) adminCustomEmojiBatchCategory(tx *gorm.DB, c *echo.Context) (sql.NullInt64, error) {
	if raw := strings.TrimSpace(c.FormValue("form_custom_emoji_batch[category_id]")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return sql.NullInt64{}, nil
		}
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	name := c.FormValue("form_custom_emoji_batch[category_name]")
	if strings.TrimSpace(name) == "" {
		return sql.NullInt64{}, nil
	}
	now := time.Now().UTC()
	category := models.CustomEmojiCategory{Name: models.CustomEmojiCategoryName(name), CreatedAt: now, UpdatedAt: now}
	if err := tx.Where("name = ?", name).FirstOrCreate(&category).Error; err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: category.ID, Valid: true}, nil
}

func (s *Server) copyAdminCustomEmojis(tx *gorm.DB, ids []int64, actorAccountID int64, at time.Time) ([]models.CustomEmoji, error) {
	var emojis []models.CustomEmoji
	if err := tx.Where("id IN ?", ids).Find(&emojis).Error; err != nil {
		return nil, err
	}
	copied := make([]models.CustomEmoji, 0, len(emojis))
	for _, emoji := range emojis {
		if emoji.Local() {
			continue
		}
		copy := models.CustomEmoji{}
		created := false
		err := tx.Where("domain IS NULL AND shortcode = ?", emoji.Shortcode).First(&copy).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			copy = models.CustomEmoji{
				Shortcode:       emoji.Shortcode,
				VisibleInPicker: true,
				CreatedAt:       at,
			}
			created = true
		} else if err != nil {
			return nil, err
		}
		previous := copy

		copy.ImageFileName = emoji.ImageFileName
		copy.ImageContentType = emoji.ImageContentType
		copy.ImageFileSize = emoji.ImageFileSize
		copy.ImageUpdatedAt = emoji.ImageUpdatedAt
		copy.ImageRemoteURL = emoji.ImageRemoteURL
		copy.ImageStorageSchemaVersion = emoji.ImageStorageSchemaVersion
		copy.UpdatedAt = at
		if created {
			if err := tx.Create(&copy).Error; err != nil {
				if isUniqueConstraintError(err) {
					continue
				}
				return nil, err
			}
		} else if err := tx.Model(&models.CustomEmoji{}).Where("id = ?", copy.ID).Updates(map[string]any{
			"image_file_name":              copy.ImageFileName,
			"image_content_type":           copy.ImageContentType,
			"image_file_size":              copy.ImageFileSize,
			"image_updated_at":             copy.ImageUpdatedAt,
			"image_remote_url":             copy.ImageRemoteURL,
			"image_storage_schema_version": copy.ImageStorageSchemaVersion,
			"updated_at":                   copy.UpdatedAt,
		}).Error; err != nil {
			return nil, err
		}
		if err := logAdminAction(tx, actorAccountID, "create", customEmojiAuditLogTarget(copy), at); err != nil {
			return nil, err
		}
		if copy.ID != emoji.ID {
			if !created {
				s.removeReplacedCustomEmojiFiles(previous, copy)
			}
			if err := s.copyCustomEmojiFiles(emoji, copy); err != nil {
				return nil, err
			}
		}
		copied = append(copied, copy)
	}
	return copied, nil
}

func (s *Server) removeReplacedCustomEmojiFiles(previous models.CustomEmoji, next models.CustomEmoji) {
	if s == nil || previous.ID == 0 || !previous.ImageFileName.Valid || strings.TrimSpace(previous.ImageFileName.String) == "" {
		return
	}
	if !next.ImageFileName.Valid {
		next.ImageFileName = sql.NullString{}
	}
	previousOriginal := previous.ImageFileName.String
	nextOriginal := ""
	if next.ImageFileName.Valid {
		nextOriginal = next.ImageFileName.String
	}
	if previousOriginal != nextOriginal {
		_ = os.Remove(s.customEmojiImagePathFor(previous, "original", previousOriginal))
		s.deletePaperclipObject(context.Background(), customEmojiObjectKey(previous, "original", previousOriginal))
	}
	previousStatic := customEmojiStaticFilename(previousOriginal)
	nextStatic := ""
	if nextOriginal != "" {
		nextStatic = customEmojiStaticFilename(nextOriginal)
	}
	if previousStatic != nextStatic {
		_ = os.Remove(s.customEmojiImagePathFor(previous, "static", previousStatic))
		s.deletePaperclipObject(context.Background(), customEmojiObjectKey(previous, "static", previousStatic))
	}
}

func (s *Server) copyCustomEmojiFiles(source models.CustomEmoji, target models.CustomEmoji) error {
	if !source.ImageFileName.Valid || source.ImageFileName.String == "" || !target.ImageFileName.Valid || target.ImageFileName.String == "" {
		return nil
	}
	originalSource := s.customEmojiImagePathFor(source, "original", source.ImageFileName.String)
	originalTarget := s.customEmojiImagePathFor(target, "original", target.ImageFileName.String)
	if _, err := os.Stat(originalSource); err == nil {
		if err := copyFile(originalSource, originalTarget); err != nil {
			return err
		}
		if err := s.uploadPaperclipObject(context.Background(), customEmojiObjectKey(target, "original", target.ImageFileName.String), originalTarget, target.ImageContentType.String); err != nil {
			return err
		}
	}
	staticSource := s.customEmojiImagePathFor(source, "static", customEmojiStaticFilename(source.ImageFileName.String))
	staticTarget := s.customEmojiImagePathFor(target, "static", customEmojiStaticFilename(target.ImageFileName.String))
	if _, err := os.Stat(staticSource); err == nil {
		if err := copyFile(staticSource, staticTarget); err != nil {
			return err
		}
		return s.uploadPaperclipObject(context.Background(), customEmojiObjectKey(target, "static", customEmojiStaticFilename(target.ImageFileName.String)), staticTarget, "image/png")
	}
	if _, err := os.Stat(originalTarget); err == nil {
		return s.generateCustomEmojiStaticFile(target.ID, originalTarget, target.ImageFileName.String)
	}
	return nil
}

func (s *Server) customEmojiImagePathFor(emoji models.CustomEmoji, style string, filename string) string {
	prefix := "custom_emojis"
	if !emoji.Local() && emoji.ImageStorageSchemaVersion.Valid && emoji.ImageStorageSchemaVersion.Int64 >= 1 {
		prefix = filepath.Join("cache", "custom_emojis")
	}
	return s.cfg.SystemAssetPath(prefix, "images", adminPaperclipIDPartition(emoji.ID), style, filename)
}

func copyFile(source string, target string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	dst, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func parseAdminCustomEmojiIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["form_custom_emoji_batch[custom_emoji_ids][]"]
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func adminCustomEmojiBatchAction(c *echo.Context) string {
	for _, action := range []string{"update", "list", "unlist", "enable", "disable", "copy", "delete"} {
		if adminBatchFormParamExists(c, action) {
			return action
		}
	}
	return ""
}

func adminCustomEmojiBatchRedirectURL(c *echo.Context, messageKey string, message string) string {
	values := url.Values{}
	for _, key := range []string{"page", "local", "remote", "shortcode", "by_domain"} {
		value := strings.TrimSpace(firstNonEmpty(c.FormValue(key), c.QueryParam(key)))
		if value != "" {
			values.Set(key, value)
		}
	}
	if messageKey != "" && message != "" {
		values.Set(messageKey, message)
	}
	query := values.Encode()
	if query == "" {
		return "/admin/custom_emojis"
	}
	return "/admin/custom_emojis?" + query
}

func adminCustomEmojiMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.custom_emojis."+key, fallback)
}

type adminCustomEmojiFilters struct {
	Page      string
	Local     string
	Remote    string
	Shortcode string
	ByDomain  string
}

func adminCustomEmojiBatchHiddenFields(filters adminCustomEmojiFilters) string {
	values := map[string]string{
		"page":      firstNonEmpty(filters.Page, "1"),
		"local":     filters.Local,
		"remote":    filters.Remote,
		"shortcode": filters.Shortcode,
		"by_domain": filters.ByDomain,
	}
	var out strings.Builder
	for _, key := range []string{"page", "local", "remote", "shortcode", "by_domain"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			out.WriteString(`<input type="hidden" name="` + key + `" value="` + html.EscapeString(value) + `">`)
		}
	}
	return out.String()
}

func adminCustomEmojisHTML(emojis []models.CustomEmoji, categories []models.CustomEmojiCategory, notice string, errorText string, filters adminCustomEmojiFilters, locale ...string) string {
	return adminCustomEmojisHTMLWithConfig(config.Config{}, emojis, categories, notice, errorText, filters, locale...)
}

func adminCustomEmojisHTMLWithConfig(cfg config.Config, emojis []models.CustomEmoji, categories []models.CustomEmojiCategory, notice string, errorText string, filters adminCustomEmojiFilters, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><a class="button" href="/admin/custom_emojis/new">` + html.EscapeString(adminT(loc, "admin.custom_emojis.upload", "Upload custom emoji")) + `</a></div><div class="filters">`)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.accounts.location.title", "Location"), []relationshipFilterLink{
		{Label: adminT(loc, "admin.accounts.location.all", "All"), Href: adminCustomEmojiFilterHref(filters, ""), Active: filters.Local != "1" && filters.Remote != "1"},
		{Label: adminT(loc, "admin.accounts.location.local", "Local"), Href: adminCustomEmojiFilterHref(filters, "local"), Active: filters.Local == "1"},
		{Label: adminT(loc, "admin.accounts.location.remote", "Remote"), Href: adminCustomEmojiFilterHref(filters, "remote"), Active: filters.Remote == "1"},
	}))
	body.WriteString(`</div><form method="get" action="/admin/custom_emojis" class="simple_form">`)
	if filters.Local != "" {
		body.WriteString(`<input type="hidden" name="local" value="` + html.EscapeString(filters.Local) + `">`)
	}
	if filters.Remote != "" {
		body.WriteString(`<input type="hidden" name="remote" value="` + html.EscapeString(filters.Remote) + `">`)
	}
	body.WriteString(`<div class="fields-group">` + adminAccountSearchInputHTML("shortcode", filters.Shortcode, adminT(loc, "admin.custom_emojis.shortcode", "Shortcode")) + adminAccountSearchInputHTML("by_domain", filters.ByDomain, adminT(loc, "admin.custom_emojis.domain", "Domain")) + `</div><div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.accounts.search", "Search")) + `</button> <a class="button negative" href="/admin/custom_emojis">` + html.EscapeString(adminT(loc, "admin.accounts.reset", "Reset")) + `</a></div></form>`)
	body.WriteString(`<form method="post" action="/admin/custom_emojis/batch" class="new_form_custom_emoji_batch">` + adminCustomEmojiBatchHiddenFields(filters) + `<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions">`)
	confirm := html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?"))
	if filters.Local == "1" {
		body.WriteString(adminCustomEmojiBatchButton("save", "update", adminT(loc, "generic.save_changes", "Save changes"), confirm))
		body.WriteString(adminCustomEmojiBatchButton("eye", "list", adminT(loc, "admin.custom_emojis.list", "List"), confirm))
		body.WriteString(adminCustomEmojiBatchButton("eye-slash", "unlist", adminT(loc, "admin.custom_emojis.unlist", "Unlist"), confirm))
	}
	body.WriteString(adminCustomEmojiBatchButton("power-off", "enable", adminT(loc, "admin.custom_emojis.enable", "Enable"), confirm))
	body.WriteString(adminCustomEmojiBatchButton("power-off", "disable", adminT(loc, "admin.custom_emojis.disable", "Disable"), confirm))
	body.WriteString(adminCustomEmojiBatchButton("times", "delete", adminT(loc, "admin.custom_emojis.delete", "Delete"), confirm))
	if filters.Local != "1" {
		body.WriteString(adminCustomEmojiBatchButton("copy", "copy", adminT(loc, "admin.custom_emojis.copy", "Copy"), confirm))
	}
	body.WriteString(`</div></div>`)
	if filters.Local == "1" {
		body.WriteString(`<div class="batch-table__form simple_form"><div class="fields-row"><div class="fields-group fields-row__column fields-row__column-6"><div class="input select optional"><div class="label_input">` + adminCustomEmojiCategorySelect(categories, loc) + `</div></div></div><div class="fields-group fields-row__column fields-row__column-6"><div class="input string optional"><div class="label_input"><input class="string optional" name="form_custom_emoji_batch[category_name]" placeholder="` + html.EscapeString(adminT(loc, "admin.custom_emojis.create_new_category", "Create new category")) + `" aria-label="` + html.EscapeString(adminT(loc, "admin.custom_emojis.create_new_category", "Create new category")) + `"></div></div></div></div></div>`)
	}
	body.WriteString(`<div class="batch-table__body">`)
	if len(emojis) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, emoji := range emojis {
			body.WriteString(adminCustomEmojiRowHTMLWithConfig(cfg, emoji, loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/custom_emojis", filters.Page, adminCustomEmojiFiltersQuery(filters), len(emojis)))
	return authPageHTML(adminT(loc, "admin.custom_emojis.title", "Custom emojis"), notice, errorText, body.String(), loc)
}

func adminCustomEmojiFilterHref(filters adminCustomEmojiFilters, location string) string {
	values := adminCustomEmojiFiltersQuery(filters)
	values.Del("page")
	values.Del("local")
	values.Del("remote")
	if location == "local" {
		values.Set("local", "1")
	} else if location == "remote" {
		values.Set("remote", "1")
	}
	if query := values.Encode(); query != "" {
		return "/admin/custom_emojis?" + query
	}
	return "/admin/custom_emojis"
}

func adminCustomEmojiFiltersQuery(filters adminCustomEmojiFilters) url.Values {
	values := url.Values{}
	for key, value := range map[string]string{"local": filters.Local, "remote": filters.Remote, "shortcode": filters.Shortcode, "by_domain": filters.ByDomain} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return values
}

func adminCustomEmojiBatchButton(icon string, name string, label string, confirm string) string {
	return `<button class="table-action-link" name="` + html.EscapeString(name) + `" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-` + html.EscapeString(icon) + `"></i> ` + html.EscapeString(label) + `</button>`
}

func adminCustomEmojiRowHTML(emoji models.CustomEmoji, locale ...string) string {
	return adminCustomEmojiRowHTMLWithConfig(config.Config{}, emoji, settingsLocaleArgOrEnglish(locale...))
}

func adminCustomEmojiRowHTMLWithConfig(cfg config.Config, emoji models.CustomEmoji, loc string) string {
	domain := adminT(loc, "admin.accounts.location.local", "Local")
	if emoji.Domain.Valid && emoji.Domain.String != "" {
		domain = emoji.Domain.String
	}
	category := ""
	if emoji.Category.ID != 0 {
		category = emoji.Category.Name.String
	}
	status := adminT(loc, "admin.custom_emojis.enabled", "Enabled")
	if emoji.Disabled {
		status = adminT(loc, "admin.custom_emojis.disabled", "Disabled")
	}
	if !emoji.VisibleInPicker {
		status += " / " + adminT(loc, "admin.custom_emojis.unlisted", "unlisted")
	}
	categoryHTML := ""
	if !emoji.Domain.Valid || strings.TrimSpace(emoji.Domain.String) == "" {
		categoryLabel := firstNonEmpty(category, adminT(loc, "admin.custom_emojis.uncategorized", "Uncategorized"))
		categoryHTML = `<span class="information-badge">` + html.EscapeString(categoryLabel) + `</span>`
	}
	imageURL := statusEmbedCustomEmojiURLWithConfig(cfg, emoji, "static")
	return `<div class="batch-table__row"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="form_custom_emoji_batch[custom_emoji_ids][]" value="` + strconv.FormatInt(emoji.ID, 10) + `"></label><div class="batch-table__row__content batch-table__row__content--with-image"><div class="batch-table__row__content__image"><img class="emojione" src="` + html.EscapeString(imageURL) + `" alt=":` + html.EscapeString(emoji.Shortcode) + `:"></div><div class="batch-table__row__content__text"><samp>:` + html.EscapeString(emoji.Shortcode) + `:</samp>` + categoryHTML + `</div><div class="batch-table__row__content__extra">` + html.EscapeString(domain) + `<br>` + html.EscapeString(status) + `</div></div></div>`
}

func adminCustomEmojiFormHTML(errorText string, locale ...string) string {
	return adminCustomEmojiFormHTMLWithShortcode("", errorText, locale...)
}

func adminCustomEmojiFormHTMLWithShortcode(shortcode string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	body := `<form class="simple_form new_custom_emoji" method="post" action="/admin/custom_emojis" enctype="multipart/form-data">` +
		simpleTextInput(adminT(loc, "simple_form.labels.custom_emoji.shortcode", "Shortcode"), "custom_emoji[shortcode]", shortcode, "text", `pattern="[A-Za-z0-9_]{2,}" required`) +
		`<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.custom_emoji.image", "Image")) + `</label><input type="file" name="custom_emoji[image]" accept="image/png,image/gif,image/webp" required></div></div></div>` +
		simpleCheckbox(adminT(loc, "simple_form.labels.custom_emoji.visible_in_picker", "Visible in picker"), "custom_emoji[visible_in_picker]", true) +
		simpleSubmit(adminT(loc, "admin.custom_emojis.upload", "Upload")) +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.custom_emojis.upload", "Upload custom emoji"), "", errorText, body, loc)
}

func adminCustomEmojiCategorySelect(categories []models.CustomEmojiCategory, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var out strings.Builder
	out.WriteString(`<select class="select optional" name="form_custom_emoji_batch[category_id]" aria-label="` + html.EscapeString(adminT(loc, "admin.custom_emojis.assign_category", "Assign category")) + `"><option value="">` + html.EscapeString(adminT(loc, "admin.custom_emojis.assign_category", "Assign category")) + `</option>`)
	for _, category := range categories {
		out.WriteString(`<option value="` + strconv.FormatInt(category.ID, 10) + `">` + html.EscapeString(category.Name.String) + `</option>`)
	}
	out.WriteString(`</select>`)
	return out.String()
}

func adminPaperclipIDPartition(id int64) string {
	text := strconv.FormatInt(id, 10)
	if len(text) < 9 {
		text = strings.Repeat("0", 9-len(text)) + text
	}
	parts := make([]string, 0, 3)
	for len(text) > 3 {
		parts = append(parts, text[:3])
		text = text[3:]
	}
	parts = append(parts, text)
	return strings.Join(parts, "/")
}
