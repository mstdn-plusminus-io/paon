package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestLoginActivitiesHTMLRendersRailsPaginationLinks(t *testing.T) {
	activities := make([]models.LoginActivity, settingsLoginActivitiesPageSize)
	html := loginActivitiesHTML(activities, "2")
	for _, want := range []string{
		`href="/settings/login_activities?page=1"`,
		`href="/settings/login_activities?page=3"`,
		"Prev",
		"Next",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
}

func TestSettingsLoginActivitiesRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/login_activities", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsLoginActivitiesPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/login_activities")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestUserLoginActivitiesUsesRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("login_activities.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"userLoginActivities", "Offset(adminPageOffset(c, settingsLoginActivitiesPageSize))"},
		{"userLoginActivities", "Limit(settingsLoginActivitiesPageSize)"},
		{"settingsLoginActivitiesPage", "loginActivitiesHTML(activities, adminTrendsPageValue(c), renderArgs...)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestUserAgentDescriptionUsesRailsBrowserAndPlatformIDs(t *testing.T) {
	tests := []struct {
		name        string
		userAgent   string
		browserID   string
		platformID  string
		description string
	}{
		{
			name:        "chrome mac",
			userAgent:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
			browserID:   "chrome",
			platformID:  "mac",
			description: "Chrome on macOS",
		},
		{
			name:        "edge windows",
			userAgent:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
			browserID:   "edge",
			platformID:  "windows",
			description: "Microsoft Edge on Windows",
		},
		{
			name:        "mobile safari ios",
			userAgent:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			browserID:   "safari",
			platformID:  "ios",
			description: "Safari on iOS",
		},
		{
			name:        "unknown",
			userAgent:   "",
			browserID:   "unknown_browser",
			platformID:  "unknown_platform",
			description: "Unknown Browser on Unknown Platform",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection := detectUserAgent(tt.userAgent)
			if detection.BrowserID != tt.browserID || detection.PlatformID != tt.platformID {
				t.Fatalf("ids = %s/%s, want %s/%s", detection.BrowserID, detection.PlatformID, tt.browserID, tt.platformID)
			}
			if got := userAgentDescription(tt.userAgent); got != tt.description {
				t.Fatalf("description = %q, want %q", got, tt.description)
			}
		})
	}
}
