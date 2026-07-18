package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func newBrowserSecurityTestServer() *Server {
	return &Server{cfg: config.Config{SecretKeyBase: "browser-session-test-secret"}}
}

func browserSessionCookieFromRecorder(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookie := cookiesByName(recorder.Result().Cookies())[browserSessionCookieName]
	if cookie == nil {
		t.Fatal("encrypted browser session cookie was not set")
	}
	return cookie
}

func TestBrowserSessionEncryptionRejectsTampering(t *testing.T) {
	server := newBrowserSecurityTestServer()
	state := &browserSessionState{
		Version:   browserSessionVersion,
		ID:        "session-id",
		Binding:   "anonymous:session-id",
		CSRFToken: "csrf-secret-not-visible",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	sealed, err := server.sealBrowserSession(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, state.CSRFToken) {
		t.Fatal("sealed browser session exposes plaintext state")
	}
	tampered, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	tampered[len(tampered)-1] ^= 0xff
	if _, err := server.openBrowserSession(base64.RawURLEncoding.EncodeToString(tampered)); err == nil {
		t.Fatal("tampered browser session was accepted")
	}
}

func TestBrowserSessionCookieSecureFlagFollowsRailsForceSSLNotURLScheme(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", ForceSSL: false, SecretKeyBase: "browser-session-test-secret"}}
	recorder := httptest.NewRecorder()
	c := echo.NewContext(httptest.NewRequest(http.MethodGet, "/auth/sign_in", nil), recorder, echo.New())
	state, err := server.browserSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistBrowserSession(c, state); err != nil {
		t.Fatal(err)
	}
	if cookie := browserSessionCookieFromRecorder(t, recorder); cookie.Secure {
		t.Fatal("development LOCAL_HTTPS URL scheme made the session cookie Secure without Rails force_ssl")
	}

	server.cfg.ForceSSL = true
	recorder = httptest.NewRecorder()
	c = echo.NewContext(httptest.NewRequest(http.MethodGet, "/auth/sign_in", nil), recorder, echo.New())
	state, err = server.browserSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistBrowserSession(c, state); err != nil {
		t.Fatal(err)
	}
	if cookie := browserSessionCookieFromRecorder(t, recorder); !cookie.Secure {
		t.Fatal("production force_ssl session cookie is not Secure")
	}
}

func TestBrowserSessionIsBoundToAuthenticationCookie(t *testing.T) {
	server := newBrowserSecurityTestServer()
	setupRecorder := httptest.NewRecorder()
	setupContext := echo.NewContext(httptest.NewRequest(http.MethodGet, "/auth/sign_in", nil), setupRecorder, echo.New())
	state, err := server.browserSession(setupContext, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistBrowserSession(setupContext, state); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
	req.AddCookie(browserSessionCookieFromRecorder(t, setupRecorder))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "different-authentication"})
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := server.browserSession(c, false); err == nil {
		t.Fatal("anonymous browser state was accepted after authentication identity changed")
	}
}

func TestBrowserChallengeRejectsWrongUserAndFutureTimestamp(t *testing.T) {
	server := newBrowserSecurityTestServer()
	setupRecorder := httptest.NewRecorder()
	setupContext := echo.NewContext(httptest.NewRequest(http.MethodPost, "/auth/challenge", nil), setupRecorder, echo.New())
	if err := server.setBrowserChallengePassed(setupContext, 42); err != nil {
		t.Fatal(err)
	}
	cookie := browserSessionCookieFromRecorder(t, setupRecorder)

	req := httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
	req.AddCookie(cookie)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if server.browserChallengePassedRecently(c, 41) {
		t.Fatal("challenge state was accepted for a different user")
	}

	futureRecorder := httptest.NewRecorder()
	futureContext := echo.NewContext(httptest.NewRequest(http.MethodGet, "/settings/profile", nil), futureRecorder, echo.New())
	futureContext.Request().AddCookie(cookie)
	state, err := server.browserSession(futureContext, false)
	if err != nil {
		t.Fatal(err)
	}
	state.ChallengePassedAt = time.Now().UTC().Add(time.Minute)
	if err := server.persistBrowserSession(futureContext, state); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
	req.AddCookie(browserSessionCookieFromRecorder(t, futureRecorder))
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if server.browserChallengePassedRecently(c, 42) {
		t.Fatal("future challenge timestamp bypassed the challenge timeout")
	}
}

func TestBrowserSecurityMiddlewareInjectsAndValidatesCSRF(t *testing.T) {
	server := newBrowserSecurityTestServer()
	e := echo.New()
	e.Use(server.browserSecurityMiddleware)
	e.GET("/auth/sign_in", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, `<!doctype html><html><head></head><body><form method="post" action="/auth/sign_in"><input name="user[email]"></form></body></html>`)
	})
	e.POST("/auth/sign_in", func(c *echo.Context) error {
		return c.String(http.StatusOK, "signed in")
	})

	getRecorder := httptest.NewRecorder()
	e.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/auth/sign_in", nil))
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", getRecorder.Code, getRecorder.Body.String())
	}
	cookie := browserSessionCookieFromRecorder(t, getRecorder)
	state, err := server.openBrowserSession(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getRecorder.Body.String(), `name="authenticity_token" value="`+state.CSRFToken+`"`) {
		t.Fatalf("CSRF hidden input was not injected: %s", getRecorder.Body.String())
	}
	if !strings.Contains(getRecorder.Body.String(), `name="csrf-token" content="`+state.CSRFToken+`"`) {
		t.Fatalf("CSRF meta tag was not injected: %s", getRecorder.Body.String())
	}
	if !strings.Contains(getRecorder.Body.String(), `name="csrf-param" content="authenticity_token"`) {
		t.Fatalf("Rails UJS CSRF parameter meta tag was not injected: %s", getRecorder.Body.String())
	}

	missingRecorder := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/auth/sign_in", strings.NewReader(url.Values{"user[email]": {"alice@example.test"}}.Encode()))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingRequest.AddCookie(cookie)
	e.ServeHTTP(missingRecorder, missingRequest)
	if missingRecorder.Code != http.StatusUnprocessableEntity || !strings.Contains(missingRecorder.Body.String(), railsCSRFErrorMessage) {
		t.Fatalf("missing CSRF status = %d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}

	validRecorder := httptest.NewRecorder()
	validRequest := httptest.NewRequest(http.MethodPost, "/auth/sign_in", strings.NewReader(url.Values{
		"user[email]":        {"alice@example.test"},
		"authenticity_token": {state.CSRFToken},
	}.Encode()))
	validRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validRequest.AddCookie(cookie)
	e.ServeHTTP(validRecorder, validRequest)
	if validRecorder.Code != http.StatusOK || validRecorder.Body.String() != "signed in" {
		t.Fatalf("valid CSRF status = %d body=%s", validRecorder.Code, validRecorder.Body.String())
	}
}

func TestBrowserCSRFProtocolExemptions(t *testing.T) {
	for _, target := range []string{
		"/api/v1/statuses",
		"/oauth/token",
		"/oauth/revoke",
		"/inbox",
		"/users/alice/inbox",
		"/users/alice/claim",
		"/auth/auth/openid_connect/callback",
	} {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "authenticated"})
		if browserCSRFProtectedRequest(req) {
			t.Fatalf("protocol endpoint %s must not require browser CSRF", target)
		}
	}
}

func TestBrowserCSRFProtectsAuthenticatedWebMutationsAndAnonymousAuthForms(t *testing.T) {
	for _, target := range []string{
		"/invites/1",
		"/settings/preferences/appearance",
		"/admin/accounts/batch",
		"/oauth/authorize",
	} {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "authenticated"})
		if !browserCSRFProtectedRequest(req) {
			t.Fatalf("authenticated browser mutation %s is missing CSRF protection", target)
		}
	}
	for _, target := range []string{
		"/auth",
		"/auth/sign_in",
		"/auth/password",
		"/auth/confirmation",
		"/auth/captcha_confirmation",
		"/auth/auth/openid_connect",
	} {
		if !browserCSRFProtectedRequest(httptest.NewRequest(http.MethodPost, target, nil)) {
			t.Fatalf("anonymous auth mutation %s is missing CSRF protection", target)
		}
	}
}

func TestInjectBrowserCSRFNormalizesExistingTokensAndHTMLFragments(t *testing.T) {
	body := `<!doctype html><html><head><meta content="old-param" name="csrf-param"><meta content="old-token" name="csrf-token"></head><body>` +
		`<form method="post"><input value="old-form-token" name="authenticity_token"><button>Save</button></form>` +
		`</body></html>`
	got := injectBrowserCSRF(body, "current-token")
	for _, want := range []string{
		`<meta name="csrf-param" content="authenticity_token">`,
		`<meta name="csrf-token" content="current-token">`,
		`<input type="hidden" name="authenticity_token" value="current-token">`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized HTML missing %q: %s", want, got)
		}
	}
	for _, stale := range []string{"old-param", "old-token", "old-form-token"} {
		if strings.Contains(got, stale) {
			t.Fatalf("normalized HTML retained stale CSRF value %q: %s", stale, got)
		}
	}

	fragment := injectBrowserCSRF(`<a data-method="delete" href="/invites/1">Deactivate</a>`, "fragment-token")
	for _, want := range []string{
		`<meta name="csrf-param" content="authenticity_token">`,
		`<meta name="csrf-token" content="fragment-token">`,
		`data-method="delete"`,
	} {
		if !strings.Contains(fragment, want) {
			t.Fatalf("HTML fragment missing %q: %s", want, fragment)
		}
	}
}

func TestHTMLHasUnsafeActionIncludesRailsDataMethodLinks(t *testing.T) {
	for _, method := range []string{"post", "patch", "put", "delete"} {
		body := `<a data-method="` + method + `" href="/admin/example">Action</a>`
		if !htmlHasUnsafeAction(body) {
			t.Fatalf("data-method %s was not treated as a CSRF-protected action", method)
		}
	}
	if htmlHasUnsafeAction(`<a data-method="get" href="/admin/example">View</a>`) {
		t.Fatal("data-method get should not require a CSRF token")
	}
}

func TestBrowserSecurityMiddlewareInjectsCSRFForRailsDataMethodLinks(t *testing.T) {
	server := newBrowserSecurityTestServer()
	e := echo.New()
	e.Use(server.browserSecurityMiddleware)
	e.GET("/settings/example", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, `<!doctype html><html><head></head><body><a data-method="delete" href="/settings/example">Delete</a></body></html>`)
	})

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings/example", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	cookie := browserSessionCookieFromRecorder(t, recorder)
	state, err := server.openBrowserSession(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), `name="csrf-token" content="`+state.CSRFToken+`"`) {
		t.Fatalf("CSRF meta tag was not injected: %s", recorder.Body.String())
	}
}

func TestBrowserSecurityMiddlewareNormalizesReactShellCSRFMeta(t *testing.T) {
	server := newBrowserSecurityTestServer()
	e := echo.New()
	e.Pre(methodOverrideMiddleware)
	e.Use(server.browserSecurityMiddleware)
	e.GET("/deck", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, `<!doctype html><html><head><meta name="csrf-param" content="authenticity_token"><meta name="csrf-token" content="renderer-token"></head><body><div id="mastodon"></div></body></html>`)
	})
	e.DELETE("/auth/sign_out", func(c *echo.Context) error {
		return c.String(http.StatusOK, "signed out")
	})

	getRecorder := httptest.NewRecorder()
	e.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/deck", nil))
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", getRecorder.Code, getRecorder.Body.String())
	}
	cookie := browserSessionCookieFromRecorder(t, getRecorder)
	state, err := server.openBrowserSession(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(getRecorder.Body.String(), "renderer-token") {
		t.Fatalf("React shell retained stale renderer CSRF token: %s", getRecorder.Body.String())
	}
	if !strings.Contains(getRecorder.Body.String(), `name="csrf-token" content="`+state.CSRFToken+`"`) {
		t.Fatalf("React shell CSRF meta tag was not normalized: %s", getRecorder.Body.String())
	}

	postRecorder := httptest.NewRecorder()
	postRequest := httptest.NewRequest(http.MethodPost, "/auth/sign_out", strings.NewReader(url.Values{
		"authenticity_token": {state.CSRFToken},
		"_method":            {"delete"},
	}.Encode()))
	postRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRequest.AddCookie(cookie)
	e.ServeHTTP(postRecorder, postRequest)
	if postRecorder.Code != http.StatusOK || postRecorder.Body.String() != "signed out" {
		t.Fatalf("React shell logout status = %d body=%s", postRecorder.Code, postRecorder.Body.String())
	}
}

func TestRailsDataMethodGeneratedFormPassesBrowserCSRF(t *testing.T) {
	server := newBrowserSecurityTestServer()
	e := echo.New()
	e.Pre(methodOverrideMiddleware)
	e.Use(server.browserSecurityMiddleware)
	e.GET("/settings/otp_authentication", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, `<!doctype html><html><head></head><body><a data-method="delete" href="/settings/otp_authentication">Delete</a></body></html>`)
	})
	e.DELETE("/settings/otp_authentication", func(c *echo.Context) error {
		return c.String(http.StatusOK, "deleted")
	})

	getRecorder := httptest.NewRecorder()
	e.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/settings/otp_authentication", nil))
	cookie := browserSessionCookieFromRecorder(t, getRecorder)
	state, err := server.openBrowserSession(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}

	postRecorder := httptest.NewRecorder()
	postRequest := httptest.NewRequest(http.MethodPost, "/settings/otp_authentication", strings.NewReader(url.Values{
		"authenticity_token": {state.CSRFToken},
		"_method":            {"delete"},
	}.Encode()))
	postRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRequest.AddCookie(cookie)
	e.ServeHTTP(postRecorder, postRequest)
	if postRecorder.Code != http.StatusOK || postRecorder.Body.String() != "deleted" {
		t.Fatalf("Rails data-method POST status = %d body=%s", postRecorder.Code, postRecorder.Body.String())
	}
}

func TestBrowserTransientStateIgnoresLegacyUnsignedCookies(t *testing.T) {
	server := newBrowserSecurityTestServer()
	req := httptest.NewRequest(http.MethodGet, "/auth/sessions/security_key_options", nil)
	req.AddCookie(&http.Cookie{Name: webauthnAttemptUserCookie, Value: "42"})
	req.AddCookie(&http.Cookie{Name: webauthnChallengeCookie, Value: "attacker-challenge"})
	req.AddCookie(&http.Cookie{Name: "paon_new_otp_secret", Value: "ATTACKERSECRET"})
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, _, ok := server.browserTwoFactorAttempt(c); ok {
		t.Fatal("unsigned two-factor user cookie was accepted")
	}
	if _, ok := server.browserWebAuthnChallenge(c, 42, "login"); ok {
		t.Fatal("unsigned WebAuthn challenge cookie was accepted")
	}
	if _, ok := server.browserNewOTPSecret(c, 42); ok {
		t.Fatal("unsigned OTP secret cookie was accepted")
	}
}

func TestExtractAdminDocumentContentPromotesRailsHeadingActions(t *testing.T) {
	document := `<!doctype html><html><head><title>Accounts</title></head><body class="app-body"><main role="main"><h1>Accounts</h1><div class="content__heading__actions"><a class="button" href="/admin/accounts/new">New</a></div><nav class="content__heading__tabs"><ul><li class="active"><a href="/admin/accounts">Accounts</a></li></ul></nav><form method="get" action="/admin/accounts"><input name="username"><button type="submit">Search</button></form><table><tbody><tr><td>One</td></tr></tbody></table></main></body></html>`
	title, content, tabs, actions, ok := extractAdminDocumentContent(document)
	if !ok {
		t.Fatal("admin document was not parsed")
	}
	if title != "Accounts" {
		t.Fatalf("title = %q", title)
	}
	if strings.Contains(content, "<h1") || strings.Contains(content, "content__heading__actions") || strings.Contains(content, "content__heading__tabs") {
		t.Fatalf("content retained promoted heading nodes: %s", content)
	}
	if !strings.Contains(content, `action="/admin/accounts"`) || !strings.Contains(content, `class="simple_form"`) || !strings.Contains(content, `<button type="submit" class="button">`) || !strings.Contains(content, `<table class="table">`) {
		t.Fatalf("content lost page form: %s", content)
	}
	if !strings.Contains(actions, `href="/admin/accounts/new"`) {
		t.Fatalf("heading actions = %s", actions)
	}
	if !strings.Contains(tabs, `href="/admin/accounts"`) {
		t.Fatalf("heading tabs = %s", tabs)
	}
}

func TestExtractAdminDocumentContentDoesNotStyleFormForBatchAsSimpleForm(t *testing.T) {
	document := `<!doctype html><html><head><title>Accounts</title></head><body><main><h1>Accounts</h1><form class="simple_form new_form_account_batch" method="post"><div class="batch-table"><div class="batch-table__toolbar__actions"><button type="submit">Suspend</button></div></div></form></main></body></html>`
	_, content, _, _, ok := extractAdminDocumentContent(document)
	if !ok {
		t.Fatal("admin batch document was not parsed")
	}
	if strings.Contains(content, `class="simple_form new_form_account_batch"`) || strings.Contains(content, `class="new_form_account_batch simple_form"`) {
		t.Fatalf("batch form retained simple_form class: %s", content)
	}
	if !strings.Contains(content, `class="new_form_account_batch"`) || !strings.Contains(content, `class="table-action-link"`) {
		t.Fatalf("batch form lost Rails form_for structure: %s", content)
	}
}
