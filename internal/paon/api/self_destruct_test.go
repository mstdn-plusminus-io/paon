package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestSelfDestructTokenRequiresPurposeSignatureAndExactDomain(t *testing.T) {
	const secret = "secret-key-base"
	token, err := GenerateSelfDestructToken(secret, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	const rails71Fixture = "ImV4YW1wbGUuY29tIg==--11b65edfc2e4831111a9af12b114c7373e287ce3"
	if token != rails71Fixture {
		t.Fatalf("token = %q, want Rails 7.1 fixture %q", token, rails71Fixture)
	}
	if !VerifySelfDestructToken(token, secret, "example.com") {
		t.Fatal("correctly signed token was rejected")
	}
	wrongPurpose, err := generateSelfDestructTokenForPurpose(secret, "example.com", "not-self-destruct")
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]string{
		"raw domain":    "example.com",
		"wrong purpose": wrongPurpose,
		"tampered":      token + "0",
		"empty":         "",
	} {
		if VerifySelfDestructToken(candidate, secret, "example.com") {
			t.Fatalf("%s token was accepted", name)
		}
	}
	if VerifySelfDestructToken(token, secret, "other.example") {
		t.Fatal("token signed for another local domain was accepted")
	}
	if VerifySelfDestructToken(" "+token, secret, "example.com") {
		t.Fatal("whitespace around the signed token must not be normalized")
	}
	spacedSecret, err := GenerateSelfDestructToken(" secret ", "example.com")
	if err != nil || !VerifySelfDestructToken(spacedSecret, " secret ", "example.com") || VerifySelfDestructToken(spacedSecret, "secret", "example.com") {
		t.Fatal("SECRET_KEY_BASE bytes must match Rails exactly")
	}
}

func TestSelfDestructLoadThresholdsAreStrictAndUseSmallerPositiveMemory(t *testing.T) {
	if reason := (selfDestructLoad{Queue: selfDestructQueueStats{Pending: 10_000}}).pauseReason(); reason != "" {
		t.Fatalf("exactly 10,000 enqueued should continue, got %q", reason)
	}
	if reason := (selfDestructLoad{Queue: selfDestructQueueStats{Pending: 10_001}}).pauseReason(); reason == "" {
		t.Fatal("more than 10,000 enqueued should pause")
	}
	if got := selfDestructPositiveMemoryLimit(8_000, 12_000); got != 8_000 {
		t.Fatalf("memory reference = %d, want 8000", got)
	}
	if got := selfDestructPositiveMemoryLimit(0, 12_000); got != 12_000 {
		t.Fatalf("positive memory reference = %d, want 12000", got)
	}
	if reason := (selfDestructLoad{UsedMemory: 4_000, MaxMemory: 8_000}).pauseReason(); reason != "" {
		t.Fatalf("exactly 50%% memory should continue, got %q", reason)
	}
	if reason := (selfDestructLoad{UsedMemory: 4_001, MaxMemory: 8_000}).pauseReason(); reason == "" {
		t.Fatal("more than 50% memory should pause")
	}
	if got := (selfDestructQueueStats{Archived: 1}).unfinished(); got != 1 {
		t.Fatalf("archived work must prevent completion, unfinished = %d", got)
	}
}

func TestSelfDestructInboxBatchesNeverExceedOneThousand(t *testing.T) {
	inboxes := make([]string, 2_001)
	for index := range inboxes {
		inboxes[index] = "https://remote.example/inbox"
	}
	batches := selfDestructInboxBatches(inboxes, selfDestructMaxDeliveryBatch)
	if len(batches) != 3 || len(batches[0]) != 1_000 || len(batches[1]) != 1_000 || len(batches[2]) != 1 {
		t.Fatalf("batch sizes = %d/%d/%d (count %d)", len(batches[0]), len(batches[1]), len(batches[2]), len(batches))
	}
}

func TestSelfDestructDeliveryIdentityIsStableAndAccountScoped(t *testing.T) {
	one := selfDestructDeliveryTaskID(1, "https://remote.example/inbox")
	if one != selfDestructDeliveryTaskID(1, "https://remote.example/inbox") {
		t.Fatal("task ID is not stable")
	}
	if one == selfDestructDeliveryTaskID(2, "https://remote.example/inbox") {
		t.Fatal("task ID is not account scoped")
	}
	if selfDestructDeliveryMarkerKey("") != "self_destruct:delivered" || selfDestructDeliveryMarkerKey("mastodon:") != "mastodon:self_destruct:delivered" {
		t.Fatal("delivery marker namespace is not stable")
	}
}

func TestSelfDestructAllowedRequestBoundary(t *testing.T) {
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/auth/sign_in"},
		{http.MethodPost, "/auth/sign_in"},
		{http.MethodPost, "/auth/challenge"},
		{http.MethodPatch, "/auth/password"},
		{http.MethodGet, "/auth/auth/saml"},
		{http.MethodGet, "/auth/auth/saml/callback"},
		{http.MethodGet, "/auth/edit"},
		{http.MethodGet, "/backups/12/download"},
		{http.MethodPost, "/settings/export"},
		{http.MethodGet, "/settings/exports/follows.csv"},
		{http.MethodGet, "/settings/login_activities"},
		{http.MethodGet, "/settings/otp_authentication"},
		{http.MethodPost, "/settings/otp_authentication.json"},
		{http.MethodGet, "/settings/two_factor_authentication/confirmation/new"},
		{http.MethodPost, "/settings/two_factor_authentication/confirmation"},
		{http.MethodPost, "/settings/two_factor_authentication/recovery_codes.json"},
		{http.MethodGet, "/settings/security_keys/options"},
	}
	for _, request := range allowed {
		if !selfDestructAllowedRequest(request.method, request.path) {
			t.Errorf("expected allowed: %s %s", request.method, request.path)
		}
	}
	blocked := []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/about"},
		{http.MethodGet, "/public"},
		{http.MethodGet, "/api/v1/instance"},
		{http.MethodGet, "/users/alice"},
		{http.MethodGet, "/users/alice/outbox"},
		{http.MethodGet, "/auth/sign_up"},
		{http.MethodPost, "/auth"},
		{http.MethodGet, "/settings/preferences"},
	}
	for _, request := range blocked {
		if selfDestructAllowedRequest(request.method, request.path) {
			t.Errorf("expected blocked: %s %s", request.method, request.path)
		}
	}
}

func TestSelfDestructMiddlewareReturnsGoneForHTMLAndJSON(t *testing.T) {
	token, err := GenerateSelfDestructToken("secret", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{SelfDestruct: token, SecretKeyBase: "secret", LocalDomain: "example.com", DefaultLocale: "en"}}
	e := echo.New()
	e.Use(s.selfDestructMiddleware)
	e.GET("/", func(c *echo.Context) error { return c.String(http.StatusOK, "wrong") })
	e.GET("/api/v1/instance", func(c *echo.Context) error { return c.String(http.StatusOK, "wrong") })
	e.GET("/auth/sign_in", func(c *echo.Context) error { return c.String(http.StatusOK, "allowed") })

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusGone || !strings.Contains(recorder.Body.String(), "closing down") {
		t.Fatalf("HTML response = %d %q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	request.Header.Set("Accept", "application/json")
	e.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusGone || !strings.Contains(recorder.Body.String(), `"error":"Gone"`) {
		t.Fatalf("JSON response = %d %q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/sign_in", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "allowed" {
		t.Fatalf("allowed response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestSelfDestructSchedulerStaticSafetyBoundaries(t *testing.T) {
	scheduler, err := os.ReadFile("self_destruct_scheduler.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, scheduler, "processSelfDestructAccountGroup")
	for _, want := range []string{
		`Limit(selfDestructMaxAccountsPerGroup)`,
		`"suspended_at"`,
		`"suspension_origin"`,
		`Delete(&models.AccountDeletionRequest{})`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("account group processing missing %q", want)
		}
	}
	if strings.Contains(body, `Delete(&models.Account{})`) || strings.Contains(body, `Unscoped()`) {
		t.Fatal("self-destruct scheduler must retain account and user data for archive/export")
	}
	delivery, err := os.ReadFile("self_destruct_delivery.go")
	if err != nil {
		t.Fatal(err)
	}
	deliveryBody := functionBody(t, delivery, "handleAsynqSelfDestructDelivery")
	lookupIndex := strings.Index(deliveryBody, `"HEXISTS"`)
	performIndex := strings.Index(deliveryBody, `s.performActivityPubDeliveryInitial`)
	markerIndex := strings.Index(deliveryBody, `"HSET"`)
	if lookupIndex < 0 || performIndex < 0 || markerIndex < 0 || !(lookupIndex < performIndex && performIndex < markerIndex) {
		t.Fatal("delivery must check its durable marker before delivery and record it only after success")
	}

	workers, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	startBody := functionBody(t, workers, "StartBackgroundWorkers")
	modeIndex := strings.Index(startBody, `if s.selfDestructEnabled()`)
	normalIndex := strings.Index(startBody, `workers.Go(ctx, s.runScheduledStatusPublishWorker)`)
	if modeIndex < 0 || normalIndex < 0 || modeIndex > normalIndex || !strings.Contains(startBody, `workers.Go(ctx, s.runSelfDestructScheduler)`) || !strings.Contains(startBody, `return workers`) {
		t.Fatal("self-destruct mode must replace the normal recurring scheduler set")
	}
}
