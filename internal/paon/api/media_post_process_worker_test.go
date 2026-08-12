package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMediaPostProcessWorkerConstantsMatchRailsAsyncShape(t *testing.T) {
	if mediaPostProcessWorkerInterval != 15*time.Second {
		t.Fatalf("mediaPostProcessWorkerInterval = %s", mediaPostProcessWorkerInterval)
	}
	if mediaPostProcessBatchSize != 25 {
		t.Fatalf("mediaPostProcessBatchSize = %d", mediaPostProcessBatchSize)
	}
}

func TestMediaPostProcessWorkerIsStarted(t *testing.T) {
	src, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "StartBackgroundWorkers", "workers.Go(ctx, s.runMediaPostProcessWorker)") {
		t.Fatal("StartBackgroundWorkers does not start media post-process worker")
	}
}

func TestMediaPostProcessWorkerUsesRailsProcessingTransitions(t *testing.T) {
	src, err := os.ReadFile("media_post_process_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		`Where("processing = ?", 0)`,
		`Where("file_file_name IS NOT NULL")`,
		`Where("remote_url = '' OR remote_url IS NULL")`,
		`Updates(map[string]any{"processing": 1, "updated_at": now})`,
		`updates := map[string]any{"processing": processing, "updated_at": time.Now().UTC()}`,
		`s.mediaAttachmentPostProcessThumbnail(&attachment, time.Now().UTC())`,
		`if attachment.Type == 4`,
		`updates["thumbnail_file_name"] = attrs.filename`,
		`meta := s.mediaAttachmentPostProcessMeta(attachment)`,
		`updates["file_meta"] = meta`,
		`processing := int64(2)`,
		`processing = 3`,
		`if !s.mediaAttachmentOriginalExists(attachment)`,
		`s.restoreMediaAttachmentOriginal(ctx, attachment)`,
		`logMediaPostProcessFailure(s, attachment, "load_original"`,
		`logMediaPostProcessFailure(s, attachment, "restore_original", err)`,
		`logMediaPostProcessFailure(s, attachment, "transcode_original", err)`,
		`logMediaPostProcessFailure(s, *attachment, "generate_thumbnail", err)`,
		`logMediaPostProcessFailure(s, attachment, "persist_result", err)`,
	}
	for _, want := range checks {
		if !strings.Contains(string(src), want) {
			t.Fatalf("media post-process worker missing %q", want)
		}
	}
}

func TestMediaTranscodeMetadataFromFFProbeJSONMatchesRailsTranscoderInputs(t *testing.T) {
	raw := []byte(`{
		"format":{"duration":"2.500000"},
		"streams":[{
			"codec_type":"video",
			"codec_name":"h264",
			"pix_fmt":"yuv420p",
			"width":640,
			"height":360,
			"r_frame_rate":"30000/1001",
			"side_data_list":[{"rotation":90}]
		},{"codec_type":"audio","codec_name":"aac"}]
	}`)
	metadata := mediaTranscodeMetadataFromFFProbeJSON(raw)
	if !metadata.valid {
		t.Fatal("metadata should be valid")
	}
	if metadata.videoCodec != "h264" || metadata.audioCodec != "aac" || metadata.colorspace != "yuv420p" {
		t.Fatalf("codecs/colorspace = %#v", metadata)
	}
	if metadata.width != 360 || metadata.height != 640 {
		t.Fatalf("rotated dimensions = %dx%d", metadata.width, metadata.height)
	}
	if metadata.duration != 2.5 || metadata.rFrameRate != "30000/1001" {
		t.Fatalf("duration/frame_rate = %#v", metadata)
	}
	if !metadata.eligibleForPassthrough() {
		t.Fatalf("metadata should be passthrough-eligible: %#v", metadata)
	}
	if metadata.highVariableFrameRate() {
		t.Fatalf("metadata should not be high-vfr: %#v", metadata)
	}
}

func TestMediaTranscodeMarksSilentVideoAsGifvLikeRailsTypeCorrector(t *testing.T) {
	metadata := mediaTranscodeMetadata{
		valid:      true,
		videoCodec: "h264",
		colorspace: "yuv420p",
	}
	if !metadata.eligibleForPassthrough() {
		t.Fatalf("silent h264/yuv420p video should be passthrough-eligible: %#v", metadata)
	}
	src, err := os.ReadFile("media_post_process_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`videoMeta.audioCodec == ""`,
		`updates["type"] = int64(1)`,
		`attachment.Type = 1`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("silent video gifv type correction missing %q", want)
		}
	}
}

func TestMastodon44YUVJ420PVideoIsEligibleForPassthrough(t *testing.T) {
	metadata := mediaTranscodeMetadata{
		valid:      true,
		videoCodec: "h264",
		audioCodec: "aac",
		colorspace: "yuvj420p",
	}
	if !metadata.eligibleForPassthrough() {
		t.Fatal("Mastodon 4.4 must not re-encode an otherwise-compatible yuvj420p video")
	}
}

func TestMediaAttachmentOriginalExistsUsesPaperclipPath(t *testing.T) {
	root := t.TempDir()
	server := &Server{cfg: config.Config{PublicDir: root}}
	path := server.mediaFilePath(42, "video.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	attachment := models.MediaAttachment{
		ID:           42,
		FileFileName: sql.NullString{String: "video.mp4", Valid: true},
	}
	if !server.mediaAttachmentOriginalExists(attachment) {
		t.Fatal("stored original file was not found")
	}
	attachment.FileFileName.String = "missing.mp4"
	if server.mediaAttachmentOriginalExists(attachment) {
		t.Fatal("missing original file should not be found")
	}
	attachment.FileFileName = sql.NullString{}
	if server.mediaAttachmentOriginalExists(attachment) {
		t.Fatal("invalid filename should not be found")
	}
}

func TestRestoreMediaAttachmentOriginalStreamsPrivateS3ObjectToWorker(t *testing.T) {
	var gotMethod string
	var gotPath string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotMethod = request.Method
		gotPath = request.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("video-from-s3")),
			Request:    request,
		}, nil
	})}

	server := &Server{cfg: config.Config{
		PublicDir:         t.TempDir(),
		S3Enabled:         true,
		S3Bucket:          "private-media",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "ap-northeast-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3Permission:      "private",
	}}
	attachment := models.MediaAttachment{
		ID:           42,
		Type:         2,
		FileFileName: sql.NullString{String: "attachment.mp4", Valid: true},
	}

	restored, err := server.restoreMediaAttachmentOriginal(context.Background(), attachment)
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("S3 original was not restored")
	}
	body, err := os.ReadFile(server.mediaFilePath(attachment.ID, attachment.FileFileName.String))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "video-from-s3" {
		t.Fatalf("restored body = %q", body)
	}
	if gotMethod != http.MethodGet || gotPath != "/private-media/media_attachments/files/000/000/042/original/attachment.mp4" {
		t.Fatalf("S3 request method=%q path=%q", gotMethod, gotPath)
	}
}

func TestMediaPostProcessGeneratesVideoThumbnailWhenFFmpegAvailable(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root := t.TempDir()
	server := &Server{cfg: config.Config{PublicDir: root}}
	sourceBytes, err := os.ReadFile(testWebMFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	source := server.mediaFilePath(42, "attachment.webm")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, sourceBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	attachment := models.MediaAttachment{
		ID:           42,
		Type:         2,
		FileFileName: sql.NullString{String: "attachment.webm", Valid: true},
	}
	updates := server.mediaAttachmentPostProcessThumbnail(&attachment, time.Now().UTC())
	if _, ok := updates["thumbnail_file_name"]; ok || attachment.ThumbnailFileName.Valid {
		t.Fatalf("generated video preview must stay in file small style, updates=%#v attachment=%#v", updates, attachment.ThumbnailFileName)
	}
	if info, err := os.Stat(server.mediaFileStylePath(42, "small", "attachment.png")); err != nil || info.Size() == 0 {
		t.Fatalf("small style file missing or empty info=%#v err=%v", info, err)
	}

	meta := server.mediaAttachmentPostProcessMeta(attachment)
	var got map[string]any
	if err := json.Unmarshal(meta, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["small"].(map[string]any); !ok {
		t.Fatalf("small metadata missing after thumbnail generation: %#v", got)
	}
}

func TestTranscodeMediaOriginalFileSafelyReplacesMP4Input(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	source := testMP4Fixture(t)
	args := railsVideoTranscodeFFmpegArgs(source)
	if err := transcodeMediaOriginalFile(source, source, args); err != nil {
		t.Fatalf("transcodeMediaOriginalFile() error = %v", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("transcoded MP4 is empty")
	}
	if metadata := mediaTranscodeMetadataForFile(source); !metadata.eligibleForPassthrough() {
		t.Fatalf("transcoded MP4 metadata = %#v", metadata)
	}
}

func TestMediaCommandExecutionErrorIncludesBoundedStderr(t *testing.T) {
	detail := strings.Repeat("x", 5*1024) + "\ninvalid codec"
	err := mediaCommandExecutionError("/usr/bin/ffmpeg", errors.New("exit status 1"), []byte(detail))
	message := err.Error()
	if !strings.Contains(message, "ffmpeg failed: exit status 1") || !strings.Contains(message, "invalid codec") {
		t.Fatalf("media command error missing command output: %q", message)
	}
	if len(message) > 4300 {
		t.Fatalf("media command error is not bounded: %d bytes", len(message))
	}
}

func TestMediaPostProcessStorageBackend(t *testing.T) {
	if got := mediaPostProcessStorageBackend(&Server{}); got != "local" {
		t.Fatalf("local backend = %q", got)
	}
	s3Server := &Server{cfg: config.Config{S3Enabled: true, S3Bucket: "media"}}
	if got := mediaPostProcessStorageBackend(s3Server); got != "s3" {
		t.Fatalf("S3 backend = %q", got)
	}
}
