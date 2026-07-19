package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func TestAuthorizedApplicationsRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorized_applications", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.oauthAuthorizedApplications(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/oauth/authorized_applications")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestDestroyAuthorizedApplicationRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodDelete, "/oauth/authorized_applications/10", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.destroyOAuthAuthorizedApplication(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/oauth/authorized_applications/10")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestAuthorizedApplicationsHTMLRendersAppsAndRevokeActions(t *testing.T) {
	html := authorizedApplicationsHTML([]authorizedOAuthApplication{
		{
			ID:           10,
			Name:         "Mobile App",
			Website:      "https://app.example.test",
			Scopes:       "read write follow",
			AuthorizedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			LastUsedAt:   sql.NullTime{Time: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC), Valid: true},
		},
		{
			ID:       11,
			Name:     "First-party",
			Superapp: true,
		},
	}, "saved", "", true, "en", "mastodon-light")
	for _, want := range []string{"Mobile App", "https://app.example.test", "Full access to your Paon account", "Read and write access", "Follows, Mutes and Blocks", "Jun 19, 2026", "/oauth/authorized_applications/10", "First-party", "Internal", "saved", "mastodon-light.css", `target="_blank"`, `data-method="delete"`, `data-confirm="Are you sure?"`, `class="applications-list"`, `class="applications-list__item"`, `class="announcements-list__item__permissions"`, `class="permissions-list"`, `class="permissions-list__item__icon"`, `class="permissions-list__item__text__type"`, `class="table-action-link"`, `<body class="admin theme-`, `class="sidebar"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "/oauth/authorized_applications/11") {
		t.Fatalf("superapp should not render revoke action: %s", html)
	}
}

func TestAuthorizedApplicationsHTMLUsesConfiguredApplicationNameAndRailsDateFormat(t *testing.T) {
	html := authorizedApplicationsHTML([]authorizedOAuthApplication{{
		ID:           10,
		Name:         "Mobile App",
		Scopes:       "read write",
		AuthorizedAt: time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC),
	}}, "", "", true, "ja", "default", `<ul></ul>`, "Paon")
	for _, want := range []string{"Paonアカウントへのフルアクセス", "2026年07月07日", `data-method="delete"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("configured application html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "Mastodonアカウントへのフルアクセス") {
		t.Fatalf("configured application name was not applied: %s", html)
	}
}

func TestAuthorizedApplicationsHTMLUsesDoorkeeperLocale(t *testing.T) {
	html := authorizedApplicationsHTML([]authorizedOAuthApplication{{
		ID:           10,
		Name:         "Mobile App",
		Scopes:       "read write",
		AuthorizedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}}, "", "", true, "ja")
	for _, want := range []string{"認証済みアプリ", "APIを使用してアカウントにアクセスできる", "Paonアカウントへのフルアクセス", "読み取りおよび書き込みアクセス", "取消"} {
		if !strings.Contains(html, want) {
			t.Fatalf("localized html missing %q: %s", want, html)
		}
	}
}

func TestAuthorizedApplicationsQueryAggregatesTokenScopesFallback(t *testing.T) {
	src, err := os.ReadFile("authorized_applications.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`COALESCE(NULLIF(oauth_applications.scopes, ''), STRING_AGG(DISTINCT tokens.scopes, ' '), '') AS scopes`,
		`oauth_applications.created_at AS authorized_at`,
		`MAX(tokens.last_used_at) AS last_used_at`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("authorized applications query missing %q", want)
		}
	}
}

func TestScopeListFallsBackToNone(t *testing.T) {
	if got := strings.Join(scopeList(""), ","); got != "none" {
		t.Fatalf("scopeList empty = %q", got)
	}
	if got := strings.Join(scopeList("read write read"), ","); got != "read,write" {
		t.Fatalf("scopeList values = %q", got)
	}
}

func TestGroupedOAuthScopesMirrorRailsTransformer(t *testing.T) {
	groups := groupedOAuthScopes("read write follow read:accounts write:accounts admin:read:reports")
	got := make([]string, 0, len(groups))
	for _, group := range groups {
		got = append(got, group.Key+"="+group.Access)
	}
	want := "all=read/write,follow=read/write,accounts=read/write,admin/reports=read"
	if strings.Join(got, ",") != want {
		t.Fatalf("groupedOAuthScopes = %q, want %q", strings.Join(got, ","), want)
	}
}
