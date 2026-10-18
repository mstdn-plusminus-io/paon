package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestFilterKeywordRowHTMLUsesCocoonExistingClassForPersistedKeywords(t *testing.T) {
	persisted := filterKeywordRowHTML(models.CustomFilterKeyword{ID: 11, Keyword: "spoiler", WholeWord: true}, "0", "en")
	wantPersisted := `<input value="false" type="hidden" name="custom_filter[keywords_attributes][0][_destroy]" id="custom_filter_keywords_attributes_0__destroy"><a class="table-action-link remove_fields existing" href="#">`
	if !strings.Contains(persisted, wantPersisted) {
		t.Fatalf("persisted keyword row must use cocoon 'existing' with _destroy directly before the link: %s", persisted)
	}
	if strings.Contains(persisted, "remove_fields dynamic") {
		t.Fatalf("persisted keyword row must not be detached by cocoon: %s", persisted)
	}

	fresh := filterKeywordRowHTML(models.CustomFilterKeyword{WholeWord: true}, "new_keywords", "en")
	if !strings.Contains(fresh, `class="table-action-link remove_fields dynamic"`) || strings.Contains(fresh, "remove_fields existing") {
		t.Fatalf("new keyword row must stay cocoon 'dynamic': %s", fresh)
	}
}

func TestWebFilterFormHTMLKeepsDynamicClassForPlaceholderAndTemplate(t *testing.T) {
	form := webFilterFormHTML("Edit filter", "/filters/7", "put", models.CustomFilter{
		ID:       7,
		Phrase:   "Spoilers",
		Context:  models.StringArray{"home"},
		Keywords: []models.CustomFilterKeyword{{ID: 11, Keyword: "spoiler"}, {Keyword: "unsaved"}},
	}, "", "", "en")
	if got := strings.Count(form, `remove_fields existing`); got != 1 {
		t.Fatalf("existing remove links = %d, want 1: %s", got, form)
	}
	// The unsaved row is rendered directly; the template row is HTML-escaped in a data attribute.
	if !strings.Contains(form, `class="table-action-link remove_fields dynamic"`) || !strings.Contains(form, `remove_fields dynamic&#34;`) {
		t.Fatalf("unsaved row and template must remain dynamic: %s", form)
	}
}

func TestWebFilterFromPayloadRerenderKeepsExistingRemoveClass(t *testing.T) {
	payload := filterPayload{
		Title:              "Spoilers",
		KeywordsAttributes: []filterKeywordPayload{{ID: "11", Keyword: "", KeywordSet: true}},
	}
	filter := webFilterFromPayload(models.CustomFilter{ID: 7}, payload, time.Now().UTC())
	form := webFilterFormHTML("Edit filter", "/filters/7", "put", filter, "", "", "en")
	if !strings.Contains(form, `name="custom_filter[keywords_attributes][0][id]" value="11"`) || !strings.Contains(form, `remove_fields existing`) {
		t.Fatalf("re-rendered persisted keyword must keep id and existing class: %s", form)
	}
}

func TestWebFilterStatusesHTMLSelectAllCheckboxHasRailsID(t *testing.T) {
	out := webFilterStatusesHTML(models.CustomFilter{ID: 7, Statuses: []models.CustomFilterStatus{{ID: 1, StatusID: 2}}}, "", "", "1")
	if !strings.Contains(out, `<input type="checkbox" name="batch_checkbox_all" id="batch_checkbox_all">`) {
		t.Fatalf("select-all checkbox must carry id=batch_checkbox_all for admin.js: %s", out)
	}
}

func TestWebFilterUpdateContextsValidMatchesRailsPresenceValidation(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		expected bool
	}{
		{"only hidden blank submitted", "custom_filter%5Bcontext%5D%5B%5D=", false},
		{"valid context", "custom_filter%5Bcontext%5D%5B%5D=&custom_filter%5Bcontext%5D%5B%5D=home", true},
		{"invalid context", "custom_filter%5Bcontext%5D%5B%5D=bogus", false},
		{"context field absent", "custom_filter%5Btitle%5D=x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form, err := url.ParseQuery(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			contexts := normalizeFilterContexts(form["custom_filter[context][]"])
			if got := webFilterUpdateContextsValid(form, contexts); got != tc.expected {
				t.Fatalf("webFilterUpdateContextsValid(%q) = %v, want %v", tc.body, got, tc.expected)
			}
		})
	}
}

func TestWebFilterExistingKeywordBlankDetectsPersistedBlankKeywordOnly(t *testing.T) {
	cases := []struct {
		name     string
		attrs    []filterKeywordPayload
		expected bool
	}{
		{"persisted blank", []filterKeywordPayload{{ID: "11", Keyword: "  ", KeywordSet: true}}, true},
		{"persisted blank but destroyed", []filterKeywordPayload{{ID: "11", Keyword: "", KeywordSet: true, Destroy: true}}, false},
		{"persisted keyword not submitted", []filterKeywordPayload{{ID: "11"}}, false},
		{"persisted keyword present", []filterKeywordPayload{{ID: "11", Keyword: "spoiler", KeywordSet: true}}, false},
		{"new blank row is skipped", []filterKeywordPayload{{Keyword: "", KeywordSet: true}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := webFilterExistingKeywordBlank(tc.attrs); got != tc.expected {
				t.Fatalf("webFilterExistingKeywordBlank = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestWebFilterKeywordsFromFormBlankPersistedKeywordIsDetected(t *testing.T) {
	body := strings.Join([]string{
		"custom_filter%5Btitle%5D=Spoilers",
		"custom_filter%5Bcontext%5D%5B%5D=",
		"custom_filter%5Bcontext%5D%5B%5D=home",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5Bkeyword%5D=",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5Bwhole_word%5D=0",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5Bid%5D=11",
		"custom_filter%5Bkeywords_attributes%5D%5B0%5D%5B_destroy%5D=false",
	}, "&")
	req := httptest.NewRequest(http.MethodPost, "/filters/7", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	payload, err := parseWebFilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !webFilterExistingKeywordBlank(payload.KeywordsAttributes) {
		t.Fatalf("blank persisted keyword should be detected: %#v", payload.KeywordsAttributes)
	}
	if !webFilterUpdateContextsValid(c.Request().Form, normalizeFilterContexts(payload.Context)) {
		t.Fatalf("home context should be valid: %#v", payload.Context)
	}
}

func TestWebFilterValidationErrorMessageUsesRailsGenericKey(t *testing.T) {
	if got := webFilterValidationErrorMessage("en"); got != "Something isn't quite right yet! Please review the error below" {
		t.Fatalf("en validation message = %q", got)
	}
}

func TestUpdateWebFilterValidatesBeforeWriting(t *testing.T) {
	src, err := os.ReadFile("web_filters.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "updateWebFilter")
	contextCheck := strings.Index(body, "webFilterUpdateContextsValid(c.Request().Form, contexts)")
	keywordCheck := strings.Index(body, "webFilterExistingKeywordBlank(payload.KeywordsAttributes)")
	transaction := strings.Index(body, "s.db.Transaction(")
	if contextCheck < 0 || keywordCheck < 0 || transaction < 0 {
		t.Fatalf("updateWebFilter missing validation or transaction: context=%d keyword=%d tx=%d", contextCheck, keywordCheck, transaction)
	}
	if contextCheck > transaction || keywordCheck > transaction {
		t.Fatal("updateWebFilter must re-render the edit form before writing anything")
	}
}

func TestWebFilterFormReRenderKeepsPersistedKeywordMarkedForDestruction(t *testing.T) {
	keywords := webFilterKeywordsForForm([]filterKeywordPayload{
		{ID: "11", Keyword: "kept"},
		{ID: "12", Keyword: "removed", Destroy: true},
		{Keyword: "new and removed", Destroy: true},
	})
	if len(keywords) != 2 {
		t.Fatalf("keywords = %#v", keywords)
	}
	if keywords[0].MarkedForDestruction || !keywords[1].MarkedForDestruction || keywords[1].ID != 12 {
		t.Fatalf("keywords = %#v", keywords)
	}
	row := filterKeywordRowHTML(keywords[1], "1", "en")
	for _, want := range []string{
		`<tr class="nested-fields" style="display: none;">`,
		`<input type="hidden" name="custom_filter[keywords_attributes][1][id]" value="12">`,
		`<input value="true" type="hidden" name="custom_filter[keywords_attributes][1][_destroy]"`,
		`class="table-action-link remove_fields existing destroyed"`,
	} {
		if !strings.Contains(row, want) {
			t.Fatalf("destroyed keyword row missing %q: %s", want, row)
		}
	}
	kept := filterKeywordRowHTML(keywords[0], "0", "en")
	if strings.Contains(kept, `display: none`) || !strings.Contains(kept, `<input value="false" type="hidden" name="custom_filter[keywords_attributes][0][_destroy]"`) {
		t.Fatalf("kept keyword row = %s", kept)
	}
}
