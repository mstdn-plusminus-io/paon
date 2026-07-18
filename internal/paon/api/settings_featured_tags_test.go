package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestFeaturedTagsSettingsHTMLRendersRowsAndSuggestions(t *testing.T) {
	html := featuredTagsSettingsHTML(
		[]models.FeaturedTag{{
			ID:            7,
			StatusesCount: 3,
			LastStatusAt:  sql.NullTime{Time: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC), Valid: true},
			Name:          sql.NullString{String: "GoLang", Valid: true},
		}},
		[]models.Tag{{Name: "ruby", DisplayName: sql.NullString{String: "Ruby", Valid: true}}},
		"",
	)
	for _, want := range []string{
		`class="simple_form new_featured_tag"`,
		`id="new_featured_tag"`,
		`class="fields-group"`,
		`class="input with_block_label string required featured_tag_name field_with_hint"`,
		`<abbr title="required">*</abbr>`,
		`name="featured_tag[name]"`,
		`data-method="post" href="/settings/featured_tags?featured_tag%5Bname%5D=ruby">#Ruby`,
		`class="hint"`,
		`#Ruby`,
		`class="btn"`,
		`class="directory__tag"`,
		`class="fa fa-hashtag fa-fw"`,
		`GoLang`,
		`class="trends__item__current">3`,
		`datetime="2026-06-19T00:00:00Z"`,
		`/settings/featured_tags/7`,
		`data-method="delete"`,
		`class="table-action-link"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `<table>`) {
		t.Fatalf("featured tags settings should use Rails directory tag cards, not a generic table: %s", html)
	}
}

func TestSettingsFeaturedTagsRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/featured_tags", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsFeaturedTagsPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/featured_tags")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}
