package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestQueryParamValuePresentMatchesRailsPresentForWhitespace(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/blocks?blank=%20%09&value=%2001%20&empty=", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if queryParamValuePresent(c, "blank") {
		t.Fatal("whitespace-only query param should be absent like Rails present?")
	}
	if queryParamValuePresent(c, "empty") {
		t.Fatal("empty query param should be absent like Rails present?")
	}
	if queryParamValuePresent(c, "missing") {
		t.Fatal("missing query param should be absent")
	}
	if !queryParamValuePresent(c, "value") {
		t.Fatal("nonblank query param should be present even when padded")
	}
}

func TestLimitOnlyPaginationLinkMatchesRailsRelationshipAllowlist(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/blocks?limit=5&local=true&extra=1&max_id=99&min_id=1", nil)
	req.Host = "social.example"
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	got := limitOnlyPaginationLink(c, 110, 100, "since_id", true)
	if !strings.Contains(got, "limit=5") || !strings.Contains(got, "max_id=100") || !strings.Contains(got, "since_id=110") {
		t.Fatalf("Link missing Rails relationship params: %q", got)
	}
	for _, unwanted := range []string{"local=", "extra=", "min_id="} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("Link should omit Rails-filtered param %q: %q", unwanted, got)
		}
	}
}
