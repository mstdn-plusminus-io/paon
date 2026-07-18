package api

import (
	"os"
	"testing"
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
		`image.Decode(bytes.NewReader(download.body))`,
		`profileImageUpdates(kind, filename, contentType, int64(len(data)), now)`,
	} {
		if !functionBodyContains(t, src, "downloadAndStoreRemoteAccountImage", want) {
			t.Fatalf("downloadAndStoreRemoteAccountImage missing %q", want)
		}
	}
}

func TestResizeRemoteAccountImageMatchesRailsStyles(t *testing.T) {
	src, err := os.ReadFile("remote_account_media.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "resizeRemoteAccountImage", `resizeImageToFill(img, remoteAccountAvatarTarget, remoteAccountAvatarTarget)`) {
		t.Fatal("avatar must use 400x400 fill crop like Rails avatar style")
	}
	if !functionBodyContains(t, src, "resizeRemoteAccountImage", `resizeImageToMaxPixels(img, remoteAccountHeaderMaxPixels)`) {
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
