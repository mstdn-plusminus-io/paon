package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
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

func TestAdminWebhooksRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/webhooks?page=2", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/webhooks?page=2")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminWebhookFormAcceptsRailsNestedFields(t *testing.T) {
	form := url.Values{}
	form.Set("webhook[url]", " https://hooks.example/mastodon ")
	form.Add("webhook[events][]", " account.created ")
	form.Add("webhook[events][]", "")
	form.Add("webhook[events][]", "report.updated")
	form.Set("webhook[template]", " { \"content\": \"hi\" } ")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/webhooks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, err := parseAdminWebhookForm(c)
	if err != nil {
		t.Fatal(err)
	}
	want := adminWebhookForm{URL: " https://hooks.example/mastodon ", Events: []string{"account.created", "report.updated"}, Template: ` { "content": "hi" } `}
	if got.URL != want.URL || got.Template != want.Template || strings.Join(got.Events, ",") != strings.Join(want.Events, ",") {
		t.Fatalf("form = %#v, want %#v", got, want)
	}
}

func TestAdminWebhookEventPermission(t *testing.T) {
	cases := map[string]int64{
		"account.created": rolePermissionManageUsers,
		"report.updated":  rolePermissionManageReports,
		"status.created":  rolePermissionViewDevops,
		"unknown":         0,
	}
	for event, want := range cases {
		if got := adminWebhookEventPermission(event); got != want {
			t.Fatalf("%s permission = %d, want %d", event, got, want)
		}
	}
}

func TestNewAdminWebhookSecret(t *testing.T) {
	secret, err := newAdminWebhookSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 40 {
		t.Fatalf("secret length = %d, want 40", len(secret))
	}
}

func TestAdminWebhooksHTMLIncludesRailsFields(t *testing.T) {
	html := adminWebhooksIndexHTML([]models.Webhook{{ID: 2, URL: "https://hooks.example/mastodon", Enabled: true, Events: models.StringArray{"account.created"}}}, "saved", "", "1")
	for _, want := range []string{
		"Webhooks",
		`href="/admin/webhooks/new"`,
		`href="/admin/webhooks/2"`,
		"https://hooks.example/mastodon",
		"Active",
		"account.created",
		`href="/admin/webhooks/2/edit"`,
		`class="applications-list"`,
		`data-method="delete"`,
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("webhooks html missing %q: %s", want, html)
		}
	}
}

func TestAdminWebhooksHTMLRendersRailsPaginationLinks(t *testing.T) {
	webhooks := make([]models.Webhook, adminRailsDefaultPageSize)
	for i := range webhooks {
		webhooks[i] = models.Webhook{ID: int64(i + 1), URL: "https://hooks.example/webhook", Enabled: true}
	}
	html := adminWebhooksIndexHTML(webhooks, "", "", "2")
	for _, want := range []string{
		`href="/admin/webhooks?page=1"`,
		`href="/admin/webhooks?page=3"`,
		"Prev",
		"Next",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("webhooks html missing pagination %q: %s", want, html)
		}
	}
}

func TestAdminWebhookModelsUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_webhooks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"adminWebhooksPage", "s.adminWebhookModels(c)"},
		{"adminWebhooksPage", "adminWebhooksIndexHTML(webhooks, c.QueryParam(\"notice\"), c.QueryParam(\"error\"), adminTrendsPageValue(c), s.webLocale(c, user))"},
		{"adminWebhookModels", "Offset(adminRailsPageOffset(c))"},
		{"adminWebhookModels", "Limit(adminRailsDefaultPageSize)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestAdminWebhookRedirectErrorsUseLocaleKeys(t *testing.T) {
	goSrc, err := os.ReadFile("admin_webhooks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"createAdminWebhook", `locale := s.webLocale(c, user)`},
		{"createAdminWebhook", `adminWebhookMessage(locale, "errors.invalid", "Webhook is invalid")`},
		{"createAdminWebhook", `adminWebhookErrorText(locale, err)`},
		{"createAdminWebhook", `adminWebhookMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")`},
		{"updateAdminWebhook", `locale := s.webLocale(c, user)`},
		{"updateAdminWebhook", `adminWebhookErrorText(locale, err)`},
		{"updateAdminWebhook", `adminWebhookMessage(locale, "errors.invalid", "Webhook is invalid")`},
		{"destroyAdminWebhook", `locale := s.webLocale(c, user)`},
		{"destroyAdminWebhook", `adminWebhookErrorText(locale, err)`},
		{"rotateAdminWebhookSecret", `adminWebhookMessage(locale, "secret_rotated_msg", "Webhook secret rotated")`},
	} {
		if !functionBodyContains(t, goSrc, check.fn, check.want) {
			t.Fatalf("%s missing localized redirect helper %q", check.fn, check.want)
		}
	}
	for _, check := range []struct {
		fn        string
		forbidden string
	}{
		{"createAdminWebhook", `QueryEscape("Webhook is invalid")`},
		{"createAdminWebhook", `QueryEscape(err.Error())`},
		{"createAdminWebhook", `QueryEscape("DATABASE_URL is not set")`},
		{"updateAdminWebhook", `QueryEscape("Webhook is invalid")`},
		{"updateAdminWebhook", `QueryEscape(err.Error())`},
		{"destroyAdminWebhook", `QueryEscape(err.Error())`},
	} {
		if functionBodyContains(t, goSrc, check.fn, check.forbidden) {
			t.Fatalf("%s still contains display literal %q", check.fn, check.forbidden)
		}
	}
}

func TestAdminWebhookMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"errors.invalid", "Webhook is invalid", "Webhook is invalid", "不正"},
		{"errors.url_blank", "Webhook URL can't be blank", "can't be blank", "入力"},
		{"errors.url_invalid", "Webhook URL is invalid", "is invalid", "不正"},
		{"errors.events_blank", "Webhook events can't be blank", "can't be blank", "選択"},
		{"errors.events_invalid", "Webhook events are invalid", "are invalid", "不正"},
		{"errors.event_permissions_invalid", "Webhook event permissions are invalid", "are invalid", "権限"},
		{"errors.template_invalid", "Webhook template is invalid", "is invalid", "テンプレート"},
		{"secret_rotated_msg", "Webhook secret rotated", "Webhook secret rotated", "ローテート"},
	} {
		got := adminWebhookMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminWebhookMessage(%q) = %q", check.key, got)
		}
	}
	if got := adminWebhookErrorText("ja", errAdminSetting("Webhook URL is invalid")); strings.Contains(got, "is invalid") || !strings.Contains(got, "不正") {
		t.Fatalf("adminWebhookErrorText invalid = %q", got)
	}
}

func TestAdminWebhookShowHTMLIncludesActions(t *testing.T) {
	html := adminWebhookShowHTML(models.Webhook{ID: 3, URL: "https://hooks.example/mastodon", Enabled: false, Events: models.StringArray{"report.created"}, Secret: "secret-value"}, []adminWebhookRetryRow{{
		Attempts:  2,
		CreatedAt: time.Date(2026, 6, 19, 10, 11, 12, 0, time.UTC),
		Event:     "report.created",
		Body:      `{"event":"report.created","object":{"id":"42"}}`,
	}}, []adminWebhookDeliveryHistoryRow{{
		DeliveredAt: time.Date(2026, 6, 19, 10, 12, 13, 0, time.UTC),
		Status:      "failure",
		Event:       "report.updated",
		HTTPStatus:  http.StatusInternalServerError,
		Error:       "webhook delivery failed with status 500",
		Body:        `{"event":"report.updated","object":{"id":"42"}}`,
	}}, "", "bad")
	for _, want := range []string{
		"https://hooks.example/mastodon",
		`href="/admin/webhooks/3/edit"`,
		`class="table horizontal-table"`,
		`class="negative-hint"`,
		`href="/admin/webhooks/3/enable"`,
		`data-method="post"`,
		"report.created",
		"1 enabled event",
		"secret-value",
		`href="/admin/webhooks/3/secret/rotate"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("webhook show html missing %q: %s", want, html)
		}
	}
	for _, forbidden := range []string{"Delivery retries", "Delivery history", "webhook delivery failed with status 500"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("webhook show html contains non-Rails section %q: %s", forbidden, html)
		}
	}
}

func TestAdminWebhookShowHTMLMatchesRailsShowWithoutWorkerDiagnostics(t *testing.T) {
	html := adminWebhookShowHTML(models.Webhook{ID: 3, URL: "https://hooks.example/mastodon", Enabled: true, Events: models.StringArray{"report.created"}, Secret: "secret-value"}, nil, nil, "", "")
	for _, want := range []string{`class="positive-hint"`, `href="/admin/webhooks/3/disable"`, `data-method="post"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("webhook show html missing %q: %s", want, html)
		}
	}
	for _, forbidden := range []string{"No pending failed deliveries.", "No delivery history has been recorded yet."} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("webhook show html contains non-Rails diagnostics %q: %s", forbidden, html)
		}
	}
}

func TestAdminWebhookRetryRowsFromMembersFiltersWebhookAndExtractsEvent(t *testing.T) {
	body := json.RawMessage(`{"event":"report.created","object":{"id":"42"}}`)
	wanted, _ := json.Marshal(webhookDeliveryRetryJob{WebhookID: 3, Body: body, Attempts: 2, CreatedAt: time.Date(2026, 6, 19, 10, 11, 12, 0, time.UTC).Unix()})
	other, _ := json.Marshal(webhookDeliveryRetryJob{WebhookID: 4, Body: body, Attempts: 5, CreatedAt: time.Now().Unix()})

	rows := adminWebhookRetryRowsFromMembers(3, []string{"bad-json", string(other), string(wanted)}, 10)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Attempts != 2 || rows[0].Event != "report.created" || rows[0].CreatedAt.Format(time.RFC3339) != "2026-06-19T10:11:12Z" {
		t.Fatalf("row = %#v", rows[0])
	}
}

func TestAdminWebhookDeliveryHistoryRowsFromMembers(t *testing.T) {
	kept, _ := json.Marshal(webhookDeliveryHistoryItem{
		DeliveredAt: time.Date(2026, 6, 19, 10, 12, 13, 0, time.UTC).Unix(),
		Status:      "discarded",
		Event:       "status.updated",
		HTTPStatus:  http.StatusGone,
		Body:        json.RawMessage(`{"event":"status.updated","object":{"id":"42"}}`),
	})
	rows := adminWebhookDeliveryHistoryRowsFromMembers([]string{"bad-json", string(kept)}, 10)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Status != "discarded" || rows[0].Event != "status.updated" || rows[0].HTTPStatus != http.StatusGone || rows[0].DeliveredAt.Format(time.RFC3339) != "2026-06-19T10:12:13Z" {
		t.Fatalf("row = %#v", rows[0])
	}
}

func TestWebhookDeliveryHistoryUsesBoundedRedisList(t *testing.T) {
	src, err := os.ReadFile("webhooks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"deliverWebhook", `s.recordWebhookDeliveryHistory(webhook, body, "success", resp.StatusCode, nil)`},
		{"deliverWebhook", `s.recordWebhookDeliveryHistory(webhook, body, "discarded", resp.StatusCode, nil)`},
		{"deliverWebhook", `s.recordWebhookDeliveryHistory(webhook, body, "failure", resp.StatusCode, err)`},
		{"recordWebhookDeliveryHistory", `"LPUSH", key, string(encoded)`},
		{"recordWebhookDeliveryHistory", `"LTRIM", key, "0", strconv.Itoa(webhookDeliveryHistoryMaxItems-1)`},
		{"recordWebhookDeliveryHistory", `"EXPIRE", key, strconv.FormatInt(int64(webhookDeliveryHistoryTTL/time.Second), 10)`},
		{"webhookDeliveryHistoryRedisKey", `"paon:webhooks:delivery:history:"`},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("webhooks.go:%s missing %q", check.functionName, check.want)
		}
	}
}

func TestAdminWebhookFormHTMLIncludesRailsFields(t *testing.T) {
	html := adminWebhookFormHTML("Edit webhook", "/admin/webhooks/3", "patch", adminWebhookForm{
		URL:      "https://hooks.example/mastodon",
		Events:   []string{"account.created"},
		Template: "payload",
	}, []string{"account.created", "report.created"}, "Save changes", "bad")
	for _, want := range []string{
		"Edit webhook",
		`action="/admin/webhooks/3"`,
		`name="_method" value="patch"`,
		`name="webhook[url]" value="https://hooks.example/mastodon"`,
		`name="webhook[events][]" id="webhook_events_account_created" value="account.created" checked`,
		`name="webhook[events][]" id="webhook_events_report_created" value="report.created"`,
		`name="webhook[template]"`,
		"Save changes",
		"payload",
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("webhook form html missing %q: %s", want, html)
		}
	}
	newHTML := adminWebhookFormHTML("New webhook", "/admin/webhooks", "", adminWebhookForm{}, []string{"account.created"}, "Add endpoint", "", "en")
	if !strings.Contains(newHTML, "Add endpoint") || strings.Contains(newHTML, "Save changes") {
		t.Fatalf("webhook new submit label mismatch: %s", newHTML)
	}
}

func TestAdminWebhookFormUsesTemplateNullString(t *testing.T) {
	webhook := models.Webhook{Template: sql.NullString{String: "body", Valid: true}}
	form := adminWebhookForm{Template: webhook.Template.String}
	if form.Template != "body" {
		t.Fatalf("template = %q", form.Template)
	}
}

func TestWebhookEventBodyUsesRailsShape(t *testing.T) {
	body, err := webhookEventBody("report.created", map[string]any{"id": "42"}, time.Date(2026, 6, 19, 10, 11, 12, 123000000, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "report.created" || got["created_at"] != "2026-06-19T10:11:12.123Z" {
		t.Fatalf("event body = %s", body)
	}
	object, ok := got["object"].(map[string]any)
	if !ok || object["id"] != "42" {
		t.Fatalf("object = %#v", got["object"])
	}

	updatedBody, err := webhookEventBody("report.updated", map[string]any{"id": "42"}, time.Date(2026, 6, 19, 10, 11, 12, 123000000, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedBody), `"event":"report.updated"`) {
		t.Fatalf("updated event body = %s", updatedBody)
	}
	for _, event := range []string{"account.created", "account.approved", "account.updated", "status.created", "status.updated"} {
		body, err := webhookEventBody(event, map[string]any{"id": "42"}, time.Date(2026, 6, 19, 10, 11, 12, 123000000, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"event":"`+event+`"`) {
			t.Fatalf("%s event body = %s", event, body)
		}
	}
}

func TestWebhookSignatureMatchesRailsHMAC(t *testing.T) {
	body := []byte(`{"event":"report.created"}`)
	want := "sha256=0c2f4866e732530c675faf15b7d197599ac27ae1a8ea600bfe594cc6c71cd4de"
	if got := webhookSignature("secret", body); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

func TestWebhookDeliveryResponseBoundaryMatchesRails(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusBadRequest, http.StatusForbidden, http.StatusGone, http.StatusNotFound} {
		if !webhookDeliveryResponseSuccessfulOrUnsalvageable(status) {
			t.Fatalf("status %d should not retry like Rails response_error_unsalvageable?", status)
		}
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusRequestTimeout, http.StatusTooManyRequests} {
		if webhookDeliveryResponseSuccessfulOrUnsalvageable(status) {
			t.Fatalf("status %d should remain retryable", status)
		}
	}
}

func TestFilterEnabledWebhooksForEvent(t *testing.T) {
	webhooks := []models.Webhook{
		{ID: 1, Enabled: true, Events: models.StringArray{"report.created"}},
		{ID: 2, Enabled: false, Events: models.StringArray{"report.created"}},
		{ID: 3, Enabled: true, Events: models.StringArray{"status.created"}},
	}
	got := filterEnabledWebhooksForEvent(webhooks, "report.created")
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("filtered webhooks = %#v", got)
	}
}

func TestDeliverWebhookPostsRailsHeadersAndRenderedBody(t *testing.T) {
	type deliveredWebhook struct {
		body          string
		contentType   string
		userAgent     string
		signature     string
		requestMethod string
	}
	received := make(chan deliveredWebhook, 1)
	previousClient := webhookHTTPClient
	t.Cleanup(func() { webhookHTTPClient = previousClient })
	webhookHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		received <- deliveredWebhook{
			body:          string(body),
			contentType:   r.Header.Get("Content-Type"),
			userAgent:     r.Header.Get("User-Agent"),
			signature:     r.Header.Get("X-Hub-Signature"),
			requestMethod: r.Method,
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})}

	server := &Server{cfg: config.Config{Version: "6.0.2", MastodonVersion: "4.2.27", Scheme: "https", WebDomain: "example.com"}}
	body, err := webhookEventBody("report.created", map[string]any{"id": "42"}, time.Date(2026, 6, 19, 10, 11, 12, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	webhook := models.Webhook{
		URL:      "https://hooks.example/mastodon",
		Secret:   "secret",
		Template: sql.NullString{String: `{"message":"{{event}} {{object.id}}"}`, Valid: true},
	}
	if err := server.deliverWebhook(webhook, body); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-received:
		wantBody := `{"message":"report.created 42"}`
		if got.requestMethod != http.MethodPost || got.body != wantBody {
			t.Fatalf("request = %#v", got)
		}
		if got.contentType != "application/json" {
			t.Fatalf("Content-Type = %q", got.contentType)
		}
		if got.userAgent != "http.rb/5.1.1 (Paon/6.0.2; based Mastodon/4.2.27; +https://example.com/)" {
			t.Fatalf("User-Agent = %q", got.userAgent)
		}
		if got.signature != webhookSignature("secret", []byte(wantBody)) {
			t.Fatalf("X-Hub-Signature = %q", got.signature)
		}
	case <-time.After(time.Second):
		t.Fatal("webhook request was not received")
	}
}
