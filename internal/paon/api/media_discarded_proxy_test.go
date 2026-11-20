package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v5"
)

// A remote Update can rewrite file_content_type (for example to text/html)
// while the cached file stays on disk, so a discarded attachment must be typed
// by its stored file name and sandboxed like the public/system mount.
func TestDiscardedMediaProxyLocalFileUsesSystemFileHeaders(t *testing.T) {
	const payload = "<html><script>alert(document.cookie)</script></html>"
	cases := []struct {
		file            string
		wantType        string
		wantDisposition string
	}{
		{file: "clip.mp4", wantType: "video/mp4", wantDisposition: "inline"},
		{file: "photo.png", wantType: "image/png", wantDisposition: "inline"},
		{file: "payload.html", wantType: "application/octet-stream", wantDisposition: "attachment"},
		{file: "drawing.svg", wantType: "application/octet-stream", wantDisposition: "attachment"},
		{file: "media", wantType: "application/octet-stream", wantDisposition: "attachment"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			localPath := filepath.Join(t.TempDir(), tc.file)
			if err := os.WriteFile(localPath, []byte(payload), 0o644); err != nil {
				t.Fatal(err)
			}
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/media_proxy/123/original", nil)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, e)
			if err := (&Server{}).serveDiscardedMediaProxyLocalFile(c, localPath); err != nil {
				t.Fatal(err)
			}
			headers := rec.Header()
			if rec.Code != http.StatusOK || rec.Body.String() != payload {
				t.Fatalf("response = %d %q, want the stored file", rec.Code, rec.Body.String())
			}
			if got := headers.Get("Content-Type"); got != tc.wantType {
				t.Fatalf("Content-Type = %q, want %q", got, tc.wantType)
			}
			if got := headers.Get("Content-Disposition"); got != tc.wantDisposition {
				t.Fatalf("Content-Disposition = %q, want %q", got, tc.wantDisposition)
			}
			if got := headers.Get("Content-Security-Policy"); got != paperclipStaticContentSecurityPolicy {
				t.Fatalf("Content-Security-Policy = %q, want the sandboxing policy", got)
			}
			if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := headers.Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q, want private, no-store", got)
			}
		})
	}
}
