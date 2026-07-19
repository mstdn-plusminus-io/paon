package api

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestBackupDumpURLUsesPaperclipPath(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https"}}
	got := s.backupDumpURL(models.Backup{
		ID:           123,
		DumpFileName: sql.NullString{String: "archive one.zip", Valid: true},
	})
	want := "https://example.test/system/backups/dumps/000/000/123/original/archive%20one.zip"
	if got != want {
		t.Fatalf("backupDumpURL = %q, want %q", got, want)
	}
	if got := s.backupDumpURL(models.Backup{}); got != "" {
		t.Fatalf("empty backupDumpURL = %q", got)
	}

	s.cfg.StorageHost = "https://media.example.test/"
	got = s.backupDumpURL(models.Backup{
		ID:           123,
		DumpFileName: sql.NullString{String: "archive one.zip", Valid: true},
	})
	want = "https://media.example.test/backups/dumps/000/000/123/original/archive%20one.zip"
	if got != want {
		t.Fatalf("storage-host backupDumpURL = %q, want %q", got, want)
	}
}

func TestBackupDumpURLUsesRailsLikeS3ExpiringURL(t *testing.T) {
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "backup-bucket",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "ap-northeast-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3SessionToken:    "session",
	}}
	got := s.backupDumpURL(models.Backup{
		ID:           123,
		DumpFileName: sql.NullString{String: "archive one.zip", Valid: true},
	})
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "storage.example.test" || parsed.EscapedPath() != "/backup-bucket/backups/dumps/000/000/123/original/archive%20one.zip" {
		t.Fatalf("presigned URL path = %s", got)
	}
	query := parsed.Query()
	for _, key := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature", "X-Amz-Security-Token"} {
		if query.Get(key) == "" {
			t.Fatalf("presigned URL missing %s: %s", key, got)
		}
	}
	if query.Get("X-Amz-Expires") != "3600" || query.Get("X-Amz-SignedHeaders") != "host" {
		t.Fatalf("presigned query = %s", parsed.RawQuery)
	}
}

func TestBackupDumpURLUsesRailsLikeAzureExpiringURL(t *testing.T) {
	s := &Server{cfg: config.Config{
		AzureEnabled:          true,
		AzureStorageAccount:   "acct",
		AzureStorageAccessKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		AzureContainerName:    "media",
	}}
	got := s.backupDumpURL(models.Backup{
		ID:           123,
		DumpFileName: sql.NullString{String: "archive one.zip", Valid: true},
	})
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "acct.blob.core.windows.net" || parsed.EscapedPath() != "/media/backups/dumps/000/000/123/original/archive%20one.zip" {
		t.Fatalf("azure URL path = %s", got)
	}
	query := parsed.Query()
	for _, key := range []string{"sv", "spr", "se", "sr", "sp", "sig"} {
		if query.Get(key) == "" {
			t.Fatalf("azure URL missing %s: %s", key, got)
		}
	}
	if query.Get("sp") != "r" || query.Get("sr") != "b" || query.Get("spr") != "https" {
		t.Fatalf("azure SAS query = %s", parsed.RawQuery)
	}
}

func TestBackupDumpURLUsesRailsLikeSwiftTempURL(t *testing.T) {
	s := &Server{cfg: config.Config{
		SwiftEnabled:    true,
		SwiftObjectURL:  "https://swift.example.test/v1/AUTH_project/container",
		SwiftTempURLKey: "temp-key",
	}}
	got := s.backupDumpURL(models.Backup{
		ID:           123,
		DumpFileName: sql.NullString{String: "archive one.zip", Valid: true},
	})
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "swift.example.test" || parsed.EscapedPath() != "/v1/AUTH_project/container/backups/dumps/000/000/123/original/archive%20one.zip" {
		t.Fatalf("swift temp URL path = %s", got)
	}
	query := parsed.Query()
	if query.Get("temp_url_sig") == "" || query.Get("temp_url_expires") == "" {
		t.Fatalf("swift temp query = %s", parsed.RawQuery)
	}

	s.cfg.SwiftObjectURL = "https://swift.example.test/v1/AUTH_project"
	s.cfg.SwiftContainer = "container"
	got = s.backupDumpURL(models.Backup{
		ID:           123,
		DumpFileName: sql.NullString{String: "archive one.zip", Valid: true},
	})
	parsed, err = url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.EscapedPath() != "/v1/AUTH_project/container/backups/dumps/000/000/123/original/archive%20one.zip" {
		t.Fatalf("swift temp URL with separate container path = %s", got)
	}
}

func TestBackupDumpS3ACLMatchesRailsBackupPermission(t *testing.T) {
	if got := backupDumpS3ACL("public-read"); got != "private" {
		t.Fatalf("backup ACL = %q", got)
	}
	if got := backupDumpS3ACL(""); got != "" {
		t.Fatalf("empty backup ACL = %q", got)
	}
	if got := backupDumpS3ACL("   "); got != "private" {
		t.Fatalf("blank-looking nonempty backup ACL = %q", got)
	}
}

func TestDownloadBackupRequiresAuthentication(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/backups/1/download", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.downloadBackup(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/backups/1/download")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestBackupDumpPathUsesPaperclipPath(t *testing.T) {
	s := &Server{cfg: config.Config{PublicDir: "/srv/paon/public"}}
	got := s.backupDumpPath(123, "archive.zip")
	want := "/srv/paon/public/system/backups/dumps/000/000/123/original/archive.zip"
	if got != want {
		t.Fatalf("backupDumpPath = %q, want %q", got, want)
	}

	s.cfg.PaperclipRootPath = "/mnt/mastodon/system"
	got = s.backupDumpPath(123, "archive.zip")
	want = "/mnt/mastodon/system/backups/dumps/000/000/123/original/archive.zip"
	if got != want {
		t.Fatalf("custom root backupDumpPath = %q, want %q", got, want)
	}
}

func TestBackupDumpLocalPathUsesSafePaperclipPath(t *testing.T) {
	s := &Server{cfg: config.Config{PublicDir: "/srv/paon/public"}}
	got := s.backupDumpLocalPath(models.Backup{ID: 123, DumpFileName: sql.NullString{String: "../archive.zip", Valid: true}})
	want := "/srv/paon/public/system/backups/dumps/000/000/123/original/archive.zip"
	if got != want {
		t.Fatalf("local backup path = %q, want %q", got, want)
	}
	if got := s.backupDumpLocalPath(models.Backup{}); got != "" {
		t.Fatalf("empty local backup path = %q", got)
	}
}

func TestSetBackupDownloadHeadersUsesRailsAttachmentShape(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/backups/123/download", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	s.setBackupDownloadHeaders(c, models.Backup{
		DumpFileName:    sql.NullString{String: "archive one.zip", Valid: true},
		DumpContentType: sql.NullString{String: "application/zip", Valid: true},
	}, "https://example.test/system/backups/dumps/000/000/123/original/archive%20one.zip")

	if rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Content-Disposition") != `attachment; filename="archive one.zip"` {
		t.Fatalf("content disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestDownloadBackupRedirectsFilesystemBackupsLikeRails(t *testing.T) {
	src, err := os.ReadFile("backups.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "downloadBackup")
	if strings.Contains(body, "http.ServeFile") || strings.Contains(body, "backupDumpLocalPath") {
		t.Fatalf("downloadBackup should match Rails filesystem behavior by redirecting instead of streaming local files:\n%s", body)
	}
	if !strings.Contains(body, `return c.Redirect(http.StatusFound, target)`) {
		t.Fatalf("downloadBackup missing redirect to backup URL:\n%s", body)
	}
}

func TestBackupArchiveFilenameUsesRailsZipShape(t *testing.T) {
	got, err := backupArchiveFilename(time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^archive-20260620010203-[0-9a-f]{32}\.zip$`).MatchString(got) {
		t.Fatalf("filename = %q", got)
	}
}

func TestWriteZipEntryWritesArchiveMember(t *testing.T) {
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)
	if err := writeZipEntry(zipWriter, "follows.csv", []byte("Account address\nalice@example.test\n")); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zipReader.File) != 1 || zipReader.File[0].Name != "follows.csv" {
		t.Fatalf("zip files = %#v", zipReader.File)
	}
	file, err := zipReader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "Account address\nalice@example.test\n" {
		t.Fatalf("entry body = %q", body)
	}
}

func TestWriteZipFileEntryCopiesArchiveMember(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "photo one.png")
	if err := os.WriteFile(source, []byte("image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)
	if err := writeZipFileEntry(zipWriter, "media_attachments/files/000/000/008/original/photo one.png", source); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zipReader.File) != 1 {
		t.Fatalf("zip files = %#v", zipReader.File)
	}
	if zipReader.File[0].Name != "media_attachments/files/000/000/008/original/photo one.png" {
		t.Fatalf("zip name = %q", zipReader.File[0].Name)
	}
	file, err := zipReader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "image-bytes" {
		t.Fatalf("entry body = %q", body)
	}
}

func TestBackupSystemArchiveNameStripsPublicSystemPrefix(t *testing.T) {
	publicDir := filepath.Join(t.TempDir(), "public")
	path := filepath.Join(publicDir, "system", "media_attachments", "files", "000", "000", "008", "original", "photo one.png")
	got, ok := backupSystemArchiveName(publicDir, path)
	if !ok {
		t.Fatal("backupSystemArchiveName rejected system path")
	}
	want := "media_attachments/files/000/000/008/original/photo one.png"
	if got != want {
		t.Fatalf("archive name = %q, want %q", got, want)
	}
	outside, ok := backupSystemArchiveName(publicDir, filepath.Join(filepath.Dir(publicDir), "other", "photo.png"))
	if ok || outside != "" {
		t.Fatalf("outside path accepted as %q", outside)
	}
}

func TestBackupSystemURLPathMatchesZipMediaName(t *testing.T) {
	got := backupSystemURLPath("https://example.test", "https://example.test/system/media_attachments/files/000/000/008/original/photo%20one.png")
	want := "media_attachments/files/000/000/008/original/photo one.png"
	if got != want {
		t.Fatalf("backupSystemURLPath = %q, want %q", got, want)
	}
	remote := "https://remote.test/system/media_attachments/files/000/000/008/original/photo.png"
	if got := backupSystemURLPath("https://example.test", remote); got != remote {
		t.Fatalf("remote URL changed to %q", got)
	}
}

func TestBackupPaperclipArchiveEntryFallsBackToS3Object(t *testing.T) {
	s := &Server{cfg: config.Config{
		PublicDir:         t.TempDir(),
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "us-east-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	entry, ok, err := s.backupPaperclipArchiveEntry(
		"media_attachments/files/000/000/008/original/photo.png",
		filepath.Join(t.TempDir(), "missing.png"),
		"media_attachments/files/000/000/008/original/photo.png",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || entry.Name != "media_attachments/files/000/000/008/original/photo.png" || entry.ObjectKey != "media_attachments/files/000/000/008/original/photo.png" || entry.Path != "" || entry.Body != nil {
		t.Fatalf("entry = %#v ok=%v", entry, ok)
	}
}

func TestWriteZipS3ObjectEntryStreamsRemoteObject(t *testing.T) {
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != "/bucket-name/media_attachments/files/000/000/008/original/photo.png" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("remote-image")), Request: r}, nil
	})}
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "us-east-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)
	if err := s.writeZipS3ObjectEntry(zipWriter, "media_attachments/files/000/000/008/original/photo.png", "media_attachments/files/000/000/008/original/photo.png"); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zipReader.File) != 1 {
		t.Fatalf("zip files = %#v", zipReader.File)
	}
	file, err := zipReader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "remote-image" {
		t.Fatalf("entry body = %q", body)
	}
}

func TestBackupArchiveStreamsS3ObjectsInsteadOfBufferingThem(t *testing.T) {
	src, err := os.ReadFile("backups.go")
	if err != nil {
		t.Fatal(err)
	}
	if functionBodyContains(t, src, "backupPaperclipArchiveEntry", `getS3Object(`) {
		t.Fatal("backupPaperclipArchiveEntry must not read S3 media into memory before zip writing")
	}
	for _, want := range []string{
		`ObjectKey string`,
		`s.writeZipS3ObjectEntry(zipWriter, entry.Name, entry.ObjectKey)`,
		`return backupArchiveEntry{Name: name, ObjectKey: objectKey}, true, nil`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("backups.go missing streaming S3 archive fragment %q", want)
		}
	}
}

func TestBackupAccountImageArchiveEntryUsesRailsRootNames(t *testing.T) {
	s := &Server{cfg: config.Config{PublicDir: t.TempDir()}}
	avatar := s.accountImagePath(42, "avatar", "avatar image.png")
	if err := os.MkdirAll(filepath.Dir(avatar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(avatar, []byte("avatar"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := s.backupAccountImageArchiveEntry(models.Account{
		ID:                42,
		AvatarFileName:    sql.NullString{String: "avatar image.png", Valid: true},
		AvatarContentType: sql.NullString{String: "image/png", Valid: true},
	}, "avatar")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("avatar entry was not created")
	}
	if entry.Name != "avatar.png" || entry.Path != avatar {
		t.Fatalf("avatar entry = %#v", entry)
	}
	image := backupActivityImageObject("image/png", entry.Name)
	if image["type"] != "Image" || image["mediaType"] != "image/png" || image["url"] != "avatar.png" {
		t.Fatalf("image object = %#v", image)
	}
}

func TestBackupOrderedCollectionMatchesRailsArchiveShape(t *testing.T) {
	collection := backupOrderedCollection("outbox.json", 2, []any{"one", "two"}, true)
	if collection["id"] != "outbox.json" || collection["type"] != "OrderedCollection" || collection["totalItems"] != 2 {
		t.Fatalf("collection = %#v", collection)
	}
	if _, ok := collection["@context"]; !ok {
		t.Fatalf("missing context: %#v", collection)
	}
	body, err := json.Marshal(collection)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"orderedItems":["one","two"]`) {
		t.Fatalf("collection json = %s", body)
	}
}

func TestSettingsExportPostRouteReachesBackupHandler(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/export", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed || rec.Code == http.StatusNotImplemented {
		t.Fatalf("POST /settings/export did not reach handler: status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestBackupRetentionDaysMatchRailsDefaultAndPositiveOnly(t *testing.T) {
	days, ok := (&Server{}).backupRetentionDays()
	if !ok || days != 7 {
		t.Fatalf("default retention = %d ok=%v", days, ok)
	}
	for _, raw := range []string{"14", `"30"`} {
		days, ok := parsePositiveRetentionDays(raw)
		if !ok || days <= 0 {
			t.Fatalf("retention %q = %d ok=%v", raw, days, ok)
		}
	}
	for _, raw := range []string{"", "0", "-1", "abc"} {
		if days, ok := parsePositiveRetentionDays(raw); ok || days != 0 {
			t.Fatalf("invalid retention %q = %d ok=%v", raw, days, ok)
		}
	}
}

func TestRemoveBackupDumpFilesRemovesPaperclipDirectory(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	path := s.backupDumpPath(123, "archive.zip")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.removeBackupDumpFiles(models.Backup{ID: 123}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "backups", "dumps", "000", "000", "123")); !os.IsNotExist(err) {
		t.Fatalf("backup dump directory still exists err=%v", err)
	}
}

func TestBackupVacuumWorkerUsesRailsBackupsVacuumShape(t *testing.T) {
	src, err := os.ReadFile("backup_vacuum_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runBackupVacuumWorker", `s.vacuumExpiredBackups(ctx, now.UTC())`},
		{"vacuumExpiredBackups", `days, ok := s.backupRetentionDays()`},
		{"vacuumExpiredBackups", `cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)`},
		{"vacuumExpiredBackups", `Where("created_at < ?", cutoff)`},
		{"vacuumExpiredBackups", `Limit(backupVacuumBatchSize)`},
		{"vacuumExpiredBackups", `s.removeBackupDumpFiles(backup)`},
		{"vacuumExpiredBackups", `Delete(&models.Backup{}, backup.ID)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runBackupVacuumWorker)") {
		t.Fatal("StartBackgroundWorkers does not start backup vacuum worker")
	}
}
