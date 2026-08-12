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
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func TestTermsOfServiceSerializerMatchesMastodon44Shape(t *testing.T) {
	effective := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	successorDate := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	terms := models.TermsOfService{
		Text:          "# Terms for %{domain}\n\n<script>alert(1)</script>",
		PublishedAt:   sql.NullTime{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		EffectiveDate: sql.NullTime{Time: effective, Valid: true},
	}
	successor := &models.TermsOfService{EffectiveDate: sql.NullTime{Time: successorDate, Valid: true}}
	out := serializer.TermsOfServiceFromModel(config.Config{LocalDomain: "social.example"}, terms, successor, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if out.EffectiveDate != "2026-09-01" || !out.Effective || out.SucceededBy == nil || *out.SucceededBy != "2026-10-01" {
		t.Fatalf("terms metadata = %#v", out)
	}
	if !strings.Contains(out.Content, "social.example") || strings.Contains(out.Content, "<script>") {
		t.Fatalf("terms markdown was not formatted and escaped safely: %q", out.Content)
	}
}

func TestTermsOfServiceInterstitialOnlyAllowsTermsRoute(t *testing.T) {
	user := &models.User{RequireTOSInterstitial: true}
	for _, path := range []string{"/", "/web/timelines/home", "/settings/profile"} {
		if !termsOfServiceInterstitialRequired(path, user) {
			t.Fatalf("%s bypassed the terms interstitial", path)
		}
	}
	for _, path := range []string{"/terms-of-service", "/terms-of-service/2026-09-01"} {
		if termsOfServiceInterstitialRequired(path, user) {
			t.Fatalf("%s recursively triggered the terms interstitial", path)
		}
	}
	if termsOfServiceInterstitialRequired("/", nil) {
		t.Fatal("anonymous request triggered terms interstitial")
	}
}

func TestMastodon44TermsRoutesAndInitialStateAreRegistered(t *testing.T) {
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`e.GET("/api/v1/instance/terms_of_service", s.instanceTermsOfService)`,
		`e.GET("/api/v1/instance/terms_of_service/:date", s.instanceTermsOfServiceVersion)`,
		`e.GET("/terms-of-service", s.termsOfServicePage)`,
		`e.GET("/admin/terms_of_service", s.adminTermsOfServicePage)`,
		`e.GET("/admin/terms_of_service/generate", s.adminTermsOfServiceGeneratePage)`,
		`e.POST("/admin/terms_of_service/generate", s.generateAdminTermsOfService)`,
		`e.GET("/admin/terms_of_service/draft", s.adminTermsOfServiceDraftPage)`,
		`e.PUT("/admin/terms_of_service/draft", s.updateAdminTermsOfServiceDraft)`,
		`e.GET("/admin/terms_of_service/history", s.adminTermsOfServiceHistoryPage)`,
		`e.GET("/admin/terms_of_service/:id/preview", s.adminTermsOfServicePreviewPage)`,
		`e.POST("/admin/terms_of_service/:id/test", s.testAdminTermsOfServiceDistribution)`,
		`e.POST("/admin/terms_of_service/:id/distribution", s.distributeAdminTermsOfService)`,
	} {
		if !strings.Contains(string(serverSource), route) {
			t.Fatalf("Mastodon 4.4 terms route missing: %s", route)
		}
	}
	state := serializer.InitialStateFromConfigWithOptions(config.Config{LocalDomain: "social.example"}, nil, "", serializer.InitialStateOptions{TermsOfServiceEnabled: true})
	if state.Meta["terms_of_service_enabled"] != true {
		t.Fatalf("terms_of_service_enabled = %#v", state.Meta["terms_of_service_enabled"])
	}
}

func TestAdminTermsOfServiceGeneratorMatchesMastodon44Contract(t *testing.T) {
	values := url.Values{}
	for key, value := range map[string]string{
		"admin_email":         "legal@example.test",
		"arbitration_address": "1 Example Street",
		"arbitration_website": "N/A",
		"choice_of_law":       "Example State",
		"dmca_address":        "2 Example Street",
		"dmca_email":          "dmca@example.test",
		"domain":              "social.example.test",
		"jurisdiction":        "Example Country",
		"min_age":             "16",
	} {
		values.Set("terms_of_service_generator["+key+"]", value)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/terms_of_service/generate", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	form, err := parseAdminTermsOfServiceGeneratorForm(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAdminTermsOfServiceGeneratorForm(form); err != nil {
		t.Fatalf("valid generator form: %v", err)
	}
	generated := renderAdminTermsOfServiceTemplate(form)
	for _, want := range []string{
		"located at social.example.test",
		"at least 16 years old",
		"Address: 2 Example Street",
		"Email: dmca@example.test",
		"sent to legal@example.test",
		"laws of Example State",
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated terms missing %q", want)
		}
	}
	if strings.Contains(generated, "%{") {
		t.Fatalf("generated terms contain an unresolved template variable: %s", generated)
	}

	form.DMCAEmail = ""
	if err := validateAdminTermsOfServiceGeneratorForm(form); err == nil {
		t.Fatal("generator accepted a blank required variable")
	}
	page := adminTermsOfServiceGeneratorHTML(adminTermsOfServiceGeneratorForm{Domain: `<social.example>`, AdminEmail: `legal+admin@example.test`}, "", "", "en", "system")
	for _, want := range []string{
		`action="/admin/terms_of_service/generate"`,
		`name="terms_of_service_generator[domain]"`,
		`name="terms_of_service_generator[arbitration_website]"`,
		`value="&lt;social.example&gt;"`,
		`The generated terms of service will not be published automatically.`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("generator page missing %q: %s", want, page)
		}
	}
}

func TestAdminTermsOfServiceGeneratorRejectsInvalidNestedRoot(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/terms_of_service/generate", strings.NewReader("terms_of_service_generator=invalid"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := parseAdminTermsOfServiceGeneratorForm(c); !errors.Is(err, errAdminTermsGeneratorParamsMissing) {
		t.Fatalf("invalid nested root error = %v", err)
	}
}

func TestAdminTermsOfServiceFormSeparatesDraftAndPublishValidation(t *testing.T) {
	values := url.Values{}
	values.Set("terms_of_service[text]", "# Updated terms")
	values.Set("terms_of_service[changelog]", "Summary")
	values.Set("terms_of_service[effective_date]", "2026-09-01")
	req := httptest.NewRequest(http.MethodPut, "/admin/terms_of_service/draft", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	form, err := parseAdminTermsOfServiceForm(c)
	if err != nil {
		t.Fatal(err)
	}
	if form.Text != "# Updated terms" || form.Changelog != "Summary" || !form.EffectiveDate.Valid || form.EffectiveDate.Time.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("parsed terms form = %#v", form)
	}
	if err := validateAdminTermsOfServiceForm(form, true); err != nil {
		t.Fatalf("valid publish form: %v", err)
	}
	form.Changelog = ""
	if err := validateAdminTermsOfServiceForm(form, false); err != nil {
		t.Fatalf("draft unexpectedly requires changelog: %v", err)
	}
	if !errors.Is(validateAdminTermsOfServiceForm(form, true), errAdminTermsChangelogBlank) {
		t.Fatal("publish accepted an empty changelog")
	}
}

func TestTermsOfServiceNotificationCutoffUsesCalendarYear(t *testing.T) {
	published := time.Date(2024, time.February, 29, 15, 4, 5, 6, time.UTC)
	terms := models.TermsOfService{PublishedAt: sql.NullTime{Time: published, Valid: true}}
	want := time.Date(2023, time.February, 28, 15, 4, 5, 6, time.UTC)
	if got := termsOfServiceNotificationCutoff(terms); !got.Equal(want) {
		t.Fatalf("notification cutoff = %s, want %s", got, want)
	}
}

func TestTermsOfServiceChangedMailUsesBulkEligibleLocalizedPayload(t *testing.T) {
	terms := models.TermsOfService{
		ID:            7,
		Changelog:     "- 変更点",
		PublishedAt:   sql.NullTime{Time: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), Valid: true},
		EffectiveDate: sql.NullTime{Time: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	}
	user := models.User{ID: 9, Email: "alice@example.test", Locale: sql.NullString{String: "ja", Valid: true}}
	message := termsOfServiceChangedMailMessage(config.Config{LocalDomain: "social.example", Scheme: "https"}, user, terms)
	if message.To != user.Email || !strings.Contains(message.Subject, "利用規約") {
		t.Fatalf("terms mail envelope = %#v", message)
	}
	for _, want := range []string{"social.example", "/terms-of-service/2026-09-01", "2026年09月01日", "- 変更点"} {
		if !strings.Contains(message.Body, want) {
			t.Fatalf("terms mail body missing %q: %s", want, message.Body)
		}
	}
	message.Bulk = true
	if _, err := newAsynqMailerDeliveryTask(user.ID, "bulk_terms_of_service", message, "mailers"); err != nil {
		t.Fatalf("terms mail was rejected by bulk eligibility contract: %v", err)
	}
}

func TestTermsOfServiceChangedMailLinksLegacyTermsWithoutInventingAVersionDate(t *testing.T) {
	terms := models.TermsOfService{
		ID:          7,
		Changelog:   "Legacy update",
		PublishedAt: sql.NullTime{Time: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), Valid: true},
	}
	user := models.User{ID: 9, Email: "alice@example.test"}
	message := termsOfServiceChangedMailMessage(config.Config{LocalDomain: "social.example", Scheme: "https"}, user, terms)
	if !strings.Contains(message.Body, "https://social.example/terms-of-service\n") {
		t.Fatalf("legacy terms mail does not link the current terms page: %s", message.Body)
	}
	if strings.Contains(message.Body, "https://social.example/terms-of-service/2026-") {
		t.Fatalf("legacy terms mail invented a version date: %s", message.Body)
	}
}

func TestAdminTermsOfServiceHTMLIsLocalizedAndEscapesDraftInput(t *testing.T) {
	form := adminTermsOfServiceForm{Text: `<script>alert(1)</script>`, EffectiveDateText: "2026-09-01"}
	draft := adminTermsOfServiceDraftHTML(form, "", "", "ja", "system")
	for _, want := range []string{"下書きを保存", "公開", `name="terms_of_service[effective_date]"`, `&lt;script&gt;alert(1)&lt;/script&gt;`} {
		if !strings.Contains(draft, want) {
			t.Fatalf("terms draft missing %q: %s", want, draft)
		}
	}
	if strings.Contains(draft, `<script>alert(1)</script>`) {
		t.Fatal("terms draft rendered unescaped input")
	}
}

func TestAdminTermsOfServiceChangelogUsesEscapedMarkdown(t *testing.T) {
	terms := models.TermsOfService{
		ID:          7,
		Text:        "# Terms",
		Changelog:   "- Safer defaults\n- Better tools\n\n<script>alert(1)</script>",
		PublishedAt: sql.NullTime{Time: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), Valid: true},
	}
	page := adminTermsOfServiceIndexHTML(&Server{cfg: config.Config{LocalDomain: "social.example"}}, &terms, "", "", "en", "system")
	for _, want := range []string{"<ul>", "<li>Safer defaults</li>", "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(page, want) {
			t.Fatalf("terms page missing Markdown output %q: %s", want, page)
		}
	}
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatal("terms changelog rendered unsafe HTML")
	}
}

func TestTermsOfServiceDistributionWorkerIsRegistered(t *testing.T) {
	source, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `mux.HandleFunc(asynqTaskDistributeTermsOfService, s.handleAsynqDistributeTermsOfService)`) {
		t.Fatal("terms of service distribution worker is not registered")
	}
}
