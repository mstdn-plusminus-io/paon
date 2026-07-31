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
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func testBoolPtr(value bool) *bool {
	return &value
}

func TestParseV1FilterPayloadAcceptsFormContextAndBooleans(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/filters", strings.NewReader("phrase=spoiler&context%5B%5D=home&context%5B%5D=notifications&irreversible=true&whole_word=false&expires_in=60"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseV1FilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Phrase != "spoiler" || len(payload.Context) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	if !payload.PhraseSet {
		t.Fatal("phrase presence was not tracked")
	}
	if payload.Irreversible == nil || !*payload.Irreversible {
		t.Fatalf("irreversible = %#v", payload.Irreversible)
	}
	if payload.WholeWord == nil || *payload.WholeWord {
		t.Fatalf("whole_word = %#v", payload.WholeWord)
	}
	if payload.ExpiresIn == nil || *payload.ExpiresIn != 60 {
		t.Fatalf("expires_in = %#v", payload.ExpiresIn)
	}
}

func TestParseV1FilterPayloadTracksBlankFormBooleansLikeRails(t *testing.T) {
	req := httptest.NewRequest("PUT", "/api/v1/filters/1", strings.NewReader("irreversible=&whole_word="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err := parseV1FilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Irreversible == nil || *payload.Irreversible {
		t.Fatalf("blank irreversible should be explicit false like Rails boolean cast, got %#v", payload.Irreversible)
	}
	if payload.WholeWord == nil || *payload.WholeWord {
		t.Fatalf("blank whole_word should be explicit false like Rails boolean cast, got %#v", payload.WholeWord)
	}
}

func TestParseV1FilterPayloadIgnoresScalarContextLikeRailsStrongParams(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/filters", strings.NewReader("phrase=spoiler&context=home"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err := parseV1FilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ContextSet || len(payload.Context) != 0 {
		t.Fatalf("scalar form context = set %v values %#v, want ignored", payload.ContextSet, payload.Context)
	}

	req = httptest.NewRequest("POST", "/api/v1/filters", strings.NewReader(`{"phrase":"spoiler","context":"home"}`))
	req.Header.Set("Content-Type", "application/json")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err = parseV1FilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ContextSet || len(payload.Context) != 0 {
		t.Fatalf("scalar json context = set %v values %#v, want ignored", payload.ContextSet, payload.Context)
	}
}

func TestParseV1FilterPayloadTracksExplicitBlankPhrase(t *testing.T) {
	req := httptest.NewRequest("PUT", "/api/v1/filters/1", strings.NewReader(`{"phrase":"","context":[],"whole_word":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseV1FilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.PhraseSet || payload.Phrase != "" {
		t.Fatalf("phrase presence = %v phrase = %q", payload.PhraseSet, payload.Phrase)
	}
	if !payload.ContextSet || len(payload.Context) != 0 {
		t.Fatalf("context presence = %v context = %#v", payload.ContextSet, payload.Context)
	}
	if payload.WholeWord == nil || !*payload.WholeWord {
		t.Fatalf("whole_word = %#v", payload.WholeWord)
	}
}

func TestUpdateV1FilterRejectsExplicitBlankPhraseLikeRails(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if payload.PhraseSet && strings.TrimSpace(payload.Phrase) == "" {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Keyword can't be blank")`,
		`if payload.PhraseSet {`,
		`keywordUpdates["keyword"] = payload.Phrase`,
		`filterUpdates["phrase"] = payload.Phrase`,
		`if payload.ContextSet {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter context is invalid")`,
	} {
		if !functionBodyContains(t, src, "updateV1Filter", want) {
			t.Fatalf("updateV1Filter missing %q", want)
		}
	}
}

func TestV1FilterParentParamsChangedMatchesRailsDeprecatedAPIMultipleKeywordGuard(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	filter := models.CustomFilter{
		Phrase:    "spoiler",
		Context:   models.StringArray{"home", "public"},
		Action:    0,
		ExpiresAt: sql.NullTime{Time: now.Add(time.Hour), Valid: true},
	}

	if v1FilterParentParamsChanged(filter, v1FilterPayload{WholeWord: testBoolPtr(false)}, now) {
		t.Fatal("keyword-only v1 update must not trigger Rails deprecated multiple-keyword guard")
	}
	if v1FilterParentParamsChanged(filter, v1FilterPayload{PhraseSet: true, Phrase: "spoiler"}, now) {
		t.Fatal("unchanged phrase must not trigger Rails deprecated multiple-keyword guard")
	}
	if !v1FilterParentParamsChanged(filter, v1FilterPayload{PhraseSet: true, Phrase: " spoiler "}, now) {
		t.Fatal("phrase whitespace changes must trigger Rails dirty-check guard")
	}
	if !v1FilterParentParamsChanged(filter, v1FilterPayload{PhraseSet: true, Phrase: "new spoiler"}, now) {
		t.Fatal("changed phrase must trigger Rails deprecated multiple-keyword guard")
	}
	if !v1FilterParentParamsChanged(filter, v1FilterPayload{ContextSet: true, Context: []string{"home"}}, now) {
		t.Fatal("changed context must trigger Rails deprecated multiple-keyword guard")
	}
	if !v1FilterParentParamsChanged(filter, v1FilterPayload{ContextSet: true, Context: []string{}}, now) {
		t.Fatal("explicit empty context must trigger Rails deprecated multiple-keyword guard")
	}
	if !v1FilterParentParamsChanged(filter, v1FilterPayload{Irreversible: testBoolPtr(true)}, now) {
		t.Fatal("changed irreversible/action must trigger Rails deprecated multiple-keyword guard")
	}
}

func TestUpdateV1FilterChecksMultipleKeywordsOnlyWhenParentFilterChangesLikeRails(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if v1FilterParentParamsChanged(keyword.CustomFilter, payload, now) {`,
		`if err := s.ensureSingleKeywordFilter(keyword.CustomFilterID); err != nil`,
		`Validation failed: These parameters cannot be changed from this application because they apply to more than one filter keyword. Use a more recent application or the web interface.`,
	} {
		if !functionBodyContains(t, src, "updateV1Filter", want) && !strings.Contains(string(src), want) {
			t.Fatalf("updateV1Filter missing Rails deprecated multiple keyword guard %q", want)
		}
	}
}

func TestV1FiltersRequireAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/filters", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.v1Filters(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestParseFilterPayloadAcceptsRailsNestedFormKeywordsAttributes(t *testing.T) {
	e := echo.New()
	form := url.Values{}
	form.Set("title", "Noise")
	form.Add("context[]", "home")
	form.Add("context[]", "notifications")
	form.Set("filter_action", "hide")
	form.Set("expires_in", "60")
	form.Set("keywords_attributes[1][id]", "7")
	form.Set("keywords_attributes[1][keyword]", "spoiler")
	form.Set("keywords_attributes[1][whole_word]", "false")
	form.Set("keywords_attributes[0][keyword]", "noise")
	form.Set("keywords_attributes[0][_destroy]", "0")
	req := httptest.NewRequest("POST", "/api/v2/filters", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseFilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Title != "Noise" || payload.FilterAction != "hide" || len(payload.Context) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	if !payload.TitleSet || !payload.ContextSet {
		t.Fatalf("presence flags were not tracked: %#v", payload)
	}
	if payload.ExpiresIn == nil || *payload.ExpiresIn != 60 {
		t.Fatalf("ExpiresIn = %#v", payload.ExpiresIn)
	}
	if len(payload.KeywordsAttributes) != 2 {
		t.Fatalf("KeywordsAttributes = %#v", payload.KeywordsAttributes)
	}
	first := payload.KeywordsAttributes[0]
	if first.Keyword != "noise" || !first.KeywordSet || first.Destroy {
		t.Fatalf("first keyword attr = %#v", first)
	}
	second := payload.KeywordsAttributes[1]
	if second.ID != "7" || second.Keyword != "spoiler" || !second.KeywordSet || second.WholeWord == nil || *second.WholeWord {
		t.Fatalf("second keyword attr = %#v", second)
	}
}

func TestParseFilterPayloadIgnoresScalarContextLikeRailsStrongParams(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v2/filters", strings.NewReader("title=Noise&context=home"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err := parseFilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ContextSet || len(payload.Context) != 0 {
		t.Fatalf("scalar form context = set %v values %#v, want ignored", payload.ContextSet, payload.Context)
	}

	req = httptest.NewRequest("POST", "/api/v2/filters", strings.NewReader(`{"title":"Noise","context":"home"}`))
	req.Header.Set("Content-Type", "application/json")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err = parseFilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ContextSet || len(payload.Context) != 0 {
		t.Fatalf("scalar json context = set %v values %#v, want ignored", payload.ContextSet, payload.Context)
	}
}

func TestParseFilterPayloadTracksExplicitBlankTitleAndContext(t *testing.T) {
	req := httptest.NewRequest("PUT", "/api/v2/filters/1", strings.NewReader(`{"title":"","context":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseFilterPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.TitleSet || payload.Title != "" {
		t.Fatalf("title presence = %v title = %q", payload.TitleSet, payload.Title)
	}
	if !payload.ContextSet || len(payload.Context) != 0 {
		t.Fatalf("context presence = %v context = %#v", payload.ContextSet, payload.Context)
	}
}

func TestFilterActionValueRejectsUnknownActions(t *testing.T) {
	if action, ok := filterActionValue("hide"); !ok || action != 1 {
		t.Fatalf("hide action = %d, %v", action, ok)
	}
	if action, ok := filterActionValue("warn"); !ok || action != 0 {
		t.Fatalf("warn action = %d, %v", action, ok)
	}
	if _, ok := filterActionValue("delete"); ok {
		t.Fatal("unknown filter action should be invalid")
	}
}

func TestFilterHandlersValidateActionEnumLikeRails(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"createFilter": {
			`action, ok := filterActionValue(firstNonEmpty(payload.FilterAction, "warn"))`,
			`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter action is invalid")`,
		},
		"updateFilter": {
			`action, ok := filterActionValue(payload.FilterAction)`,
			`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter action is invalid")`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
}

func TestUpdateFilterRejectsExplicitBlankTitleAndContextLikeRails(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if payload.TitleSet {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Title can't be blank")`,
		`if payload.ContextSet {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter context is invalid")`,
	} {
		if !functionBodyContains(t, src, "updateFilter", want) {
			t.Fatalf("updateFilter must validate explicit blank params like Rails; missing %q", want)
		}
	}
}

func TestParseFilterKeywordPayloadTracksExplicitBlankKeyword(t *testing.T) {
	req := httptest.NewRequest("PUT", "/api/v2/filters/keywords/1", strings.NewReader(`{"keyword":"","whole_word":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseFilterKeywordPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.KeywordSet || payload.Keyword != "" {
		t.Fatalf("keyword presence = %v keyword = %q", payload.KeywordSet, payload.Keyword)
	}
	if payload.WholeWord == nil || *payload.WholeWord {
		t.Fatalf("whole_word = %#v", payload.WholeWord)
	}
}

func TestParseFilterKeywordPayloadUsesRailsBooleanSemantics(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		json bool
		want bool
	}{
		{name: "json off", body: `{"whole_word":"off"}`, json: true, want: false},
		{name: "json no", body: `{"whole_word":"no"}`, json: true, want: true},
		{name: "form on", body: `whole_word=on`, want: true},
		{name: "form f", body: `whole_word=f`, want: false},
	} {
		req := httptest.NewRequest("PUT", "/api/v2/filters/keywords/1", strings.NewReader(tt.body))
		if tt.json {
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())
		payload, err := parseFilterKeywordPayload(c)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if payload.WholeWord == nil || *payload.WholeWord != tt.want {
			t.Fatalf("%s: whole_word = %#v, want %v", tt.name, payload.WholeWord, tt.want)
		}
	}

	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "parseFilterKeywordPayload", `wholeWord := railsBool(value, false)`) {
		t.Fatal("parseFilterKeywordPayload must use Rails boolean casting for JSON whole_word")
	}
}

func TestUpdateFilterKeywordRejectsExplicitBlankKeywordLikeRails(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if payload.KeywordSet && strings.TrimSpace(payload.Keyword) == "" {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Keyword can't be blank")`,
		`if payload.KeywordSet {`,
		`updates["keyword"] = payload.Keyword`,
	} {
		if !functionBodyContains(t, src, "updateFilterKeyword", want) {
			t.Fatalf("updateFilterKeyword missing %q", want)
		}
	}
}

func TestBulkFilterKeywordUpdateRejectsExplicitBlankKeywordLikeRails(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if attr.KeywordSet && strings.TrimSpace(attr.Keyword) == "" {`,
		`return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Keyword can't be blank"}`,
		`if attr.KeywordSet {`,
		`updates["keyword"] = attr.Keyword`,
	} {
		if !functionBodyContains(t, src, "applyFilterKeywordAttributes", want) {
			t.Fatalf("applyFilterKeywordAttributes missing %q", want)
		}
	}
}

func TestValidFilterContextsRejectsUnknownContext(t *testing.T) {
	if !validFilterContexts([]string{"home", "notifications", "public", "thread", "account"}) {
		t.Fatal("expected known contexts to be valid")
	}
	if validFilterContexts([]string{"home", "hashtag"}) {
		t.Fatal("expected unknown context to be invalid")
	}
}

func TestFilterStatusCreationAppliesStatusVisibilityGuard(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	want := `s.findVisibleStatusForAccount(account, strconv.FormatInt(statusID, 10))`
	if !strings.Contains(string(src), want) {
		t.Fatalf("filter status visibility guard missing %q", want)
	}
}

func TestFilterStatusCreationRejectsDuplicateLikeRailsValidation(t *testing.T) {
	src, err := os.ReadFile("filters.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Model(&models.CustomFilterStatus{}).Where("custom_filter_id = ? AND status_id = ?", filter.ID, statusID).Count(&existing)`,
		`if existing > 0 {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Status has already been taken")`,
		`s.db.Create(&filterStatus)`,
	} {
		if !functionBodyContains(t, src, "createFilterStatus", want) {
			t.Fatalf("createFilterStatus must match Rails uniqueness validation; missing %q", want)
		}
	}
	if functionBodyContains(t, src, "createFilterStatus", "OnConflict") || functionBodyContains(t, src, "createFilterStatus", "DoNothing") {
		t.Fatal("createFilterStatus must not silently return an existing status filter on duplicates")
	}
}
