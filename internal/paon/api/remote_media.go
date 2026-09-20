package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type remoteMediaDownload struct {
	filename    string
	contentType string
	body        []byte
}

var (
	errRemoteMediaURLInvalid             = errors.New("invalid remote media url")
	errRemoteMediaHostNotAllowed         = errors.New("remote media host is not allowed")
	errRemoteMediaSizeInvalid            = errors.New("remote media size is invalid")
	errRemoteMediaContentTypeUnsupported = errors.New("remote media content type is unsupported")
	errRemoteMediaNotImage               = errors.New("remote media is not an image")
	errRemoteMediaUnreadable             = errors.New("remote media is unreadable")
)

func (s *Server) cacheRemoteMediaAttachment(tx *gorm.DB, media *models.MediaAttachment, now time.Time) {
	s.cacheRemoteMediaAttachmentConfigured(tx, media, now, true)
}

func (s *Server) cacheRemoteMediaAttachmentConfigured(tx *gorm.DB, media *models.MediaAttachment, now time.Time, enqueueRetry bool) bool {
	ok, err := s.cacheRemoteMediaAttachmentConfiguredResult(tx, media, now)
	if err != nil && enqueueRetry && !remoteMediaErrorUnsalvageable(err) {
		s.enqueueRemoteMediaRedownload(media.ID)
	}
	return ok
}

func (s *Server) cacheRemoteMediaAttachmentConfiguredResult(tx *gorm.DB, media *models.MediaAttachment, now time.Time) (bool, error) {
	if tx == nil || media == nil || s.cfg.DisableRemoteMediaCache || media.RemoteURL == "" || !activityPubMediaAttachmentCacheable(*media) {
		return false, nil
	}
	if s.remoteMediaRejectedByDomainBlock(tx, media.RemoteURL) {
		return false, nil
	}
	maxBytes := s.remoteMediaSizeLimit(media.Type)
	download, err := fetchRemoteMedia(context.Background(), media.RemoteURL, maxBytes, media.Type)
	if err != nil {
		return false, err
	}
	if err := s.storeRemoteMediaFiles(media, download, now); err != nil {
		return false, err
	}
	updates := map[string]any{
		"file_file_name":              media.FileFileName,
		"file_content_type":           media.FileContentType,
		"file_file_size":              media.FileFileSize,
		"file_updated_at":             media.FileUpdatedAt,
		"file_storage_schema_version": media.FileStorageSchemaVersion,
		"thumbnail_file_name":         media.ThumbnailFileName,
		"thumbnail_content_type":      media.ThumbnailContentType,
		"thumbnail_file_size":         media.ThumbnailFileSize,
		"thumbnail_updated_at":        media.ThumbnailUpdatedAt,
		"thumbnail_remote_url":        media.ThumbnailRemoteURL,
		"file_meta":                   media.FileMeta,
		"blurhash":                    media.Blurhash,
		"updated_at":                  now,
	}
	if err := tx.Model(&models.MediaAttachment{}).Where("id = ?", media.ID).Updates(updates).Error; err != nil {
		return false, err
	}
	s.invalidateMediaAttachmentParentStatusCache(context.Background(), *media)
	return true, nil
}

func activityPubMediaAttachmentCacheable(media models.MediaAttachment) bool {
	if media.Type != 0 && media.Type != 1 && media.Type != 2 && media.Type != 4 {
		return false
	}
	contentType := strings.TrimSpace(media.FileContentType.String)
	return contentType == "" || mediaContentTypeSupported(contentType, media.Type)
}

func remoteMediaAttachmentTypeFromHead(ctx context.Context, rawURL string) (int, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return 0, false
	}
	if !activityFetchHostAllowed(parsed.Hostname()) {
		return 0, false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0, false
	}
	client := activityHTTPClient
	if client == nil {
		client = activityHTTPClientClone(5 * time.Second)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		return 0, false
	}
	return mediaTypeFromContentType(contentType), true
}

func (s *Server) remoteMediaSizeLimit(mediaType int) int {
	if mediaType == 2 || mediaType == 4 {
		return s.videoSizeLimitBytes()
	}
	return s.imageSizeLimitBytes()
}

func (s *Server) remoteMediaRejectedByDomainBlock(tx *gorm.DB, rawURL string) bool {
	if tx == nil {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	domains := domainRuleVariants(parsed.Hostname())
	if len(domains) == 0 {
		return false
	}
	var block models.DomainBlock
	err = tx.Where("lower(domain) IN ?", domains).
		Order("char_length(domain) DESC").
		Limit(1).
		First(&block).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	if err != nil {
		return false
	}
	return block.RejectMedia
}

func (s *Server) storeRemoteMediaFiles(media *models.MediaAttachment, download remoteMediaDownload, now time.Time) error {
	if media == nil || media.ID == 0 || len(download.body) == 0 {
		return errors.New("remote media is empty")
	}
	isImage := media.Type == 0 || media.Type == 1
	convertibleImage := mediaOriginalConvertibleImageContentType(download.contentType, media.Type)
	if isImage && !convertibleImage {
		if _, ok := imageConfigFromReader(bytes.NewReader(download.body)); !ok {
			return errors.New("remote media is not a readable image")
		}
	}
	if !mediaContentTypeSupported(download.contentType, media.Type) {
		return errors.New("remote media is not a readable image")
	}
	storedFilename := download.filename
	storedContentType := download.contentType
	if convertibleImage {
		storedFilename = convertedMediaImageFilename(download.filename)
		storedContentType = "image/jpeg"
	}
	if err := os.MkdirAll(filepath.Dir(s.mediaFilePathWithCachePrefix(media.ID, storedFilename, true)), 0o755); err != nil {
		return err
	}
	if convertibleImage {
		source := s.mediaFilePathWithCachePrefix(media.ID, download.filename, true)
		target := s.mediaFilePathWithCachePrefix(media.ID, storedFilename, true)
		if err := os.WriteFile(source, download.body, 0o644); err != nil {
			return err
		}
		defer func() {
			if source != target {
				_ = os.Remove(source)
			}
		}()
		if err := convertImageFileToJPEG(source, target); err != nil {
			return err
		}
	} else {
		if err := os.WriteFile(s.mediaFilePathWithCachePrefix(media.ID, storedFilename, true), download.body, 0o644); err != nil {
			return err
		}
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKeyWithCachePrefix(media.ID, "files", "original", storedFilename, true), s.mediaFilePathWithCachePrefix(media.ID, storedFilename, true), storedContentType); err != nil {
		return err
	}
	info, err := os.Stat(s.mediaFilePathWithCachePrefix(media.ID, storedFilename, true))
	if err != nil {
		return err
	}
	media.FileFileName = sql.NullString{String: storedFilename, Valid: true}
	media.FileContentType = sql.NullString{String: storedContentType, Valid: true}
	media.FileFileSize = sql.NullInt64{Int64: info.Size(), Valid: true}
	media.FileUpdatedAt = sql.NullTime{Time: now, Valid: true}
	media.FileStorageSchemaVersion = sql.NullInt64{Int64: 1, Valid: true}

	media.FileMeta = mediaMetaForStoredFile(s.mediaFilePathWithCachePrefix(media.ID, storedFilename, true), media.Type)
	var (
		thumbnail         mediaThumbnailAttrs
		ok                bool
		separateThumbnail bool
	)
	if remoteThumbnail, err := s.storeRemoteMediaThumbnail(media, now); err == nil && remoteThumbnail.filename != "" {
		thumbnail = remoteThumbnail
		ok = true
		separateThumbnail = true
	} else if convertibleImage {
		if attrs, err := s.generateConvertedImageThumbnailWithCachePrefix(media.ID, storedFilename, now, true); err == nil {
			thumbnail = attrs
			ok = true
		}
	} else {
		thumbnail, ok = s.generateRemoteMediaThumbnail(media, storedFilename, now)
	}
	if ok {
		smallPath := s.mediaFileStylePathWithCachePrefix(media.ID, "small", thumbnail.filename, true)
		if separateThumbnail {
			smallPath = s.mediaThumbnailPathWithCachePrefix(media.ID, thumbnail.filename, true)
		}
		media.FileMeta, _ = mediaMetaWithGeometry(media.FileMeta, "small", smallPath)
		if blurhash := blurhashForStoredImage(smallPath); blurhash != "" {
			media.Blurhash = sql.NullString{String: blurhash, Valid: true}
		}
	}
	return nil
}

func (s *Server) storeRemoteMediaThumbnail(media *models.MediaAttachment, now time.Time) (mediaThumbnailAttrs, error) {
	if media == nil || !media.ThumbnailRemoteURL.Valid || strings.TrimSpace(media.ThumbnailRemoteURL.String) == "" {
		return mediaThumbnailAttrs{}, nil
	}
	download, err := fetchRemoteMedia(context.Background(), media.ThumbnailRemoteURL.String, mediaPreviewImageSizeLimit, 0)
	if err != nil {
		return mediaThumbnailAttrs{}, err
	}
	filename := download.filename
	contentType := download.contentType
	if mediaOriginalConvertibleImageContentType(contentType, 0) {
		filename = convertedMediaImageFilename(filename)
		contentType = "image/jpeg"
	}
	target := s.mediaThumbnailPathWithCachePrefix(media.ID, filename, true)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	if contentType == "image/jpeg" && filename != download.filename {
		source := s.mediaThumbnailPathWithCachePrefix(media.ID, download.filename, true)
		if err := os.WriteFile(source, download.body, 0o644); err != nil {
			return mediaThumbnailAttrs{}, err
		}
		defer func() {
			if source != target {
				_ = os.Remove(source)
			}
		}()
		if err := convertImageFileToJPEG(source, target); err != nil {
			return mediaThumbnailAttrs{}, err
		}
	} else if err := os.WriteFile(target, download.body, 0o644); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKeyWithCachePrefix(media.ID, "thumbnails", "original", filename, true), target, contentType); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return mediaThumbnailAttrs{}, err
	}
	media.ThumbnailFileName = sql.NullString{String: filename, Valid: true}
	media.ThumbnailContentType = sql.NullString{String: contentType, Valid: true}
	media.ThumbnailFileSize = sql.NullInt64{Int64: info.Size(), Valid: true}
	media.ThumbnailUpdatedAt = sql.NullTime{Time: now, Valid: true}
	return mediaThumbnailAttrs{filename: filename, contentType: contentType, size: info.Size(), updatedAt: now}, nil
}

func fetchRemoteImageMedia(ctx context.Context, rawURL string, maxBytes int) (remoteMediaDownload, error) {
	return fetchRemoteMedia(ctx, rawURL, maxBytes, 0)
}

func fetchRemoteMedia(ctx context.Context, rawURL string, maxBytes int, mediaType int) (remoteMediaDownload, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", remoteTaskTargetHost(rawURL), errRemoteMediaURLInvalid)
	}
	if !activityFetchHostAllowed(parsed.Hostname()) {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", parsed.Hostname(), errRemoteMediaHostNotAllowed)
	}
	if maxBytes <= 0 {
		maxBytes = 40 * 1024 * 1024
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", parsed.Hostname(), err)
	}
	req.Header.Set("Accept", remoteMediaAcceptHeader(mediaType))
	resp, err := activityHTTPClient.Do(req)
	if err != nil {
		return remoteMediaDownload{}, taskTargetError("failed to fetch remote media", "remote", parsed.Hostname(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return remoteMediaDownload{}, activityFetchHTTPError{StatusCode: resp.StatusCode, URL: rawURL}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", parsed.Hostname(), err)
	}
	if len(body) == 0 || len(body) > maxBytes {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", parsed.Hostname(), errRemoteMediaSizeInvalid)
	}
	filename := remoteMediaFilename(rawURL, resp.Header.Get("Content-Type"))
	contentType := mediaContentType(filename, resp.Header.Get("Content-Type"))
	if !mediaContentTypeSupported(contentType, mediaType) {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", parsed.Hostname(), errRemoteMediaContentTypeUnsupported)
	}
	if (mediaType == 0 || mediaType == 1) && mediaTypeFromContentType(contentType) != 0 && mediaTypeFromContentType(contentType) != 1 {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", parsed.Hostname(), errRemoteMediaNotImage)
	}
	if (mediaType == 0 || mediaType == 1) && !remoteMediaImageReadable(body) {
		return remoteMediaDownload{}, taskTargetError("remote media fetch", "remote", parsed.Hostname(), errRemoteMediaUnreadable)
	}
	return remoteMediaDownload{filename: filename, contentType: contentType, body: body}, nil
}

func remoteMediaAcceptHeader(mediaType int) string {
	switch mediaType {
	case 2:
		return "video/*"
	case 4:
		return "audio/*"
	default:
		return "image/*"
	}
}

func remoteMediaImageReadable(body []byte) bool {
	_, ok := imageConfigFromReader(bytes.NewReader(body))
	return ok
}

func (s *Server) generateRemoteMediaThumbnail(media *models.MediaAttachment, filename string, now time.Time) (mediaThumbnailAttrs, bool) {
	if media == nil {
		return mediaThumbnailAttrs{}, false
	}
	var (
		attrs mediaThumbnailAttrs
		err   error
	)
	switch media.Type {
	case 0, 1:
		attrs, err = s.generateMediaThumbnailWithCachePrefix(media.ID, filename, now, true)
	case 2:
		attrs, err = s.generateVideoThumbnailWithCachePrefix(media.ID, filename, now, true)
	default:
		return mediaThumbnailAttrs{}, false
	}
	if err != nil {
		return mediaThumbnailAttrs{}, false
	}
	return attrs, true
}

func remoteMediaFilename(rawURL string, contentType string) string {
	filename := ""
	if parsed, err := url.Parse(rawURL); err == nil {
		filename = sanitizeFilename(pathBase(parsed.Path))
	}
	if filename == "" || filename == "." || filename == "/" {
		filename = "media"
	}
	if filepath.Ext(filename) == "" {
		if ext := remoteMediaExtension(contentType); ext != "" {
			filename += ext
		}
	}
	return filename
}

func remoteMediaExtension(contentType string) string {
	contentType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	if contentType == "" || contentType == "application/octet-stream" {
		return ""
	}
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "video/mp4":
		return ".mp4"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	}
	extensions, err := mime.ExtensionsByType(contentType)
	if err != nil || len(extensions) == 0 {
		return ""
	}
	return extensions[0]
}
