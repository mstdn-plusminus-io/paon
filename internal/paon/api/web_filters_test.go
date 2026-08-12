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
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseWebFilterPayloadAcceptsRailsNestedParams(t *testing.T) {
	body := strings.Join([]string{
		"custom_filter%5Btitle%5D=+Spoilers+",
		"custom_filter%5Bexpires_in%5D=3600",
		"custom_filter%5Bcontext%5D%5B%5D=home",
		"custom_filter%5Bcontext%5D%5B%5D=notifications",
		"custom_filter%5Bfilter_action%5D=hide",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5Bkeyword%5D=+spoiler+",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5Bwhole_word%5D=0",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5Bwhole_word%5D=1",
	}, "&")
	req := httptest.NewRequest(http.MethodPost, "/filters", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err := parseWebFilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Title != " Spoilers " || payload.FilterAction != "hide" || len(payload.Context) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.ExpiresIn == nil || *payload.ExpiresIn != 3600 {
		t.Fatalf("expires_in = %#v", payload.ExpiresIn)
	}
	if len(payload.KeywordsAttributes) != 1 || payload.KeywordsAttributes[0].Keyword != " spoiler " || payload.KeywordsAttributes[0].WholeWord == nil || !*payload.KeywordsAttributes[0].WholeWord {
		t.Fatalf("keywords = %#v", payload.KeywordsAttributes)
	}
	if !payload.KeywordsAttributes[0].KeywordSet {
		t.Fatalf("keyword field should be marked as set: %#v", payload.KeywordsAttributes[0])
	}
}

func TestParseWebFilterPayloadAcceptsDynamicNestedKeywordIndexes(t *testing.T) {
	body := strings.Join([]string{
		"custom_filter%5Btitle%5D=Spoilers",
		"custom_filter%5Bcontext%5D%5B%5D=home",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5Bkeyword%5D=first",
		"custom_filter%5Bkeywords_attributes%5D%5B1720000000000%5D%5Bkeyword%5D=dynamic",
		"custom_filter%5Bkeywords_attributes%5D%5Bnew_keywords%5D%5Bkeyword%5D=template",
	}, "&")
	req := httptest.NewRequest(http.MethodPost, "/filters", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err := parseWebFilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.KeywordsAttributes) != 3 || payload.KeywordsAttributes[0].Keyword != "first" || payload.KeywordsAttributes[1].Keyword != "dynamic" || payload.KeywordsAttributes[2].Keyword != "template" {
		t.Fatalf("dynamic keywords = %#v", payload.KeywordsAttributes)
	}
}

func TestWebFiltersHTMLRendersFilters(t *testing.T) {
	html := webFiltersHTML([]models.CustomFilter{{
		ID:       7,
		Phrase:   "Spoilers",
		Context:  models.StringArray{"home"},
		Keywords: []models.CustomFilterKeyword{{ID: 1, Keyword: "spoiler"}},
		Statuses: []models.CustomFilterStatus{{ID: 2}},
	}}, "", "")
	for _, want := range []string{"/filters/7/edit", "Spoilers", "spoiler", `class="applications-list"`, `class="filters-list__item"`, `class="filters-list__item__title"`, `class="filters-list__item__permissions"`, `class="permissions-list__item"`, `class="announcements-list__item__action-bar"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
}

func TestWebFilterFormHTMLRendersRailsSimpleFormStructure(t *testing.T) {
	html := webFilterFormHTML("Edit filter", "/filters/7", "put", models.CustomFilter{
		ID:      7,
		Phrase:  "Spoilers",
		Context: models.StringArray{"home", "public"},
		Action:  1,
		Keywords: []models.CustomFilterKeyword{{
			ID:        11,
			Keyword:   "spoiler",
			WholeWord: true,
		}},
		Statuses: []models.CustomFilterStatus{{ID: 3}},
	}, "", "", "en")
	for _, want := range []string{
		`class="simple_form edit_custom_filter"`,
		`id="edit_custom_filter_7"`,
		`action="/filters/7"`,
		`name="_method" value="put"`,
		`class="fields-row"`,
		`class="fields-row__column fields-row__column-6 fields-group"`,
		`class="input with_label string required custom_filter_title"`,
		`name="custom_filter[title]" id="custom_filter_title"`,
		`id="custom_filter_expires_in" name="custom_filter[expires_in]"`,
		`class="input with_block_label check_boxes required custom_filter_context field_with_hint"`,
		`name="custom_filter[context][]" value="home" checked`,
		`value="hide" name="custom_filter[filter_action]" id="custom_filter_filter_action_hide" checked`,
		`href="/filters/7/statuses"`,
		`class="table-wrapper"`,
		`class="table keywords-table"`,
		`class="nested-fields"`,
		`name="custom_filter[keywords_attributes][0][id]" value="11"`,
		`value="spoiler" name="custom_filter[keywords_attributes][0][keyword]"`,
		`value="1" name="custom_filter[keywords_attributes][0][whole_word]" id="custom_filter_keywords_attributes_0_whole_word" checked`,
		`value="false" type="hidden" name="custom_filter[keywords_attributes][0][_destroy]"`,
		`class="table-action-link remove_fields dynamic"`,
		`class="table-action-link add_fields"`,
		`data-association-insertion-template=`,
		`class="btn"`,
		`class="actions"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("filter form html missing %q: %s", want, html)
		}
	}
}

func TestWebFiltersRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/filters", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.webFiltersPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/filters")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestWebFilterStatusesHTMLRendersRailsPaginationLinks(t *testing.T) {
	statuses := make([]models.CustomFilterStatus, webFilterStatusesPageSize)
	for i := range statuses {
		statuses[i] = models.CustomFilterStatus{ID: int64(i + 1), StatusID: int64(100 + i)}
	}
	html := webFilterStatusesHTML(models.CustomFilter{ID: 7, Statuses: statuses}, "", "", "2")
	for _, want := range []string{
		`href="/filters/7/statuses?page=1"`,
		`href="/filters/7/statuses?page=3"`,
		"Previous",
		"Next",
		`name="form_status_filter_batch_action[status_filter_ids][]" value="1"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("filtered statuses html missing %q: %s", want, html)
		}
	}
}

func TestFormInt64ValuesParsesRailsRootedArrayOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/relationships", strings.NewReader("form_account_batch%5Baccount_ids%5D%5B%5D=11&form_account_batch%5Baccount_ids%5D=12&form_account_batch%5Baccount_ids%5D%5B%5D=bad"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	got := formInt64Values(c, "form_account_batch[account_ids][]")
	if len(got) != 1 || got[0] != 11 {
		t.Fatalf("ids = %#v", got)
	}
	if flat := formInt64Values(c, "form_account_batch[account_ids]"); len(flat) != 1 || flat[0] != 12 {
		t.Fatalf("flat ids = %#v", flat)
	}
}

func TestWebFilterEditRedirectPathEncodesOnlyExplicitFlash(t *testing.T) {
	if got := webFilterEditRedirectPath(7, "", ""); got != "/filters/7/edit" {
		t.Fatalf("redirect path = %q", got)
	}
	got := webFilterEditRedirectPath(7, "error", "No statuses selected")
	if got != "/filters/7/edit?error=No+statuses+selected" {
		t.Fatalf("redirect path with error = %q", got)
	}
}

func TestWebFilterStatusesHTMLRendersRailsStatusRows(t *testing.T) {
	created := time.Date(2026, 6, 29, 1, 2, 0, 0, time.UTC)
	edited := created.Add(time.Hour)
	status := models.Status{
		ID:          100,
		Text:        "hello #tag",
		SpoilerText: "cw",
		CreatedAt:   created,
		EditedAt:    sql.NullTime{Time: edited, Valid: true},
		Sensitive:   true,
		Visibility:  1,
		AccountID:   42,
		Account: models.Account{
			ID:             42,
			Username:       "alice",
			DisplayName:    "Alice",
			AvatarFileName: sql.NullString{String: "avatar.png", Valid: true},
		},
		MediaAttachments: []models.MediaAttachment{{
			FileFileName: sql.NullString{String: "image.png", Valid: true},
			Description:  sql.NullString{String: "Image description", Valid: true},
		}},
	}
	html := webFilterStatusesHTMLWithConfig(config.Config{Scheme: "https", WebDomain: "social.example"}, models.CustomFilter{
		ID:       7,
		Statuses: []models.CustomFilterStatus{{ID: 1, StatusID: 100, Status: &status}},
	}, "", "", "1")
	for _, want := range []string{
		`class="filters"`,
		`class="back-link"`,
		`class="hint"`,
		`class="batch-table"`,
		`class="batch-table__row"`,
		`class="batch-table__row__select batch-checkbox"`,
		`class="batch-table__row__content"`,
		`class="status__content"`,
		`class="detailed-status__meta"`,
		`name="page" value="1"`,
		`name="remove" value="1" class="table-action-link"`,
		`name="form_status_filter_batch_action[status_filter_ids][]" value="1"`,
		`Content warning`,
		`hello`,
		`alice`,
		`image.png`,
		`Sensitive content`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("filtered statuses html missing %q: %s", want, html)
		}
	}
}

func TestWebFilterStatusModelsUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("web_filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"webFilterStatusesPage", "s.findFilterForStatusPage(account.ID, c.Param(\"filter_id\"))"},
		{"webFilterStatusesPage", "s.webFilterStatusModels(filter.ID, c)"},
		{"webFilterStatusModels", `Preload("Status")`},
		{"webFilterStatusModels", `Preload("Status.Account")`},
		{"webFilterStatusModels", `Preload("Status.MediaAttachments")`},
		{"webFilterStatusModels", "Offset(adminPageOffset(c, webFilterStatusesPageSize))"},
		{"webFilterStatusModels", "Limit(webFilterStatusesPageSize)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestWebFilterHandlersValidateActionEnumLikeRails(t *testing.T) {
	src, err := os.ReadFile("web_filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"createWebFilter": {
			`action, ok := filterActionValue(firstNonEmpty(payload.FilterAction, "warn"))`,
			`settingsT(locale, "filters.errors.invalid_action", "Filter action is invalid")`,
		},
		"updateWebFilter": {
			`action, ok := filterActionValue(payload.FilterAction)`,
			`settingsT(locale, "filters.errors.invalid_action", "Filter action is invalid")`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
}

func TestFilterActionRadiosIncludeMastodon44MediaBlur(t *testing.T) {
	html := filterActionRadios(2, "en")
	for _, want := range []string{`value="blur"`, `id="custom_filter_filter_action_blur"`, `value="blur" name="custom_filter[filter_action]" id="custom_filter_filter_action_blur" checked`} {
		if !strings.Contains(html, want) {
			t.Fatalf("filter action radios missing %q: %s", want, html)
		}
	}
}
