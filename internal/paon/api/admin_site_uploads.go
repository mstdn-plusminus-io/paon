package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) destroyAdminSiteUpload(c *echo.Context) error {
	user, handled, err := s.requireAdminSiteUploadWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if c.Request().Method == http.MethodPost && !strings.EqualFold(c.FormValue("_method"), "delete") {
		return c.HTML(http.StatusMethodNotAllowed, authPageHTML(adminT(locale, "admin.site_uploads.delete", "Site upload"), "", adminT(locale, "admin.site_uploads.unsupported_action", "Unsupported site upload action."), "", locale))
	}
	id := c.Param("id")
	var upload models.SiteUpload
	if err := s.db.Where("id = ?", id).First(&upload).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.HTML(http.StatusNotFound, authPageHTML(adminT(locale, "admin.site_uploads.delete", "Site upload"), "", adminT(locale, "admin.site_uploads.not_found", "Site upload not found."), "", locale))
		}
		return err
	}
	if err := s.db.Delete(&upload).Error; err != nil {
		return err
	}
	s.invalidateSiteUploadCache(c.Request().Context(), upload.Var)
	s.deleteSiteUploadObjects(upload)
	if err := s.removeSiteUploadFiles(upload.ID); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/settings?notice="+url.QueryEscape(adminT(locale, "admin.site_uploads.destroyed_msg", "Site upload deleted")))
}

func (s *Server) requireAdminSiteUploadWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageSettings) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.site_uploads.delete", "Site upload"), "", adminT(locale, "admin.site_uploads.not_permitted", "You are not allowed to manage site uploads."), "", locale))
	}
	if s.db == nil {
		return nil, true, echo.NewHTTPError(http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	return user, false, nil
}

func (s *Server) storeAdminSiteUploadFromForm(c *echo.Context, name string) error {
	fieldName := "form_admin_settings[" + name + "]"
	header, ok, err := optionalFormFile(c, fieldName)
	if err != nil || !ok {
		return err
	}
	filename, contentType, meta, err := validateAdminSiteUploadHeader(header)
	if err != nil {
		return err
	}
	upload, created, err := s.ensureSiteUploadRow(name)
	if err != nil {
		return err
	}
	storedSize, err := s.storeSiteUploadFileStyles(header, upload.ID, name, filename)
	if err != nil {
		if created {
			_ = s.db.Delete(&upload).Error
		}
		return err
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"file_file_name":    sql.NullString{String: filename, Valid: true},
		"file_content_type": sql.NullString{String: contentType, Valid: true},
		"file_file_size":    sql.NullInt64{Int64: storedSize, Valid: true},
		"file_updated_at":   sql.NullTime{Time: now, Valid: true},
		"updated_at":        now,
	}
	if len(meta) > 0 {
		updates["meta"] = models.JSONValue(meta)
	}
	if blurhash := s.siteUploadBlurhash(upload.ID, name, filename); blurhash != "" {
		updates["blurhash"] = sql.NullString{String: blurhash, Valid: true}
	}
	if err := s.db.Model(&models.SiteUpload{}).Where("id = ?", upload.ID).Updates(updates).Error; err != nil {
		return err
	}
	s.invalidateSiteUploadCache(c.Request().Context(), name)
	if !created {
		return s.removeReplacedSiteUploadFiles(upload, name, filename)
	}
	return nil
}

func validateAdminSiteUploadFromForm(c *echo.Context, name string) error {
	fieldName := "form_admin_settings[" + name + "]"
	header, ok, err := optionalFormFile(c, fieldName)
	if err != nil || !ok {
		return err
	}
	_, _, _, err = validateAdminSiteUploadHeader(header)
	return err
}

func validateAdminSiteUploadHeader(header *multipart.FileHeader) (string, string, []byte, error) {
	filename := paperclipObfuscatedFilename(header.Filename)
	contentType := mediaContentType(filename, header.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "image/") {
		return "", "", nil, errAdminSetting("Site upload must be an image")
	}
	meta, err := siteUploadMetaFromHeader(header)
	if err != nil {
		return "", "", nil, err
	}
	return filename, contentType, meta, nil
}

func (s *Server) ensureSiteUploadRow(name string) (models.SiteUpload, bool, error) {
	var upload models.SiteUpload
	err := s.db.Where("var = ?", name).First(&upload).Error
	if err == nil {
		return upload, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return upload, false, err
	}
	now := time.Now().UTC()
	upload = models.SiteUpload{Var: name, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&upload).Error; err != nil {
		return upload, false, err
	}
	return upload, true, nil
}

func (s *Server) storeSiteUploadFileStyles(header *multipart.FileHeader, id int64, name string, filename string) (int64, error) {
	if name == "thumbnail" {
		return s.storeSiteUploadThumbnailStyles(header, id, filename)
	}
	contentType := mediaContentType(filename, header.Header.Get("Content-Type"))
	var originalSize int64
	for _, style := range siteUploadStyles(name) {
		target := s.siteUploadFilePath(id, style, filename)
		size, err := s.storeSiteUploadOriginalStyle(header, target, contentType)
		if err != nil {
			return 0, err
		}
		if style == "original" {
			originalSize = size
		}
		if err := s.uploadPaperclipObject(context.Background(), siteUploadObjectKey(id, style, filename), target, contentType); err != nil {
			return 0, err
		}
	}
	if originalSize <= 0 {
		originalSize = header.Size
	}
	return originalSize, nil
}

func (s *Server) storeSiteUploadThumbnailStyles(header *multipart.FileHeader, id int64, filename string) (int64, error) {
	original := s.siteUploadFilePath(id, "original", filename)
	contentType := mediaContentType(filename, header.Header.Get("Content-Type"))
	originalSize, err := s.storeSiteUploadOriginalStyle(header, original, contentType)
	if err != nil {
		return 0, err
	}
	if err := s.uploadPaperclipObject(context.Background(), siteUploadObjectKey(id, "original", filename), original, contentType); err != nil {
		return 0, err
	}
	for _, style := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "@1x", width: 1200, height: 630},
		{name: "@2x", width: 2400, height: 1260},
	} {
		styleFilename := siteUploadStyleFilename("thumbnail", style.name, filename)
		target := s.siteUploadFilePath(id, style.name, styleFilename)
		if err := resizeVIPSFileToFill(original, target, "image/png", style.width, style.height); err != nil {
			return 0, err
		}
		if err := s.uploadPaperclipObject(context.Background(), siteUploadObjectKey(id, style.name, styleFilename), target, "image/png"); err != nil {
			return 0, err
		}
	}
	return originalSize, nil
}

func (s *Server) storeSiteUploadOriginalStyle(header *multipart.FileHeader, target string, contentType string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	if siteUploadOriginalCanReencode(contentType) {
		file, err := header.Open()
		if err != nil {
			return 0, err
		}
		img, _, err := image.Decode(file)
		_ = file.Close()
		if err != nil {
			return 0, err
		}
		dst, err := os.Create(target)
		if err != nil {
			return 0, err
		}
		if err := encodeSiteUploadOriginal(dst, img, contentType); err != nil {
			_ = dst.Close()
			return 0, err
		}
		if err := dst.Close(); err != nil {
			return 0, err
		}
		return storedFileSize(target, header.Size), nil
	}
	if err := s.storeMediaAttachmentFile(header, target); err != nil {
		return 0, err
	}
	return storedFileSize(target, header.Size), nil
}

func siteUploadOriginalCanReencode(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/jpeg", "image/png":
		return true
	default:
		return false
	}
}

func encodeSiteUploadOriginal(dst *os.File, img image.Image, contentType string) error {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/jpeg":
		return jpeg.Encode(dst, img, &jpeg.Options{Quality: 90})
	default:
		return png.Encode(dst, img)
	}
}

func storedFileSize(path string, fallback int64) int64 {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return fallback
	}
	return info.Size()
}

func siteUploadStyles(name string) []string {
	switch name {
	case "thumbnail":
		return []string{"original", "@1x", "@2x"}
	default:
		return []string{"original"}
	}
}

func (s *Server) siteUploadFilePath(id int64, style string, filename string) string {
	return s.cfg.SystemAssetPath("site_uploads", "files", mediaPaperclipIDPartition(id), style, filename)
}

func (s *Server) siteUploadBlurhash(id int64, name string, filename string) string {
	style := "original"
	if name == "thumbnail" {
		style = "@1x"
	}
	return blurhashForStoredImage(s.siteUploadFilePath(id, style, siteUploadStyleFilename(name, style, filename)))
}

func siteUploadStyleFilename(name string, style string, filename string) string {
	if name == "thumbnail" && style != "original" {
		ext := filepath.Ext(filename)
		if ext != "" {
			return strings.TrimSuffix(filename, ext) + ".png"
		}
		return filename + ".png"
	}
	return filename
}

func siteUploadMetaFromHeader(header *multipart.FileHeader) ([]byte, error) {
	cfg, err := imageConfigFromHeader(header)
	if err != nil {
		return nil, errAdminSetting("Site upload must be a readable image")
	}
	return siteUploadMetaFromConfig(cfg), nil
}

func siteUploadMetaForStoredFile(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	cfg, ok := imageConfigFromReader(file)
	if !ok {
		return nil
	}
	return siteUploadMetaFromConfig(cfg)
}

func siteUploadMetaFromConfig(cfg image.Config) []byte {
	meta, _ := json.Marshal(map[string]any{
		"width":  cfg.Width,
		"height": cfg.Height,
	})
	return meta
}

func (s *Server) removeSiteUploadFiles(id int64) error {
	dir := s.cfg.SystemAssetPath("site_uploads", "files", mediaPaperclipIDPartition(id))
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return removeEmptyPaperclipParents(filepath.Dir(dir), s.cfg.SystemAssetPath("site_uploads", "files"))
}

func (s *Server) removeReplacedSiteUploadFiles(upload models.SiteUpload, name string, keepFilename string) error {
	if !upload.FileFileName.Valid || upload.FileFileName.String == "" || upload.FileFileName.String == keepFilename {
		return nil
	}
	for _, style := range siteUploadStyles(name) {
		styleFilename := siteUploadStyleFilename(name, style, upload.FileFileName.String)
		path := s.siteUploadFilePath(upload.ID, style, styleFilename)
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.deletePaperclipObject(context.Background(), siteUploadObjectKey(upload.ID, style, styleFilename))
		if err := removeEmptyPaperclipParents(filepath.Dir(path), s.cfg.SystemAssetPath("site_uploads", "files")); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deleteSiteUploadObjects(upload models.SiteUpload) {
	if !upload.FileFileName.Valid || strings.TrimSpace(upload.FileFileName.String) == "" {
		return
	}
	for _, style := range siteUploadStyles(upload.Var) {
		s.deletePaperclipObject(context.Background(), siteUploadObjectKey(upload.ID, style, siteUploadStyleFilename(upload.Var, style, upload.FileFileName.String)))
	}
}

func siteUploadCacheKey(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "site_uploads/" + name
}

func (s *Server) invalidateSiteUploadCache(ctx context.Context, name string) {
	key := siteUploadCacheKey(name)
	if s == nil || key == "" {
		return
	}
	keys := railsCacheRedisKeyCandidates(s.cfg, key)
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}

func removeEmptyPaperclipParents(start string, stop string) error {
	start = filepath.Clean(start)
	stop = filepath.Clean(stop)
	for start != stop && strings.HasPrefix(start, stop) {
		err := os.Remove(start)
		if err == nil {
			start = filepath.Dir(start)
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			start = filepath.Dir(start)
			continue
		}
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return nil
		}
		return err
	}
	return nil
}
