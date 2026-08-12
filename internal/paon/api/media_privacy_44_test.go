package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestMediaPrivacyStaticHandlerBlocksPrivateRetainedMedia(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "media_attachments", "files", "000", "000", "042", "original", "private.png")
	publicPath := filepath.Join(root, "media_attachments", "files", "000", "000", "043", "original", "public.png")
	unrelatedPath := filepath.Join(root, "accounts", "avatars", "000", "000", "042", "original", "avatar.png")
	for _, name := range []string{privatePath, publicPath, unrelatedPath} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(privatePath, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, []byte("public"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(publicPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelatedPath, []byte("avatar"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	e.GET("/system*", (*Server)(nil).mediaPrivacyStaticHandler(os.DirFS(root)))
	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{path: "/system/media_attachments/files/000/000/043/original/public.png", code: http.StatusOK, body: "public"},
		{path: "/system/media_attachments/files/000/000/042/original/private.png", code: http.StatusNotFound},
		{path: "/system/accounts/avatars/000/000/042/original/avatar.png", code: http.StatusOK, body: "avatar"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != tc.code || (tc.body != "" && rec.Body.String() != tc.body) {
			t.Fatalf("%s: status = %d body = %q, want %d %q", tc.path, rec.Code, rec.Body.String(), tc.code, tc.body)
		}
	}
}

func TestMediaAttachmentStaticAssetID(t *testing.T) {
	for _, tc := range []struct {
		path string
		id   int64
		ok   bool
	}{
		{path: "media_attachments/files/000/000/042/original/photo.png", id: 42, ok: true},
		{path: "cache/media_attachments/thumbnails/109/915/428/643/912/138/original/thumb.png", id: 109915428643912138, ok: true},
		{path: "accounts/avatars/000/000/042/original/avatar.png"},
		{path: "media_attachments/files/not/an/id/original/photo.png"},
	} {
		id, ok := mediaAttachmentStaticAssetID(tc.path)
		if id != tc.id || ok != tc.ok {
			t.Fatalf("mediaAttachmentStaticAssetID(%q) = (%d, %t), want (%d, %t)", tc.path, id, ok, tc.id, tc.ok)
		}
	}
}

func TestStatusRemovalPermanenceMatchesMastodon44(t *testing.T) {
	for _, tc := range []struct {
		name     string
		options  asynqRemovalPayload
		reported bool
		want     bool
	}{
		{name: "ordinary deletion", want: true},
		{name: "reported", reported: true, want: false},
		{name: "preserved moderation deletion", options: asynqRemovalPayload{Preserve: true}, want: false},
		{name: "immediate overrides report", options: asynqRemovalPayload{Immediate: true}, reported: true, want: true},
		{name: "immediate overrides preserve", options: asynqRemovalPayload{Immediate: true, Preserve: true}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusRemovalPermanently(tc.options, tc.reported); got != tc.want {
				t.Fatalf("statusRemovalPermanently() = %t, want %t", got, tc.want)
			}
		})
	}
}
