package api

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestLocalesDirForMatchesDropInRuntimePublicDir(t *testing.T) {
	if got, want := localesDirFor("/opt/mastodon/public"), "/opt/mastodon/config/locales"; got != want {
		t.Fatalf("localesDirFor(/opt/mastodon/public) = %q, want %q", got, want)
	}
}

func TestWebLocaleMastodon44PrefersBrowserUnlessForced(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/about", nil)
	req.Header.Set("Accept-Language", "ja")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{cfg: config.Config{DefaultLocale: "en", DefaultLocaleSet: true}}
	if got := s.webLocale(c, nil); got != "ja" {
		t.Fatalf("DEFAULT_LOCALE without FORCE_DEFAULT_LOCALE selected %q, want browser locale ja", got)
	}

	s.cfg.ForceDefaultLocale = true
	if got := s.webLocale(c, nil); got != "en" {
		t.Fatalf("FORCE_DEFAULT_LOCALE selected %q, want configured locale en", got)
	}
}
