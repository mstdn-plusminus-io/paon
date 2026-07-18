package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestDirectoryDefaultsToEnabledWithoutDB(t *testing.T) {
	if !(&Server{}).profileDirectoryEnabled() {
		t.Fatal("profileDirectoryEnabled = false")
	}
}

func TestDirectoryReturnsEmptyListWithoutDBWhenEnabled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/directory", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.directory(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=15, public, stale-while-revalidate=30, stale-if-error=86400" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestDirectoryDisabledDoesNotApplyAnonymousPublicCacheLikeRails(t *testing.T) {
	previous, hadPrevious := railsSettingDefaults["profile_directory"]
	railsSettingDefaults["profile_directory"] = "false"
	t.Cleanup(func() {
		if hadPrevious {
			railsSettingDefaults["profile_directory"] = previous
		} else {
			delete(railsSettingDefaults, "profile_directory")
		}
	})

	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/directory", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.directory(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("disabled directory must not apply Rails anonymous public cache, got %q", got)
	}
}

func TestOffsetParsesNonNegativeQueryValue(t *testing.T) {
	e := echo.New()
	for _, tt := range []struct {
		target string
		want   int
	}{
		{"/api/v1/directory?offset=20", 20},
		{"/api/v1/trends/tags?offset=20abc", 20},
	} {
		req := httptest.NewRequest("GET", tt.target, nil)
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := offset(c); got != tt.want {
			t.Fatalf("offset(%s) = %d, want %d", tt.target, got, tt.want)
		}
	}
}

func TestOffsetRejectsNegativeAndInvalidValues(t *testing.T) {
	e := echo.New()
	for _, target := range []string{"/api/v1/directory?offset=-1", "/api/v1/directory?offset=bad"} {
		req := httptest.NewRequest("GET", target, nil)
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := offset(c); got != 0 {
			t.Fatalf("offset(%s) = %d, want 0", target, got)
		}
	}
}

func TestLimitMatchesRailsLimitParam(t *testing.T) {
	e := echo.New()
	for _, tt := range []struct {
		target string
		def    int
		max    int
		want   int
	}{
		{"/api/v1/directory", 40, 80, 40},
		{"/api/v1/directory?limit=0", 40, 80, 0},
		{"/api/v1/directory?limit=", 40, 80, 0},
		{"/api/v1/directory?limit=-5", 40, 80, 5},
		{"/api/v1/directory?limit=999", 40, 80, 80},
		{"/api/v1/directory?limit=bad", 40, 80, 0},
		{"/api/v1/directory?limit=12abc", 40, 80, 12},
		{"/api/v1/domain_blocks?limit=250", 100, 200, 200},
	} {
		req := httptest.NewRequest("GET", tt.target, nil)
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := limit(c, tt.def, tt.max); got != tt.want {
			t.Fatalf("limit(%s) = %d, want %d", tt.target, got, tt.want)
		}
		if got := limitParam(c, tt.def, tt.max); got != tt.want {
			t.Fatalf("limitParam(%s) = %d, want %d", tt.target, got, tt.want)
		}
	}
}

func TestDirectoryUsesRailsDefaultAccountsLimit(t *testing.T) {
	source, err := os.ReadFile("directory.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `Limit(limit(c, 40, 80))`) {
		t.Fatal("directory must use Rails DEFAULT_ACCOUNTS_LIMIT default and 80 max")
	}
}

func TestDirectoryLocalParamUsesRailsTruthySemantics(t *testing.T) {
	source, err := os.ReadFile("directory.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	if !strings.Contains(body, `truthy(c.QueryParam("local"))`) {
		t.Fatal("directory local filtering should use Rails-compatible truthy semantics")
	}
	if strings.Contains(body, `QueryParam("local") == "true"`) {
		t.Fatal("directory local filtering should not use exact true/1 comparisons")
	}
}

func TestDirectoryDomainBlockExclusionUsesCaseInsensitiveRailsDomains(t *testing.T) {
	source, err := os.ReadFile("directory.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, source, "directoryQuery", `query = applyDirectoryExclusions(query, current.ID, !truthy(c.QueryParam("local")))`) {
		t.Fatal("directoryQuery must skip Rails domain-block exclusions for local directory requests")
	}
	if !functionBodyContains(t, source, "applyDirectoryExclusions", `lower(account_domain_blocks.domain) = lower(accounts.domain)`) {
		t.Fatal("directory domain-block exclusion must match the Rails excluded_from_timeline_domains comparison case-insensitively")
	}
	if !functionBodyContains(t, source, "applyDirectoryExclusions", `if includeDomainBlocks {`) {
		t.Fatal("directory domain-block exclusion must be gated like Rails account_domain_block_scope")
	}
	if functionBodyContains(t, source, "applyDirectoryExclusions", `account_domain_blocks.domain = accounts.domain`) {
		t.Fatal("directory domain-block exclusion must not use a case-sensitive domain comparison")
	}
}

func TestDirectoryOrderParamMatchesRailsAllowlist(t *testing.T) {
	source, err := os.ReadFile("directory.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`switch c.QueryParam("order")`,
		`case "new":`,
		`return query.Order("accounts.id DESC")`,
		`case "", "active":`,
		`return query.Order("account_stats.last_status_at DESC NULLS LAST")`,
		`default:
		return query`,
	} {
		if !functionBodyContains(t, source, "directoryQuery", want) {
			t.Fatalf("directoryQuery must match Rails order allowlist; missing %q", want)
		}
	}
	if strings.Contains(string(source), `if c.QueryParam("order") == "new"`) {
		t.Fatal("directoryQuery must not treat unknown order values as active")
	}
	if functionBodyContains(t, source, "directoryQuery", `Order("account_stats.last_status_at DESC NULLS LAST").Order("accounts.id DESC")`) {
		t.Fatal("directory active order must not add a Go-only id tie-breaker")
	}
}
