package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestParseFeaturedTagPayloadAcceptsJSONName(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/featured_tags", strings.NewReader(`{"name":" GoLang "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseFeaturedTagPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Name != "GoLang" {
		t.Fatalf("Name = %q", payload.Name)
	}
	normalized, display, ok := normalizeTagName(payload.Name)
	if !ok || normalized != "golang" || display != "GoLang" {
		t.Fatalf("normalized=%q display=%q ok=%v", normalized, display, ok)
	}
}

func TestParseFeaturedTagPayloadTrimsMissingNameLikeRailsRequire(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/featured_tags", strings.NewReader(`{"name":" "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseFeaturedTagPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Name != "" {
		t.Fatalf("Name = %q, want empty", payload.Name)
	}
}

func TestNormalizeTagNameRejectsInvalidFeaturedTag(t *testing.T) {
	if _, _, ok := normalizeTagName("bad tag"); ok {
		t.Fatal("expected spaces to be rejected")
	}
	if normalized, display, ok := normalizeTagName("#日本語"); !ok || normalized != "日本語" || display != "日本語" {
		t.Fatalf("normalized=%q display=%q ok=%v", normalized, display, ok)
	}
}

func TestRequireAccountScopeRejectsBearerWithoutDatabaseToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/featured_tags", nil)
	req.Header.Set("Authorization", "Bearer missing")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	_, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestFeaturedTagAPIsCheckRailsScopes(t *testing.T) {
	src, err := os.ReadFile("featured_tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, want := range map[string]string{
		"featuredTags":           `s.requireAccountScope(c, "read", "read:accounts")`,
		"featuredTagSuggestions": `s.requireAccountScope(c, "read", "read:accounts")`,
		"createFeaturedTag":      `s.requireAccountScope(c, "write", "write:accounts")`,
		"deleteFeaturedTag":      `s.requireAccountScope(c, "write", "write:accounts")`,
	} {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("featured_tags.go:%s does not contain %q", fn, want)
		}
	}
	if !functionBodyContains(t, src, "createFeaturedTag", `return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: name")`) {
		t.Fatal("createFeaturedTag must match Rails params.require(:name) 400 response")
	}
}

func TestFeaturedTagSuggestionsUseRailsRecentlyUsedScope(t *testing.T) {
	src, err := os.ReadFile("featured_tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`func (s *Server) featuredTagSuggestionQuery(accountID int64) *gorm.DB`,
		`Where("account_id = ? AND deleted_at IS NULL", accountID)`,
		`Order("id DESC")`,
		`Limit(1000)`,
		`Order("COUNT(*) DESC")`,
		`Limit(10)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("featured tag suggestion query missing %q", want)
		}
	}
	if functionBodyContains(t, src, "featuredTagSuggestionQuery", `statuses.visibility IN`) {
		t.Fatal("featured tag suggestions should match Rails recently_used and not filter by status visibility")
	}
	if !functionBodyContains(t, src, "featuredTagSuggestions", `query := s.featuredTagSuggestionQuery(account.ID)`) {
		t.Fatal("REST featured tag suggestions should use shared Rails-compatible query")
	}

	settings, err := os.ReadFile("settings_featured_tags.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, settings, "featuredTagSettingSuggestions", `query := s.featuredTagSuggestionQuery(accountID)`) {
		t.Fatal("settings featured tag suggestions should use shared Rails-compatible query")
	}
}

func TestFeaturedTagStatsUsesRailsRecentStatusForLastStatusAt(t *testing.T) {
	src, err := os.ReadFile("featured_tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`query.Count(&stats.StatusesCount)`,
		`Select("statuses.created_at")`,
		`Order("statuses.id DESC")`,
		`Limit(1)`,
		`Scan(&stats.LastStatusAt)`,
	} {
		if !functionBodyContains(t, src, "featuredStats", want) {
			t.Fatalf("featuredStats must match Rails FeaturedTag#reset_data recent status lookup; missing %q", want)
		}
	}
	if functionBodyContains(t, src, "featuredStats", `MAX(statuses.created_at)`) {
		t.Fatal("featuredStats must not use MAX(created_at); Rails uses Status.default_scope recent/id DESC")
	}
}
