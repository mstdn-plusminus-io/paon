package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseWebPushPayloadAcceptsSubscriptionAndData(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/web/push_subscriptions", strings.NewReader(`{
		"subscription":{"endpoint":"https://push.example/1","keys":{"auth":"auth","p256dh":"key"}},
		"data":{"policy":"all","alerts":{"mention":true}}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebPushPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Subscription.Endpoint != "https://push.example/1" || payload.Subscription.Keys.Auth != "auth" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestParseWebPushPayloadRejectsRawJSONFormStringsLikeRailsRequire(t *testing.T) {
	e := echo.New()
	form := url.Values{}
	form.Set("subscription", `{"endpoint":"https://push.example/1","keys":{"auth":"auth","p256dh":"key"}}`)
	form.Set("data", `{"policy":"all"}`)
	req := httptest.NewRequest("POST", "/api/v1/push/subscription", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	payload, err := parseWebPushPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !webPushSubscriptionPayloadMissing(payload) {
		t.Fatalf("raw subscription string must not satisfy Rails params.require(:subscription): %#v", payload.Subscription)
	}
	if !webPushDataPayloadMissing(payload) {
		t.Fatalf("raw data string must not satisfy Rails params.require(:data): %s", string(payload.Data))
	}
}

func TestParseWebPushPayloadRejectsFlatSubscriptionLikeRailsRequire(t *testing.T) {
	e := echo.New()
	form := url.Values{}
	form.Set("endpoint", "https://push.example/1")
	form.Set("auth", "auth")
	form.Set("p256dh", "key")
	form.Set("data[policy]", "all")
	req := httptest.NewRequest("POST", "/api/v1/push/subscription", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebPushPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !webPushSubscriptionPayloadMissing(payload) {
		t.Fatalf("flat subscription keys must not satisfy Rails params.require(:subscription): %#v", payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["policy"] != "all" {
		t.Fatalf("data root should still parse independently, got %#v", decoded)
	}
}

func TestWebPushCreateRequiresSubscriptionLikeRails(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/push/subscription", strings.NewReader(`{"data":{"policy":"all"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebPushPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !webPushSubscriptionPayloadMissing(payload) {
		t.Fatalf("payload should be missing subscription: %#v", payload)
	}

	src, err := os.ReadFile("web_push_subscriptions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"createPushSubscription", "createWebPushSubscription"} {
		if !functionBodyContains(t, src, fn, `return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: subscription")`) {
			t.Fatalf("%s must match Rails params.require(:subscription) 400 response", fn)
		}
	}
}

func TestDefaultWebPushDataMergesPolicyAndAlerts(t *testing.T) {
	data := defaultWebPushData(json.RawMessage(`{"alerts":{"mention":true,"unknown":true}}`))
	if !json.Valid(data) {
		t.Fatalf("invalid json = %s", data)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["policy"] != "all" {
		t.Fatalf("policy = %#v", decoded["policy"])
	}
	alerts, ok := decoded["alerts"].(map[string]any)
	if !ok || alerts["mention"] != true {
		t.Fatalf("alerts = %#v", decoded["alerts"])
	}
	for _, key := range webPushAlertTypeList {
		if _, ok := alerts[key]; !ok {
			t.Fatalf("default alert key %q missing from %#v", key, alerts)
		}
	}
	if alerts["status"] != false || alerts["admin.report"] != false {
		t.Fatalf("default alert values = %#v", alerts)
	}
	if _, ok := alerts["unknown"]; ok {
		t.Fatalf("unknown alert persisted: %#v", alerts)
	}
}

func TestDefaultWebPushDataMatchesRailsWebShape(t *testing.T) {
	data := defaultWebPushData(nil)
	var decoded struct {
		Policy string          `json:"policy"`
		Alerts map[string]bool `json:"alerts"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Policy != "all" {
		t.Fatalf("policy = %q", decoded.Policy)
	}
	if len(decoded.Alerts) != len(webPushAlertTypeList) {
		t.Fatalf("alerts = %#v", decoded.Alerts)
	}
	for _, key := range webPushAlertTypeList {
		if decoded.Alerts[key] {
			t.Fatalf("alert %q should default to false: %#v", key, decoded.Alerts)
		}
	}
}

func TestWebPushDataWithMobileSessionDefaultsAlertsToEnabled(t *testing.T) {
	data := webPushDataWithDefaultAlerts(json.RawMessage(`{"alerts":{"mention":false}}`), true)
	var decoded struct {
		Policy string          `json:"policy"`
		Alerts map[string]bool `json:"alerts"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range webPushAlertTypeList {
		if key == "mention" {
			continue
		}
		if !decoded.Alerts[key] {
			t.Fatalf("mobile/tablet alert %q should default to true: %#v", key, decoded.Alerts)
		}
	}
	if decoded.Alerts["mention"] {
		t.Fatalf("submitted alert override was not preserved: %#v", decoded.Alerts)
	}
}

func TestWebPushAlertsEnabledForUserAgentMatchesRailsMobileTabletDefault(t *testing.T) {
	cases := map[string]bool{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148":     true,
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148":              true,
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/124.0 Mobile Safari/537.36": true,
		"Mozilla/5.0 (Linux; Android 13; Nexus 10) AppleWebKit/537.36 Chrome/124.0 Safari/537.36":       true,
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 Safari/605.1.15":             false,
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/124.0 Safari/537.36":                 false,
		"": false,
	}
	for ua, want := range cases {
		if got := webPushAlertsEnabledForUserAgent(ua); got != want {
			t.Fatalf("webPushAlertsEnabledForUserAgent(%q) = %v, want %v", ua, got, want)
		}
	}
}

func TestNormalizeWebPushDataDefaultsBlankToObject(t *testing.T) {
	data := normalizeWebPushData(nil)
	if string(data) != "{}" {
		t.Fatalf("data = %s", data)
	}
}

func TestWebPushDataPayloadMissingMatchesRailsWebUpdateRequirement(t *testing.T) {
	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(``),
		json.RawMessage(`null`),
		json.RawMessage(` null `),
	} {
		if !webPushDataPayloadMissing(webPushPayload{Data: raw}) {
			t.Fatalf("raw data %q should be treated as missing", string(raw))
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"policy":"all"}`),
		json.RawMessage(`{"alerts":{"mention":true}}`),
	} {
		if webPushDataPayloadMissing(webPushPayload{Data: raw}) {
			t.Fatalf("raw data %q should be accepted", string(raw))
		}
	}
}

func TestWebPushDataKeepsOnlyRailsNotificationAlertTypes(t *testing.T) {
	data := normalizeWebPushData(json.RawMessage(`{"policy":"followed","alerts":{"mention":true,"admin.report":false,"unknown":true},"ignored":true}`))
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["policy"] != "followed" {
		t.Fatalf("policy = %#v", decoded["policy"])
	}
	if _, ok := decoded["ignored"]; ok {
		t.Fatalf("unexpected top-level key persisted: %#v", decoded)
	}
	alerts, ok := decoded["alerts"].(map[string]any)
	if !ok {
		t.Fatalf("alerts = %#v", decoded["alerts"])
	}
	if alerts["mention"] != true || alerts["admin.report"] != false {
		t.Fatalf("alerts = %#v", alerts)
	}
	if _, ok := alerts["unknown"]; ok {
		t.Fatalf("unknown alert persisted: %#v", alerts)
	}
}

func TestWebPushSessionLinkHelpersIgnoreMissingIDs(t *testing.T) {
	s := &Server{}
	if err := s.linkWebPushSubscriptionToSession(nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.unlinkWebPushSubscriptionFromSession(nil, 0); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationActivityTableMatchesRailsPushWorkerActivities(t *testing.T) {
	cases := []struct {
		activityType string
		kind         string
		want         string
	}{
		{"Mention", "mention", "mentions"},
		{"Status", "reblog", "statuses"},
		{"Follow", "follow", "follows"},
		{"FollowRequest", "follow_request", "follow_requests"},
		{"Favourite", "favourite", "favourites"},
		{"Poll", "poll", "polls"},
		{"Report", "admin.report", "reports"},
		{"Account", "admin.sign_up", "accounts"},
		{"", "update", "statuses"},
	}
	for _, tt := range cases {
		if got := notificationActivityTable(tt.activityType, tt.kind); got != tt.want {
			t.Fatalf("notificationActivityTable(%q, %q) = %q, want %q", tt.activityType, tt.kind, got, tt.want)
		}
	}
	if got := notificationActivityTable("", "unknown"); got != "" {
		t.Fatalf("unknown activity table = %q", got)
	}
}

func TestWebPushSubscriptionExpiredMatchesRailsDeletionCases(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusGone, http.StatusUnauthorized} {
		if !webPushSubscriptionExpired(status) {
			t.Fatalf("status %d should expire subscription", status)
		}
	}
	for _, status := range []int{http.StatusOK, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError} {
		if webPushSubscriptionExpired(status) {
			t.Fatalf("status %d should not expire subscription", status)
		}
	}
}

func TestDefaultWebPushDelivererRequiresVAPIDKeys(t *testing.T) {
	_, err := defaultWebPushDeliverer(
		httptest.NewRequest("GET", "/", nil).Context(),
		config.Config{},
		models.WebPushSubscription{},
		[]byte(`{}`),
	)
	if err == nil {
		t.Fatal("expected missing VAPID keys error")
	}
}

func TestWebPushDelivererReceivesVAPIDHeaderInputs(t *testing.T) {
	src, err := os.ReadFile("web_push_delivery.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`webpush.SendNotificationWithContext(ctx, payload`,
		`Endpoint: subscription.Endpoint`,
		`Auth:   subscription.KeyAuth`,
		`P256dh: subscription.KeyP256dh`,
		`Subscriber:      cfg.VapidSubject`,
		`TTL:             webPushTTL`,
		`Urgency:         webPushUrgency`,
		`VAPIDPublicKey:  cfg.VapidPublicKey`,
		`VAPIDPrivateKey: cfg.VapidPrivateKey`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("defaultWebPushDeliverer missing VAPID request fragment %q", want)
		}
	}

	payload := []byte(`{"title":"hello"}`)
	subscription := models.WebPushSubscription{
		ID:        42,
		Endpoint:  "https://push.example/send/42",
		KeyAuth:   "auth-secret",
		KeyP256dh: "client-public-key",
	}
	var capturedCfg config.Config
	var capturedSubscription models.WebPushSubscription
	var capturedPayload []byte
	server := &Server{
		cfg: config.Config{
			VapidSubject:    "mailto:admin@example.test",
			VapidPublicKey:  "public-key",
			VapidPrivateKey: "private-key",
		},
		webPushDeliverer: func(_ context.Context, cfg config.Config, sub models.WebPushSubscription, body []byte) (*http.Response, error) {
			capturedCfg = cfg
			capturedSubscription = sub
			capturedPayload = append([]byte(nil), body...)
			return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
		},
	}
	if err := server.deliverWebPushTargetForWorker(httptest.NewRequest("POST", "/", nil).Context(), subscription, payload); err != nil {
		t.Fatal(err)
	}
	if capturedCfg.VapidSubject != "mailto:admin@example.test" || capturedCfg.VapidPublicKey != "public-key" || capturedCfg.VapidPrivateKey != "private-key" {
		t.Fatalf("captured VAPID config = %#v", capturedCfg)
	}
	if capturedSubscription.Endpoint != subscription.Endpoint || capturedSubscription.KeyAuth != subscription.KeyAuth || capturedSubscription.KeyP256dh != subscription.KeyP256dh {
		t.Fatalf("captured subscription = %#v", capturedSubscription)
	}
	if string(capturedPayload) != string(payload) {
		t.Fatalf("captured payload = %s", string(capturedPayload))
	}
	if webPushTTL != 48*60*60 || string(webPushUrgency) != "normal" {
		t.Fatalf("web push options = ttl:%d urgency:%q", webPushTTL, webPushUrgency)
	}
}

func TestNotificationStreamingCallsWebPushDelivery(t *testing.T) {
	src, err := os.ReadFile("notification_streaming.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "publishNotificationIDWithContext", `s.deliverWebPushNotification(ctx, notifications[0], account)`) {
		t.Fatal("publishNotificationIDWithContext does not call web push delivery")
	}
}

func TestWebPushDeliveryChecksActivityPresenceLikeRailsWorker(t *testing.T) {
	src, err := os.ReadFile("web_push_delivery.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "deliverWebPushNotification", `s.webPushNotificationActivityPresent(ctx, notification)`) {
		t.Fatal("deliverWebPushNotification does not guard on notification activity presence")
	}
	for _, want := range []string{
		`func (s *Server) webPushNotificationActivityPresent(ctx context.Context, notification models.Notification) bool`,
		`notificationActivityTable(notification.ActivityType, notification.ResolvedType())`,
		`Table(table).Where("id = ?", notification.ActivityID).Count(&count)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("web push activity presence check missing %q", want)
		}
	}
}

func TestWebPushDeliveryRetryFallbackMatchesRailsWorker(t *testing.T) {
	src, err := os.ReadFile("web_push_delivery.go")
	if err != nil {
		t.Fatal(err)
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`const (`,
		`webPushDeliveryRetryKey`,
		`webPushDeliveryRetryAttempts = 5`,
		`type webPushDeliveryRetryJob struct`,
		`s.enqueueWebPushDeliveryRetry(target.Subscription.ID, notification.ID)`,
		`func (s *Server) runWebPushDeliveryRetryWorker(ctx context.Context)`,
		`s.processDueWebPushDeliveryRetries(ctx, 25)`,
		`s.claimRedisRetryJobs(ctx, key, limit, now)`,
		`s.performWebPushNotificationDelivery(ctx, job.SubscriptionID, job.NotificationID)`,
		`s.replaceRedisRetryJob(ctx, key, claim, successor, runAt)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("web push retry fallback missing Go fragment %q", want)
		}
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", `workers.Go(ctx, s.runWebPushDeliveryRetryWorker)`) {
		t.Fatal("StartBackgroundWorkers does not start web push delivery retry worker")
	}
}

func TestShowPushSubscriptionRequiresAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/push/subscription", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.showPushSubscription(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "The access token is invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPushSubscriptionEarlyAuthErrorKeepsRailsAuthorizationVary(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/push/subscription", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
	if !strings.Contains(rec.Body.String(), "The access token is invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWebPushSubscriptionUpdateRailsMemberRouteIsRegistered(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `e.PUT("/api/v1/push/subscription", s.updatePushSubscription)`) {
		t.Fatal("Rails PUT update route for api/v1 push subscription is not registered")
	}
	if !strings.Contains(string(src), `e.PATCH("/api/v1/push/subscription", s.updatePushSubscription)`) {
		t.Fatal("Rails PATCH update route for api/v1 push subscription is not registered")
	}
	if !strings.Contains(string(src), `e.PUT("/api/web/push_subscriptions/:id", s.apiWebCSRF(s.updateWebPushSubscription))`) {
		t.Fatal("Rails member update route for api/web push subscriptions is not registered")
	}
	if !strings.Contains(string(src), `e.PUT("/api/web/push_subscriptions/:id/update", s.apiWebCSRF(s.updateWebPushSubscription))`) {
		t.Fatal("legacy compatibility update route for api/web push subscriptions is not registered")
	}
}

func TestWebPushCreateKeepsTokenFallbackWhenCurrentSessionIsUnavailable(t *testing.T) {
	src, err := os.ReadFile("web_push_subscriptions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`} else {
			if err := tx.Where("access_token_id = ?", accessToken.ID).Delete(&models.WebPushSubscription{}).Error; err != nil {`,
		`return s.linkWebPushSubscriptionToSession(tx, accessToken.ID, subscription.ID)`,
	} {
		if !functionBodyContains(t, src, "createWebPushSubscription", want) {
			t.Fatalf("web push create token fallback missing %q", want)
		}
	}
}

func TestCurrentWebPushSessionActivationPrefersRailsSessionCookie(t *testing.T) {
	src, err := os.ReadFile("web_push_subscriptions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.railsSessionIDFromCookie(c)`,
		`s.railsSessionIDFromEncryptedSession(c)`,
		`Where("session_id = ? AND user_id = ?", sessionID, userID)`,
		`Where("access_token_id = ? AND user_id = ?", accessTokenID, userID).Order("updated_at DESC")`,
	} {
		if !functionBodyContains(t, src, "currentWebPushSessionActivation", want) {
			t.Fatalf("currentWebPushSessionActivation missing %q", want)
		}
	}
}

func TestWebPushInitialStateLookupUsesCurrentToken(t *testing.T) {
	src, err := os.ReadFile("web_push_subscriptions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`func (s *Server) webPushSubscriptionForToken(token string, userID int64) *models.WebPushSubscription`,
		`JOIN oauth_access_tokens ON oauth_access_tokens.id = web_push_subscriptions.access_token_id`,
		`oauth_access_tokens.token = ? AND oauth_access_tokens.revoked_at IS NULL AND web_push_subscriptions.user_id = ?`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("web push initial-state lookup missing %q", want)
		}
	}
	serverSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serverSrc), `options.PushSubscription = s.webPushSubscriptionForToken(token, user.ID)`) {
		t.Fatal("webAppOptions does not attach the current token's push subscription")
	}
}
