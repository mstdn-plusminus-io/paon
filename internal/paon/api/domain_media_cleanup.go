package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) clearDomainMediaCache(database *gorm.DB, domain string) error {
	if database == nil || domain == "" {
		return nil
	}
	domain = strings.Trim(strings.ToLower(domain), ".")
	if domain == "" {
		return nil
	}
	if err := s.clearDomainAccountImages(database, domain); err != nil {
		return err
	}
	if err := s.clearDomainMediaAttachments(database, domain); err != nil {
		return err
	}
	return s.deleteDomainCustomEmojis(database, domain)
}

func (s *Server) clearDomainMediaAttachments(database *gorm.DB, domain string) error {
	var attachments []models.MediaAttachment
	if err := database.
		Joins("JOIN accounts ON accounts.id = media_attachments.account_id").
		Where(domainAndSubdomainsSQL("accounts.domain"), domain, "%."+domain).
		Where("media_attachments.file_file_name IS NOT NULL OR media_attachments.thumbnail_file_name IS NOT NULL").
		Find(&attachments).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, attachment := range attachments {
		s.removeMediaAttachmentLocalFiles(attachment)
		if err := database.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Updates(clearMediaAttachmentFileUpdates(now)).Error; err != nil {
			return err
		}
		s.invalidateMediaAttachmentParentStatusCache(context.Background(), attachment)
	}
	return nil
}

func (s *Server) clearDomainAccountImages(database *gorm.DB, domain string) error {
	var accounts []models.Account
	if err := database.
		Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
		Where("avatar_file_name IS NOT NULL OR header_file_name IS NOT NULL").
		Find(&accounts).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, account := range accounts {
		s.removeAccountImageObjects(account)
		s.removeAccountLocalImageFiles(account.ID)
		if err := database.Model(&models.Account{}).Where("id = ?", account.ID).Updates(clearAccountImageFileUpdates(now)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deleteDomainCustomEmojis(database *gorm.DB, domain string) error {
	var emojis []models.CustomEmoji
	if err := database.
		Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
		Find(&emojis).Error; err != nil {
		return err
	}
	for _, emoji := range emojis {
		s.removeCustomEmojiLocalFiles(emoji)
		if err := database.Delete(&models.CustomEmoji{}, emoji.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

func domainAndSubdomainsSQL(column string) string {
	return "lower(" + column + ") = ? OR lower(" + column + ") LIKE ?"
}

func clearMediaAttachmentFileUpdates(now time.Time) map[string]any {
	return map[string]any{
		"file_file_name":              sql.NullString{},
		"file_content_type":           sql.NullString{},
		"file_file_size":              sql.NullInt64{},
		"file_updated_at":             sql.NullTime{},
		"file_storage_schema_version": sql.NullInt64{},
		"thumbnail_file_name":         sql.NullString{},
		"thumbnail_content_type":      sql.NullString{},
		"thumbnail_file_size":         sql.NullInt64{},
		"thumbnail_updated_at":        sql.NullTime{},
		"file_meta":                   nil,
		"blurhash":                    sql.NullString{},
		"updated_at":                  now,
	}
}

func clearAccountImageFileUpdates(now time.Time) map[string]any {
	return map[string]any{
		"avatar_file_name":              sql.NullString{},
		"avatar_content_type":           sql.NullString{},
		"avatar_file_size":              sql.NullInt64{},
		"avatar_updated_at":             sql.NullTime{},
		"avatar_storage_schema_version": sql.NullInt64{},
		"header_file_name":              sql.NullString{},
		"header_content_type":           sql.NullString{},
		"header_file_size":              sql.NullInt64{},
		"header_updated_at":             sql.NullTime{},
		"header_storage_schema_version": sql.NullInt64{},
		"updated_at":                    now,
	}
}

func (s *Server) removeMediaAttachmentLocalFiles(attachment models.MediaAttachment) {
	if s == nil || attachment.ID == 0 {
		return
	}
	s.bustMediaAttachmentCache(attachment)
	for _, object := range s.mediaAttachmentStoredObjects(attachment) {
		s.deletePaperclipObject(context.Background(), object.key)
	}
	for _, root := range []string{
		s.cfg.SystemAssetPath("media_attachments", "files", mediaPaperclipIDPartition(attachment.ID)),
		s.cfg.SystemAssetPath("media_attachments", "thumbnails", mediaPaperclipIDPartition(attachment.ID)),
		s.cfg.SystemAssetPath("cache", "media_attachments", "files", mediaPaperclipIDPartition(attachment.ID)),
		s.cfg.SystemAssetPath("cache", "media_attachments", "thumbnails", mediaPaperclipIDPartition(attachment.ID)),
	} {
		_ = os.RemoveAll(root)
	}
}

func (s *Server) applyMediaAttachmentVisibility(ctx context.Context, attachment models.MediaAttachment, private bool) error {
	if s == nil || attachment.ID == 0 {
		return nil
	}
	mode := os.FileMode(0o644)
	acl := s.cfg.S3Permission
	if private {
		mode = 0o600
		if acl != "" {
			acl = "private"
		}
	}
	for _, object := range s.mediaAttachmentStoredObjects(attachment) {
		if err := s.putS3ObjectACL(ctx, object.key, acl); err != nil {
			return err
		}
		if object.path != "" {
			if err := os.Chmod(object.path, mode); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if s.cfg.CacheBusterEnabled {
			s.bustCacheURL(s.cacheBusterMediaAttachmentURL(attachment.ID, object.attachment, object.style, object.filename))
		}
	}
	return nil
}

type mediaAttachmentStoredObject struct {
	attachment string
	style      string
	filename   string
	key        string
	path       string
}

func (s *Server) mediaAttachmentStoredObjects(attachment models.MediaAttachment) []mediaAttachmentStoredObject {
	objects := make([]mediaAttachmentStoredObject, 0, 3)
	if attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != "" {
		filename := attachment.FileFileName.String
		for _, style := range mediaAttachmentFileStyles(attachment) {
			objects = append(objects, mediaAttachmentStoredObject{
				attachment: "files",
				style:      style,
				filename:   filename,
				key:        mediaAttachmentObjectKey(attachment.ID, "files", style, filename),
				path:       s.mediaAttachmentStylePath(attachment.ID, "files", style, filename),
			})
		}
	}
	if attachment.ThumbnailFileName.Valid && strings.TrimSpace(attachment.ThumbnailFileName.String) != "" {
		filename := attachment.ThumbnailFileName.String
		objects = append(objects, mediaAttachmentStoredObject{
			attachment: "thumbnails",
			style:      "original",
			filename:   filename,
			key:        mediaAttachmentObjectKey(attachment.ID, "thumbnails", "original", filename),
			path:       s.mediaThumbnailPath(attachment.ID, filename),
		})
	}
	return objects
}

func mediaAttachmentFileStyles(attachment models.MediaAttachment) []string {
	styles := []string{"original"}
	if len(attachment.FileMeta) == 0 {
		return styles
	}
	var meta map[string]any
	if err := json.Unmarshal(attachment.FileMeta, &meta); err != nil {
		return styles
	}
	if _, ok := meta["small"]; ok && mediaProxyHasSmallFileStyle(attachment) {
		styles = append(styles, "small")
	}
	return styles
}

func (s *Server) mediaAttachmentStylePath(id int64, attachment string, style string, filename string) string {
	if attachment == "thumbnails" {
		return s.mediaThumbnailPath(id, filename)
	}
	return s.mediaFileStylePath(id, style, filename)
}

func (s *Server) removeAccountLocalImageFiles(accountID int64) {
	if accountID == 0 {
		return
	}
	id := mediaPaperclipIDPartition(accountID)
	for _, root := range []string{
		s.cfg.SystemAssetPath("accounts", "avatars", id),
		s.cfg.SystemAssetPath("accounts", "headers", id),
	} {
		_ = os.RemoveAll(root)
	}
}

func (s *Server) removeAccountLocalImageFilesForKind(accountID int64, kind string) {
	if accountID == 0 {
		return
	}
	dir := "avatars"
	if kind == "header" {
		dir = "headers"
	}
	_ = os.RemoveAll(s.cfg.SystemAssetPath("accounts", dir, mediaPaperclipIDPartition(accountID)))
}

func (s *Server) removeAccountImageObjects(account models.Account) {
	if account.ID == 0 {
		return
	}
	s.bustAccountImageCache(account)
	if account.AvatarFileName.Valid && strings.TrimSpace(account.AvatarFileName.String) != "" {
		s.deletePaperclipObject(context.Background(), accountImageObjectKey(account.ID, "avatar", "original", account.AvatarFileName.String))
		s.deletePaperclipObject(context.Background(), accountImageObjectKey(account.ID, "avatar", "static", profileImageStaticFilename(account.AvatarFileName.String, account.AvatarContentType.String)))
	}
	if account.HeaderFileName.Valid && strings.TrimSpace(account.HeaderFileName.String) != "" {
		s.deletePaperclipObject(context.Background(), accountImageObjectKey(account.ID, "header", "original", account.HeaderFileName.String))
		s.deletePaperclipObject(context.Background(), accountImageObjectKey(account.ID, "header", "static", profileImageStaticFilename(account.HeaderFileName.String, account.HeaderContentType.String)))
	}
}

func (s *Server) removeCustomEmojiLocalFiles(emoji models.CustomEmoji) {
	if s == nil || emoji.ID == 0 {
		return
	}
	s.invalidateCustomEmojiEntityCaches(context.Background(), []models.CustomEmoji{emoji})
	if emoji.ImageFileName.Valid && strings.TrimSpace(emoji.ImageFileName.String) != "" {
		s.deletePaperclipObject(context.Background(), customEmojiObjectKey(emoji, "original", emoji.ImageFileName.String))
		s.deletePaperclipObject(context.Background(), customEmojiObjectKey(emoji, "static", customEmojiStaticFilename(emoji.ImageFileName.String)))
	}
	for _, root := range []string{
		s.cfg.SystemAssetPath("custom_emojis", "images", adminPaperclipIDPartition(emoji.ID)),
		s.cfg.SystemAssetPath("cache", "custom_emojis", "images", adminPaperclipIDPartition(emoji.ID)),
	} {
		_ = os.RemoveAll(root)
	}
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}
