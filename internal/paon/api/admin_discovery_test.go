package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminSettingsDiscoveryRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/settings/discovery?tab=public", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/settings/discovery?tab=public")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminSettingsDiscoveryHTMLIncludesRailsFields(t *testing.T) {
	html := adminSettingsDiscoveryHTML(adminDiscoverySettings{
		Trends:                    true,
		TrendsAsLandingPage:       true,
		TrendableByDefault:        false,
		TimelinePreview:           true,
		AllowReferrerOrigin:       true,
		ActivityAPIEnabled:        true,
		PeersAPIEnabled:           false,
		AuthorizedFetch:           true,
		BootstrapTimelineAccounts: "alice, bob",
		ProfileDirectory:          true,
	}, "saved", "")

	for _, want := range []string{
		"Discovery",
		`action="/admin/settings/discovery"`,
		`name="form_admin_settings[trends]" value="1" checked`,
		`name="form_admin_settings[trends_as_landing_page]" value="1" checked`,
		`name="form_admin_settings[trendable_by_default]" value="1"`,
		`name="form_admin_settings[timeline_preview]" value="1" checked`,
		`name="form_admin_settings[allow_referrer_origin]" value="1" checked`,
		`name="form_admin_settings[activity_api_enabled]" value="1" checked`,
		`name="form_admin_settings[peers_api_enabled]" value="1"`,
		`name="form_admin_settings[authorized_fetch]" value="1" checked`,
		`name="form_admin_settings[bootstrap_timeline_accounts]" value="alice, bob"`,
		`name="form_admin_settings[profile_directory]" value="1" checked`,
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("discovery html missing %q: %s", want, html)
		}
	}
}
