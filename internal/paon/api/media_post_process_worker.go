package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	mediaPostProcessWorkerInterval = 15 * time.Second
	mediaPostProcessBatchSize      = 25
)

func (s *Server) runMediaPostProcessWorker(ctx context.Context) {
	s.processQueuedMediaAttachments(ctx, mediaPostProcessBatchSize)
	ticker := time.NewTicker(mediaPostProcessWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processQueuedMediaAttachments(ctx, mediaPostProcessBatchSize)
		}
	}
}

func (s *Server) processQueuedMediaAttachments(ctx context.Context, limit int) int {
	if s == nil || s.db == nil || limit <= 0 {
		return 0
	}
	var attachments []models.MediaAttachment
	if err := s.db.WithContext(ctx).
		Where("processing = ?", 0).
		Where("file_file_name IS NOT NULL").
		Where("remote_url = '' OR remote_url IS NULL").
		Order("id ASC").
		Limit(limit).
		Find(&attachments).Error; err != nil {
		return 0
	}
	processed := 0
	now := time.Now().UTC()
	for _, attachment := range attachments {
		ok, err := s.postProcessMediaAttachment(ctx, attachment, now, false)
		if err != nil {
			s.markMediaPostProcessFailed(ctx, attachment.ID)
			continue
		}
		if ok {
			processed++
		}
	}
	return processed
}

func (s *Server) postProcessMediaAttachmentByID(ctx context.Context, mediaAttachmentID int64) error {
	if s == nil || s.db == nil || mediaAttachmentID == 0 {
		return nil
	}
	var attachment models.MediaAttachment
	if err := s.db.WithContext(ctx).
		Where("id = ?", mediaAttachmentID).
		First(&attachment).Error; err != nil {
		return workerLookupError("post process media attachment lookup", err)
	}
	if attachment.Processing.Valid && attachment.Processing.Int64 != 0 && attachment.Processing.Int64 != 1 {
		return nil
	}
	if ok, err := s.postProcessMediaAttachment(ctx, attachment, time.Now().UTC(), true); err != nil {
		return err
	} else if ok {
		return nil
	}
	var current models.MediaAttachment
	if err := s.db.WithContext(ctx).
		Select("id", "processing").
		Where("id = ?", mediaAttachmentID).
		First(&current).Error; err != nil {
		return workerLookupError("post process media state lookup", err)
	}
	if current.Processing.Valid && current.Processing.Int64 != 0 {
		return nil
	}
	return sql.ErrNoRows
}

func (s *Server) markMediaPostProcessFailed(ctx context.Context, mediaAttachmentID int64) {
	if s == nil || s.db == nil || mediaAttachmentID == 0 {
		return
	}
	_ = s.db.WithContext(ctx).
		Model(&models.MediaAttachment{}).
		Where("id = ?", mediaAttachmentID).
		Updates(map[string]any{
			"processing": 3,
			"updated_at": time.Now().UTC(),
		}).Error
}

func (s *Server) postProcessMediaAttachment(ctx context.Context, attachment models.MediaAttachment, now time.Time, allowInProgress bool) (bool, error) {
	if s == nil || s.db == nil || attachment.ID == 0 {
		return false, nil
	}
	query := s.db.WithContext(ctx).
		Model(&models.MediaAttachment{}).
		Where("id = ?", attachment.ID)
	if allowInProgress {
		query = query.Where("processing IN ?", []int64{0, 1})
	} else {
		query = query.Where("processing = ?", 0)
	}
	claimed := query.Updates(map[string]any{"processing": 1, "updated_at": now})
	if claimed.Error != nil || claimed.RowsAffected == 0 {
		return false, nil
	}
	s.invalidateMediaAttachmentParentStatusCache(ctx, attachment)
	processing := int64(2)
	if !s.mediaAttachmentOriginalExists(attachment) {
		processing = 3
	}
	updates := map[string]any{"processing": processing, "updated_at": time.Now().UTC()}
	if processing == 2 {
		if attachment.Type == 4 {
			for key, value := range s.mediaAttachmentPostProcessThumbnail(&attachment, time.Now().UTC()) {
				updates[key] = value
			}
		}
		if transcodeUpdates, _, err := s.transcodeMediaOriginal(&attachment, time.Now().UTC()); err != nil {
			return false, err
		} else if len(transcodeUpdates) > 0 {
			for key, value := range transcodeUpdates {
				updates[key] = value
			}
			if name, ok := transcodeUpdates["file_file_name"].(string); ok {
				attachment.FileFileName = sql.NullString{String: name, Valid: true}
			}
			if ct, ok := transcodeUpdates["file_content_type"].(string); ok {
				attachment.FileContentType = sql.NullString{String: ct, Valid: true}
			}
		}
		for key, value := range s.mediaAttachmentPostProcessThumbnail(&attachment, time.Now().UTC()) {
			updates[key] = value
		}
		if meta := s.mediaAttachmentPostProcessMeta(attachment); len(meta) > 0 {
			updates["file_meta"] = meta
		}
	}
	if err := s.db.WithContext(ctx).
		Model(&models.MediaAttachment{}).
		Where("id = ?", attachment.ID).
		Updates(updates).Error; err != nil {
		return false, nil
	}
	s.invalidateMediaAttachmentParentStatusCache(ctx, attachment)
	return true, nil
}

func (s *Server) mediaAttachmentPostProcessThumbnail(attachment *models.MediaAttachment, now time.Time) map[string]any {
	if attachment == nil || !mediaAttachmentPostProcessGeneratesThumbnail(*attachment) || attachmentHasLocalThumbnail(*attachment) {
		return nil
	}
	filename := strings.TrimSpace(attachment.FileFileName.String)
	if filename == "" {
		return nil
	}
	var (
		attrs mediaThumbnailAttrs
		err   error
	)
	if attachment.Type == 4 {
		attrs, err = s.generateAudioThumbnail(attachment.ID, filename, now)
	} else {
		attrs, err = s.generateVideoThumbnail(attachment.ID, filename, now)
	}
	if err != nil {
		return nil
	}
	updates := map[string]any{}
	previewPath := s.mediaFileStylePath(attachment.ID, "small", attrs.filename)
	if attachment.Type == 4 {
		updates["thumbnail_file_name"] = attrs.filename
		updates["thumbnail_content_type"] = attrs.contentType
		updates["thumbnail_file_size"] = attrs.size
		updates["thumbnail_updated_at"] = attrs.updatedAt
		attachment.ThumbnailFileName = sqlNullString(attrs.filename)
		attachment.ThumbnailContentType = sqlNullString(attrs.contentType)
		attachment.ThumbnailFileSize = sql.NullInt64{Int64: attrs.size, Valid: true}
		attachment.ThumbnailUpdatedAt = sql.NullTime{Time: attrs.updatedAt, Valid: true}
		previewPath = s.mediaThumbnailPath(attachment.ID, attrs.filename)
	}
	if blurhash := blurhashForStoredImage(previewPath); blurhash != "" {
		updates["blurhash"] = blurhash
		attachment.Blurhash = sqlNullString(blurhash)
	}
	return updates
}

func (s *Server) mediaAttachmentPostProcessMeta(attachment models.MediaAttachment) []byte {
	meta := []byte(attachment.FileMeta)
	if attachment.FileFileName.Valid {
		filename := strings.TrimSpace(attachment.FileFileName.String)
		if filename != "" {
			meta = mergeMediaMetadata(meta, mediaMetaForStoredFile(s.mediaFilePath(attachment.ID, filename), attachment.Type))
		}
	}
	if attachment.ThumbnailFileName.Valid {
		filename := strings.TrimSpace(attachment.ThumbnailFileName.String)
		if filename != "" {
			if withThumbnail, ok := mediaMetaWithGeometry(meta, "small", s.mediaThumbnailPath(attachment.ID, filename)); ok {
				meta = withThumbnail
			}
		}
	} else if smallFilename := mediaGeneratedSmallStyleFilename(strings.TrimSpace(attachment.FileFileName.String), attachment.Type); smallFilename != "" {
		if withSmall, ok := mediaMetaWithGeometry(meta, "small", s.mediaFileStylePath(attachment.ID, "small", smallFilename)); ok {
			meta = withSmall
		}
	}
	return meta
}

func mediaAttachmentPostProcessGeneratesThumbnail(attachment models.MediaAttachment) bool {
	return (attachment.Type == 1 || attachment.Type == 2 || attachment.Type == 4) && attachment.FileFileName.Valid
}

func attachmentHasLocalThumbnail(attachment models.MediaAttachment) bool {
	return attachment.ThumbnailFileName.Valid && strings.TrimSpace(attachment.ThumbnailFileName.String) != ""
}

func (s *Server) mediaAttachmentOriginalExists(attachment models.MediaAttachment) bool {
	if s == nil || attachment.ID == 0 || !attachment.FileFileName.Valid {
		return false
	}
	filename := strings.TrimSpace(attachment.FileFileName.String)
	if filename == "" {
		return false
	}
	if _, err := os.Stat(s.mediaFilePath(attachment.ID, filename)); err != nil {
		return false
	}
	return true
}

// mediaFFmpegTranscodeTimeout bounds ffmpeg re-encode time for a single queued attachment.
const mediaFFmpegTranscodeTimeout = 5 * time.Minute

// transcodeMediaOriginal mirrors Rails' Paperclip `transcoder` processor: it re-encodes a
// queued local video/audio original into Mastodon's canonical mp4 (video: H.264/AAC, even
// dimensions cropped for h264) or mp3 (audio) style via ffmpeg, persists the result at the
// Paperclip-compatible original path, uploads it, and returns the updated file metadata
// columns. It reports attempted=false for non video/audio attachments; attempted transcode
// failures are returned as errors so PostProcessMediaWorker retries like Rails Sidekiq.
func (s *Server) transcodeMediaOriginal(attachment *models.MediaAttachment, now time.Time) (map[string]any, bool, error) {
	if attachment == nil || !attachment.FileFileName.Valid || !attachment.FileContentType.Valid {
		return nil, false, nil
	}
	contentType := strings.TrimSpace(attachment.FileContentType.String)
	sourceFilename := strings.TrimSpace(attachment.FileFileName.String)
	if contentType == "" || sourceFilename == "" {
		return nil, false, nil
	}
	source := s.mediaFilePath(attachment.ID, sourceFilename)
	var ffmpegArgs []string
	var targetContentType string
	var targetExt string
	var videoMeta mediaTranscodeMetadata
	switch {
	case strings.HasPrefix(contentType, "video/"):
		videoMeta = mediaTranscodeMetadataForFile(source)
		ffmpegArgs = railsVideoTranscodeFFmpegArgsForMetadataAndLimit(source, videoMeta, s.videoSizeLimitBytes())
		targetContentType = "video/mp4"
		targetExt = ".mp4"
	case strings.HasPrefix(contentType, "audio/"):
		ffmpegArgs = railsAudioTranscodeFFmpegArgs(source)
		targetContentType = "audio/mpeg"
		targetExt = ".mp3"
	default:
		return nil, false, nil
	}
	targetFilename := strings.TrimSuffix(sourceFilename, filepath.Ext(sourceFilename)) + targetExt
	target := s.mediaFilePath(attachment.ID, targetFilename)
	if err := transcodeMediaOriginalFile(source, target, ffmpegArgs); err != nil {
		return nil, true, fmt.Errorf("transcode media original: %w", err)
	}
	if err := s.uploadPaperclipObject(context.Background(), mediaAttachmentObjectKey(attachment.ID, "files", "original", targetFilename), target, targetContentType); err != nil {
		return nil, true, fmt.Errorf("upload transcoded media original: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, true, fmt.Errorf("stat transcoded media original: %w", err)
	}
	updates := map[string]any{
		"file_file_name":    targetFilename,
		"file_content_type": targetContentType,
		"file_file_size":    info.Size(),
		"file_updated_at":   now,
	}
	if strings.HasPrefix(contentType, "video/") && videoMeta.valid && videoMeta.audioCodec == "" {
		updates["type"] = int64(1)
		attachment.Type = 1
	}
	return updates, true, nil
}

func railsVideoTranscodeFFmpegArgs(source string) []string {
	return railsVideoTranscodeFFmpegArgsForMetadata(source, mediaTranscodeMetadataForFile(source))
}

func railsVideoTranscodeFFmpegArgsForMetadata(source string, metadata mediaTranscodeMetadata) []string {
	return railsVideoTranscodeFFmpegArgsForMetadataAndLimit(source, metadata, railsVideoLimitBytes)
}

func railsVideoTranscodeFFmpegArgsForMetadataAndLimit(source string, metadata mediaTranscodeMetadata, videoLimitBytes int) []string {
	if metadata.eligibleForPassthrough() {
		return []string{
			"-y", "-i", source,
			"-loglevel", "fatal",
			"-map_metadata", "-1",
			"-movflags", "faststart",
			"-c:v", "copy",
			"-c:a", "copy",
		}
	}
	args := []string{
		"-y", "-i", source,
		"-loglevel", "fatal",
		"-preset", "veryfast",
		"-movflags", "faststart",
		"-pix_fmt", "yuv420p",
		"-vf", "crop=floor(iw/2)*2:floor(ih/2)*2",
		"-c:v", "h264",
		"-c:a", "aac",
		"-b:a", "192k",
		"-map_metadata", "-1",
		"-frames:v", "36000",
	}
	if metadata.valid {
		if bitrate := railsVideoTranscodeBitrateWithLimit(metadata, videoLimitBytes); bitrate != "" {
			args = append(args, "-b:v", bitrate, "-maxrate", railsVideoTranscodeMaxrateWithLimit(metadata, videoLimitBytes), "-bufsize", railsVideoTranscodeBufsizeWithLimit(metadata, videoLimitBytes))
		}
		if metadata.highVariableFrameRate() {
			args = append(args, "-vsync", "vfr", "-r", "120")
		}
	}
	return args
}

func railsAudioTranscodeFFmpegArgs(source string) []string {
	return []string{
		"-y", "-i", source,
		"-loglevel", "fatal",
		"-q:a", "2",
	}
}

func transcodeMediaOriginalFile(source string, target string, args []string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaFFmpegTranscodeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, mediaFFmpegBinary(), append(args, target)...).Run()
}

const (
	railsVideoTranscodeBitsPerPixel = 0.11
	railsVideoLimitBytes            = 90 * 1024 * 1024
	railsVideoAudioBitrate          = 192000
	railsMaxVideoFrameRate          = 120.0
)

type mediaTranscodeMetadata struct {
	valid      bool
	duration   float64
	width      int
	height     int
	videoCodec string
	audioCodec string
	colorspace string
	rFrameRate string
}

func mediaTranscodeMetadataForFile(path string) mediaTranscodeMetadata {
	ctx, cancel := context.WithTimeout(context.Background(), mediaFFProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, mediaFFprobeBinary(), "-i", path, "-print_format", "json", "-show_format", "-show_streams", "-show_error", "-loglevel", "fatal").Output()
	if err != nil {
		return mediaTranscodeMetadata{}
	}
	return mediaTranscodeMetadataFromFFProbeJSON(output)
}

func mediaTranscodeMetadataFromFFProbeJSON(raw []byte) mediaTranscodeMetadata {
	var payload struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			PixFmt       string `json:"pix_fmt"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			RFrameRate   string `json:"r_frame_rate"`
			SideDataList []struct {
				Rotation any `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Error != nil {
		return mediaTranscodeMetadata{}
	}
	metadata := mediaTranscodeMetadata{
		valid:    true,
		duration: positiveFloatString(payload.Format.Duration),
	}
	for _, stream := range payload.Streams {
		switch stream.CodecType {
		case "video":
			if metadata.videoCodec != "" {
				continue
			}
			width, height := stream.Width, stream.Height
			if mediaStreamRotated90(stream.SideDataList) {
				width, height = height, width
			}
			metadata.videoCodec = stream.CodecName
			metadata.colorspace = stream.PixFmt
			metadata.width = width
			metadata.height = height
			metadata.rFrameRate = stream.RFrameRate
		case "audio":
			if metadata.audioCodec == "" {
				metadata.audioCodec = stream.CodecName
			}
		}
	}
	return metadata
}

func (metadata mediaTranscodeMetadata) eligibleForPassthrough() bool {
	return metadata.valid && metadata.videoCodec == "h264" && (metadata.audioCodec == "aac" || metadata.audioCodec == "") && metadata.colorspace == "yuv420p"
}

func (metadata mediaTranscodeMetadata) highVariableFrameRate() bool {
	frameRate := parseFrameRate(metadata.rFrameRate)
	return frameRate > railsMaxVideoFrameRate
}

func railsVideoTranscodeBitrate(metadata mediaTranscodeMetadata) string {
	return railsVideoTranscodeBitrateWithLimit(metadata, railsVideoLimitBytes)
}

func railsVideoTranscodeBitrateWithLimit(metadata mediaTranscodeMetadata, videoLimitBytes int) string {
	bitrate := railsVideoTranscodeBitrateValue(metadata, videoLimitBytes)
	if bitrate <= 0 {
		return ""
	}
	return strconv.Itoa(bitrate)
}

func railsVideoTranscodeMaxrate(metadata mediaTranscodeMetadata) string {
	return railsVideoTranscodeMaxrateWithLimit(metadata, railsVideoLimitBytes)
}

func railsVideoTranscodeMaxrateWithLimit(metadata mediaTranscodeMetadata, videoLimitBytes int) string {
	bitrate := railsVideoTranscodeBitrateValue(metadata, videoLimitBytes)
	if bitrate <= 0 {
		return ""
	}
	return strconv.Itoa(bitrate + railsVideoAudioBitrate)
}

func railsVideoTranscodeBufsize(metadata mediaTranscodeMetadata) string {
	return railsVideoTranscodeBufsizeWithLimit(metadata, railsVideoLimitBytes)
}

func railsVideoTranscodeBufsizeWithLimit(metadata mediaTranscodeMetadata, videoLimitBytes int) string {
	bitrate := railsVideoTranscodeBitrateValue(metadata, videoLimitBytes)
	if bitrate <= 0 {
		return ""
	}
	return strconv.Itoa(bitrate * 5)
}

func railsVideoTranscodeBitrateValue(metadata mediaTranscodeMetadata, videoLimitBytes int) int {
	if !metadata.valid || metadata.width <= 0 || metadata.height <= 0 {
		return 0
	}
	if videoLimitBytes <= 0 {
		videoLimitBytes = railsVideoLimitBytes
	}
	duration := metadata.duration
	if duration < 1 {
		duration = 1
	}
	desired := int(math.Floor(float64(metadata.width) * float64(metadata.height) * 30 * railsVideoTranscodeBitsPerPixel))
	maximum := int(math.Floor(float64(videoLimitBytes*8)/duration)) - railsVideoAudioBitrate
	if maximum < desired {
		return maximum
	}
	return desired
}

func (s *Server) videoSizeLimitBytes() int {
	if s != nil && (s.cfg.VideoSizeLimitSet || s.cfg.VideoSizeLimit > 0) {
		return s.cfg.VideoSizeLimit
	}
	return railsVideoLimitBytes
}

func parseFrameRate(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "0/0" {
		return 0
	}
	if strings.Contains(value, "/") {
		parts := strings.SplitN(value, "/", 2)
		numerator := positiveFloatString(parts[0])
		denominator := positiveFloatString(parts[1])
		if numerator > 0 && denominator > 0 {
			return numerator / denominator
		}
		return 0
	}
	return positiveFloatString(value)
}

// mediaAttachmentShouldTranscode reports whether a queued local attachment should be
// re-encoded by the transcoder processor (Rails transcodes video and audio originals).
func mediaAttachmentShouldTranscode(attachment models.MediaAttachment) bool {
	if !attachment.FileContentType.Valid {
		return false
	}
	contentType := strings.TrimSpace(attachment.FileContentType.String)
	return strings.HasPrefix(contentType, "video/") || strings.HasPrefix(contentType, "audio/")
}
