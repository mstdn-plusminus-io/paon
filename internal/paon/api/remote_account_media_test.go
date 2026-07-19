package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestDownloadAndStoreRemoteAccountImageGuardsAndPipeline(t *testing.T) {
	src, err := os.ReadFile("remote_account_media.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.cfg.DisableRemoteMediaCache`,
		`fetchRemoteImageMedia(ctx, remoteURL, remoteAccountImageLimit)`,
		`profileImageContentTypeSupported(contentType)`,
		`resizeAccountImageBuffer(kind, download.body, contentType)`,
		`remoteProfileImageUpdates(kind, filename, contentType, int64(len(data)), remoteURL, now)`,
	} {
		if !functionBodyContains(t, src, "downloadAndStoreRemoteAccountImage", want) {
			t.Fatalf("downloadAndStoreRemoteAccountImage missing %q", want)
		}
	}
}

func TestRemoteProfileImageUpdatesPreserveRemoteURLAndCacheSchema(t *testing.T) {
	now := time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC)
	avatar := remoteProfileImageUpdates("avatar", "avatar.png", "image/png", 123, "https://remote.example/avatar.png", now)
	if avatar["avatar_remote_url"] != (sql.NullString{String: "https://remote.example/avatar.png", Valid: true}) {
		t.Fatalf("avatar remote URL = %#v", avatar["avatar_remote_url"])
	}
	if avatar["avatar_storage_schema_version"] != (sql.NullInt64{Int64: 1, Valid: true}) {
		t.Fatalf("avatar storage schema = %#v", avatar["avatar_storage_schema_version"])
	}

	header := remoteProfileImageUpdates("header", "header.png", "image/png", 456, "https://remote.example/header.png", now)
	if header["header_remote_url"] != "https://remote.example/header.png" {
		t.Fatalf("header remote URL = %#v", header["header_remote_url"])
	}
	if header["header_storage_schema_version"] != (sql.NullInt64{Int64: 1, Valid: true}) {
		t.Fatalf("header storage schema = %#v", header["header_storage_schema_version"])
	}
}

func TestStoreRemoteAccountImageUsesRailsCachePrefix(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	if err := s.storeAccountImageBytes(42, "avatar", "avatar.png", "image/png", siteUploadTestPNG(t, 20, 20)); err != nil {
		t.Fatal(err)
	}
	for _, style := range []string{"original", "static"} {
		path := filepath.Join(root, "system", "cache", "accounts", "avatars", "000", "000", "042", style, "avatar.png")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("remote avatar %s missing: %v", style, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "system", "accounts", "avatars", "000", "000", "042", "original", "avatar.png")); !os.IsNotExist(err) {
		t.Fatalf("remote avatar must not be stored in local account path: %v", err)
	}
}

func TestResizeAccountImageBufferMatchesRailsStyles(t *testing.T) {
	src, err := os.ReadFile("remote_account_media.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "resizeAccountImageBuffer", `resizeVIPSBufferToFill(data, contentType, remoteAccountAvatarTarget, remoteAccountAvatarTarget)`) {
		t.Fatal("avatar must use 400x400 fill crop like Rails avatar style")
	}
	if !functionBodyContains(t, src, "resizeAccountImageBuffer", `resizeVIPSBufferToMaxPixels(data, contentType, remoteAccountHeaderMaxPixels)`) {
		t.Fatal("header must use 750000 max pixels like Rails header style")
	}
	if remoteAccountAvatarTarget != 400 {
		t.Fatalf("avatar target = %d", remoteAccountAvatarTarget)
	}
	if remoteAccountHeaderMaxPixels != 750000 {
		t.Fatalf("header max pixels = %d", remoteAccountHeaderMaxPixels)
	}
	if remoteAccountImageLimit != 2*1024*1024 {
		t.Fatalf("remote account image limit = %d", remoteAccountImageLimit)
	}
}

func TestHandleAsynqRedownloadAccountMediaIsPerMedia(t *testing.T) {
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`asynqTaskRedownloadAvatar`,
		`asynqTaskRedownloadHeader`,
		`s.downloadAndStoreRemoteAccountImage(ctx, account.ID, kind, remoteURL)`,
	} {
		if !functionBodyContains(t, src, "handleAsynqRedownloadAccountMedia", want) {
			t.Fatalf("handleAsynqRedownloadAccountMedia missing %q", want)
		}
	}
	// The per-media worker must NOT re-fetch the whole actor; that is the admin redownload action.
	if functionBodyContains(t, src, "handleAsynqRedownloadAccountMedia", `redownloadAdminRemoteAccount`) {
		t.Fatal("handleAsynqRedownloadAccountMedia must not call redownloadAdminRemoteAccount")
	}
}

func TestUpsertRemoteActorDownloadsAvatarHeaderOnUrlChange(t *testing.T) {
	src, err := os.ReadFile("activitypub_signature.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`previousAvatarURL = existing.AvatarRemoteURL.String`,
		`previousHeaderURL = existing.HeaderRemoteURL`,
		`s.downloadAndStoreRemoteAccountImage(context.Background(), account.ID, "avatar", actor.AvatarRemoteURL)`,
		`s.downloadAndStoreRemoteAccountImage(context.Background(), account.ID, "header", actor.HeaderRemoteURL)`,
		`s.enqueueRedownloadAvatarTask(account.ID)`,
		`s.enqueueRedownloadHeaderTask(account.ID)`,
	} {
		if !functionBodyContains(t, src, "upsertRemoteActivityActorDBForRequest", want) {
			t.Fatalf("upsertRemoteActivityActorDBForRequest missing %q", want)
		}
	}
}

func TestAdminRedownloadRunsResolveAccountSynchronously(t *testing.T) {
	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "applyAdminAccountWebAction", `s.redownloadAdminRemoteAccount(account, time.Now().UTC())`) {
		t.Fatal("admin redownload must run redownloadAdminRemoteAccount synchronously like Rails ResolveAccountService")
	}
	if functionBodyContains(t, src, "applyAdminAccountWebAction", `enqueueRedownloadAvatarTask`) {
		t.Fatal("admin redownload must not enqueue redownload tasks (those are the per-media worker path)")
	}
}
