package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestTranslationLanguagesReturnsEmptyObjectWhenDisabled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/instance/translation_languages", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.translationLanguages(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "{}\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestNormalizeTranslationLanguage(t *testing.T) {
	if got := normalizeTranslationLanguage("pt_br"); got != "pt-BR" {
		t.Fatalf("language = %q", got)
	}
	if got := normalizeTranslationLanguage("EN-us"); got != "en-US" {
		t.Fatalf("language = %q", got)
	}
}

func TestLibreTranslateLanguageMapMatchesRailsShape(t *testing.T) {
	payload := []struct {
		Code    string   `json:"code"`
		Targets []string `json:"targets"`
	}{
		{Code: "en", Targets: []string{"ja", "de", "en"}},
		{Code: "pt_BR", Targets: []string{"en", "ja"}},
	}
	got := libreTranslateLanguageMap(payload)
	if targets := got["en"]; len(targets) != 2 || targets[0] != "de" || targets[1] != "ja" {
		t.Fatalf("en targets = %#v", targets)
	}
	if targets := got["pt-BR"]; len(targets) != 2 || targets[0] != "en" || targets[1] != "ja" {
		t.Fatalf("pt-BR targets = %#v", targets)
	}
	if targets := got["und"]; len(targets) != 3 || targets[0] != "de" || targets[1] != "en" || targets[2] != "ja" {
		t.Fatalf("und targets = %#v", targets)
	}
}

func TestTranslationLanguageCacheMatchesRailsTTL(t *testing.T) {
	if translationLanguageCacheKey != "paon:translation_service:languages" {
		t.Fatalf("translationLanguageCacheKey = %q", translationLanguageCacheKey)
	}
	if translationLanguageCacheTTL != 7*24*time.Hour {
		t.Fatalf("translationLanguageCacheTTL = %s", translationLanguageCacheTTL)
	}
}

func TestTranslationConfiguredMatchesRailsPresentSemantics(t *testing.T) {
	if translationConfigured(config.Config{DeepLAPIKey: " "}) {
		t.Fatal("whitespace-only DeepL API key should not configure translation")
	}
	if translationConfigured(config.Config{LibreTranslateEndpoint: " "}) {
		t.Fatal("whitespace-only LibreTranslate endpoint should not configure translation")
	}
	if !translationConfigured(config.Config{DeepLAPIKey: "deepl-key"}) {
		t.Fatal("DeepL API key should configure translation")
	}
	if !translationConfigured(config.Config{LibreTranslateEndpoint: "https://translate.example"}) {
		t.Fatal("LibreTranslate endpoint should configure translation")
	}
}

func TestTranslationLanguageCacheRoundTrip(t *testing.T) {
	raw, err := marshalTranslationLanguageMap(map[string][]string{"en": []string{"ja", "de"}, "und": []string{"en"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalTranslationLanguageMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["en"]) != 2 || got["en"][0] != "ja" || got["en"][1] != "de" || got["und"][0] != "en" {
		t.Fatalf("languages = %#v", got)
	}
	if _, err := unmarshalTranslationLanguageMap("{"); err == nil {
		t.Fatal("invalid cache JSON was accepted")
	}
}

func TestTranslationLanguageMapStrictUsesRedisCache(t *testing.T) {
	src, err := os.ReadFile("translations.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"translationLanguageMapStrict", `s.cachedTranslationLanguageMap(ctx)`},
		{"translationLanguageMapStrict", `s.cacheTranslationLanguageMap(ctx, languages)`},
		{"cachedTranslationLanguageMap", `"GET", redisConfig(s.cfg).prefix+translationLanguageCacheKey`},
		{"cacheTranslationLanguageMap", `"SETEX", redisConfig(s.cfg).prefix+translationLanguageCacheKey`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestFetchDeepLLanguagesClassifiesServiceErrorsLikeRails(t *testing.T) {
	previous := translationHTTPClient
	t.Cleanup(func() { translationHTTPClient = previous })

	for _, tt := range []struct {
		status int
		want   error
	}{
		{status: http.StatusTooManyRequests, want: errTranslationTooManyRequests},
		{status: 456, want: errTranslationQuotaExceeded},
	} {
		translationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Authorization") != "DeepL-Auth-Key deepl-key" {
				t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
			}
			return &http.Response{StatusCode: tt.status, Body: http.NoBody, Header: make(http.Header)}, nil
		})}

		if _, err := fetchDeepLLanguages(context.Background(), config.Config{DeepLAPIKey: "deepl-key"}, "source"); !errors.Is(err, tt.want) {
			t.Fatalf("status %d err = %v, want %v", tt.status, err, tt.want)
		}
	}
}

func TestDeepLPlanUsesRailsEndpointSelection(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "unset manual config uses free", cfg: config.Config{DeepLAPIKey: "deepl-key"}, want: false},
		{name: "explicit free uses free", cfg: config.Config{DeepLAPIKey: "deepl-key", DeepLPlan: "free", DeepLPlanSet: true}, want: false},
		{name: "explicit blank is not Rails default", cfg: config.Config{DeepLAPIKey: "deepl-key", DeepLPlan: "", DeepLPlanSet: true}, want: true},
		{name: "non-free uses paid", cfg: config.Config{DeepLAPIKey: "deepl-key", DeepLPlan: "pro", DeepLPlanSet: true}, want: true},
	}
	for _, tt := range cases {
		if got := deepLPlanUsesPaidEndpoint(tt.cfg); got != tt.want {
			t.Fatalf("%s: paid endpoint = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTranslateTextsSkipsWhitespaceDeepLKeyForLibreTranslate(t *testing.T) {
	previous := translationHTTPClient
	t.Cleanup(func() { translationHTTPClient = previous })

	translationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "translate.example" || req.URL.Path != "//translate" {
			t.Fatalf("request URL = %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"translatedText":["hola"],"detectedLanguage":[{"language":"en"}]}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	got, err := translateTexts(context.Background(), config.Config{
		DeepLAPIKey:            " ",
		LibreTranslateEndpoint: "https://translate.example/",
	}, []string{"hello"}, "en", "es")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Provider != "LibreTranslate" || got[0].Text != "hola" {
		t.Fatalf("translations = %#v", got)
	}
}

func TestDeepLTranslateIncludesBlankSourceLangForAutoDetectLikeRails(t *testing.T) {
	previous := translationHTTPClient
	t.Cleanup(func() { translationHTTPClient = previous })

	translationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		got := string(body)
		want := "text=Guten+Tag&source_lang&target_lang=en&tag_handling=html"
		if got != want {
			t.Fatalf("DeepL body = %q, want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"translations":[{"text":"Good morning","detected_source_language":"DE"}]}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	got, err := translateTextsWithDeepL(context.Background(), config.Config{DeepLAPIKey: "deepl-key", DeepLPlan: "advanced"}, []string{"Guten Tag"}, "und", "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DetectedSourceLanguage != "de" || got[0].Provider != "DeepL.com" {
		t.Fatalf("translations = %#v", got)
	}
}

func TestTranslationHTTPDoPreservesLibreTranslateAllowLocal(t *testing.T) {
	previousTranslationClient := translationHTTPClient
	previousActivityClient := activityHTTPClient
	t.Cleanup(func() {
		translationHTTPClient = previousTranslationClient
		activityHTTPClient = previousActivityClient
	})
	translationHTTPClient = nil

	requests := 0
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     http.Header{},
			Request:    req,
		}, nil
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:5000/languages", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := translationHTTPDo(req, false); !errors.Is(err, errUnexpectedTranslationResponse) {
		t.Fatalf("non-allow-local translation request err = %v, want %v", err, errUnexpectedTranslationResponse)
	}
	if requests != 0 {
		t.Fatalf("non-allow-local request reached transport %d times", requests)
	}
	resp, err := translationHTTPDo(req, true)
	if err != nil {
		t.Fatalf("allow-local request err = %v", err)
	}
	_ = resp.Body.Close()
	if requests != 1 {
		t.Fatalf("allow-local request count = %d, want 1", requests)
	}
}

func TestTranslationHTTPDoAllowLocalUsesGuardedActivityTransport(t *testing.T) {
	previousTranslationClient := translationHTTPClient
	previousExceptions := activityPrivateAddressExceptions
	previousProxyConfigured := activityHTTPProxyConfigured
	t.Cleanup(func() {
		translationHTTPClient = previousTranslationClient
		activityPrivateAddressExceptions = previousExceptions
		activityHTTPProxyConfigured = previousProxyConfigured
	})
	activityPrivateAddressExceptions = nil
	activityHTTPProxyConfigured = false

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := activityHTTPClientFromConfig(config.Config{})
	translationHTTPClient = client
	t.Cleanup(client.CloseIdleConnections)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/languages", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := translationHTTPDo(req, false); !errors.Is(err, errUnexpectedTranslationResponse) {
		t.Fatalf("non-allow-local translation request err = %v, want %v", err, errUnexpectedTranslationResponse)
	}
	if requests != 0 {
		t.Fatalf("non-allow-local request reached local server %d times", requests)
	}

	resp, err := translationHTTPDo(req, true)
	if err != nil {
		t.Fatalf("allow-local request through activity transport err = %v", err)
	}
	_ = resp.Body.Close()
	if requests != 1 {
		t.Fatalf("allow-local request count = %d, want 1", requests)
	}
}

func TestTranslationHTTPDoAllowLocalFollowsSafeLocalRedirect(t *testing.T) {
	previousTranslationClient := translationHTTPClient
	previousExceptions := activityPrivateAddressExceptions
	previousProxyConfigured := activityHTTPProxyConfigured
	t.Cleanup(func() {
		translationHTTPClient = previousTranslationClient
		activityPrivateAddressExceptions = previousExceptions
		activityHTTPProxyConfigured = previousProxyConfigured
	})
	activityPrivateAddressExceptions = nil
	activityHTTPProxyConfigured = false

	destinationRequests := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationRequests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(destination.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, destination.URL+"/languages", http.StatusFound)
	}))
	t.Cleanup(source.Close)
	client := activityHTTPClientFromConfig(config.Config{})
	translationHTTPClient = client
	t.Cleanup(client.CloseIdleConnections)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.URL+"/languages", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := translationHTTPDo(req, true)
	if err != nil {
		t.Fatalf("allow-local redirected request err = %v", err)
	}
	_ = resp.Body.Close()
	if destinationRequests != 1 {
		t.Fatalf("local redirect destination request count = %d, want 1", destinationRequests)
	}
}

func TestStatusTranslationPollOptionCacheKeysUsePollOptionIDsLikeRails(t *testing.T) {
	status := models.Status{
		ID:          10,
		Text:        "hello",
		SpoilerText: "cw",
		Poll:        &models.Poll{ID: 20, Options: models.StringArray{"yes", "no"}},
	}
	sources := statusTranslationSources(config.Config{WebDomain: "example.test", LocalDomain: "example.test", Scheme: "https"}, status)
	var pollSources []statusTranslationSource
	for _, source := range sources {
		if source.kind == "poll_option" {
			pollSources = append(pollSources, source)
		}
	}
	if len(pollSources) != 2 {
		t.Fatalf("poll sources = %#v", pollSources)
	}
	for index, source := range pollSources {
		if source.pollOptionID != index {
			t.Fatalf("poll option source %d = %#v", index, source)
		}
	}
	if statusTranslationSourceCacheKey(99, pollSources[0]) != "Poll::Option-0" || statusTranslationSourceCacheKey(100, pollSources[1]) != "Poll::Option-1" {
		t.Fatalf("poll option cache keys = %q %q", statusTranslationSourceCacheKey(99, pollSources[0]), statusTranslationSourceCacheKey(100, pollSources[1]))
	}
}

func TestTranslateStatusForUserUsesStatusTranslationCache(t *testing.T) {
	src, err := os.ReadFile("translations.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		`cacheKey := statusTranslationCacheKey(sourceLanguage, targetLanguage, sources)`,
		`if translation, ok := s.cachedStatusTranslation(ctx, cacheKey); ok {
		return translation, nil
	}`,
		`s.cacheStatusTranslation(ctx, cacheKey, translation)`,
	}
	for _, want := range checks {
		if !functionBodyContains(t, src, "translateStatusForUser", want) {
			t.Fatalf("translateStatusForUser missing %q", want)
		}
	}
}

func TestTranslationTargetLanguageUsesContentLocaleShape(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/statuses/1/translate?lang=pt-BR", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := translationTargetLanguage(c, models.User{Locale: sql.NullString{String: "en-GB", Valid: true}}, config.Config{DefaultLocale: "fr"})
	if got != "pt" {
		t.Fatalf("target language = %q", got)
	}

	req = httptest.NewRequest("POST", "/api/v1/statuses/1/translate", nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	got = translationTargetLanguage(c, models.User{Locale: sql.NullString{String: "en-GB", Valid: true}}, config.Config{DefaultLocale: "fr"})
	if got != "en" {
		t.Fatalf("target language = %q", got)
	}

	req = httptest.NewRequest("POST", "/api/v1/statuses/1/translate?lang=zz", nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	got = translationTargetLanguage(c, models.User{Locale: sql.NullString{String: "en-US", Valid: true}}, config.Config{DefaultLocale: "fr"})
	if got != "fr" {
		t.Fatalf("invalid locale target language = %q", got)
	}

	req = httptest.NewRequest("POST", "/api/v1/statuses/1/translate", nil)
	req.Header.Set("Accept-Language", "zz;q=1, es-MX;q=0.8, ja;q=0.4")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	got = translationTargetLanguage(c, models.User{}, config.Config{DefaultLocale: "fr"})
	if got != "es" {
		t.Fatalf("accept-language target language = %q", got)
	}
}

func TestLibreTranslateResultsKeepsProviderAndDetectedLanguage(t *testing.T) {
	response := libreTranslateResponse{
		TranslatedText: []string{"<p>こんにちは</p>", "注意"},
		DetectedLanguage: []struct {
			Language string `json:"language"`
		}{{Language: "en_US"}},
	}
	got, err := libreTranslateResults(response, "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "<p>こんにちは</p>" || got[0].DetectedSourceLanguage != "en-US" || got[0].Provider != "LibreTranslate" {
		t.Fatalf("results = %#v", got)
	}
	if got[1].DetectedSourceLanguage != "en" {
		t.Fatalf("fallback detected language = %#v", got[1])
	}
}

func TestTranslateTextsRejectsProviderResultCountMismatch(t *testing.T) {
	previous := translationHTTPClient
	t.Cleanup(func() { translationHTTPClient = previous })

	translationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/translate" {
			t.Fatalf("LibreTranslate path = %q", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"translatedText":["hola"]}`)),
			Header:     make(http.Header),
		}, nil
	})}
	_, err := translateTextsWithLibreTranslate(context.Background(), config.Config{LibreTranslateEndpoint: "https://translate.example"}, []string{"hello", "world"}, "en", "es")
	if !errors.Is(err, errUnexpectedTranslationResponse) {
		t.Fatalf("LibreTranslate err = %v, want %v", err, errUnexpectedTranslationResponse)
	}

	translationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v2/translate" {
			t.Fatalf("DeepL path = %q", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"translations":[]}`)),
			Header:     make(http.Header),
		}, nil
	})}
	_, err = translateTextsWithDeepL(context.Background(), config.Config{DeepLAPIKey: "deepl-key"}, []string{"hello"}, "en", "es")
	if !errors.Is(err, errUnexpectedTranslationResponse) {
		t.Fatalf("DeepL err = %v, want %v", err, errUnexpectedTranslationResponse)
	}
}

func TestBuildStatusTranslationIncludesPollAndMedia(t *testing.T) {
	status := models.Status{
		ID:          10,
		Text:        "hello",
		SpoilerText: "cw",
		Language:    sql.NullString{String: "en", Valid: true},
		Poll:        &models.Poll{ID: 20, Options: models.StringArray{"yes", "no"}},
		MediaAttachments: []models.MediaAttachment{
			{ID: 30, Description: sql.NullString{String: "alt", Valid: true}},
		},
	}
	sources := statusTranslationSources(config.Config{WebDomain: "example.test", LocalDomain: "example.test", Scheme: "https"}, status)
	results := []translationResult{
		{Text: "<p>こんにちは</p>", DetectedSourceLanguage: "en", Provider: "LibreTranslate"},
		{Text: "閲覧注意", DetectedSourceLanguage: "en", Provider: "LibreTranslate"},
		{Text: "はい", DetectedSourceLanguage: "en", Provider: "LibreTranslate"},
		{Text: "いいえ", DetectedSourceLanguage: "en", Provider: "LibreTranslate"},
		{Text: "代替テキスト", DetectedSourceLanguage: "en", Provider: "LibreTranslate"},
	}
	got := buildStatusTranslation(status, "ja", sources, results)
	if got.Language != "ja" || got.Content != "<p>こんにちは</p>" || got.SpoilerText != "閲覧注意" {
		t.Fatalf("translation = %#v", got)
	}
	if got.DetectedSourceLanguage == nil || *got.DetectedSourceLanguage != "en" || got.Provider == nil || *got.Provider != "LibreTranslate" {
		t.Fatalf("metadata = %#v %#v", got.DetectedSourceLanguage, got.Provider)
	}
	if got.Poll == nil || got.Poll.ID != "20" || len(got.Poll.Options) != 2 || got.Poll.Options[0].Title != "はい" {
		t.Fatalf("poll = %#v", got.Poll)
	}
	if len(got.MediaAttachments) != 1 || got.MediaAttachments[0].ID != "30" || got.MediaAttachments[0].Description != "代替テキスト" {
		t.Fatalf("media attachments = %#v", got.MediaAttachments)
	}
}

func TestTranslateStatusRequiresAuthentication(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/statuses/1/translate", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	err := s.translateStatus(c)
	apiErr, ok := err.(apiHTTPError)
	if !ok {
		t.Fatalf("err = %#v", err)
	}
	if apiErr.status != http.StatusUnauthorized || apiErr.message != "The access token is invalid" {
		t.Fatalf("apiErr = %#v", apiErr)
	}
}
