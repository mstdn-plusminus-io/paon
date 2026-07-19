package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestSettingsSessionDestroyRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodDelete, "/settings/sessions/123", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.destroySettingsSession(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/sessions/123")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSessionDescriptionUsesRailsJapaneseTemplate(t *testing.T) {
	got := sessionDescription("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/537.36 Chrome/125.0.0.0 Safari/537.36", "ja")
	if got != "macOS上のChrome" {
		t.Fatalf("Japanese sessionDescription = %q", got)
	}
	html := authEditSessionsHTML(nil, 0, "ja", "Paon")
	if strings.Contains(html, "Mastodonアカウント") || !strings.Contains(html, "Paonアカウント") {
		t.Fatalf("session explanation did not use configured application name: %s", html)
	}
}

func TestSessionDescriptionUsesRailsBrowserAndPlatformDescription(t *testing.T) {
	got := sessionDescription("Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0")
	if got != "Firefox on Linux" {
		t.Fatalf("sessionDescription = %q", got)
	}
}
