package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	nethtml "golang.org/x/net/html"
)

const (
	browserSessionCookieName = "paon_browser_session"
	browserSessionLifetime   = 365 * 24 * time.Hour
	browserTransientLifetime = 5 * time.Minute
	browserSessionVersion    = 1
	browserSessionContextKey = "paon.browser_session"
)

type browserSessionState struct {
	Version   int       `json:"v"`
	ID        string    `json:"id"`
	Binding   string    `json:"binding"`
	CSRFToken string    `json:"csrf"`
	ExpiresAt time.Time `json:"expires_at"`

	ChallengePassedAt time.Time `json:"challenge_passed_at,omitempty"`
	ChallengeUserID   int64     `json:"challenge_user_id,omitempty"`

	AttemptUserID    int64     `json:"attempt_user_id,omitempty"`
	AttemptRedirect  string    `json:"attempt_redirect,omitempty"`
	AttemptExpiresAt time.Time `json:"attempt_expires_at,omitempty"`

	WebAuthnChallenge string    `json:"webauthn_challenge,omitempty"`
	WebAuthnUserID    int64     `json:"webauthn_user_id,omitempty"`
	WebAuthnPurpose   string    `json:"webauthn_purpose,omitempty"`
	WebAuthnExpiresAt time.Time `json:"webauthn_expires_at,omitempty"`

	NewOTPSecret    string    `json:"new_otp_secret,omitempty"`
	NewOTPUserID    int64     `json:"new_otp_user_id,omitempty"`
	NewOTPExpiresAt time.Time `json:"new_otp_expires_at,omitempty"`

	OIDCState     string    `json:"oidc_state,omitempty"`
	OIDCNonce     string    `json:"oidc_nonce,omitempty"`
	OIDCExpiresAt time.Time `json:"oidc_expires_at,omitempty"`
}

var (
	processBrowserSessionKey      [32]byte
	processBrowserSessionKeyOnce  sync.Once
	formTagPattern                = regexp.MustCompile(`(?is)<form\b[^>]*>`)
	formBlockPattern              = regexp.MustCompile(`(?is)<form\b[^>]*>.*?</form\s*>`)
	postMethodPattern             = regexp.MustCompile(`(?i)\bmethod\s*=\s*["']?post(?:["'\s>]|$)`)
	unsafeDataMethodPattern       = regexp.MustCompile(`(?i)\bdata-method\s*=\s*["'](?:post|patch|put|delete)["']`)
	authenticityTokenInputPattern = regexp.MustCompile(`(?is)<input\b[^>]*\bname\s*=\s*["']authenticity_token["'][^>]*>`)
	csrfParamMetaPattern          = regexp.MustCompile(`(?is)<meta\b[^>]*\bname\s*=\s*["']csrf-param["'][^>]*>`)
	csrfTokenMetaPattern          = regexp.MustCompile(`(?is)<meta\b[^>]*\bname\s*=\s*["']csrf-token["'][^>]*>`)
)

func deriveBrowserSessionKey(cfg config.Config) ([32]byte, error) {
	if strings.TrimSpace(cfg.SecretKeyBase) != "" {
		return sha256.Sum256([]byte("paon/browser-session/v1\x00" + cfg.SecretKeyBase)), nil
	}
	var keyErr error
	processBrowserSessionKeyOnce.Do(func() {
		_, keyErr = io.ReadFull(rand.Reader, processBrowserSessionKey[:])
	})
	if keyErr != nil {
		return [32]byte{}, keyErr
	}
	return processBrowserSessionKey, nil
}

func (s *Server) browserSessionKeyValue() ([32]byte, error) {
	if s != nil && s.browserSessionKey != ([32]byte{}) {
		return s.browserSessionKey, nil
	}
	if s == nil {
		return [32]byte{}, errors.New("browser session server is nil")
	}
	return deriveBrowserSessionKey(s.cfg)
}

func (s *Server) sealBrowserSession(state *browserSessionState) (string, error) {
	key, err := s.browserSessionKeyValue()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, []byte(browserSessionCookieName))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Server) openBrowserSession(value string) (*browserSessionState, error) {
	key, err := s.browserSessionKeyValue()
	if err != nil {
		return nil, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("browser session ciphertext is too short")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(browserSessionCookieName))
	if err != nil {
		return nil, err
	}
	var state browserSessionState
	if err := json.Unmarshal(plaintext, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func randomBrowserSessionValue(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func browserSessionCookieIdentity(c *echo.Context, paonTokenOverride string) string {
	if strings.TrimSpace(paonTokenOverride) != "" {
		return "paon:" + browserSessionDigest(paonTokenOverride)
	}
	for _, name := range []string{sessionCookieName, railsSessionIDCookieName, railsSessionCookieName} {
		cookie, err := c.Cookie(name)
		if err == nil && strings.TrimSpace(cookie.Value) != "" {
			return name + ":" + browserSessionDigest(cookie.Value)
		}
	}
	return ""
}

func browserSessionDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func browserSessionExpectedBinding(c *echo.Context, stateID string, paonTokenOverride string) string {
	if identity := browserSessionCookieIdentity(c, paonTokenOverride); identity != "" {
		return identity
	}
	return "anonymous:" + stateID
}

func (s *Server) browserSession(c *echo.Context, create bool) (*browserSessionState, error) {
	if cached := c.Get(browserSessionContextKey); cached != nil {
		if state, ok := cached.(*browserSessionState); ok {
			return state, nil
		}
	}
	now := time.Now().UTC()
	if cookie, err := c.Cookie(browserSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		state, openErr := s.openBrowserSession(cookie.Value)
		if openErr == nil && state.Version == browserSessionVersion && state.ID != "" && state.CSRFToken != "" && state.ExpiresAt.After(now) && subtle.ConstantTimeCompare([]byte(state.Binding), []byte(browserSessionExpectedBinding(c, state.ID, ""))) == 1 {
			c.Set(browserSessionContextKey, state)
			return state, nil
		}
	}
	if !create {
		return nil, http.ErrNoCookie
	}
	id, err := randomBrowserSessionValue(24)
	if err != nil {
		return nil, err
	}
	csrf, err := randomBrowserSessionValue(32)
	if err != nil {
		return nil, err
	}
	state := &browserSessionState{
		Version:   browserSessionVersion,
		ID:        id,
		CSRFToken: csrf,
		ExpiresAt: now.Add(browserSessionLifetime),
	}
	state.Binding = browserSessionExpectedBinding(c, state.ID, "")
	c.Set(browserSessionContextKey, state)
	return state, nil
}

func (s *Server) persistBrowserSession(c *echo.Context, state *browserSessionState) error {
	if state == nil {
		return errors.New("browser session state is nil")
	}
	now := time.Now().UTC()
	state.Version = browserSessionVersion
	state.ExpiresAt = now.Add(browserSessionLifetime)
	if state.Binding == "" {
		state.Binding = browserSessionExpectedBinding(c, state.ID, "")
	}
	value, err := s.sealBrowserSession(state)
	if err != nil {
		return err
	}
	http.SetCookie(c.Response(), &http.Cookie{
		Name:     browserSessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(browserSessionLifetime.Seconds()),
		Expires:  state.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.ForceSSL,
	})
	c.Set(browserSessionContextKey, state)
	return nil
}

func (s *Server) clearBrowserSession(c *echo.Context) {
	expireCookie(c, browserSessionCookieName, s.cfg.ForceSSL)
	c.Set(browserSessionContextKey, nil)
}

func (s *Server) rotateBrowserSessionForLogin(c *echo.Context, token string) error {
	state, err := s.browserSession(c, true)
	if err != nil {
		return err
	}
	id, err := randomBrowserSessionValue(24)
	if err != nil {
		return err
	}
	csrf, err := randomBrowserSessionValue(32)
	if err != nil {
		return err
	}
	*state = browserSessionState{
		Version:   browserSessionVersion,
		ID:        id,
		CSRFToken: csrf,
		Binding:   browserSessionExpectedBinding(c, id, token),
	}
	return s.persistBrowserSession(c, state)
}

func (s *Server) setBrowserChallengePassed(c *echo.Context, userID int64) error {
	state, err := s.browserSession(c, true)
	if err != nil {
		return err
	}
	state.ChallengePassedAt = time.Now().UTC()
	state.ChallengeUserID = userID
	return s.persistBrowserSession(c, state)
}

func (s *Server) browserChallengePassedRecently(c *echo.Context, userID int64) bool {
	state, err := s.browserSession(c, false)
	if err != nil || userID <= 0 || state.ChallengeUserID != userID || state.ChallengePassedAt.IsZero() {
		return false
	}
	now := time.Now().UTC()
	if state.ChallengePassedAt.After(now) || now.Sub(state.ChallengePassedAt) > railsChallengeTimeout {
		return false
	}
	state.ChallengePassedAt = now
	_ = s.persistBrowserSession(c, state)
	return true
}

func (s *Server) setBrowserTwoFactorAttempt(c *echo.Context, userID int64, redirect string) error {
	state, err := s.browserSession(c, true)
	if err != nil {
		return err
	}
	state.AttemptUserID = userID
	state.AttemptRedirect = safeRedirect(redirect)
	state.AttemptExpiresAt = time.Now().UTC().Add(browserTransientLifetime)
	state.WebAuthnChallenge = ""
	state.WebAuthnUserID = 0
	state.WebAuthnPurpose = ""
	state.WebAuthnExpiresAt = time.Time{}
	return s.persistBrowserSession(c, state)
}

func (s *Server) browserTwoFactorAttempt(c *echo.Context) (int64, string, bool) {
	state, err := s.browserSession(c, false)
	if err != nil || state.AttemptUserID <= 0 || !state.AttemptExpiresAt.After(time.Now().UTC()) {
		return 0, "", false
	}
	return state.AttemptUserID, state.AttemptRedirect, true
}

func (s *Server) clearBrowserTwoFactorAttempt(c *echo.Context) error {
	state, err := s.browserSession(c, false)
	if err != nil {
		return nil
	}
	state.AttemptUserID = 0
	state.AttemptRedirect = ""
	state.AttemptExpiresAt = time.Time{}
	state.WebAuthnChallenge = ""
	state.WebAuthnUserID = 0
	state.WebAuthnPurpose = ""
	state.WebAuthnExpiresAt = time.Time{}
	return s.persistBrowserSession(c, state)
}

func (s *Server) setBrowserWebAuthnChallenge(c *echo.Context, userID int64, purpose string, challenge string) error {
	state, err := s.browserSession(c, true)
	if err != nil {
		return err
	}
	state.WebAuthnChallenge = challenge
	state.WebAuthnUserID = userID
	state.WebAuthnPurpose = purpose
	state.WebAuthnExpiresAt = time.Now().UTC().Add(browserTransientLifetime)
	return s.persistBrowserSession(c, state)
}

func (s *Server) browserWebAuthnChallenge(c *echo.Context, userID int64, purpose string) (string, bool) {
	state, err := s.browserSession(c, false)
	if err != nil || state.WebAuthnUserID != userID || state.WebAuthnPurpose != purpose || strings.TrimSpace(state.WebAuthnChallenge) == "" || !state.WebAuthnExpiresAt.After(time.Now().UTC()) {
		return "", false
	}
	return state.WebAuthnChallenge, true
}

func (s *Server) clearBrowserWebAuthnChallenge(c *echo.Context) error {
	state, err := s.browserSession(c, false)
	if err != nil {
		return nil
	}
	state.WebAuthnChallenge = ""
	state.WebAuthnUserID = 0
	state.WebAuthnPurpose = ""
	state.WebAuthnExpiresAt = time.Time{}
	return s.persistBrowserSession(c, state)
}

func (s *Server) setBrowserNewOTPSecret(c *echo.Context, userID int64, secret string) error {
	state, err := s.browserSession(c, true)
	if err != nil {
		return err
	}
	state.NewOTPSecret = secret
	state.NewOTPUserID = userID
	state.NewOTPExpiresAt = time.Now().UTC().Add(browserTransientLifetime)
	return s.persistBrowserSession(c, state)
}

func (s *Server) browserNewOTPSecret(c *echo.Context, userID int64) (string, bool) {
	state, err := s.browserSession(c, false)
	if err != nil || state.NewOTPUserID != userID || strings.TrimSpace(state.NewOTPSecret) == "" || !state.NewOTPExpiresAt.After(time.Now().UTC()) {
		return "", false
	}
	return state.NewOTPSecret, true
}

func (s *Server) clearBrowserNewOTPSecret(c *echo.Context) error {
	state, err := s.browserSession(c, false)
	if err != nil {
		return nil
	}
	state.NewOTPSecret = ""
	state.NewOTPUserID = 0
	state.NewOTPExpiresAt = time.Time{}
	return s.persistBrowserSession(c, state)
}

func (s *Server) setBrowserOIDCState(c *echo.Context, oidcState string, nonce string) error {
	state, err := s.browserSession(c, true)
	if err != nil {
		return err
	}
	state.OIDCState = oidcState
	state.OIDCNonce = nonce
	state.OIDCExpiresAt = time.Now().UTC().Add(browserTransientLifetime)
	return s.persistBrowserSession(c, state)
}

func (s *Server) browserOIDCState(c *echo.Context) (string, string, bool) {
	state, err := s.browserSession(c, false)
	if err != nil || strings.TrimSpace(state.OIDCState) == "" || !state.OIDCExpiresAt.After(time.Now().UTC()) {
		return "", "", false
	}
	return state.OIDCState, state.OIDCNonce, true
}

func (s *Server) clearBrowserOIDCState(c *echo.Context) error {
	state, err := s.browserSession(c, false)
	if err != nil {
		return nil
	}
	state.OIDCState = ""
	state.OIDCNonce = ""
	state.OIDCExpiresAt = time.Time{}
	return s.persistBrowserSession(c, state)
}

func (s *Server) browserSecurityMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if browserCSRFProtectedRequest(c.Request()) {
			state, err := s.browserSession(c, false)
			if err != nil || !browserCSRFTokenValid(c, state.CSRFToken) {
				return echo.NewHTTPError(http.StatusUnprocessableEntity, railsCSRFErrorMessage)
			}
		}

		if !browserHTMLResponseCandidate(c.Request()) {
			return next(c)
		}
		original := c.Response()
		writer := newBrowserHTMLResponseWriter(original)
		c.SetResponse(writer)
		err := next(c)
		if err != nil {
			c.SetResponse(original)
			return err
		}
		if err := s.finishBrowserHTMLResponse(c, writer); err != nil {
			c.SetResponse(original)
			return err
		}
		c.SetResponse(original)
		return nil
	}
}

func browserCSRFProtectedRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	path := req.URL.Path
	if strings.HasPrefix(path, "/api/") || path == "/oauth/token" || path == "/oauth/revoke" {
		return false
	}
	if strings.HasSuffix(path, "/inbox") || strings.HasSuffix(path, "/inbox.json") || strings.HasSuffix(path, "/claim") || strings.HasSuffix(path, "/claim.json") {
		return false
	}
	if strings.HasPrefix(path, "/auth/") && strings.HasSuffix(path, "/callback") {
		return false
	}
	if browserRequestHasAuthenticationCookie(req) {
		return true
	}
	return anonymousBrowserMutationNeedsCSRF(req.Method, path)
}

func browserRequestHasAuthenticationCookie(req *http.Request) bool {
	if req == nil {
		return false
	}
	for _, name := range []string{sessionCookieName, railsSessionIDCookieName, railsSessionCookieName} {
		cookie, err := req.Cookie(name)
		if err == nil && strings.TrimSpace(cookie.Value) != "" {
			return true
		}
	}
	return false
}

func anonymousBrowserMutationNeedsCSRF(method string, path string) bool {
	path = strings.TrimSuffix(path, ".html")
	if path == "/auth" {
		return method == http.MethodPost
	}
	switch path {
	case "/auth/sign_in", "/auth/password", "/auth/confirmation", "/auth/captcha_confirmation":
		return true
	}
	return strings.HasPrefix(path, "/auth/auth/") && !strings.HasSuffix(path, "/callback")
}

func browserCSRFTokenValid(c *echo.Context, expected string) bool {
	if expected == "" {
		return false
	}
	for _, value := range []string{
		c.Request().Header.Get("X-CSRF-Token"),
		c.Request().Header.Get("X-XSRF-Token"),
		c.FormValue("authenticity_token"),
	} {
		if subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1 {
			return true
		}
	}
	return false
}

func browserHTMLResponseCandidate(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	return !strings.HasPrefix(req.URL.Path, "/api/") && !strings.HasPrefix(req.URL.Path, "/packs/") && !strings.HasPrefix(req.URL.Path, "/assets/")
}

type browserHTMLResponseWriter struct {
	http.ResponseWriter
	status      int
	buffer      bytes.Buffer
	buffering   bool
	decided     bool
	wroteHeader bool
}

func newBrowserHTMLResponseWriter(writer http.ResponseWriter) *browserHTMLResponseWriter {
	return &browserHTMLResponseWriter{ResponseWriter: writer, status: http.StatusOK}
}

func (w *browserHTMLResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.decide()
	if !w.buffering {
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *browserHTMLResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.buffering {
		return w.buffer.Write(body)
	}
	return w.ResponseWriter.Write(body)
}

func (w *browserHTMLResponseWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	w.buffering = strings.HasPrefix(contentType, "text/html") || strings.Contains(contentType, "application/xhtml+xml")
}

func (w *browserHTMLResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) finishBrowserHTMLResponse(c *echo.Context, writer *browserHTMLResponseWriter) error {
	if !writer.buffering {
		if !writer.wroteHeader {
			writer.ResponseWriter.WriteHeader(writer.status)
		}
		return nil
	}
	body := writer.buffer.String()
	var err error
	body, err = s.ensureAdminPageShell(c, body)
	if err != nil {
		return err
	}
	if !htmlHasUnsafeAction(body) {
		writer.Header().Set("Content-Length", stringLength(len(body)))
		writer.ResponseWriter.WriteHeader(writer.status)
		_, err := io.WriteString(writer.ResponseWriter, body)
		return err
	}
	state, err := s.browserSession(c, true)
	if err != nil {
		return err
	}
	if err := s.persistBrowserSession(c, state); err != nil {
		return err
	}
	body = injectBrowserCSRF(body, state.CSRFToken)
	writer.Header().Set("Content-Length", stringLength(len(body)))
	writer.ResponseWriter.WriteHeader(writer.status)
	_, err = io.WriteString(writer.ResponseWriter, body)
	return err
}

func (s *Server) ensureAdminPageShell(c *echo.Context, body string) (string, error) {
	if c == nil || c.Request() == nil || c.Request().URL == nil || strings.Contains(body, `class="admin-wrapper"`) {
		return body, nil
	}
	path := c.Request().URL.Path
	if !strings.HasPrefix(path, "/admin") && !strings.HasPrefix(path, "/asynq") && !strings.HasPrefix(path, "/pghero") {
		return body, nil
	}
	title, content, headingTabs, headingActions, ok := extractAdminDocumentContent(body)
	if !ok {
		return body, nil
	}
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil || user == nil {
		return body, nil
	}
	account, err := s.accountForUser(user)
	if err != nil {
		return body, nil
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	navigation, err := s.settingsNavigationForUser(path, locale, user, account)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(title) == "" {
		title = adminT(locale, "admin.dashboard.title", "Administration")
	}
	headingTitle := title
	if strings.HasPrefix(path, "/admin/settings/") {
		headingTitle = adminT(locale, "admin.settings.title", "Server settings")
	}
	return settingsPageShellWithHeadingTitle(title, headingTitle, navigation, content, locale, theme, headingTabs, headingActions), nil
}

func extractAdminDocumentContent(document string) (title string, content string, headingTabs string, headingActions string, ok bool) {
	root, err := nethtml.Parse(strings.NewReader(document))
	if err != nil {
		return "", "", "", "", false
	}
	titleNode := findHTMLElement(root, "title")
	mainNode := findHTMLElement(root, "main")
	if mainNode == nil {
		return "", "", "", "", false
	}
	if titleNode != nil {
		title = strings.TrimSpace(htmlNodeText(titleNode))
	}
	normalizeAdminDocumentContent(mainNode)
	var rendered strings.Builder
	headingSkipped := false
	for child := mainNode.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == nethtml.ElementNode && (child.Data == "h1" || child.Data == "h2") && !headingSkipped {
			if title == "" {
				title = strings.TrimSpace(htmlNodeText(child))
			}
			headingSkipped = true
			continue
		}
		if child.Type == nethtml.ElementNode && htmlNodeHasClass(child, "content__heading__actions") && headingActions == "" {
			var actions strings.Builder
			if err := nethtml.Render(&actions, child); err != nil {
				return "", "", "", "", false
			}
			headingActions = actions.String()
			continue
		}
		if child.Type == nethtml.ElementNode && htmlNodeHasClass(child, "content__heading__tabs") && headingTabs == "" {
			var tabs strings.Builder
			if err := nethtml.Render(&tabs, child); err != nil {
				return "", "", "", "", false
			}
			headingTabs = tabs.String()
			continue
		}
		if err := nethtml.Render(&rendered, child); err != nil {
			return "", "", "", "", false
		}
	}
	return title, rendered.String(), headingTabs, headingActions, true
}

func normalizeAdminDocumentContent(mainNode *nethtml.Node) {
	if mainNode == nil {
		return
	}
	for child := mainNode.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == nethtml.ElementNode && child.Data == "form" {
			if htmlNodeDescendantHasClass(child, "batch-table") {
				htmlNodeRemoveClass(child, "simple_form")
			} else {
				htmlNodeAddClass(child, "simple_form")
			}
		}
		normalizeAdminDocumentDescendants(child)
	}
}

func normalizeAdminDocumentDescendants(node *nethtml.Node) {
	if node == nil {
		return
	}
	if node.Type == nethtml.ElementNode {
		switch node.Data {
		case "button":
			if htmlNodeClassValue(node) == "" {
				if htmlNodeAncestorHasElement(node, "td") || htmlNodeAncestorHasElement(node, "li") || htmlNodeAncestorHasClass(node, "batch-table__toolbar__actions") {
					htmlNodeAddClass(node, "table-action-link")
				} else {
					htmlNodeAddClass(node, "button")
				}
			}
		case "table":
			if htmlNodeClassValue(node) == "" {
				htmlNodeAddClass(node, "table")
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		normalizeAdminDocumentDescendants(child)
	}
}

func htmlNodeAncestorHasElement(node *nethtml.Node, tag string) bool {
	for current := node.Parent; current != nil; current = current.Parent {
		if current.Type == nethtml.ElementNode && current.Data == tag {
			return true
		}
	}
	return false
}

func htmlNodeAncestorHasClass(node *nethtml.Node, className string) bool {
	for current := node.Parent; current != nil; current = current.Parent {
		if htmlNodeHasClass(current, className) {
			return true
		}
	}
	return false
}

func htmlNodeClassValue(node *nethtml.Node) string {
	if node == nil || node.Type != nethtml.ElementNode {
		return ""
	}
	for _, attribute := range node.Attr {
		if attribute.Key == "class" {
			return attribute.Val
		}
	}
	return ""
}

func htmlNodeAddClass(node *nethtml.Node, className string) {
	if node == nil || node.Type != nethtml.ElementNode || strings.TrimSpace(className) == "" || htmlNodeHasClass(node, className) {
		return
	}
	for index := range node.Attr {
		if node.Attr[index].Key == "class" {
			node.Attr[index].Val = strings.TrimSpace(node.Attr[index].Val + " " + className)
			return
		}
	}
	node.Attr = append(node.Attr, nethtml.Attribute{Key: "class", Val: className})
}

func htmlNodeRemoveClass(node *nethtml.Node, className string) {
	if node == nil || node.Type != nethtml.ElementNode || strings.TrimSpace(className) == "" {
		return
	}
	for index := range node.Attr {
		if node.Attr[index].Key != "class" {
			continue
		}
		classes := strings.Fields(node.Attr[index].Val)
		kept := classes[:0]
		for _, current := range classes {
			if current != className {
				kept = append(kept, current)
			}
		}
		node.Attr[index].Val = strings.Join(kept, " ")
		return
	}
}

func htmlNodeDescendantHasClass(node *nethtml.Node, className string) bool {
	if node == nil {
		return false
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if htmlNodeHasClass(child, className) || htmlNodeDescendantHasClass(child, className) {
			return true
		}
	}
	return false
}

func findHTMLElement(node *nethtml.Node, tag string) *nethtml.Node {
	if node == nil {
		return nil
	}
	if node.Type == nethtml.ElementNode && node.Data == tag {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findHTMLElement(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func htmlNodeText(node *nethtml.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == nethtml.TextNode {
		return node.Data
	}
	var out strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		out.WriteString(htmlNodeText(child))
	}
	return out.String()
}

func htmlNodeHasClass(node *nethtml.Node, className string) bool {
	if node == nil || node.Type != nethtml.ElementNode {
		return false
	}
	for _, attribute := range node.Attr {
		if attribute.Key != "class" {
			continue
		}
		for _, current := range strings.Fields(attribute.Val) {
			if current == className {
				return true
			}
		}
	}
	return false
}

func htmlHasUnsafeForm(body string) bool {
	for _, tag := range formTagPattern.FindAllString(body, -1) {
		if postMethodPattern.MatchString(tag) {
			return true
		}
	}
	return false
}

func htmlHasUnsafeAction(body string) bool {
	return htmlHasUnsafeForm(body) || unsafeDataMethodPattern.MatchString(body)
}

func injectBrowserCSRF(body string, token string) string {
	hidden := `<input type="hidden" name="authenticity_token" value="` + token + `">`
	body = formBlockPattern.ReplaceAllStringFunc(body, func(form string) string {
		tag := formTagPattern.FindString(form)
		if !postMethodPattern.MatchString(tag) {
			return form
		}
		if authenticityTokenInputPattern.MatchString(form) {
			return authenticityTokenInputPattern.ReplaceAllString(form, hidden)
		}
		return strings.Replace(form, tag, tag+hidden, 1)
	})
	paramMeta := `<meta name="csrf-param" content="authenticity_token">`
	tokenMeta := `<meta name="csrf-token" content="` + token + `">`
	paramPresent := csrfParamMetaPattern.MatchString(body)
	tokenPresent := csrfTokenMetaPattern.MatchString(body)
	body = csrfParamMetaPattern.ReplaceAllString(body, paramMeta)
	body = csrfTokenMetaPattern.ReplaceAllString(body, tokenMeta)
	missingMeta := ""
	if !paramPresent {
		missingMeta += paramMeta
	}
	if !tokenPresent {
		missingMeta += tokenMeta
	}
	if missingMeta != "" {
		if index := strings.Index(strings.ToLower(body), "</head>"); index >= 0 {
			body = body[:index] + missingMeta + body[index:]
		} else {
			body = missingMeta + body
		}
	}
	return body
}

func stringLength(value int) string {
	return strconv.Itoa(value)
}
