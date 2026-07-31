package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const adminMediaPrivacyCheckReadLimit = 64 * 1024
const adminMediaPrivacyTestUploadFilename = "test-upload.jpg"
const adminMediaPrivacyTestUploadContentType = "image/jpeg"
const adminMediaPrivacyTestUploadBase64 = "" +
	"/9j/4QAiRXhpZgAATU0AKgAAAAgAAQESAAMAAAABAAYAAAA" +
	"AAAD/2wCEAAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBA" +
	"QEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE" +
	"BAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAf/AABEIAAEAAgMBEQACEQEDEQH/x" +
	"ABKAAEAAAAAAAAAAAAAAAAAAAALEAEAAAAAAAAAAAAAAAAAAAAAAQEAAAAAAAAAAAAAAAA" +
	"AAAAAEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwA/8H//2Q=="

func (s *Server) adminDashboardMediaPrivacyCheck() (adminDashboardSystemCheck, bool) {
	attachment, ok := s.adminDashboardMediaPrivacyAttachment()
	if !ok {
		return adminDashboardSystemCheck{}, false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	if s.cfg.S3Enabled {
		if s.adminDashboardS3ListingAccessible(client, attachment) {
			return adminDashboardSystemCheck{
				Key:      "upload_check_privacy_error_object_storage",
				Action:   "https://docs.joinmastodon.org/admin/optional/object-storage/#S3",
				Critical: true,
			}, true
		}
		return adminDashboardSystemCheck{}, false
	}
	if s.adminDashboardMediaDirectoryListingAccessible(client, attachment) {
		key := "upload_check_privacy_error"
		action := "https://docs.joinmastodon.org/admin/optional/object-storage/#FS"
		if strings.TrimSpace(s.cfg.StorageHost) != "" || s.cfg.AzureEnabled || s.cfg.SwiftEnabled {
			key = "upload_check_privacy_error_object_storage"
		}
		return adminDashboardSystemCheck{Key: key, Action: action, Critical: true}, true
	}
	return adminDashboardSystemCheck{}, false
}

func (s *Server) adminDashboardMediaPrivacyAttachment() (models.MediaAttachment, bool) {
	if s.db == nil {
		return models.MediaAttachment{}, false
	}
	representative, err := s.representativeActivityPubAccount()
	if err != nil {
		return models.MediaAttachment{}, false
	}
	var attachment models.MediaAttachment
	err = s.db.
		Where("account_id = ? AND file_file_name IS NOT NULL AND file_file_name <> ''", representative.ID).
		Order("id ASC").
		First(&attachment).Error
	if err == nil && strings.TrimSpace(attachment.FileFileName.String) != "" {
		now := time.Now().UTC()
		if err := s.db.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Update("updated_at", now).Error; err == nil {
			s.invalidateMediaAttachmentParentStatusCache(context.Background(), attachment)
		}
		attachment.UpdatedAt = now
		return attachment, true
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.MediaAttachment{}, false
	}
	attachment, err = s.createAdminDashboardMediaPrivacyTestAttachment(representative.ID)
	if err != nil {
		return models.MediaAttachment{}, false
	}
	return attachment, true
}

func (s *Server) createAdminDashboardMediaPrivacyTestAttachment(accountID int64) (models.MediaAttachment, error) {
	payload, err := base64.StdEncoding.DecodeString(adminMediaPrivacyTestUploadBase64)
	if err != nil {
		return models.MediaAttachment{}, err
	}
	now := time.Now().UTC()
	attachment := models.MediaAttachment{
		FileFileName:             sql.NullString{String: adminMediaPrivacyTestUploadFilename, Valid: true},
		FileContentType:          sql.NullString{String: adminMediaPrivacyTestUploadContentType, Valid: true},
		FileFileSize:             sql.NullInt64{Int64: int64(len(payload)), Valid: true},
		FileUpdatedAt:            sql.NullTime{Time: now, Valid: true},
		CreatedAt:                now,
		UpdatedAt:                now,
		RemoteURL:                "",
		Type:                     0,
		AccountID:                sql.NullInt64{Int64: accountID, Valid: true},
		Processing:               sql.NullInt64{Int64: 2, Valid: true},
		FileStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}
	if err := s.db.Create(&attachment).Error; err != nil {
		return models.MediaAttachment{}, err
	}
	target := s.mediaFilePath(attachment.ID, adminMediaPrivacyTestUploadFilename)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		_ = s.db.Delete(&attachment).Error
		return models.MediaAttachment{}, err
	}
	if err := os.WriteFile(target, payload, 0o644); err != nil {
		_ = s.db.Delete(&attachment).Error
		return models.MediaAttachment{}, err
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKey(attachment.ID, "files", "original", adminMediaPrivacyTestUploadFilename), target, adminMediaPrivacyTestUploadContentType); err != nil {
		_ = s.db.Delete(&attachment).Error
		return models.MediaAttachment{}, err
	}
	return attachment, nil
}

func (s *Server) adminDashboardMediaDirectoryListingAccessible(client *http.Client, attachment models.MediaAttachment) bool {
	fileURL := s.mediaAttachmentURL(attachment.ID, "files", "original", attachment.FileFileName.String)
	directoryURL, filename := adminDashboardDirectoryListingURL(fileURL)
	if directoryURL == "" || filename == "" {
		return false
	}
	body, ok := adminDashboardFetchSystemCheckURL(client, directoryURL)
	return ok && strings.Contains(body, filename)
}

func adminDashboardDirectoryListingURL(fileURL string) (string, string) {
	parsed, err := url.Parse(fileURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	filename := path.Base(parsed.EscapedPath())
	if filename == "." || filename == "/" {
		return "", ""
	}
	dir := path.Dir(parsed.EscapedPath())
	if dir == "." || dir == "/" {
		parsed.Path = "/"
	} else {
		parsed.Path = dir + "/"
	}
	parsed.RawPath = ""
	return parsed.String(), filename
}

func (s *Server) adminDashboardS3ListingAccessible(client *http.Client, attachment models.MediaAttachment) bool {
	for _, bucketURL := range s.adminDashboardS3BucketListingURLs(attachment) {
		body, ok := adminDashboardFetchSystemCheckURL(client, bucketURL)
		if ok && strings.Contains(body, "ListBucketResult") {
			return true
		}
	}
	return false
}

func (s *Server) adminDashboardS3BucketListingURLs(attachment models.MediaAttachment) []string {
	key := mediaAttachmentObjectKey(attachment.ID, "files", "original", attachment.FileFileName.String)
	candidates := []string{}
	if storageHost := strings.TrimRight(strings.TrimSpace(s.cfg.StorageHost), "/"); storageHost != "" {
		candidates = append(candidates, adminDashboardS3BucketListingURL(storageHost, key))
	}
	if strings.TrimSpace(s.cfg.S3Bucket) != "" && strings.TrimSpace(s.cfg.S3Hostname) != "" {
		protocol := strings.TrimSpace(s.cfg.S3Protocol)
		if protocol == "" {
			protocol = "https"
		}
		candidates = append(candidates, adminDashboardS3BucketListingURL(protocol+"://"+strings.Trim(s.cfg.S3Hostname, "/")+"/"+strings.Trim(s.cfg.S3Bucket, "/"), key))
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func adminDashboardS3BucketListingURL(storageHost string, objectKey string) string {
	parsed, err := url.Parse(storageHost)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	objectKey = strings.TrimLeft(objectKey, "/")
	basePath := strings.TrimRight(parsed.EscapedPath(), "/")
	if objectKey != "" && strings.HasSuffix(basePath, "/"+objectKey) {
		basePath = strings.TrimSuffix(basePath, "/"+objectKey)
	}
	parsed.Path = basePath
	parsed.RawPath = ""
	query := parsed.Query()
	query.Set("max-keys", "1")
	query.Set("x-random", "paon-go-system-check")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func adminDashboardFetchSystemCheckURL(client *http.Client, rawURL string) (string, bool) {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", false
	}
	res, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, adminMediaPrivacyCheckReadLimit))
	if err != nil {
		return "", false
	}
	return string(body), true
}
