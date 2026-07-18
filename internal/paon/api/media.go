package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"gorm.io/gorm"
)

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

var (
	errInvalidMediaAttachment  = errors.New("invalid media attachment")
	errMediaAttachmentsMixed   = errors.New("media attachments cannot mix images and audio or video")
	errMediaAttachmentNotReady = errors.New("media attachment is not ready")
)

const maxMediaDescriptionLength = 1500
const mediaThumbnailMaxPixels = 230400
const mediaDefaultImageSizeLimit = 40 * 1024 * 1024
const mediaDefaultVideoSizeLimit = 90 * 1024 * 1024
const mediaDefaultMatrixLimit = 16_777_216
const mediaMaxVideoFrameRate = 120.0
const mediaFFProbeTimeout = 5 * time.Second
const mediaFFmpegThumbnailTimeout = 15 * time.Second

var mediaMetaAllowedKeys = map[string]struct{}{
	"focus":    {},
	"colors":   {},
	"original": {},
	"small":    {},
}

func (s *Server) createMedia(c *echo.Context) error {
	return s.createMediaWithOptions(c, false)
}

func (s *Server) createMediaV2(c *echo.Context) error {
	return s.createMediaWithOptions(c, true)
}

func (s *Server) createMediaWithOptions(c *echo.Context, delayLargerMedia bool) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "write", "write:media")
	if err != nil {
		return err
	}

	header, err := c.FormFile("file")
	if err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "File is required")
	}
	if header.Size <= 0 {
		return apiError(c, http.StatusUnprocessableEntity, "File is empty")
	}

	filename := paperclipObfuscatedFilename(header.Filename)
	contentType := mediaContentType(filename, header.Header.Get("Content-Type"))
	mediaType := mediaTypeFromContentType(contentType)
	if mediaType == 3 {
		return apiError(c, http.StatusUnprocessableEntity, "File type of uploaded media could not be verified")
	}
	if !mediaContentTypeSupported(contentType, mediaType) {
		return apiError(c, http.StatusUnprocessableEntity, "File type of uploaded media could not be verified")
	}
	if message := s.validateUploadedMediaAttachment(header, mediaType); message != "" {
		return apiError(c, http.StatusUnprocessableEntity, message)
	}
	convertibleImage := mediaOriginalConvertibleImageContentType(contentType, mediaType)
	storedFilename := filename
	storedContentType := contentType
	if convertibleImage {
		storedFilename = convertedMediaImageFilename(filename)
		storedContentType = "image/jpeg"
	}
	readableOriginal := false
	if mediaOriginalRequiresReadableImage(contentType, mediaType) {
		if _, err := imageConfigFromHeader(header); err != nil {
			return apiError(c, http.StatusUnprocessableEntity, "File type of uploaded media could not be verified")
		}
		readableOriginal = true
	}
	description := c.FormValue("description")
	if len([]rune(description)) > maxMediaDescriptionLength {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Description is too long")
	}
	focus := c.FormValue("focus")
	if strings.TrimSpace(focus) != "" {
		if _, _, ok := parseMediaFocus(focus); !ok {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Focus is invalid")
		}
	}
	thumbnailHeader, hasThumbnail, err := optionalFormFile(c, "thumbnail")
	if err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "Thumbnail is invalid")
	}
	if hasThumbnail && !mediaAttachmentAllowsUploadedThumbnail(mediaType) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Thumbnail must be blank")
	}
	if hasThumbnail && thumbnailHeader.Size >= int64(s.imageSizeLimitBytes()) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Thumbnail is too large")
	}
	thumbnailAttrs, err := mediaThumbnailAttributes(thumbnailHeader)
	if err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "Thumbnail is invalid")
	}

	now := time.Now().UTC()
	fileSize := header.Size
	processing := mediaProcessingForCreate(delayLargerMedia, mediaType)
	synchronousPostProcess := mediaSynchronousPostProcessForCreate(delayLargerMedia, mediaType)
	initialProcessing := processing
	if synchronousPostProcess {
		initialProcessing = 0
	}
	attachment := models.MediaAttachment{
		FileFileName:             sql.NullString{String: storedFilename, Valid: true},
		FileContentType:          sql.NullString{String: storedContentType, Valid: true},
		FileFileSize:             sql.NullInt64{Int64: fileSize, Valid: true},
		FileUpdatedAt:            sql.NullTime{Time: now, Valid: true},
		CreatedAt:                now,
		UpdatedAt:                now,
		RemoteURL:                "",
		Type:                     mediaType,
		AccountID:                sql.NullInt64{Int64: account.ID, Valid: true},
		Description:              sql.NullString{String: description, Valid: description != ""},
		Processing:               sql.NullInt64{Int64: initialProcessing, Valid: true},
		FileStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}

	if err := s.db.Create(&attachment).Error; err != nil {
		return err
	}

	if convertibleImage {
		convertedSize, err := s.storeConvertedMediaImage(c.Request().Context(), header, attachment.ID, filename, storedFilename)
		if err != nil {
			_ = s.db.Delete(&attachment).Error
			return err
		}
		fileSize = convertedSize
		attachment.FileFileSize = sql.NullInt64{Int64: fileSize, Valid: true}
		if err := s.db.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Updates(map[string]any{
			"file_file_name":    storedFilename,
			"file_content_type": storedContentType,
			"file_file_size":    fileSize,
			"file_updated_at":   now,
			"updated_at":        time.Now().UTC(),
		}).Error; err != nil {
			_ = s.db.Delete(&attachment).Error
			return err
		}
	} else if err := s.storeMediaFile(c.Request().Context(), header, attachment.ID, storedFilename, storedContentType); err != nil {
		_ = s.db.Delete(&attachment).Error
		return err
	}
	if hasThumbnail {
		if err := s.storeMediaThumbnail(c.Request().Context(), thumbnailHeader, attachment.ID, thumbnailAttrs.filename, thumbnailAttrs.contentType); err != nil {
			_ = s.db.Delete(&attachment).Error
			return err
		}
		blurhash := blurhashForStoredImage(s.mediaThumbnailPath(attachment.ID, thumbnailAttrs.filename))
		updates := map[string]any{
			"thumbnail_file_name":    thumbnailAttrs.filename,
			"thumbnail_content_type": thumbnailAttrs.contentType,
			"thumbnail_file_size":    thumbnailAttrs.size,
			"thumbnail_updated_at":   now,
			"updated_at":             time.Now().UTC(),
		}
		if blurhash != "" {
			updates["blurhash"] = blurhash
			attachment.Blurhash = sql.NullString{String: blurhash, Valid: true}
		}
		if err := s.db.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Updates(updates).Error; err != nil {
			_ = s.db.Delete(&attachment).Error
			return err
		}
		attachment.ThumbnailFileName = sql.NullString{String: thumbnailAttrs.filename, Valid: true}
		attachment.ThumbnailContentType = sql.NullString{String: thumbnailAttrs.contentType, Valid: true}
		attachment.ThumbnailFileSize = sql.NullInt64{Int64: thumbnailAttrs.size, Valid: true}
		attachment.ThumbnailUpdatedAt = sql.NullTime{Time: now, Valid: true}
	} else if readableOriginal || convertibleImage {
		if convertibleImage {
			thumbnailAttrs, err = s.generateConvertedImageThumbnail(attachment.ID, storedFilename, now)
		} else {
			thumbnailAttrs, err = s.generateMediaThumbnail(attachment.ID, storedFilename, now)
		}
		if err != nil {
			_ = s.db.Delete(&attachment).Error
			return err
		}
		smallPath := s.mediaFileStylePath(attachment.ID, "small", thumbnailAttrs.filename)
		blurhash := blurhashForStoredImage(smallPath)
		updates := map[string]any{"updated_at": time.Now().UTC()}
		if blurhash != "" {
			updates["blurhash"] = blurhash
			attachment.Blurhash = sql.NullString{String: blurhash, Valid: true}
		}
		if err := s.db.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Updates(updates).Error; err != nil {
			_ = s.db.Delete(&attachment).Error
			return err
		}
	}

	meta := mediaMetaForStoredFile(s.mediaFilePath(attachment.ID, storedFilename), mediaType)
	if attachment.ThumbnailFileName.Valid && attachment.ThumbnailFileName.String != "" {
		meta, _ = mediaMetaWithGeometry(meta, "small", s.mediaThumbnailPath(attachment.ID, attachment.ThumbnailFileName.String))
	} else if smallFilename := mediaGeneratedSmallStyleFilename(storedFilename, mediaType); smallFilename != "" {
		meta, _ = mediaMetaWithGeometry(meta, "small", s.mediaFileStylePath(attachment.ID, "small", smallFilename))
	}
	if strings.TrimSpace(focus) != "" {
		meta, _ = mediaMetaWithFocus(meta, focus)
	}
	if len(meta) > 0 {
		_ = s.db.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Updates(map[string]any{
			"file_meta":  meta,
			"updated_at": time.Now().UTC(),
		}).Error
		attachment.FileMeta = meta
	}
	if synchronousPostProcess {
		if ok, err := s.postProcessMediaAttachment(c.Request().Context(), attachment, time.Now().UTC(), false); err != nil || !ok {
			return apiError(c, http.StatusInternalServerError, mediaAttachmentProcessingError)
		}
		if err := s.db.Where("id = ?", attachment.ID).First(&attachment).Error; err != nil {
			return err
		}
	} else if processing == 0 {
		s.enqueueMediaPostProcessTask(attachment.ID)
	}

	return c.JSON(mediaCreateStatusCode(attachment), serializer.MediaAttachmentFromModel(s.cfg, attachment))
}

func (s *Server) showMedia(c *echo.Context) error {
	attachment, err := s.findOwnedPendingMedia(c)
	if err != nil {
		var apiErr apiHTTPError
		if errors.As(err, &apiErr) {
			return err
		}
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if mediaAttachmentProcessingFailed(*attachment) {
		return apiError(c, http.StatusUnprocessableEntity, mediaAttachmentProcessingError)
	}
	return c.JSON(mediaAttachmentStatusCode(*attachment), serializer.MediaAttachmentFromModel(s.cfg, *attachment))
}

func (s *Server) publicMedia(c *echo.Context) error {
	if err := s.requireMediaProxyAuthenticationIfLimited(c); err != nil {
		return err
	}
	current, _, _ := s.currentAccount(c)
	attachment, err := s.findPublicMediaAttachment(publicPathWithoutAnyFormat(c.Param("id")), current)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	target := s.publicMediaRedirectURL(*attachment)
	if target == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.Redirect(http.StatusFound, target)
}

func (s *Server) mediaProxy(c *echo.Context) error {
	if err := s.requireMediaProxyAuthenticationIfLimited(c); err != nil {
		return err
	}
	acquired, releaseMediaLock, err := s.acquireActivityPubRedisLock(c.Request().Context(), "media_download:"+c.Param("id"), 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseMediaLock()
	current, _, _ := s.currentAccount(c)
	attachment, err := s.findProxyMediaAttachment(c.Param("id"), true, current)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	target := s.mediaProxyRedirectURL(*attachment, mediaProxyVersion(c.Request().URL.Path))
	if target == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.Redirect(http.StatusFound, target)
}

func (s *Server) downloadProxy(c *echo.Context) error {
	if err := s.requireMediaProxyAuthenticationIfLimited(c); err != nil {
		return err
	}
	acquired, releaseMediaLock, err := s.acquireActivityPubRedisLock(c.Request().Context(), "media_download:"+c.Param("id"), 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseMediaLock()
	current, _, _ := s.currentAccount(c)
	attachment, err := s.findProxyMediaAttachment(c.Param("id"), false, current)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	version := mediaProxyVersion(c.Request().URL.Path)
	target := s.downloadProxyTargetURL(*attachment, version)
	if target == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	s.setDownloadProxyHeaders(c, *attachment, target)
	if localPath := s.downloadProxyLocalPath(*attachment, version); localPath != "" {
		if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
			return s.serveDownloadProxyLocalFile(c, localPath)
		}
	}
	if remoteURL := downloadProxyRemoteURL(*attachment); remoteURL != "" {
		if streamed, err := s.streamDownloadProxyRemote(c, remoteURL); err != nil {
			return err
		} else if streamed {
			return nil
		}
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.Redirect(http.StatusFound, target)
}

func (s *Server) serveDownloadProxyLocalFile(c *echo.Context, localPath string) error {
	if header := strings.TrimSpace(s.cfg.SendfileHeader); header != "" {
		c.Response().Header().Set(header, localPath)
		return c.NoContent(http.StatusOK)
	}
	http.ServeFile(c.Response(), c.Request(), localPath)
	return nil
}

func (s *Server) requireMediaProxyAuthenticationIfLimited(c *echo.Context) error {
	if s == nil || !s.cfg.LimitedFederationMode {
		return nil
	}
	if _, _, _, err := s.requireFunctionalAccountForWeb(c); err != nil {
		return webAuthResponseError(err)
	}
	return nil
}

func (s *Server) publicMediaPlayer(c *echo.Context) error {
	if err := s.requireMediaProxyAuthenticationIfLimited(c); err != nil {
		return err
	}
	current, _, _ := s.currentAccount(c)
	attachment, err := s.findPublicMediaAttachment(publicPathWithoutAnyFormat(c.Param("id")), current)
	if err != nil || !playableMediaAttachment(*attachment) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	target := s.publicMediaRedirectURL(*attachment)
	if target == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	c.Response().Header().Del("X-Frame-Options")
	c.Response().Header().Set("Content-Security-Policy", railsContentSecurityPolicyWithoutDirective(s.cfg, "frame-ancestors"))
	return c.HTML(http.StatusOK, s.mediaPlayerHTML(target, *attachment))
}

func (s *Server) updateMedia(c *echo.Context) error {
	attachment, err := s.findOwnedPendingMedia(c)
	if err != nil {
		var apiErr apiHTTPError
		if errors.As(err, &apiErr) {
			return err
		}
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if mediaAttachmentProcessingFailed(*attachment) {
		return apiError(c, http.StatusUnprocessableEntity, mediaAttachmentProcessingError)
	}

	updates := map[string]any{"updated_at": time.Now().UTC()}
	description, hasDescription := formField(c, "description")
	focus, hasFocus := formField(c, "focus")
	thumbnailHeader, hasThumbnail, err := optionalFormFile(c, "thumbnail")
	if err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "Thumbnail is invalid")
	}
	if hasThumbnail && !mediaAttachmentAllowsUploadedThumbnail(attachment.Type) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Thumbnail must be blank")
	}
	if hasThumbnail && thumbnailHeader.Size >= int64(s.imageSizeLimitBytes()) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Thumbnail is too large")
	}
	thumbnailAttrs, err := mediaThumbnailAttributes(thumbnailHeader)
	if err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "Thumbnail is invalid")
	}
	if !hasDescription || !hasFocus {
		jsonDescription, jsonHasDescription, jsonFocus, jsonHasFocus := mediaUpdateJSONFields(c)
		if !hasDescription && jsonHasDescription {
			description = jsonDescription
			hasDescription = true
		}
		if !hasFocus && jsonHasFocus {
			focus = jsonFocus
			hasFocus = true
		}
	}
	if hasDescription {
		if len([]rune(description)) > maxMediaDescriptionLength {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Description is too long")
		}
		updates["description"] = sql.NullString{String: description, Valid: description != ""}
	}
	if hasFocus && strings.TrimSpace(focus) != "" {
		meta, ok := mediaMetaWithFocus(attachment.FileMeta, focus)
		if !ok {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Focus is invalid")
		}
		updates["file_meta"] = meta
		attachment.FileMeta = meta
	}
	if hasThumbnail {
		if err := s.storeMediaThumbnail(c.Request().Context(), thumbnailHeader, attachment.ID, thumbnailAttrs.filename, thumbnailAttrs.contentType); err != nil {
			return err
		}
		blurhash := blurhashForStoredImage(s.mediaThumbnailPath(attachment.ID, thumbnailAttrs.filename))
		updates["thumbnail_file_name"] = thumbnailAttrs.filename
		updates["thumbnail_content_type"] = thumbnailAttrs.contentType
		updates["thumbnail_file_size"] = thumbnailAttrs.size
		updates["thumbnail_updated_at"] = time.Now().UTC()
		attachment.ThumbnailFileName = sql.NullString{String: thumbnailAttrs.filename, Valid: true}
		attachment.ThumbnailContentType = sql.NullString{String: thumbnailAttrs.contentType, Valid: true}
		attachment.ThumbnailFileSize = sql.NullInt64{Int64: thumbnailAttrs.size, Valid: true}
		attachment.ThumbnailUpdatedAt = sql.NullTime{Time: updates["thumbnail_updated_at"].(time.Time), Valid: true}
		if blurhash != "" {
			updates["blurhash"] = blurhash
			attachment.Blurhash = sql.NullString{String: blurhash, Valid: true}
		}
		meta, ok := mediaMetaWithGeometry(attachment.FileMeta, "small", s.mediaThumbnailPath(attachment.ID, thumbnailAttrs.filename))
		if ok {
			updates["file_meta"] = meta
			attachment.FileMeta = meta
		}
	}
	if err := s.db.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Updates(updates).Error; err != nil {
		return err
	}
	s.invalidateMediaAttachmentParentStatusCache(c.Request().Context(), *attachment)
	if hasDescription {
		attachment.Description = sql.NullString{String: description, Valid: description != ""}
	}
	return c.JSON(mediaAttachmentStatusCode(*attachment), serializer.MediaAttachmentFromModel(s.cfg, *attachment))
}

func mediaUpdateJSONFields(c *echo.Context) (string, bool, string, bool) {
	if !strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		return "", false, "", false
	}
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return "", false, "", false
	}
	description, hasDescription := mediaUpdateJSONString(payload, "description", true)
	focus, hasFocus := mediaUpdateJSONString(payload, "focus", false)
	return description, hasDescription, focus, hasFocus
}

func mediaUpdateJSONString(payload map[string]json.RawMessage, key string, nullMeansPresent bool) (string, bool) {
	raw, ok := payload[key]
	if !ok {
		return "", false
	}
	if string(raw) == "null" {
		return "", nullMeansPresent
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

const mediaAttachmentProcessingError = "Error processing thumbnail for uploaded media"

func mediaAttachmentStatusCode(attachment models.MediaAttachment) int {
	if mediaAttachmentNotProcessed(attachment) {
		return http.StatusPartialContent
	}
	return http.StatusOK
}

func mediaAttachmentNotProcessed(attachment models.MediaAttachment) bool {
	if !attachment.Processing.Valid {
		return false
	}
	return attachment.Processing.Int64 != 2
}

func mediaAttachmentProcessingFailed(attachment models.MediaAttachment) bool {
	return attachment.Processing.Valid && attachment.Processing.Int64 == 3
}

func mediaCreateStatusCode(attachment models.MediaAttachment) int {
	if mediaAttachmentNotProcessed(attachment) {
		return http.StatusAccepted
	}
	return http.StatusOK
}

func mediaProcessingForCreate(delayProcessing bool, mediaType int) int64 {
	if delayProcessing && largerMediaFormat(mediaType) {
		return 0
	}
	return 2
}

func mediaSynchronousPostProcessForCreate(delayProcessing bool, mediaType int) bool {
	return !delayProcessing && largerMediaFormat(mediaType)
}

func largerMediaFormat(mediaType int) bool {
	return mediaType == 1 || mediaType == 2 || mediaType == 4
}

func mediaAttachmentAllowsUploadedThumbnail(mediaType int) bool {
	return mediaType == 2 || mediaType == 4
}

func (s *Server) validateUploadedMediaAttachment(header *multipart.FileHeader, mediaType int) string {
	if header == nil {
		return ""
	}
	if header.Size >= s.mediaSizeLimitBytes(mediaType) {
		return "Validation failed: File is too large"
	}
	switch mediaType {
	case 0, 1:
		return s.validateUploadedImageDimensions(header, mediaType)
	case 2:
		return s.validateUploadedVideoDimensions(header)
	default:
		return ""
	}
}

func (s *Server) validateUploadedImageDimensions(header *multipart.FileHeader, mediaType int) string {
	cfg, err := imageConfigFromHeader(header)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return ""
	}
	if cfg.Width*cfg.Height <= s.mediaMatrixLimit() {
		return ""
	}
	if mediaType == 1 {
		return strconv.Itoa(cfg.Width) + "x" + strconv.Itoa(cfg.Height) + " GIF files are not supported"
	}
	return strconv.Itoa(cfg.Width) + "x" + strconv.Itoa(cfg.Height) + " images are not supported"
}

func (s *Server) validateUploadedVideoDimensions(header *multipart.FileHeader) string {
	path, cleanup, ok := tempMediaUploadPath(header)
	if !ok {
		return ""
	}
	defer cleanup()
	metadata := mediaTranscodeMetadataForFile(path)
	if !metadata.valid {
		return ""
	}
	if metadata.width <= 0 || metadata.height <= 0 || parseFrameRate(metadata.rFrameRate) <= 0 {
		return "Video has no video stream"
	}
	if metadata.width*metadata.height > s.mediaMatrixLimit() {
		return strconv.Itoa(metadata.width) + "x" + strconv.Itoa(metadata.height) + " videos are not supported"
	}
	frameRate := parseFrameRate(metadata.rFrameRate)
	if math.Floor(frameRate) > mediaMaxVideoFrameRate {
		return strconv.Itoa(int(math.Floor(frameRate))) + "fps videos are not supported"
	}
	return ""
}

func tempMediaUploadPath(header *multipart.FileHeader) (string, func(), bool) {
	file, err := header.Open()
	if err != nil {
		return "", func() {}, false
	}
	defer file.Close()
	tmp, err := os.CreateTemp("", "paon-media-upload-*"+filepath.Ext(sanitizeFilename(header.Filename)))
	if err != nil {
		return "", func() {}, false
	}
	path := tmp.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, false
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, false
	}
	return path, cleanup, true
}

func (s *Server) mediaSizeLimitBytes(mediaType int) int64 {
	if largerMediaFormat(mediaType) {
		return int64(s.videoSizeLimitBytes())
	}
	return int64(s.imageSizeLimitBytes())
}

func (s *Server) imageSizeLimitBytes() int {
	if s != nil && (s.cfg.ImageSizeLimitSet || s.cfg.ImageSizeLimit > 0) {
		return s.cfg.ImageSizeLimit
	}
	return mediaDefaultImageSizeLimit
}

func (s *Server) mediaMatrixLimit() int {
	if s != nil && (s.cfg.MatrixLimitSet || s.cfg.MatrixLimit > 0) {
		return s.cfg.MatrixLimit
	}
	return mediaDefaultMatrixLimit
}

func (s *Server) findPublicMediaAttachment(rawID string, current *models.Account) (*models.MediaAttachment, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	query := s.db.Preload("Status.Account").
		Where("status_id IS NOT NULL").
		Where("file_file_name IS NOT NULL").
		Where("remote_url = ''")
	if len(rawID) == 19 {
		query = query.Where("shortcode = ?", rawID)
	} else {
		query = query.Where("id = ?", rawID)
	}
	var attachment models.MediaAttachment
	if err := query.First(&attachment).Error; err != nil {
		return nil, err
	}
	visible, err := s.publicMediaStatusVisible(attachment.Status, current)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, gorm.ErrRecordNotFound
	}
	return &attachment, nil
}

func (s *Server) findProxyMediaAttachment(rawID string, remoteOnly bool, current *models.Account) (*models.MediaAttachment, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	query := s.db.Preload("Status.Account").
		Where("status_id IS NOT NULL").
		Where("(file_file_name IS NOT NULL OR remote_url <> '')")
	if remoteOnly {
		query = query.Where("remote_url <> ''")
	}
	if len(rawID) == 19 {
		query = query.Where("shortcode = ?", rawID)
	} else {
		query = query.Where("id = ?", rawID)
	}
	var attachment models.MediaAttachment
	if err := query.First(&attachment).Error; err != nil {
		return nil, err
	}
	visible, err := s.publicMediaStatusVisible(attachment.Status, current)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, gorm.ErrRecordNotFound
	}
	if err := s.redownloadProxyMediaAttachment(&attachment); err != nil {
		return nil, err
	}
	return &attachment, nil
}

func (s *Server) proxyMediaRejectedByAccountDomain(attachment models.MediaAttachment) (bool, error) {
	if s.db == nil || !attachment.AccountID.Valid {
		return false, nil
	}
	var account models.Account
	err := s.db.Select("id", "domain").Where("id = ?", attachment.AccountID.Int64).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return activityPubDomainRejectsMedia(s.db, account.Domain)
}

func (s *Server) redownloadProxyMediaAttachment(attachment *models.MediaAttachment) error {
	if s.db == nil || attachment == nil || !proxyMediaNeedsRedownload(*attachment, s.cfg.DisableRemoteMediaCache) {
		return nil
	}
	rejected, err := s.proxyMediaRejectedByAccountDomain(*attachment)
	if err != nil || rejected {
		return err
	}
	now := time.Now().UTC()
	ok, err := s.cacheRemoteMediaAttachmentConfiguredResult(s.db, attachment, now)
	if err != nil || !ok {
		return err
	}
	attachment.CreatedAt = now
	return s.db.Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Update("created_at", now).Error
}

func proxyMediaNeedsRedownload(attachment models.MediaAttachment, disableRemoteMediaCache bool) bool {
	return !disableRemoteMediaCache && attachment.RemoteURL != "" && (!attachment.FileFileName.Valid || attachment.FileFileName.String == "")
}

func (s *Server) publicMediaStatusVisible(status models.Status, current *models.Account) (bool, error) {
	if status.ID == 0 || status.DeletedAt.Valid || status.ReblogOfID.Valid {
		return false, nil
	}
	author := status.Account
	if author.ID == 0 {
		author.ID = status.AccountID
	}
	return s.activityPubStatusVisible(status, author, current)
}

func (s *Server) publicMediaRedirectURL(attachment models.MediaAttachment) string {
	if !attachment.FileFileName.Valid || attachment.FileFileName.String == "" {
		return ""
	}
	return s.mediaAttachmentRedirectURLForAttachment(attachment, "files", "original", attachment.FileFileName.String)
}

func (s *Server) mediaProxyRedirectURL(attachment models.MediaAttachment, version string) string {
	if version == "small" {
		if attachment.ThumbnailFileName.Valid && attachment.ThumbnailFileName.String != "" {
			return s.mediaAttachmentRedirectURLForAttachment(attachment, "thumbnails", "original", attachment.ThumbnailFileName.String)
		}
		if mediaProxyHasLocalFile(attachment) && mediaProxyProcessed(attachment) && mediaProxyHasSmallFileStyle(attachment) {
			return s.mediaAttachmentRedirectURLForAttachment(attachment, "files", "small", mediaGeneratedSmallStyleFilename(attachment.FileFileName.String, attachment.Type))
		}
		if attachment.ThumbnailRemoteURL.Valid && attachment.ThumbnailRemoteURL.String != "" {
			return attachment.ThumbnailRemoteURL.String
		}
	}
	if mediaProxyHasLocalFile(attachment) {
		return s.mediaAttachmentRedirectURLForAttachment(attachment, "files", "original", attachment.FileFileName.String)
	}
	if strings.TrimSpace(attachment.RemoteURL) != "" {
		return attachment.RemoteURL
	}
	return ""
}

func (s *Server) downloadProxyTargetURL(attachment models.MediaAttachment, version string) string {
	if mediaProxyHasLocalFile(attachment) {
		return s.mediaProxyRedirectURL(attachment, version)
	}
	if strings.TrimSpace(attachment.RemoteURL) != "" {
		return attachment.RemoteURL
	}
	return ""
}

func (s *Server) downloadProxyLocalPath(attachment models.MediaAttachment, version string) string {
	if s == nil || s.cfg.PublicDir == "" {
		return ""
	}
	if version == "small" && attachment.ThumbnailFileName.Valid && strings.TrimSpace(attachment.ThumbnailFileName.String) != "" {
		return s.mediaThumbnailPath(attachment.ID, filepath.Base(strings.TrimSpace(attachment.ThumbnailFileName.String)))
	}
	if version == "small" && mediaProxyHasLocalFile(attachment) && mediaProxyProcessed(attachment) && mediaProxyHasSmallFileStyle(attachment) {
		return s.mediaFileStylePath(attachment.ID, "small", filepath.Base(mediaGeneratedSmallStyleFilename(attachment.FileFileName.String, attachment.Type)))
	}
	if attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != "" {
		return s.mediaFilePath(attachment.ID, filepath.Base(strings.TrimSpace(attachment.FileFileName.String)))
	}
	return ""
}

func mediaProxyHasLocalFile(attachment models.MediaAttachment) bool {
	return attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != ""
}

func mediaProxyProcessed(attachment models.MediaAttachment) bool {
	return !attachment.Processing.Valid || attachment.Processing.Int64 == 2
}

func mediaProxyHasSmallFileStyle(attachment models.MediaAttachment) bool {
	return attachment.Type == 0 || attachment.Type == 1 || attachment.Type == 2
}

func (s *Server) setDownloadProxyHeaders(c *echo.Context, attachment models.MediaAttachment, target string) {
	c.Response().Header().Set("Access-Control-Allow-Origin", "*")
	c.Response().Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	c.Response().Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	contentType := ""
	if attachment.FileContentType.Valid && strings.TrimSpace(attachment.FileContentType.String) != "" {
		contentType = strings.TrimSpace(attachment.FileContentType.String)
	} else if ext := filepath.Ext(downloadFilename(target)); ext != "" {
		contentType = mime.TypeByExtension(ext)
	}
	if contentType != "" {
		c.Response().Header().Set("Content-Type", contentType)
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+downloadFilename(target)+`"`)
}

func downloadProxyRemoteURL(attachment models.MediaAttachment) string {
	if strings.TrimSpace(attachment.RemoteURL) == "" {
		return ""
	}
	return attachment.RemoteURL
}

func (s *Server) streamDownloadProxyRemote(c *echo.Context, rawURL string) (bool, error) {
	if !remoteFetchURLAllowed(rawURL) {
		return false, nil
	}
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		return false, nil
	}
	resp, err := activityHTTPClient.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" && c.Response().Header().Get("Content-Type") == "" {
		c.Response().Header().Set("Content-Type", contentType)
	}
	if c.Response().Header().Get("Content-Type") == "" {
		c.Response().Header().Set("Content-Type", "application/octet-stream")
	}
	c.Response().WriteHeader(http.StatusOK)
	_, err = io.Copy(c.Response(), resp.Body)
	return err == nil, err
}

func mediaProxyVersion(path string) string {
	if strings.HasSuffix(path, "/small") {
		return "small"
	}
	return "original"
}

func downloadFilename(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "media"
	}
	if parsed.Scheme == "" && parsed.Host == "" {
		return "media"
	}
	name := filepath.Base(parsed.Path)
	if name == "." || name == "/" || strings.TrimSpace(name) == "" {
		return "media"
	}
	return strings.ReplaceAll(name, `"`, "")
}

func playableMediaAttachment(attachment models.MediaAttachment) bool {
	return attachment.Type == 1 || attachment.Type == 2 || attachment.Type == 4
}

func (s *Server) mediaPlayerHTML(src string, attachment models.MediaAttachment) string {
	player := s.mediaPlayerComponentHTML(src, attachment)
	publicScript := ""
	if s != nil {
		publicScript = s.packAssetPath("public.js")
	}
	if publicScript == "" {
		publicScript = web.FallbackPackAssetPath("public.js")
	}
	scriptHTML := `<script src="` + html.EscapeString(publicScript) + `" crossorigin="anonymous" defer></script>`
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="robots" content="noindex">
  ` + scriptHTML + `
  <style>
    html,body{margin:0;width:100%;height:100%;background:#000;color:#fff}
    body{display:flex;align-items:center;justify-content:center}
    video,audio{width:100%;height:100%;max-width:100%;max-height:100%;outline:0}
    audio{height:48px}
  </style>
</head>
<body>` + player + `</body>
</html>`
}

func (s *Server) mediaPlayerComponentHTML(src string, attachment models.MediaAttachment) string {
	media := serializer.MediaAttachmentFromModel(s.cfg, attachment)
	media.URL = src
	if media.PreviewURL == "" {
		media.PreviewURL = s.mediaPlayerPreviewURL(attachment)
	}
	props := s.mediaPlayerProps(src, attachment, media)
	raw, _ := json.Marshal(props)
	component := "Video"
	fallback := `<video controls="controls"><source src="` + html.EscapeString(src) + `"></video>`
	switch attachment.Type {
	case 1:
		component = "MediaGallery"
		fallback = `<video autoplay="autoplay" muted="muted" loop="loop"><source src="` + html.EscapeString(src) + `"></video>`
	case 4:
		component = "Audio"
		fallback = `<audio controls="controls"><source src="` + html.EscapeString(src) + `"></audio>`
	}
	return `<div data-component="` + component + `" data-props="` + html.EscapeString(string(raw)) + `">` + fallback + `</div>`
}

func (s *Server) mediaPlayerProps(src string, attachment models.MediaAttachment, media serializer.MediaAttachment) map[string]any {
	meta := mediaPlayerMetaMap(attachment.FileMeta)
	props := map[string]any{
		"media": []serializer.MediaAttachment{media},
	}
	switch attachment.Type {
	case 1:
		props["height"] = 380
		props["standalone"] = true
		props["autoplay"] = true
	case 4:
		props["src"] = src
		props["poster"] = s.mediaPlayerPosterURL(attachment, media)
		props["width"] = 670
		props["height"] = 380
		props["fullscreen"] = true
		props["alt"] = attachment.Description.String
		if colors := mediaPlayerNestedMap(meta, "colors"); colors != nil {
			if value, ok := colors["background"]; ok {
				props["backgroundColor"] = value
			}
			if value, ok := colors["foreground"]; ok {
				props["foregroundColor"] = value
			}
			if value, ok := colors["accent"]; ok {
				props["accentColor"] = value
			}
		}
		if original := mediaPlayerNestedMap(meta, "original"); original != nil {
			if value, ok := original["duration"]; ok {
				props["duration"] = value
			}
		}
	default:
		props["src"] = src
		props["preview"] = media.PreviewURL
		if original := mediaPlayerNestedMap(meta, "original"); original != nil {
			if value, ok := original["frame_rate"]; ok {
				props["frameRate"] = value
			}
		}
		props["blurhash"] = attachment.Blurhash.String
		props["width"] = 670
		props["height"] = 380
		props["editable"] = true
		props["detailed"] = true
		props["inline"] = true
		props["alt"] = attachment.Description.String
	}
	return props
}

func (s *Server) mediaPlayerPreviewURL(attachment models.MediaAttachment) string {
	if attachment.ThumbnailFileName.Valid && strings.TrimSpace(attachment.ThumbnailFileName.String) != "" {
		return s.mediaAttachmentURLForAttachment(attachment, "thumbnails", "original", attachment.ThumbnailFileName.String)
	}
	if mediaProxyHasSmallFileStyle(attachment) && attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != "" {
		return s.mediaAttachmentURLForAttachment(attachment, "files", "small", attachment.FileFileName.String)
	}
	return ""
}

func (s *Server) mediaPlayerPosterURL(attachment models.MediaAttachment, media serializer.MediaAttachment) string {
	if media.PreviewURL != "" {
		return media.PreviewURL
	}
	if attachment.Status.Account.ID != 0 {
		return serializer.AccountFromModel(s.cfg, attachment.Status.Account).AvatarStatic
	}
	return ""
}

func mediaPlayerMetaMap(raw []byte) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func mediaPlayerNestedMap(meta map[string]any, key string) map[string]any {
	if meta == nil {
		return nil
	}
	if nested, ok := meta[key].(map[string]any); ok {
		return nested
	}
	return nil
}

func (s *Server) findOwnedPendingMedia(c *echo.Context) (*models.MediaAttachment, error) {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "write", "write:media")
	if err != nil {
		return nil, err
	}
	var attachment models.MediaAttachment
	err = s.db.Where("id = ? AND account_id = ? AND status_id IS NULL", c.Param("id"), account.ID).First(&attachment).Error
	return &attachment, err
}

func (s *Server) attachMediaToStatus(tx *gorm.DB, accountID int64, statusID int64, mediaIDs []string) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	res := tx.Model(&models.MediaAttachment{}).
		Where("account_id = ? AND status_id IS NULL AND id IN ?", accountID, mediaIDs).
		Updates(map[string]any{"status_id": statusID, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != int64(len(mediaIDs)) {
		return errInvalidMediaAttachment
	}
	return nil
}

func mediaIDsFromForm(c *echo.Context) []string {
	values, err := c.FormValues()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(values["media_ids[]"]))
	ids = append(ids, values["media_ids[]"]...)
	return ids
}

func formField(c *echo.Context, name string) (string, bool) {
	values, err := c.FormValues()
	if err != nil {
		return "", false
	}
	items, ok := values[name]
	if !ok || len(items) == 0 {
		return "", false
	}
	return items[0], true
}

func formBoolField(c *echo.Context, name string) (bool, bool) {
	value, ok := formField(c, name)
	if !ok {
		return false, false
	}
	return truthy(value), true
}

func compactMediaIDs(values []string) []string {
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func submittedMediaIDsPresent(values []string) bool {
	return len(values) > 0
}

func submittedMediaIDsCount(values []string) int {
	return len(values)
}

func (s *Server) storeMediaFile(ctx context.Context, header *multipart.FileHeader, id int64, filename string, contentType string) error {
	target := s.mediaFilePath(id, filename)
	if err := s.storeMediaAttachmentFile(header, target); err != nil {
		return err
	}
	return s.uploadPaperclipObject(ctx, mediaAttachmentObjectKey(id, "files", "original", filename), target, contentType)
}

func (s *Server) storeConvertedMediaImage(ctx context.Context, header *multipart.FileHeader, id int64, sourceFilename string, targetFilename string) (int64, error) {
	source := s.mediaFilePath(id, sourceFilename)
	target := s.mediaFilePath(id, targetFilename)
	if err := s.storeMediaAttachmentFile(header, source); err != nil {
		return 0, err
	}
	defer func() {
		if source != target {
			_ = os.Remove(source)
		}
	}()
	if err := convertImageFileToJPEG(source, target); err != nil {
		return 0, err
	}
	if err := s.uploadPaperclipObject(ctx, mediaAttachmentObjectKey(id, "files", "original", targetFilename), target, "image/jpeg"); err != nil {
		return 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *Server) storeMediaThumbnail(ctx context.Context, header *multipart.FileHeader, id int64, filename string, contentType string) error {
	target := s.mediaThumbnailPath(id, filename)
	if err := s.storeMediaAttachmentFile(header, target); err != nil {
		return err
	}
	return s.uploadPaperclipObject(ctx, mediaAttachmentObjectKey(id, "thumbnails", "original", filename), target, contentType)
}

func (s *Server) generateMediaThumbnail(id int64, filename string, now time.Time) (mediaThumbnailAttrs, error) {
	return s.generateMediaThumbnailWithCachePrefix(id, filename, now, false)
}

func (s *Server) generateMediaThumbnailWithCachePrefix(id int64, filename string, now time.Time, cachePrefix bool) (mediaThumbnailAttrs, error) {
	source := s.mediaFilePathWithCachePrefix(id, filename, cachePrefix)
	thumbnailFilename := mediaGeneratedSmallStyleFilename(filename, 0)
	contentType := mediaSmallImageContentType(thumbnailFilename)
	target := s.mediaFileStylePathWithCachePrefix(id, "small", thumbnailFilename, cachePrefix)
	if err := generateImageSmallFile(source, target, contentType); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKeyWithCachePrefix(id, "files", "small", thumbnailFilename, cachePrefix), target, contentType); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return mediaThumbnailAttrs{}, err
	}
	return mediaThumbnailAttrs{
		filename:    thumbnailFilename,
		contentType: contentType,
		size:        info.Size(),
		updatedAt:   now,
	}, nil
}

func (s *Server) generateConvertedImageThumbnail(id int64, filename string, now time.Time) (mediaThumbnailAttrs, error) {
	return s.generateConvertedImageThumbnailWithCachePrefix(id, filename, now, false)
}

func (s *Server) generateConvertedImageThumbnailWithCachePrefix(id int64, filename string, now time.Time, cachePrefix bool) (mediaThumbnailAttrs, error) {
	source := s.mediaFilePathWithCachePrefix(id, filename, cachePrefix)
	thumbnailFilename := mediaGeneratedSmallStyleFilename(filename, 0)
	target := s.mediaFileStylePathWithCachePrefix(id, "small", thumbnailFilename, cachePrefix)
	if err := generateJPEGThumbnailFile(source, target); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKeyWithCachePrefix(id, "files", "small", thumbnailFilename, cachePrefix), target, "image/jpeg"); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return mediaThumbnailAttrs{}, err
	}
	return mediaThumbnailAttrs{
		filename:    thumbnailFilename,
		contentType: "image/jpeg",
		size:        info.Size(),
		updatedAt:   now,
	}, nil
}

func (s *Server) generateVideoThumbnail(id int64, filename string, now time.Time) (mediaThumbnailAttrs, error) {
	return s.generateVideoThumbnailWithCachePrefix(id, filename, now, false)
}

func (s *Server) generateVideoThumbnailWithCachePrefix(id int64, filename string, now time.Time, cachePrefix bool) (mediaThumbnailAttrs, error) {
	source := s.mediaFilePathWithCachePrefix(id, filename, cachePrefix)
	thumbnailFilename := mediaGeneratedSmallStyleFilename(filename, 2)
	target := s.mediaFileStylePathWithCachePrefix(id, "small", thumbnailFilename, cachePrefix)
	if err := generateVideoThumbnailFile(source, target); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKeyWithCachePrefix(id, "files", "small", thumbnailFilename, cachePrefix), target, "image/png"); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return mediaThumbnailAttrs{}, err
	}
	return mediaThumbnailAttrs{
		filename:    thumbnailFilename,
		contentType: "image/png",
		size:        info.Size(),
		updatedAt:   now,
	}, nil
}

func (s *Server) generateAudioThumbnail(id int64, filename string, now time.Time) (mediaThumbnailAttrs, error) {
	source := s.mediaFilePath(id, filename)
	thumbnailFilename := generatedMediaThumbnailFilename(filename)
	target := s.mediaThumbnailPath(id, thumbnailFilename)
	if err := generateAudioThumbnailFile(source, target); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKey(id, "thumbnails", "original", thumbnailFilename), target, "image/png"); err != nil {
		return mediaThumbnailAttrs{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return mediaThumbnailAttrs{}, err
	}
	return mediaThumbnailAttrs{
		filename:    thumbnailFilename,
		contentType: "image/png",
		size:        info.Size(),
		updatedAt:   now,
	}, nil
}

func generateVideoThumbnailFile(source string, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaFFmpegThumbnailTimeout)
	defer cancel()
	return exec.CommandContext(ctx, mediaFFmpegBinary(), "-y", "-i", source, "-frames:v", "1", "-vf", "thumbnail,scale=480:-2", target).Run()
}

func generateAudioThumbnailFile(source string, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaFFmpegThumbnailTimeout)
	defer cancel()
	return exec.CommandContext(ctx, mediaFFmpegBinary(), "-y", "-i", source, "-loglevel", "fatal", target).Run()
}

func (s *Server) storeMediaAttachmentFile(header *multipart.FileHeader, target string) error {
	src, err := header.Open()
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
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func (s *Server) mediaFilePath(id int64, filename string) string {
	return s.mediaFileStylePath(id, "original", filename)
}

func (s *Server) mediaFileStylePath(id int64, style string, filename string) string {
	return s.mediaFileStylePathWithCachePrefix(id, style, filename, false)
}

func (s *Server) mediaThumbnailPath(id int64, filename string) string {
	return s.mediaThumbnailPathWithCachePrefix(id, filename, false)
}

func (s *Server) mediaFilePathWithCachePrefix(id int64, filename string, cachePrefix bool) string {
	return s.mediaFileStylePathWithCachePrefix(id, "original", filename, cachePrefix)
}

func (s *Server) mediaFileStylePathWithCachePrefix(id int64, style string, filename string, cachePrefix bool) string {
	parts := []string{"media_attachments", "files", mediaPaperclipIDPartition(id), style, filename}
	if cachePrefix {
		parts = append([]string{"cache"}, parts...)
	}
	return s.cfg.SystemAssetPath(parts...)
}

func (s *Server) mediaThumbnailPathWithCachePrefix(id int64, filename string, cachePrefix bool) string {
	parts := []string{"media_attachments", "thumbnails", mediaPaperclipIDPartition(id), "original", filename}
	if cachePrefix {
		parts = append([]string{"cache"}, parts...)
	}
	return s.cfg.SystemAssetPath(parts...)
}

func (s *Server) mediaAttachmentURL(id int64, attachment string, style string, filename string) string {
	return s.mediaAttachmentURLWithCachePrefix(id, attachment, style, filename, false)
}

func (s *Server) mediaAttachmentURLForAttachment(media models.MediaAttachment, attachment string, style string, filename string) string {
	return s.mediaAttachmentURLWithCachePrefix(media.ID, attachment, style, filename, mediaAttachmentUsesCachePrefix(media.FileStorageSchemaVersion, media.RemoteURL))
}

func (s *Server) mediaAttachmentURLWithCachePrefix(id int64, attachment string, style string, filename string, cachePrefix bool) string {
	prefix := ""
	if cachePrefix {
		prefix = "cache/"
	}
	return s.cfg.SystemAssetURL(prefix + "media_attachments/" + attachment + "/" + strings.ReplaceAll(mediaPaperclipIDPartition(id), string(filepath.Separator), "/") + "/" + style + "/" + url.PathEscape(filename))
}

func (s *Server) mediaAttachmentRedirectURL(id int64, attachment string, style string, filename string) string {
	return s.mediaAttachmentRedirectURLWithCachePrefix(id, attachment, style, filename, false)
}

func (s *Server) mediaAttachmentRedirectURLForAttachment(media models.MediaAttachment, attachment string, style string, filename string) string {
	return s.mediaAttachmentRedirectURLWithCachePrefix(media.ID, attachment, style, filename, mediaAttachmentUsesCachePrefix(media.FileStorageSchemaVersion, media.RemoteURL))
}

func (s *Server) mediaAttachmentRedirectURLWithCachePrefix(id int64, attachment string, style string, filename string, cachePrefix bool) string {
	key := mediaAttachmentObjectKeyWithCachePrefix(id, attachment, style, filename, cachePrefix)
	if signed := s.presignedS3ObjectURL(key, time.Hour); signed != "" {
		return signed
	}
	if signed := s.presignedAzureBlobURL(key, time.Hour); signed != "" {
		return signed
	}
	if signed := s.presignedSwiftObjectURL(key, time.Hour); signed != "" {
		return signed
	}
	return s.mediaAttachmentURLWithCachePrefix(id, attachment, style, filename, cachePrefix)
}

func mediaAttachmentObjectKey(id int64, attachment string, style string, filename string) string {
	return mediaAttachmentObjectKeyWithCachePrefix(id, attachment, style, filename, false)
}

func mediaAttachmentObjectKeyForAttachment(media models.MediaAttachment, attachment string, style string, filename string) string {
	return mediaAttachmentObjectKeyWithCachePrefix(media.ID, attachment, style, filename, mediaAttachmentUsesCachePrefix(media.FileStorageSchemaVersion, media.RemoteURL))
}

func mediaAttachmentObjectKeyWithCachePrefix(id int64, attachment string, style string, filename string, cachePrefix bool) string {
	prefix := ""
	if cachePrefix {
		prefix = "cache/"
	}
	return prefix + "media_attachments/" + attachment + "/" + strings.ReplaceAll(mediaPaperclipIDPartition(id), string(filepath.Separator), "/") + "/" + style + "/" + filename
}

func mediaAttachmentUsesCachePrefix(storageSchemaVersion sql.NullInt64, remoteURL string) bool {
	return storageSchemaVersion.Valid && storageSchemaVersion.Int64 >= 1 && strings.TrimSpace(remoteURL) != ""
}

type mediaThumbnailAttrs struct {
	filename    string
	contentType string
	size        int64
	updatedAt   time.Time
}

func optionalFormFile(c *echo.Context, name string) (*multipart.FileHeader, bool, error) {
	if !strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "multipart/form-data") {
		return nil, false, nil
	}
	header, err := c.FormFile(name)
	if err == nil {
		return header, true, nil
	}
	if errors.Is(err, http.ErrMissingFile) {
		return nil, false, nil
	}
	return nil, false, err
}

func mediaThumbnailAttributes(header *multipart.FileHeader) (mediaThumbnailAttrs, error) {
	if header == nil {
		return mediaThumbnailAttrs{}, nil
	}
	if header.Size <= 0 {
		return mediaThumbnailAttrs{}, errInvalidMediaAttachment
	}
	filename := sanitizeFilename(header.Filename)
	contentType := mediaContentType(filename, header.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "image/") {
		return mediaThumbnailAttrs{}, errInvalidMediaAttachment
	}
	if _, err := imageConfigFromHeader(header); err != nil {
		return mediaThumbnailAttrs{}, errInvalidMediaAttachment
	}
	return mediaThumbnailAttrs{filename: filename, contentType: contentType, size: header.Size}, nil
}

func imageConfigFromHeader(header *multipart.FileHeader) (image.Config, error) {
	file, err := header.Open()
	if err != nil {
		return image.Config{}, err
	}
	defer file.Close()
	cfg, ok := imageConfigFromReader(file)
	if !ok {
		return image.Config{}, errInvalidMediaAttachment
	}
	return cfg, nil
}

func imageConfigFromReader(reader io.Reader) (image.Config, bool) {
	cfg, _, err := image.DecodeConfig(reader)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return image.Config{}, false
	}
	return cfg, true
}

func generatedMediaThumbnailFilename(filename string) string {
	stem := strings.TrimSuffix(sanitizeFilename(filename), filepath.Ext(filename))
	stem = strings.Trim(stem, "._")
	if stem == "" {
		stem = "thumbnail"
	}
	return stem + ".png"
}

func mediaGeneratedSmallStyleFilename(filename string, mediaType int) string {
	filename = sanitizeFilename(filename)
	if filename == "" {
		return ""
	}
	if mediaType == 1 || mediaType == 2 {
		return generatedMediaThumbnailFilename(filename)
	}
	return filename
}

func mediaSmallImageContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg", ".jpe":
		return "image/jpeg"
	default:
		return "image/png"
	}
}

func convertedMediaImageFilename(filename string) string {
	stem := strings.TrimSuffix(sanitizeFilename(filename), filepath.Ext(filename))
	stem = strings.Trim(stem, "._")
	if stem == "" {
		stem = "media"
	}
	return stem + ".jpeg"
}

func convertImageFileToJPEG(source string, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaFFmpegThumbnailTimeout)
	defer cancel()
	return exec.CommandContext(ctx, mediaFFmpegBinary(), "-y", "-i", source, "-frames:v", "1", "-update", "1", target).Run()
}

func generateImageSmallFile(source string, target string, contentType string) error {
	if contentType == "image/jpeg" {
		return generateJPEGThumbnailFile(source, target)
	}
	return generateImageThumbnailFile(source, target)
}

func generateImageThumbnailFile(source string, target string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	img, _, err := image.Decode(src)
	if err != nil {
		return err
	}
	resized := resizeImageToMaxPixels(img, mediaThumbnailMaxPixels)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	dst, err := os.Create(target)
	if err != nil {
		return err
	}
	defer dst.Close()
	return png.Encode(dst, resized)
}

func generateJPEGThumbnailFile(source string, target string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	img, _, err := image.Decode(src)
	if err != nil {
		return err
	}
	resized := resizeImageToMaxPixels(img, mediaThumbnailMaxPixels)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	dst, err := os.Create(target)
	if err != nil {
		return err
	}
	defer dst.Close()
	return jpeg.Encode(dst, resized, &jpeg.Options{Quality: 90})
}

func resizeImageToMaxPixels(img image.Image, maxPixels int) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 || maxPixels <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	targetWidth, targetHeight := thumbnailDimensions(width, height, maxPixels)
	out := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	xdraw.CatmullRom.Scale(out, out.Bounds(), img, bounds, xdraw.Over, nil)
	return out
}

func resizeImageToFill(img image.Image, targetWidth int, targetHeight int) image.Image {
	if targetWidth <= 0 || targetHeight <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	}
	srcX := 0
	srcY := 0
	srcWidth := width
	srcHeight := height
	sourceRatio := float64(width) / float64(height)
	targetRatio := float64(targetWidth) / float64(targetHeight)
	if sourceRatio > targetRatio {
		srcWidth = int(float64(height)*targetRatio + 0.5)
		if srcWidth < 1 {
			srcWidth = 1
		}
		srcX = (width - srcWidth) / 2
	} else if sourceRatio < targetRatio {
		srcHeight = int(float64(width)/targetRatio + 0.5)
		if srcHeight < 1 {
			srcHeight = 1
		}
		srcY = (height - srcHeight) / 2
	}
	out := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	srcRect := image.Rect(bounds.Min.X+srcX, bounds.Min.Y+srcY, bounds.Min.X+srcX+srcWidth, bounds.Min.Y+srcY+srcHeight)
	xdraw.CatmullRom.Scale(out, out.Bounds(), img, srcRect, xdraw.Over, nil)
	return out
}

func thumbnailDimensions(width int, height int, maxPixels int) (int, int) {
	if width <= 0 || height <= 0 || maxPixels <= 0 {
		return 1, 1
	}
	if width*height <= maxPixels {
		return width, height
	}
	ratio := float64(width) / float64(height)
	targetHeight := int(math.Sqrt(float64(maxPixels)/ratio) + 0.5)
	if targetHeight < 1 {
		targetHeight = 1
	}
	targetWidth := int(float64(targetHeight)*ratio + 0.5)
	if targetWidth < 1 {
		targetWidth = 1
	}
	for targetWidth*targetHeight > maxPixels {
		if targetWidth >= targetHeight && targetWidth > 1 {
			targetWidth--
		} else if targetHeight > 1 {
			targetHeight--
		} else {
			break
		}
	}
	return targetWidth, targetHeight
}

func mediaPaperclipIDPartition(id int64) string {
	value := strconv.FormatInt(id, 10)
	if len(value) < 9 {
		value = strings.Repeat("0", 9-len(value)) + value
	}
	parts := make([]string, 0, (len(value)+2)/3)
	for len(value) > 3 {
		parts = append(parts, value[:3])
		value = value[3:]
	}
	parts = append(parts, value)
	return filepath.Join(parts...)
}

func mediaMetaForStoredFile(path string, mediaType int) []byte {
	switch mediaType {
	case 0:
		geometry, ok := mediaGeometryForStoredFile(path)
		if !ok {
			return nil
		}
		meta, _ := json.Marshal(map[string]any{
			"original": geometry,
		})
		return meta
	case 1, 2, 4:
		return mediaMetaFromFFProbe(path)
	default:
		return nil
	}
}

func mediaMetaFromFFProbe(path string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), mediaFFProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, mediaFFprobeBinary(), "-i", path, "-print_format", "json", "-show_format", "-show_streams", "-show_error", "-loglevel", "fatal").Output()
	if err != nil {
		return nil
	}
	return mediaMetaFromFFProbeJSON(output)
}

func mediaFFmpegBinary() string {
	return mediaToolBinaryFromEnv("FFMPEG_BINARY", "ffmpeg")
}

func mediaFFprobeBinary() string {
	return mediaToolBinaryFromEnv("FFPROBE_BINARY", "ffprobe")
}

func mediaToolBinaryFromEnv(key string, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return value
}

func mediaMetaFromFFProbeJSON(raw []byte) []byte {
	var payload struct {
		Format struct {
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			CodecType    string `json:"codec_type"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
			RFrameRate   string `json:"r_frame_rate"`
			SideDataList []struct {
				Rotation any `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Error != nil {
		return nil
	}
	original := map[string]any{}
	if duration := positiveFloatString(payload.Format.Duration); duration > 0 {
		original["duration"] = duration
	}
	if bitrate := positiveIntString(payload.Format.BitRate); bitrate > 0 {
		original["bitrate"] = bitrate
	}
	for _, stream := range payload.Streams {
		if stream.CodecType != "video" {
			continue
		}
		width, height := stream.Width, stream.Height
		if mediaStreamRotated90(stream.SideDataList) {
			width, height = height, width
		}
		if width > 0 && height > 0 {
			original["width"] = width
			original["height"] = height
			original["size"] = strconv.Itoa(width) + "x" + strconv.Itoa(height)
			original["aspect"] = float64(width) / float64(height)
		}
		if frameRate := firstValidFrameRate(stream.AvgFrameRate, stream.RFrameRate); frameRate != "" {
			original["frame_rate"] = frameRate
		}
		break
	}
	if len(original) == 0 {
		return nil
	}
	out, err := json.Marshal(map[string]any{"original": original})
	if err != nil {
		return nil
	}
	return out
}

func firstValidFrameRate(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "0/0" {
			continue
		}
		if strings.Contains(value, "/") {
			parts := strings.SplitN(value, "/", 2)
			if positiveFloatString(parts[0]) > 0 && positiveFloatString(parts[1]) > 0 {
				return value
			}
			continue
		}
		if positiveFloatString(value) > 0 {
			return value
		}
	}
	return ""
}

func mediaStreamRotated90(values []struct {
	Rotation any `json:"rotation"`
}) bool {
	for _, value := range values {
		switch rotation := value.Rotation.(type) {
		case float64:
			if int(math.Abs(rotation)) == 90 {
				return true
			}
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(rotation), 64)
			if err == nil && int(math.Abs(parsed)) == 90 {
				return true
			}
		}
	}
	return false
}

func positiveFloatString(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func positiveIntString(value string) int {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return int(parsed)
}

func mergeMediaMetadata(previous []byte, next []byte) []byte {
	if len(next) == 0 {
		return sliceMediaMetadata(previous)
	}
	if len(previous) == 0 {
		return sliceMediaMetadata(next)
	}
	merged := map[string]any{}
	if err := json.Unmarshal(previous, &merged); err != nil {
		merged = map[string]any{}
	}
	incoming := map[string]any{}
	if err := json.Unmarshal(next, &incoming); err != nil {
		return sliceMediaMetadata(previous)
	}
	for key, value := range incoming {
		merged[key] = value
	}
	return marshalMediaMetadata(merged, sliceMediaMetadata(previous))
}

func sliceMediaMetadata(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	meta := map[string]any{}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return raw
	}
	return marshalMediaMetadata(meta, raw)
}

func marshalMediaMetadata(meta map[string]any, fallback []byte) []byte {
	filtered := make(map[string]any, len(mediaMetaAllowedKeys))
	for key, value := range meta {
		if _, ok := mediaMetaAllowedKeys[key]; ok {
			filtered[key] = value
		}
	}
	out, err := json.Marshal(filtered)
	if err != nil {
		return fallback
	}
	return out
}

func mediaMetaWithGeometry(raw []byte, key string, path string) ([]byte, bool) {
	geometry, ok := mediaGeometryForStoredFile(path)
	if !ok {
		return raw, false
	}
	meta := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			meta = map[string]any{}
		}
	}
	meta[key] = geometry
	out, err := json.Marshal(meta)
	return out, err == nil
}

func mediaGeometryForStoredFile(path string) (map[string]any, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	cfg, ok := imageConfigFromReader(file)
	if !ok {
		return nil, false
	}
	return map[string]any{
		"width":  cfg.Width,
		"height": cfg.Height,
		"size":   strconv.Itoa(cfg.Width) + "x" + strconv.Itoa(cfg.Height),
		"aspect": float64(cfg.Width) / float64(cfg.Height),
	}, true
}

func mediaMetaWithFocus(raw []byte, focus string) ([]byte, bool) {
	x, y, ok := parseMediaFocus(focus)
	if !ok {
		return nil, false
	}
	meta := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			meta = map[string]any{}
		}
	}
	meta["focus"] = map[string]any{"x": x, "y": y}
	out, err := json.Marshal(meta)
	return out, err == nil
}

func parseMediaFocus(value string) (float64, float64, bool) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	x, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, false
	}
	y, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, false
	}
	return x, y, true
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	ext := filepath.Ext(name)
	if ext == "." {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	stem = unsafeFilenameChars.ReplaceAllString(stem, "_")
	stem = strings.Trim(stem, "._")
	if stem == "" {
		stem = "upload"
	}
	ext = unsafeFilenameChars.ReplaceAllString(ext, "")
	ext = strings.Trim(ext, ".")
	if ext == "" {
		return stem
	}
	return stem + "." + ext
}

func paperclipObfuscatedFilename(name string) string {
	filename := sanitizeFilename(name)
	ext := filepath.Ext(filename)
	return randomHex(8) + ext
}

func mediaContentType(filename string, header string) string {
	header = strings.TrimSpace(strings.Split(header, ";")[0])
	if header != "" && header != "application/octet-stream" {
		return header
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if extType := railsMediaContentTypeByExtension(ext); extType != "" {
		return extType
	}
	if extType := mime.TypeByExtension(ext); extType != "" {
		return strings.Split(extType, ";")[0]
	}
	return "application/octet-stream"
}

func railsMediaContentTypeByExtension(ext string) string {
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".avif":
		return "image/avif"
	case ".webm":
		return "video/webm"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	case ".opus":
		return "audio/ogg"
	case ".aac":
		return "audio/aac"
	case ".m4a":
		return "audio/m4a"
	case ".3gp":
		return "audio/3gpp"
	case ".wma":
		return "video/x-ms-asf"
	default:
		return ""
	}
}

func mediaTypeFromContentType(contentType string) int {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch {
	case contentType == "video/x-ms-asf":
		return 4
	case strings.HasPrefix(contentType, "image/gif"):
		return 1
	case strings.HasPrefix(contentType, "image/"):
		return 0
	case strings.HasPrefix(contentType, "video/"):
		return 2
	case strings.HasPrefix(contentType, "audio/"):
		return 4
	default:
		return 3
	}
}

func mediaContentTypeSupported(contentType string, mediaType int) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mediaType {
	case 0, 1:
		switch contentType {
		case "image/jpeg", "image/png", "image/gif", "image/heic", "image/heif", "image/webp", "image/avif":
			return true
		default:
			return false
		}
	case 2:
		switch contentType {
		case "video/webm", "video/mp4", "video/quicktime", "video/ogg":
			return true
		default:
			return false
		}
	case 4:
		switch contentType {
		case "audio/wave", "audio/wav", "audio/x-wav", "audio/x-pn-wave", "audio/vnd.wave", "audio/ogg", "audio/vorbis", "audio/mpeg", "audio/mp3", "audio/webm", "audio/flac", "audio/aac", "audio/m4a", "audio/x-m4a", "audio/mp4", "audio/3gpp", "video/x-ms-asf":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func mediaOriginalRequiresReadableImage(contentType string, mediaType int) bool {
	if mediaType != 0 && mediaType != 1 {
		return false
	}
	if mediaOriginalConvertibleImageContentType(contentType, mediaType) {
		return false
	}
	return mediaOriginalCanGenerateThumbnail(contentType, mediaType)
}

func mediaOriginalConvertibleImageContentType(contentType string, mediaType int) bool {
	if mediaType != 0 {
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch contentType {
	case "image/heic", "image/heif", "image/avif":
		return true
	default:
		return false
	}
}

func mediaOriginalCanGenerateThumbnail(contentType string, mediaType int) bool {
	if mediaType != 0 && mediaType != 1 {
		return false
	}
	if mediaOriginalConvertibleImageContentType(contentType, mediaType) {
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
