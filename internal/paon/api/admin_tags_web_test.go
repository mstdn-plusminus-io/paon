package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminTagPageRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/tags/7", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/tags/7")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminTagsPageRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/tags?name=go&status=unreviewed", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/tags?name=go&status=unreviewed")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminTagsHTMLIncludesSearchFiltersRowsAndPagination(t *testing.T) {
	html := adminTagsHTML([]models.Tag{
		{
			ID:          7,
			Name:        "golang",
			DisplayName: sql.NullString{String: "Go", Valid: true},
			Usable:      sql.NullBool{Bool: false, Valid: true},
			Trendable:   sql.NullBool{Bool: true, Valid: true},
			ReviewedAt:  sql.NullTime{Time: time.Now().UTC(), Valid: true},
		},
		{
			ID:                8,
			Name:              "gopher",
			RequestedReviewAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
		},
	}, adminTagIndexFilters{Status: "not_usable", Name: "go &", Order: "oldest", Page: 2}, true, false, "en")

	for _, want := range []string{
		`action="/admin/tags"`,
		`method="get"`,
		`name="status"`,
		`value="not_usable" selected`,
		`name="order"`,
		`value="oldest" selected`,
		`name="name" value="go &amp;"`,
		`href="/admin/tags/7"`,
		`#Go`,
		`batch-table__row--muted`,
		`Review requested`,
		`rel="prev"`,
		`rel="next"`,
		`name=go+%26`,
		`page=3`,
		`status=not_usable`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin tags html missing %q: %s", want, html)
		}
	}
}

func TestAdminTagIndexFiltersValidateRailsValues(t *testing.T) {
	for _, path := range []string{
		"/admin/tags?status=unknown",
		"/admin/tags?order=popular",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
		if _, err := adminTagIndexFiltersFromContext(c); err == nil {
			t.Fatalf("adminTagIndexFiltersFromContext(%q) accepted an unknown filter", path)
		}
	}
}

func TestAdminTagSearchPatternMatchesNormalizedPrefixLiterally(t *testing.T) {
	if got, want := adminTagSearchPattern(`Go%_\`), `go\%\_\\%`; got != want {
		t.Fatalf("adminTagSearchPattern = %q, want %q", got, want)
	}
}

func TestAdminTagUpdatesRequireRailsRootParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/tags/7", strings.NewReader("display_name=Go"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if updates, _, err := adminTagUpdatesFromForm(c, models.Tag{ID: 7, Name: "go"}); !errors.Is(err, errAdminTagParamsMissing) {
		t.Fatalf("flat tag form should be rejected like Rails params.require(:tag), updates=%#v err=%v", updates, err)
	}
}

func TestAdminTagUpdatesRejectsDifferentName(t *testing.T) {
	form := url.Values{}
	form.Add("tag[name]", "rust")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/tags/7", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if _, _, err := adminTagUpdatesFromForm(c, models.Tag{ID: 7, Name: "golang"}); err == nil {
		t.Fatal("expected different tag name to be rejected")
	} else if !strings.Contains(err.Error(), "does not match the previous name") {
		t.Fatalf("different tag name error = %q", err)
	}
}

func TestAdminTagUpdatesRejectsMismatchedDisplayName(t *testing.T) {
	form := url.Values{}
	form.Add("tag[display_name]", "Rust")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/tags/7", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if _, _, err := adminTagUpdatesFromForm(c, models.Tag{ID: 7, Name: "golang"}); err == nil {
		t.Fatal("expected mismatched display name to be rejected")
	} else if !strings.Contains(err.Error(), "does not match the previous name") {
		t.Fatalf("mismatched display name error = %q", err)
	}
}

func TestAdminTagHTMLIncludesRailsFields(t *testing.T) {
	html := adminTagHTML(models.Tag{
		ID:          7,
		Name:        "golang",
		DisplayName: sql.NullString{String: "Go", Valid: true},
		Usable:      sql.NullBool{Bool: true, Valid: true},
		Trendable:   sql.NullBool{Bool: false, Valid: true},
		Listable:    sql.NullBool{Bool: true, Valid: true},
		ReviewedAt:  sql.NullTime{Time: time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC), Valid: true},
	}, "saved", "")
	for _, want := range []string{
		"#Go",
		`action="/admin/tags/7"`,
		`class="simple_form edit_tag"`,
		`name="_method" value="patch"`,
		`name="tag[display_name]" id="tag_display_name" value="Go"`,
		`name="tag[usable]" id="tag_usable" value="1" checked`,
		`name="tag[trendable]" id="tag_trendable" value="1"`,
		`name="tag[listable]" id="tag_listable" value="1" checked`,
		`class="fields-group"`,
		`class="actions"`,
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin tag html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `data-admin-component="Counter"`) {
		t.Fatalf("admin tag html rendered dashboard without view_dashboard permission: %s", html)
	}
}

func TestAdminTagHTMLUsesRailsTrendableByDefault(t *testing.T) {
	tag := models.Tag{ID: 7, Name: "golang"}
	defaultFalse := adminTagHTML(tag, "", "", adminTagHTMLOptions{Locale: "en", TrendableByDefault: false})
	if strings.Contains(defaultFalse, `id="tag_trendable" value="1" checked`) {
		t.Fatalf("default false trendable html = %s", defaultFalse)
	}
	defaultTrue := adminTagHTML(tag, "", "", adminTagHTMLOptions{Locale: "en", TrendableByDefault: true})
	if !strings.Contains(defaultTrue, `id="tag_trendable" value="1" checked`) {
		t.Fatalf("default true trendable html = %s", defaultTrue)
	}
	explicitFalse := adminTagHTML(models.Tag{
		ID:        7,
		Name:      "golang",
		Trendable: sql.NullBool{Bool: false, Valid: true},
	}, "", "", adminTagHTMLOptions{Locale: "en", TrendableByDefault: true})
	if strings.Contains(explicitFalse, `id="tag_trendable" value="1" checked`) {
		t.Fatalf("explicit false trendable html = %s", explicitFalse)
	}
}

func TestAdminTagHTMLIncludesRailsDashboardWidgetsWhenAllowed(t *testing.T) {
	html := adminTagHTML(models.Tag{
		ID:        7,
		Name:      "golang",
		Usable:    sql.NullBool{Bool: true, Valid: true},
		Trendable: sql.NullBool{Bool: false, Valid: true},
		Listable:  sql.NullBool{Bool: true, Valid: true},
	}, "", "", "en", true)
	for _, want := range []string{
		`class="dashboard"`,
		`class="dashboard__item"`,
		`data-admin-component="Counter"`,
		`data-admin-component="Dimension"`,
		`&#34;measure&#34;:&#34;tag_accounts&#34;`,
		`&#34;measure&#34;:&#34;tag_uses&#34;`,
		`&#34;measure&#34;:&#34;tag_servers&#34;`,
		`&#34;dimension&#34;:&#34;tag_servers&#34;`,
		`&#34;dimension&#34;:&#34;tag_languages&#34;`,
		`&#34;params&#34;:{&#34;id&#34;:&#34;7&#34;}`,
		`&#34;href&#34;:&#34;/tags/golang&#34;`,
		`&#34;target&#34;:&#34;_blank&#34;`,
		`class="dashboard__quick-access positive"`,
		`class="dashboard__quick-access negative"`,
		"Can be used",
		"Won&#39;t appear under trends",
		"Can be suggested",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin tag dashboard html missing %q: %s", want, html)
		}
	}
}

func TestAdminTagCheckboxUsesRailsHiddenFalse(t *testing.T) {
	html := adminTagCheckboxHTML("tag[usable]", "Usable", false)
	for _, want := range []string{`type="hidden" name="tag[usable]" value="0"`, `type="checkbox" name="tag[usable]" id="tag_usable" value="1"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("checkbox html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "checked") {
		t.Fatalf("unchecked checkbox rendered checked: %s", html)
	}
}

func TestAdminTagUpdateRefreshesSearchIndex(t *testing.T) {
	src, err := os.ReadFile("admin_tags_web.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateAdminTagWeb", "s.meiliIndexTagsBestEffort(c.Request().Context(), []int64{tag.ID})") {
		t.Fatal("updateAdminTagWeb does not refresh the Meilisearch tag document")
	}
}
