package api

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) downloadBackup(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	backup, err := s.findUserBackup(user.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	target := s.backupDumpURL(*backup)
	if target == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.Redirect(http.StatusFound, target)
}

func (s *Server) createBackup(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}

	acquired, releaseBackupLock, err := s.acquireActivityPubRedisLock(c.Request().Context(), "backup:"+fmt.Sprint(user.ID), 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseBackupLock()

	allowed, err := s.backupCreateAllowed(c.Request().Context(), user.ID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !allowed {
		locale := s.webLocale(c, user)
		theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
		return c.HTML(http.StatusForbidden, authPageHTML(settingsT(locale, "settings.export", "Export"), "", "Forbidden", "", locale, theme))
	}

	now := time.Now().UTC()
	backup := models.Backup{
		UserID:    sql.NullInt64{Int64: user.ID, Valid: true},
		Processed: false,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.Create(&backup).Error; err != nil {
		return err
	}
	s.enqueueBackupTask(backup.ID)
	return c.Redirect(http.StatusFound, "/settings/export")
}

func (s *Server) backupCreateAllowed(ctx context.Context, userID int64, now time.Time) (bool, error) {
	if s == nil || s.db == nil || userID == 0 {
		return true, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var count int64
	err := s.db.WithContext(ctx).
		Model(&models.Backup{}).
		Where("user_id = ? AND created_at >= ?", userID, now.Add(-6*24*time.Hour)).
		Count(&count).Error
	return count == 0, err
}

func (s *Server) processBackupArchive(ctx context.Context, backupID int64) error {
	if s == nil || s.db == nil || backupID == 0 {
		return nil
	}
	var backup models.Backup
	if err := s.db.WithContext(ctx).Where("id = ?", backupID).First(&backup).Error; err != nil {
		return nil
	}
	var user models.User
	if !backup.UserID.Valid || backup.UserID.Int64 == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).Preload("Account").Where("id = ?", backup.UserID.Int64).First(&user).Error; err != nil {
		return nil
	}
	now := time.Now().UTC()
	filename, err := backupArchiveFilename(now)
	if err != nil {
		return err
	}
	size, err := s.writeBackupArchive(user.AccountID, backup.ID, filename)
	if err != nil {
		return err
	}

	updates := map[string]any{
		"dump_file_name":    filename,
		"dump_content_type": "application/zip",
		"dump_file_size":    size,
		"dump_updated_at":   now,
		"processed":         true,
		"updated_at":        time.Now().UTC(),
	}
	if err := s.db.Model(&models.Backup{}).Where("id = ? AND user_id = ?", backup.ID, user.ID).Updates(updates).Error; err != nil {
		return err
	}
	backup.DumpFileName = sql.NullString{String: filename, Valid: true}
	backup.DumpContentType = sql.NullString{String: "application/zip", Valid: true}
	backup.DumpFileSize = sql.NullInt64{Int64: size, Valid: true}
	backup.DumpUpdatedAt = sql.NullTime{Time: now, Valid: true}
	backup.Processed = true
	if err := s.destroyOtherUserBackups(ctx, user.ID, backup.ID); err != nil {
		return err
	}
	return s.sendBackupReadyMail(user, backup)
}

func (s *Server) markBackupWorkerExhausted(ctx context.Context, backupID int64) {
	if s == nil || s.db == nil || backupID == 0 {
		return
	}
	_ = s.db.WithContext(ctx).Delete(&models.Backup{}, backupID).Error
}

func (s *Server) destroyOtherUserBackups(ctx context.Context, userID int64, keepID int64) error {
	if s == nil || s.db == nil || userID == 0 || keepID == 0 {
		return nil
	}
	var backups []models.Backup
	if err := s.db.WithContext(ctx).Where("user_id = ? AND id <> ?", userID, keepID).Find(&backups).Error; err != nil {
		return err
	}
	for _, backup := range backups {
		if err := s.removeBackupDumpFiles(backup); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Where("user_id = ? AND id <> ?", userID, keepID).Delete(&models.Backup{}).Error
}

func (s *Server) findUserBackup(userID int64, id string) (*models.Backup, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var backup models.Backup
	err := s.db.
		Where("id = ? AND user_id = ? AND processed = true AND dump_file_name IS NOT NULL", id, userID).
		First(&backup).Error
	return &backup, err
}

func (s *Server) backupDumpURL(backup models.Backup) string {
	if !backup.DumpFileName.Valid || strings.TrimSpace(backup.DumpFileName.String) == "" {
		return ""
	}
	key := backupDumpObjectKey(backup.ID, backup.DumpFileName.String)
	if signed := s.presignedS3ObjectURL(key, time.Hour); signed != "" {
		return signed
	}
	if signed := s.presignedAzureBlobURL(key, time.Hour); signed != "" {
		return signed
	}
	if signed := s.presignedSwiftObjectURL(key, time.Hour); signed != "" {
		return signed
	}
	return s.cfg.SystemAssetURL("backups/dumps/" + strings.ReplaceAll(mediaPaperclipIDPartition(backup.ID), string(filepath.Separator), "/") + "/original/" + url.PathEscape(backup.DumpFileName.String))
}

func (s *Server) backupDumpPath(id int64, filename string) string {
	return s.cfg.SystemAssetPath("backups", "dumps", mediaPaperclipIDPartition(id), "original", filename)
}

func (s *Server) backupDumpLocalPath(backup models.Backup) string {
	if s == nil || s.cfg.PublicDir == "" || !backup.DumpFileName.Valid || strings.TrimSpace(backup.DumpFileName.String) == "" {
		return ""
	}
	return s.backupDumpPath(backup.ID, filepath.Base(strings.TrimSpace(backup.DumpFileName.String)))
}

func (s *Server) setBackupDownloadHeaders(c *echo.Context, backup models.Backup, target string) {
	contentType := "application/zip"
	if backup.DumpContentType.Valid && strings.TrimSpace(backup.DumpContentType.String) != "" {
		contentType = strings.TrimSpace(backup.DumpContentType.String)
	}
	filename := downloadFilename(target)
	if backup.DumpFileName.Valid && strings.TrimSpace(backup.DumpFileName.String) != "" {
		filename = filepath.Base(strings.TrimSpace(backup.DumpFileName.String))
	}
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
}

func (s *Server) writeBackupArchive(accountID int64, backupID int64, filename string) (int64, error) {
	entries, err := s.backupArchiveEntries(accountID)
	if err != nil {
		return 0, err
	}
	path := s.backupDumpPath(backupID, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()

	zipWriter := zip.NewWriter(file)
	for _, entry := range entries {
		var err error
		if entry.ObjectKey != "" {
			err = s.writeZipS3ObjectEntry(zipWriter, entry.Name, entry.ObjectKey)
		} else if entry.Path != "" {
			err = writeZipFileEntry(zipWriter, entry.Name, entry.Path)
		} else {
			err = writeZipEntry(zipWriter, entry.Name, entry.Body)
		}
		if err != nil {
			_ = zipWriter.Close()
			return 0, err
		}
	}
	if err := zipWriter.Close(); err != nil {
		return 0, err
	}
	if err := file.Close(); err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if err := s.uploadPaperclipObjectWithACL(context.Background(), backupDumpObjectKey(backupID, filename), path, "application/zip", backupDumpS3ACL(s.cfg.S3Permission)); err != nil {
		return 0, err
	}
	success = true
	return info.Size(), nil
}

func backupDumpS3ACL(defaultPermission string) string {
	if defaultPermission == "" {
		return ""
	}
	return "private"
}

func (s *Server) backupArchiveEntries(accountID int64) ([]backupArchiveEntry, error) {
	activityEntries, err := s.backupActivityPubEntries(accountID)
	if err != nil {
		return nil, err
	}
	mediaEntries, err := s.backupMediaAttachmentEntries(accountID)
	if err != nil {
		return nil, err
	}
	entries := make([]backupArchiveEntry, 0, len(activityEntries)+len(mediaEntries))
	entries = append(entries, activityEntries...)
	entries = append(entries, mediaEntries...)
	return entries, nil
}

func (s *Server) backupMediaAttachmentEntries(accountID int64) ([]backupArchiveEntry, error) {
	var attachments []models.MediaAttachment
	if err := s.db.
		Where("account_id = ? AND file_file_name IS NOT NULL AND (status_id IS NOT NULL OR scheduled_status_id IS NOT NULL)", accountID).
		Order("id ASC").
		Find(&attachments).Error; err != nil {
		return nil, err
	}

	entries := make([]backupArchiveEntry, 0, len(attachments))
	for _, attachment := range attachments {
		if !attachment.FileFileName.Valid || strings.TrimSpace(attachment.FileFileName.String) == "" {
			continue
		}
		path := s.mediaFilePath(attachment.ID, attachment.FileFileName.String)
		name, ok := backupSystemArchiveName(s.cfg.PublicDir, path)
		if !ok {
			continue
		}
		entry, ok, err := s.backupPaperclipArchiveEntry(name, path, mediaAttachmentObjectKey(attachment.ID, "files", "original", attachment.FileFileName.String))
		if err != nil {
			return nil, err
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (s *Server) backupActivityPubEntries(accountID int64) ([]backupArchiveEntry, error) {
	account, err := s.backupAccount(accountID)
	if err != nil {
		return nil, err
	}
	outbox, err := s.backupOutboxJSON(*account)
	if err != nil {
		return nil, err
	}
	likes, err := s.backupStatusURICollection(accountID, "likes.json", "favourites")
	if err != nil {
		return nil, err
	}
	bookmarks, err := s.backupStatusURICollection(accountID, "bookmarks.json", "bookmarks")
	if err != nil {
		return nil, err
	}
	actor, imageEntries, err := s.backupActorJSON(*account)
	if err != nil {
		return nil, err
	}
	entries := []backupArchiveEntry{
		{Name: "outbox.json", Body: outbox},
		{Name: "likes.json", Body: likes},
		{Name: "bookmarks.json", Body: bookmarks},
		{Name: "actor.json", Body: actor},
	}
	entries = append(entries, imageEntries...)
	return entries, nil
}

func (s *Server) backupAccount(accountID int64) (*models.Account, error) {
	var account models.Account
	err := s.db.
		Preload("AccountStat").
		Preload("User.Role").
		Where("id = ?", accountID).
		First(&account).Error
	return &account, err
}

func (s *Server) backupActorJSON(account models.Account) ([]byte, []backupArchiveEntry, error) {
	actor := activityPubActorObject(s, account)
	actor["outbox"] = "outbox.json"
	actor["likes"] = "likes.json"
	actor["bookmarks"] = "bookmarks.json"
	entries := []backupArchiveEntry{}
	if entry, ok, err := s.backupAccountImageArchiveEntry(account, "avatar"); err != nil {
		return nil, nil, err
	} else if ok {
		actor["icon"] = backupActivityImageObject(account.AvatarContentType.String, entry.Name)
		entries = append(entries, entry)
	}
	if entry, ok, err := s.backupAccountImageArchiveEntry(account, "header"); err != nil {
		return nil, nil, err
	} else if ok {
		actor["image"] = backupActivityImageObject(account.HeaderContentType.String, entry.Name)
		entries = append(entries, entry)
	}
	body, err := json.Marshal(actor)
	return body, entries, err
}

func (s *Server) backupAccountImageArchiveEntry(account models.Account, kind string) (backupArchiveEntry, bool, error) {
	filename := ""
	if kind == "avatar" {
		if account.AvatarFileName.Valid {
			filename = strings.TrimSpace(account.AvatarFileName.String)
		}
	} else if kind == "header" {
		if account.HeaderFileName.Valid {
			filename = strings.TrimSpace(account.HeaderFileName.String)
		}
	}
	if filename == "" {
		return backupArchiveEntry{}, false, nil
	}
	path := s.accountImagePath(account.ID, kind, filename)
	ext := filepath.Ext(filename)
	entryName := kind
	if kind == "avatar" {
		entryName = "avatar"
	} else {
		entryName = "header"
	}
	return s.backupPaperclipArchiveEntry(entryName+ext, path, accountImageObjectKey(account.ID, kind, "original", filename))
}

func backupActivityImageObject(mediaType string, name string) map[string]any {
	return map[string]any{
		"type":      "Image",
		"mediaType": emptyNil(mediaType),
		"url":       name,
	}
}

func (s *Server) backupOutboxJSON(account models.Account) ([]byte, error) {
	var statuses []models.Status
	if err := s.statusQuery().
		Where("statuses.account_id = ? AND statuses.deleted_at IS NULL", account.ID).
		Order("statuses.id ASC").
		Find(&statuses).Error; err != nil {
		return nil, err
	}
	items := make([]any, 0, len(statuses))
	for _, status := range statuses {
		item, err := activityPubCreateWithError(s, status)
		if err != nil {
			return nil, err
		}
		delete(item, "@context")
		backupRewriteActivityAttachmentURLs(s, item)
		items = append(items, item)
	}
	return json.Marshal(backupOrderedCollection("outbox.json", len(items), items, true))
}

func backupRewriteActivityAttachmentURLs(s *Server, item map[string]any) {
	if item["type"] == "Announce" {
		return
	}
	object, ok := item["object"].(map[string]any)
	if !ok {
		return
	}
	attachments, ok := object["attachment"].([]any)
	if !ok {
		return
	}
	for _, raw := range attachments {
		attachment, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if value, ok := attachment["url"].(string); ok {
			attachment["url"] = backupSystemURLPath(s.cfg.BaseURL(), value)
		}
		if icon, ok := attachment["icon"].(map[string]any); ok {
			if value, ok := icon["url"].(string); ok {
				icon["url"] = backupSystemURLPath(s.cfg.BaseURL(), value)
			}
		}
	}
}

func (s *Server) backupStatusURICollection(accountID int64, id string, table string) ([]byte, error) {
	var statuses []models.Status
	if err := s.statusQuery().
		Joins("JOIN "+table+" ON "+table+".status_id = statuses.id").
		Where(table+".account_id = ?", accountID).
		Where("statuses.deleted_at IS NULL").
		Order("statuses.id ASC").
		Find(&statuses).Error; err != nil {
		return nil, err
	}
	items := make([]any, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, activityPubStatusURI(s, status))
	}
	collection := backupOrderedCollection(id, len(items), items, false)
	delete(collection, "totalItems")
	return json.Marshal(collection)
}

func backupOrderedCollection(id string, total int, items []any, includeContext bool) map[string]any {
	collection := map[string]any{
		"id":           id,
		"type":         "OrderedCollection",
		"totalItems":   total,
		"orderedItems": items,
	}
	if includeContext {
		collection["@context"] = activityContext()
	}
	return collection
}

type backupArchiveEntry struct {
	Name      string
	Body      []byte
	Path      string
	ObjectKey string
}

func backupArchiveFilename(now time.Time) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("archive-%s-%s.zip", now.UTC().Format("20060102150405"), hex.EncodeToString(random)), nil
}

func writeZipEntry(writer *zip.Writer, name string, body []byte) error {
	header := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: time.Now().UTC(),
	}
	header.SetMode(0o644)
	file, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = file.Write(body)
	return err
}

func writeZipFileEntry(writer *zip.Writer, name string, path string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	header := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: info.ModTime(),
	}
	header.SetMode(0o644)
	file, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, source)
	return err
}

func (s *Server) writeZipS3ObjectEntry(writer *zip.Writer, name string, objectKey string) error {
	source, ok, err := s.getS3ObjectReader(context.Background(), objectKey)
	if err != nil || !ok {
		return err
	}
	defer source.Close()
	header := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: time.Now().UTC(),
	}
	header.SetMode(0o644)
	file, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, source)
	return err
}

func backupFileArchiveEntry(name string, path string) (backupArchiveEntry, bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return backupArchiveEntry{}, false, nil
		}
		return backupArchiveEntry{}, false, err
	}
	return backupArchiveEntry{Name: name, Path: path}, true, nil
}

func (s *Server) backupPaperclipArchiveEntry(name string, localPath string, objectKey string) (backupArchiveEntry, bool, error) {
	entry, ok, err := backupFileArchiveEntry(name, localPath)
	if err != nil || ok {
		return entry, ok, err
	}
	if !s.s3ObjectStorageEnabled() {
		return backupArchiveEntry{}, false, nil
	}
	return backupArchiveEntry{Name: name, ObjectKey: objectKey}, true, nil
}

func backupSystemArchiveName(publicDir string, path string) (string, bool) {
	base := filepath.Join(publicDir, "system")
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func backupSystemURLPath(baseURL string, raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return raw
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		if !strings.EqualFold(parsed.Scheme, base.Scheme) || !strings.EqualFold(parsed.Host, base.Host) {
			return raw
		}
	}
	path := strings.TrimPrefix(parsed.Path, "/")
	if strings.HasPrefix(path, "system/") {
		return strings.TrimPrefix(path, "system/")
	}
	return raw
}
