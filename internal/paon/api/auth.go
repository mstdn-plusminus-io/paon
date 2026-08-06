package api

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/go-ldap/ldap/v3"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	dsig "github.com/russellhaering/goxmldsig"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sessionCookieName           = "paon_session"
	railsSessionCookieName      = "_mastodon_session"
	oauthJSONPayloadKey         = "paon.oauth_json_payload"
	omniauthStateCookie         = "paon_omniauth_state"
	omniauthNonceCookie         = "paon_omniauth_nonce"
	accessTokenUpdateEvery      = 24 * time.Hour
	railsTwoFactorAttemptsLimit = 10
	railsTwoFactorAttemptsTTL   = time.Hour
)

const (
	xmlencAES128CBC    = "http://www.w3.org/2001/04/xmlenc#aes128-cbc"
	xmlencAES192CBC    = "http://www.w3.org/2001/04/xmlenc#aes192-cbc"
	xmlencAES256CBC    = "http://www.w3.org/2001/04/xmlenc#aes256-cbc"
	xmlencAES128GCM    = "http://www.w3.org/2009/xmlenc11#aes128-gcm"
	xmlencAES192GCM    = "http://www.w3.org/2009/xmlenc11#aes192-gcm"
	xmlencAES256GCM    = "http://www.w3.org/2009/xmlenc11#aes256-gcm"
	xmlencRSAPKCS1     = "http://www.w3.org/2001/04/xmlenc#rsa-1_5"
	xmlencRSAOAEP      = "http://www.w3.org/2001/04/xmlenc#rsa-oaep-mgf1p"
	xmlencRSAOAEP11    = "http://www.w3.org/2009/xmlenc11#rsa-oaep"
	xmlencDigestSHA1   = "http://www.w3.org/2000/09/xmldsig#sha1"
	xmlencDigestSHA256 = "http://www.w3.org/2001/04/xmlenc#sha256"
)

var oidcHTTPClient = &http.Client{Timeout: 10 * time.Second}
var casHTTPClient = &http.Client{Timeout: 10 * time.Second}

func (s *Server) signInForm(c *echo.Context) error {
	next := c.QueryParam("redirect_to")
	if user, _, err := s.currentUser(c); err == nil && user != nil {
		return c.Redirect(http.StatusFound, s.afterSignInRedirectPath(user, next))
	}
	locale := s.webLocale(c, nil)
	loginLabel := webT(locale, "auth.login")
	title := webT(locale, "auth.sign_in.title", map[string]string{"domain": s.cfg.WebDomain})
	var body strings.Builder
	if !s.cfg.OmniAuthOnly {
		emailLabel := webT(locale, "simple_form.labels.defaults.email")
		if s.useSeamlessExternalLogin() {
			emailLabel = webT(locale, "simple_form.labels.defaults.username_or_email")
		}
		body.WriteString(simpleFormOpen("/auth/sign_in", "post"))
		body.WriteString(`<h1 class="title">` + html.EscapeString(title) + `</h1>`)
		body.WriteString(`<p class="lead">` + webT(locale, "auth.sign_in.preamble_html", map[string]string{"domain": html.EscapeString(s.cfg.WebDomain)}) + `</p>`)
		body.WriteString(`<input type="hidden" name="redirect_to" value="` + html.EscapeString(next) + `">`)
		body.WriteString(simpleTextInput(emailLabel, "user[email]", "", "email", `autocomplete="email" required autofocus`))
		body.WriteString(simpleTextInput(webT(locale, "simple_form.labels.defaults.password"), "user[password]", "", "password", `autocomplete="current-password" required`))
		body.WriteString(simpleSubmit(loginLabel))
		body.WriteString(simpleFormClose())
	}
	body.WriteString(s.omniauthAlternativeLoginHTML(locale))
	body.WriteString(s.authSharedFooterHTML("sessions", locale))
	c.Response().Header().Set("Content-Security-Policy", railsContentSecurityPolicyWithoutDirective(s.cfg, "form-action"))
	return c.HTML(http.StatusOK, authShellHTML(loginLabel, "", c.QueryParam("error"), body.String(), locale, s.settingStringValue("theme", "default")))
}

func (s *Server) useSeamlessExternalLogin() bool {
	return s != nil && (s.cfg.PAMEnabled || s.cfg.LDAPEnabled)
}

func (s *Server) omniauthAlternativeLoginHTML(locale string) string {
	providers := s.enabledOmniAuthProviders()
	if len(providers) == 0 {
		return ""
	}
	titleKey := "auth.or_log_in_with"
	if s.cfg.OmniAuthOnly {
		titleKey = "auth.log_in_with"
	}
	var body strings.Builder
	body.WriteString(`<div class="simple_form alternative-login"><h4>`)
	body.WriteString(html.EscapeString(webT(locale, titleKey)))
	body.WriteString(`</h4><div class="actions">`)
	for _, provider := range providers {
		label := s.omniauthProviderLabel(provider, locale)
		body.WriteString(`<a class="button button-`)
		body.WriteString(html.EscapeString(provider))
		body.WriteString(`" rel="nofollow" data-method="post" href="/auth/auth/`)
		body.WriteString(html.EscapeString(provider))
		body.WriteString(`">`)
		body.WriteString(html.EscapeString(label))
		body.WriteString(`</a>`)
	}
	body.WriteString(`</div></div>`)
	return body.String()
}

func (s *Server) enabledOmniAuthProviders() []string {
	if s == nil {
		return nil
	}
	providers := make([]string, 0, 3)
	if s.cfg.CASEnabled {
		providers = append(providers, "cas")
	}
	if s.cfg.SAMLEnabled {
		providers = append(providers, "saml")
	}
	if s.cfg.OIDCEnabled {
		providers = append(providers, "openid_connect")
	}
	return providers
}

func (s *Server) omniauthProviderLabel(provider string, locale string) string {
	switch provider {
	case "cas":
		if strings.TrimSpace(s.cfg.CASDisplayName) != "" {
			return s.cfg.CASDisplayName
		}
	case "saml":
		if strings.TrimSpace(s.cfg.SAMLDisplayName) != "" {
			return s.cfg.SAMLDisplayName
		}
	case "openid_connect":
		if strings.TrimSpace(s.cfg.OIDCDisplayName) != "" {
			return s.cfg.OIDCDisplayName
		}
	}
	label := webT(locale, "auth.providers."+provider)
	if label != "auth.providers."+provider {
		return label
	}
	return railsProviderLabelFallback(provider)
}

func railsProviderLabelFallback(provider string) string {
	label := strings.TrimSuffix(provider, "_oauth2")
	if label == "" {
		return label
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func (s *Server) signIn(c *echo.Context) error {
	if requestContainsWebauthnCredential(c.Request()) {
		return s.signInWithWebAuthn(c)
	}

	params, err := signInUserParams(c)
	if errors.Is(err, errSignInUserParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	email := params.Email
	password := params.Password
	otpAttempt := params.OTPAttempt
	redirectTo := strings.TrimSpace(c.FormValue("redirect_to"))
	if strings.TrimSpace(email) == "" && strings.TrimSpace(password) == "" && strings.TrimSpace(otpAttempt) != "" {
		return s.signInWithTwoFactorOTP(c, otpAttempt)
	}

	user, err := s.authenticateUserPassword(email, password)
	if err != nil {
		_ = s.clearBrowserTwoFactorAttempt(c)
		return c.Redirect(http.StatusFound, "/auth/sign_in?error="+url.QueryEscape(s.authInvalidSignInMessage(c, nil)))
	}

	hasWebAuthn, err := s.userHasWebauthnCredentials(user.ID)
	if err != nil {
		return err
	}
	if hasWebAuthn || user.OTPRequiredForLogin {
		if err := s.setBrowserTwoFactorAttempt(c, user.ID, s.afterSignInRedirectPath(user, redirectTo)); err != nil {
			return err
		}
		expireCookie(c, webauthnAttemptUserCookie, s.cfg.ForceSSL)
		expireCookie(c, webauthnAttemptRedirectCookie, s.cfg.ForceSSL)
		preferWebAuthn := hasWebAuthn && strings.TrimSpace(otpAttempt) == ""
		return c.HTML(http.StatusOK, twoFactorSignInHTML(s.packAssetPath("two_factor_authentication.js"), s.webLocale(c, user), hasWebAuthn, preferWebAuthn))
	}
	return s.finishWebSignIn(c, user, redirectTo)
}

type signInParams struct {
	Email      string
	Password   string
	OTPAttempt string
}

var errSignInUserParamsMissing = errors.New("sign in user root parameter is missing")

func signInUserParams(c *echo.Context) (signInParams, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return signInParams{}, err
	}
	const prefix = "user"
	if !formHasNestedPrefix(req.Form, prefix) {
		return signInParams{}, errSignInUserParamsMissing
	}
	return signInParams{
		Email:      lastFormValue(req.Form, prefix+"[email]"),
		Password:   lastFormValue(req.Form, prefix+"[password]"),
		OTPAttempt: lastFormValue(req.Form, prefix+"[otp_attempt]"),
	}, nil
}

func (s *Server) omniauthProviderEntry(c *echo.Context) error {
	if strings.TrimSpace(c.Param("provider")) == "cas" && s.cfg.CASEnabled {
		location, err := casLoginURL(s.cfg)
		if err == nil {
			return c.Redirect(http.StatusFound, location)
		}
	}
	if strings.TrimSpace(c.Param("provider")) == "saml" && s.cfg.SAMLEnabled {
		location, err := samlLoginURL(s.cfg)
		if err == nil {
			return c.Redirect(http.StatusFound, location)
		}
	}
	if strings.TrimSpace(c.Param("provider")) == "openid_connect" && s.cfg.OIDCEnabled {
		state := randomHex(16)
		nonce := ""
		if s.cfg.OIDCSendNonce {
			nonce = randomHex(16)
		}
		location, err := openIDConnectAuthorizationURL(s.cfg, state, nonce)
		if err == nil {
			if err := s.setBrowserOIDCState(c, state, nonce); err != nil {
				return err
			}
			expireCookie(c, omniauthStateCookie, s.cfg.ForceSSL)
			expireCookie(c, omniauthNonceCookie, s.cfg.ForceSSL)
			return c.Redirect(http.StatusFound, location)
		}
	}
	return c.Redirect(http.StatusFound, "/auth/sign_in?error="+url.QueryEscape(omniauthUnavailableMessage(c.Param("provider"))))
}

func (s *Server) omniauthCallback(c *echo.Context) error {
	provider := strings.TrimSpace(c.Param("provider"))
	authInfo := s.omniauthAuthInfoFromCallback(c, provider)
	if provider != "" {
		if provider == "openid_connect" && !s.omniauthCallbackStateValid(c) {
			return c.Redirect(http.StatusFound, "/auth/sign_in?error="+url.QueryEscape(omniauthUnavailableMessage(provider)))
		}
		if authInfo.UID == "" && provider == "cas" && requestRawParamValue(c, "ticket") != "" {
			resolvedAuth, err := s.casAuthFromCallback(c)
			if err != nil {
				return err
			}
			authInfo = resolvedAuth
		}
		if authInfo.UID == "" && provider == "saml" && requestRawParamValue(c, "SAMLResponse") != "" {
			resolvedAuth, err := s.samlAuthFromCallback(c)
			if err != nil {
				return err
			}
			authInfo = resolvedAuth
		}
		if authInfo.UID == "" && provider == "openid_connect" && requestRawParamValue(c, "code") != "" {
			resolvedAuth, err := s.openIDConnectAuthFromCallback(c)
			if err != nil {
				return err
			}
			authInfo = resolvedAuth
		}
	}
	if authInfo.Provider != "" && authInfo.UID != "" {
		var signedInUser *models.User
		if current, _, err := s.currentUser(c); err == nil {
			signedInUser = current
		}
		user, err := s.findOrCreateOmniAuthUser(authInfo, signedInUser)
		if err == nil && user != nil {
			if err := s.establishWebSessionWithSignIn(c, user, "omniauth", provider); err != nil {
				return err
			}
			if err := s.clearBrowserOIDCState(c); err != nil {
				return err
			}
			expireCookie(c, omniauthStateCookie, s.cfg.ForceSSL)
			expireCookie(c, omniauthNonceCookie, s.cfg.ForceSSL)
			if !omniauthUserEmailPresent(user.Email) {
				return c.Redirect(http.StatusFound, "/auth/setup?missing_email=1")
			}
			return c.Redirect(http.StatusFound, "/")
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/auth/sign_in?error="+url.QueryEscape(omniauthUnavailableMessage(c.Param("provider"))))
}

func (s *Server) omniauthLogout(c *echo.Context) error {
	if strings.TrimSpace(c.Param("provider")) == "openid_connect" && s.cfg.OIDCEnabled && strings.TrimSpace(s.cfg.OIDCEndSessionEndpoint) != "" {
		if location, err := openIDConnectLogoutURL(s.cfg); err == nil {
			return c.Redirect(http.StatusFound, location)
		}
	}
	return c.Redirect(http.StatusFound, "/auth/sign_in?notice="+url.QueryEscape(s.authSignedOutMessage(c, nil)))
}

func omniauthUnavailableMessage(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "External authentication is not available"
	}
	return "External authentication provider " + provider + " is not available"
}

func (s *Server) authInvalidSignInMessage(c *echo.Context, user *models.User) string {
	locale := s.webLocale(c, user)
	return settingsTVars(locale, "devise.failure.invalid", "Invalid %{authentication_keys} or password.", map[string]string{"authentication_keys": settingsT(locale, "simple_form.labels.defaults.email", "Email")})
}

func (s *Server) authSignedOutMessage(c *echo.Context, user *models.User) string {
	return settingsT(s.webLocale(c, user), "devise.sessions.signed_out", "Signed out successfully.")
}

func (s *Server) omniauthAuthInfoFromCallback(c *echo.Context, provider string) omniauthAuthInfo {
	auth := omniauthAuthInfo{
		Provider:  provider,
		UID:       omniauthCallbackUID(c),
		Email:     omniauthCallbackInfoValue(c, "verified_email", "email"),
		Name:      omniauthCallbackInfoValue(c, "full_name", "name"),
		FirstName: omniauthCallbackInfoValue(c, "first_name", "firstname", "given_name"),
		LastName:  omniauthCallbackInfoValue(c, "last_name", "lastname", "family_name"),
		Nickname:  omniauthCallbackInfoValue(c, "nickname", "preferred_username"),
		Image:     omniauthCallbackInfoValue(c, "image", "picture"),
	}
	auth.EmailVerified = omniauthCallbackInfoBool(c, "verified", "verified_email", "email_verified") || s.omniauthProviderAssumesVerifiedEmail(provider)
	if provider == "cas" {
		auth = applyCASCallbackInfoKeys(c, s.cfg, auth)
	}
	return auth
}

func omniauthCallbackInfoValue(c *echo.Context, keys ...string) string {
	for _, key := range keys {
		for _, name := range omniauthCallbackInfoParamNames(key) {
			if value := strings.TrimSpace(firstNonEmpty(c.QueryParam(name), c.FormValue(name))); value != "" {
				return value
			}
		}
	}
	return ""
}

func omniauthCallbackInfoBool(c *echo.Context, keys ...string) bool {
	for _, key := range keys {
		for _, name := range omniauthCallbackInfoParamNames(key) {
			if value := strings.TrimSpace(firstNonEmpty(c.QueryParam(name), c.FormValue(name))); value != "" && truthy(value) {
				return true
			}
		}
	}
	return false
}

func omniauthCallbackInfoParamNames(key string) []string {
	return []string{
		key,
		"info[" + key + "]",
		"omniauth[" + key + "]",
		"omniauth[info][" + key + "]",
		"user[" + key + "]",
	}
}

func (s *Server) omniauthProviderAssumesVerifiedEmail(provider string) bool {
	switch provider {
	case "cas":
		return s.cfg.CASSecurityAssumeEmailVerified
	case "openid_connect":
		return s.cfg.OIDCSecurityAssumeEmailVerified
	case "saml":
		return s.cfg.SAMLSecurityAssumeEmailVerified
	default:
		return false
	}
}

func applyCASCallbackInfoKeys(c *echo.Context, cfg config.Config, auth omniauthAuthInfo) omniauthAuthInfo {
	if auth.UID == "" {
		auth.UID = omniauthCallbackInfoValue(c, cfg.CASUIDKey, cfg.CASUIDField, "user")
	}
	auth.Email = firstNonEmpty(auth.Email, omniauthCallbackInfoValue(c, cfg.CASEmailKey, "email"))
	auth.Name = firstNonEmpty(auth.Name, omniauthCallbackInfoValue(c, cfg.CASNameKey, "name"))
	auth.FirstName = firstNonEmpty(auth.FirstName, omniauthCallbackInfoValue(c, cfg.CASFirstNameKey, "firstname", "first_name"))
	auth.LastName = firstNonEmpty(auth.LastName, omniauthCallbackInfoValue(c, cfg.CASLastNameKey, "lastname", "last_name"))
	auth.Nickname = firstNonEmpty(auth.Nickname, omniauthCallbackInfoValue(c, cfg.CASNicknameKey, "nickname"))
	auth.Location = firstNonEmpty(auth.Location, omniauthCallbackInfoValue(c, cfg.CASLocationKey, "location"))
	auth.Image = firstNonEmpty(auth.Image, omniauthCallbackInfoValue(c, cfg.CASImageKey, "image"))
	auth.Phone = firstNonEmpty(auth.Phone, omniauthCallbackInfoValue(c, cfg.CASPhoneKey, "phone"))
	auth.EmailVerified = auth.EmailVerified || cfg.CASSecurityAssumeEmailVerified
	return auth
}

func casLoginURL(cfg config.Config) (string, error) {
	endpoint, err := casEndpointURL(cfg, cfg.CASLoginURL, "/login")
	if err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("service", casServiceURL(cfg))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Server) casAuthFromCallback(c *echo.Context) (omniauthAuthInfo, error) {
	ticket := requestRawParamValue(c, "ticket")
	if ticket == "" {
		return omniauthAuthInfo{Provider: "cas"}, nil
	}
	endpoint, err := casEndpointURL(s.cfg, s.cfg.CASValidateURL, "/serviceValidate")
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	q := u.Query()
	q.Set("service", casServiceURL(s.cfg))
	q.Set("ticket", ticket)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	req.Header.Set("Accept", "application/xml, text/xml")
	client, err := casHTTPClientForConfig(s.cfg)
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return omniauthAuthInfo{}, errors.New("CAS serviceValidate endpoint returned non-success status")
	}
	auth, err := casAuthInfoFromServiceResponse(resp.Body, s.cfg)
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	return auth, nil
}

func casHTTPClientForConfig(cfg config.Config) (*http.Client, error) {
	if !cfg.CASDisableSSLVerification && strings.TrimSpace(cfg.CASCAPath) == "" {
		return casHTTPClient, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CASDisableSSLVerification {
		tlsConfig.InsecureSkipVerify = true
	}
	if path := strings.TrimSpace(cfg.CASCAPath); path != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if err := appendCASCAPem(pool, path); err != nil {
			return nil, err
		}
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: casHTTPClient.Timeout}, nil
}

func appendCASCAPem(pool *x509.CertPool, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !pool.AppendCertsFromPEM(data) {
			return errors.New("CAS_CA_PATH did not contain any PEM certificates")
		}
		return nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	loaded := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".pem") && !strings.HasSuffix(name, ".crt") && !strings.HasSuffix(name, ".cer") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return err
		}
		if pool.AppendCertsFromPEM(data) {
			loaded = true
		}
	}
	if !loaded {
		return errors.New("CAS_CA_PATH directory did not contain any PEM certificates")
	}
	return nil
}

func casEndpointURL(cfg config.Config, explicit string, path string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	if strings.TrimSpace(cfg.CASURL) != "" {
		base := strings.TrimRight(strings.TrimSpace(cfg.CASURL), "/")
		return base + path, nil
	}
	if strings.TrimSpace(cfg.CASHost) != "" {
		scheme := "http"
		if cfg.CASSL {
			scheme = "https"
		}
		host := strings.TrimSpace(cfg.CASHost)
		if strings.TrimSpace(cfg.CASPort) != "" {
			host = host + ":" + strings.TrimSpace(cfg.CASPort)
		}
		return scheme + "://" + host + path, nil
	}
	return "", errors.New("CAS endpoint is not configured")
}

func casServiceURL(cfg config.Config) string {
	if strings.TrimSpace(cfg.CASCallbackURL) != "" {
		return strings.TrimSpace(cfg.CASCallbackURL)
	}
	return strings.TrimRight(cfg.Scheme+"://"+cfg.WebDomain, "/") + "/auth/auth/cas/callback"
}

type casServiceResponse struct {
	AuthenticationSuccess *casAuthenticationSuccess `xml:"authenticationSuccess"`
	AuthenticationFailure *casAuthenticationFailure `xml:"authenticationFailure"`
}

type casAuthenticationSuccess struct {
	User       string        `xml:"user"`
	Attributes casAttributes `xml:"attributes"`
}

type casAuthenticationFailure struct {
	Code    string `xml:"code,attr"`
	Message string `xml:",chardata"`
}

type casAttributes struct {
	Values []casAttributeValue `xml:",any"`
}

type casAttributeValue struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

func casAuthInfoFromServiceResponse(r io.Reader, cfg config.Config) (omniauthAuthInfo, error) {
	var response casServiceResponse
	if err := xml.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&response); err != nil {
		return omniauthAuthInfo{}, err
	}
	if response.AuthenticationSuccess == nil {
		if response.AuthenticationFailure != nil {
			return omniauthAuthInfo{}, errors.New("CAS authentication failed")
		}
		return omniauthAuthInfo{}, errors.New("CAS serviceValidate response did not include authenticationSuccess")
	}
	success := response.AuthenticationSuccess
	attrs := success.Attributes.mapByLocalName()
	auth := omniauthAuthInfo{
		Provider:      "cas",
		UID:           firstNonEmpty(attrs[cfg.CASUIDKey], attrs[cfg.CASUIDField], success.User),
		Email:         attrs[cfg.CASEmailKey],
		EmailVerified: cfg.CASSecurityAssumeEmailVerified,
		Name:          attrs[cfg.CASNameKey],
		FirstName:     attrs[cfg.CASFirstNameKey],
		LastName:      attrs[cfg.CASLastNameKey],
		Nickname:      attrs[cfg.CASNicknameKey],
		Location:      attrs[cfg.CASLocationKey],
		Image:         attrs[cfg.CASImageKey],
		Phone:         attrs[cfg.CASPhoneKey],
	}
	if auth.UID == "" {
		return omniauthAuthInfo{}, errors.New("CAS serviceValidate response did not include a uid")
	}
	return auth, nil
}

func (attrs casAttributes) mapByLocalName() map[string]string {
	out := map[string]string{}
	for _, attr := range attrs.Values {
		key := strings.TrimSpace(attr.XMLName.Local)
		if key == "" {
			continue
		}
		if _, exists := out[key]; !exists {
			out[key] = strings.TrimSpace(attr.Value)
		}
	}
	return out
}

func samlLoginURL(cfg config.Config) (string, error) {
	endpoint := strings.TrimSpace(cfg.SAMLIDPSSOTargetURL)
	if endpoint == "" {
		return "", errors.New("SAML IdP SSO target URL is not configured")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("SAML IdP SSO target URL is invalid")
	}
	request, err := samlAuthnRequest(cfg)
	if err != nil {
		return "", err
	}
	deflated, err := samlDeflateRaw([]byte(request))
	if err != nil {
		return "", err
	}
	requestParam := base64.StdEncoding.EncodeToString(deflated)
	q := u.Query()
	for key, values := range samlTargetQueryParams(cfg.SAMLIDPSSOTargetParams) {
		if samlRedirectReservedParam(key) {
			continue
		}
		for _, value := range values {
			q.Add(key, value)
		}
	}
	q.Set("SAMLRequest", requestParam)
	if strings.TrimSpace(cfg.SAMLPrivateKey) != "" {
		signature, err := samlRedirectSignature(requestParam, cfg.SAMLPrivateKey)
		if err != nil {
			return "", err
		}
		q.Set("SigAlg", samlRedirectRSASHA256SignatureAlgorithm)
		q.Set("Signature", signature)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func samlTargetQueryParams(raw string) url.Values {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "?"))
	if raw == "" {
		return url.Values{}
	}
	sep := "&"
	if !strings.Contains(raw, "&") && strings.Contains(raw, ",") {
		sep = ","
	}
	pairs := strings.Split(raw, sep)
	cleaned := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if trimmed := strings.TrimSpace(pair); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	values, err := url.ParseQuery(strings.Join(cleaned, "&"))
	if err != nil {
		return url.Values{}
	}
	return values
}

func samlRedirectReservedParam(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "samlrequest", "samlresponse", "relaystate", "sigalg", "signature":
		return true
	default:
		return false
	}
}

const samlRedirectRSASHA256SignatureAlgorithm = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"

func samlRedirectSignature(request string, privateKeyPEM string) (string, error) {
	privateKey, err := parseSAMLRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	signed := "SAMLRequest=" + url.QueryEscape(request) + "&SigAlg=" + url.QueryEscape(samlRedirectRSASHA256SignatureAlgorithm)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func parseSAMLRSAPrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, errors.New("SAML_PRIVATE_KEY must be a PEM encoded RSA private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("SAML_PRIVATE_KEY must be a PEM encoded RSA private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("SAML_PRIVATE_KEY must be an RSA private key")
	}
	return key, nil
}

func samlAuthnRequest(cfg config.Config) (string, error) {
	acs := strings.TrimSpace(cfg.SAMLACSURL)
	issuer := strings.TrimSpace(cfg.SAMLIssuer)
	if acs == "" || issuer == "" {
		return "", errors.New("SAML ACS URL and issuer are required")
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	id := "_paon_" + randomHex(16)
	var b strings.Builder
	b.WriteString(`<samlp:AuthnRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="`)
	b.WriteString(html.EscapeString(id))
	b.WriteString(`" Version="2.0" IssueInstant="`)
	b.WriteString(html.EscapeString(now))
	b.WriteString(`" AssertionConsumerServiceURL="`)
	b.WriteString(html.EscapeString(acs))
	b.WriteString(`">`)
	b.WriteString(`<saml:Issuer>`)
	b.WriteString(html.EscapeString(issuer))
	b.WriteString(`</saml:Issuer>`)
	if format := strings.TrimSpace(cfg.SAMLNameIdentifierFormat); format != "" {
		b.WriteString(`<samlp:NameIDPolicy Format="`)
		b.WriteString(html.EscapeString(format))
		b.WriteString(`" AllowCreate="true"></samlp:NameIDPolicy>`)
	}
	b.WriteString(`</samlp:AuthnRequest>`)
	return b.String(), nil
}

func samlDeflateRaw(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Server) samlAuthFromCallback(c *echo.Context) (omniauthAuthInfo, error) {
	raw := requestRawParamValue(c, "SAMLResponse")
	if raw == "" {
		return omniauthAuthInfo{Provider: "saml"}, nil
	}
	xmlBytes, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	return samlAuthInfoFromResponse(xmlBytes, s.cfg)
}

type samlResponse struct {
	XMLName            xml.Name
	Status             samlStatus              `xml:"Status"`
	Assertion          *samlAssertion          `xml:"Assertion"`
	EncryptedAssertion *samlEncryptedAssertion `xml:"EncryptedAssertion"`
}

type samlStatus struct {
	StatusCode samlStatusCode `xml:"StatusCode"`
}

type samlStatusCode struct {
	Value string `xml:"Value,attr"`
}

type samlAssertion struct {
	Subject             samlSubject              `xml:"Subject"`
	Conditions          samlConditions           `xml:"Conditions"`
	AttributeStatements []samlAttributeStatement `xml:"AttributeStatement"`
}

type samlConditions struct {
	NotBefore    string `xml:"NotBefore,attr"`
	NotOnOrAfter string `xml:"NotOnOrAfter,attr"`
}

type samlSubject struct {
	NameID string `xml:"NameID"`
}

type samlAttributeStatement struct {
	Attributes []samlAttribute `xml:"Attribute"`
}

type samlAttribute struct {
	Name         string               `xml:"Name,attr"`
	FriendlyName string               `xml:"FriendlyName,attr"`
	Values       []samlAttributeValue `xml:"AttributeValue"`
}

type samlAttributeValue struct {
	Value string `xml:",chardata"`
}

type samlEncryptedAssertion struct{}

func samlAuthInfoFromResponse(xmlBytes []byte, cfg config.Config) (omniauthAuthInfo, error) {
	xmlText := string(xmlBytes)
	if strings.Contains(xmlText, "EncryptedAssertion") {
		decrypted, err := decryptSAMLEncryptedAssertions(xmlBytes, cfg)
		if err != nil {
			return omniauthAuthInfo{}, err
		}
		xmlBytes = decrypted
		xmlText = string(xmlBytes)
	} else if cfg.SAMLSecurityWantAssertionsEncrypted {
		return omniauthAuthInfo{}, errors.New("SAML encrypted assertions are required by this provider configuration but the response did not include an EncryptedAssertion")
	}
	hasSignature := strings.Contains(xmlText, "Signature")
	if hasSignature {
		if err := verifySAMLXMLSignature(xmlBytes, cfg); err != nil {
			return omniauthAuthInfo{}, err
		}
	} else if cfg.SAMLSecurityWantAssertionsSigned || strings.TrimSpace(cfg.SAMLIDPCert) != "" || strings.TrimSpace(cfg.SAMLIDPCertFingerprint) != "" || strings.TrimSpace(cfg.SAMLIDPCertFingerprintValidator) != "" {
		return omniauthAuthInfo{}, errors.New("SAML response is unsigned but this provider configuration requires signature validation")
	}
	var response samlResponse
	if err := xml.NewDecoder(bytes.NewReader(xmlBytes)).Decode(&response); err != nil {
		return omniauthAuthInfo{}, err
	}
	if response.Status.StatusCode.Value != "" && !strings.HasSuffix(response.Status.StatusCode.Value, ":Success") {
		return omniauthAuthInfo{}, errors.New("SAML response status is not success")
	}
	if response.Assertion == nil {
		return omniauthAuthInfo{}, errors.New("SAML response did not include an assertion")
	}
	if err := validateSAMLConditions(response.Assertion.Conditions, cfg, time.Now().UTC()); err != nil {
		return omniauthAuthInfo{}, err
	}
	attrs := response.Assertion.attributes()
	auth := omniauthAuthInfo{
		Provider:      "saml",
		UID:           firstNonEmpty(samlAttr(attrs, cfg.SAMLAttributeUID), samlAttr(attrs, cfg.SAMLUIDAttribute), response.Assertion.Subject.NameID),
		Email:         samlAttr(attrs, cfg.SAMLAttributeEmail, "email", "mail"),
		EmailVerified: cfg.SAMLSecurityAssumeEmailVerified || boolClaim(samlAttr(attrs, cfg.SAMLAttributeVerified, cfg.SAMLAttributeVerifiedEmail, "verified", "verified_email", "email_verified")),
		Name:          samlAttr(attrs, cfg.SAMLAttributeFullName, "full_name", "name", "displayName"),
		FirstName:     samlAttr(attrs, cfg.SAMLAttributeFirstName, "first_name", "givenName"),
		LastName:      samlAttr(attrs, cfg.SAMLAttributeLastName, "last_name", "sn"),
	}
	if auth.Email == "" {
		auth.Email = samlAttr(attrs, cfg.SAMLAttributeVerifiedEmail)
	}
	if auth.UID == "" {
		return omniauthAuthInfo{}, errors.New("SAML response did not include a uid")
	}
	return auth, nil
}

func decryptSAMLEncryptedAssertions(xmlBytes []byte, cfg config.Config) ([]byte, error) {
	if strings.TrimSpace(cfg.SAMLPrivateKey) == "" {
		return nil, errors.New("SAML encrypted assertions require SAML_PRIVATE_KEY")
	}
	privateKey, err := parseSAMLRSAPrivateKey(cfg.SAMLPrivateKey)
	if err != nil {
		return nil, err
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(xmlBytes); err != nil {
		return nil, err
	}
	root := doc.Root()
	encryptedAssertions := samlElementsByTag(root, "EncryptedAssertion")
	if len(encryptedAssertions) == 0 {
		return xmlBytes, nil
	}
	for _, encryptedAssertion := range encryptedAssertions {
		parent := encryptedAssertion.Parent()
		if parent == nil {
			return nil, errors.New("SAML EncryptedAssertion has no parent element")
		}
		assertion, err := decryptSAMLEncryptedAssertion(root, encryptedAssertion, privateKey)
		if err != nil {
			return nil, err
		}
		index := encryptedAssertion.Index()
		parent.RemoveChild(encryptedAssertion)
		parent.InsertChildAt(index, assertion)
	}
	out, err := doc.WriteToBytes()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func decryptSAMLEncryptedAssertion(root, encryptedAssertion *etree.Element, privateKey *rsa.PrivateKey) (*etree.Element, error) {
	encryptedData := samlFirstElementByTag(encryptedAssertion, "EncryptedData")
	if encryptedData == nil {
		return nil, errors.New("SAML EncryptedAssertion did not include EncryptedData")
	}
	encryptedKey, err := samlEncryptedKeyForEncryptedData(root, encryptedData)
	if err != nil {
		return nil, err
	}
	wrappedKey, err := samlCipherValue(encryptedKey)
	if err != nil {
		return nil, err
	}
	keyAlgorithm := samlEncryptionAlgorithm(encryptedKey)
	contentKey, err := decryptSAMLXMLKey(wrappedKey, keyAlgorithm, encryptedKey, privateKey)
	if err != nil {
		return nil, err
	}
	encryptedPayload, err := samlCipherValue(encryptedData)
	if err != nil {
		return nil, err
	}
	plaintext, err := decryptSAMLXMLData(encryptedPayload, samlEncryptionAlgorithm(encryptedData), contentKey)
	if err != nil {
		return nil, err
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(bytes.TrimSpace(plaintext)); err != nil {
		return nil, fmt.Errorf("SAML decrypted assertion XML is invalid: %w", err)
	}
	assertion := doc.Root()
	if assertion == nil || assertion.Tag != "Assertion" {
		return nil, errors.New("SAML decrypted EncryptedAssertion did not produce an Assertion element")
	}
	return assertion, nil
}

func samlEncryptedKeyForEncryptedData(root, encryptedData *etree.Element) (*etree.Element, error) {
	if encryptedKey := samlFirstElementByTag(encryptedData, "EncryptedKey"); encryptedKey != nil {
		return encryptedKey, nil
	}
	for _, keyInfo := range samlElementsByTag(encryptedData, "KeyInfo") {
		for _, retrievalMethod := range samlElementsByTag(keyInfo, "RetrievalMethod") {
			attr := retrievalMethod.SelectAttr("URI")
			if attr == nil || !strings.HasPrefix(strings.TrimSpace(attr.Value), "#") {
				continue
			}
			keyID := strings.TrimPrefix(strings.TrimSpace(attr.Value), "#")
			for _, candidate := range samlElementsByTag(root, "EncryptedKey") {
				if samlElementIDMatches(candidate, keyID) {
					return candidate, nil
				}
			}
		}
	}
	return nil, errors.New("SAML EncryptedAssertion did not include EncryptedKey")
}

func samlElementIDMatches(el *etree.Element, id string) bool {
	id = strings.TrimSpace(id)
	if el == nil || id == "" {
		return false
	}
	for _, attrName := range []string{"Id", "ID", "id"} {
		if attr := el.SelectAttr(attrName); attr != nil && strings.TrimSpace(attr.Value) == id {
			return true
		}
	}
	return false
}

func decryptSAMLXMLKey(ciphertext []byte, algorithm string, encryptedKey *etree.Element, privateKey *rsa.PrivateKey) ([]byte, error) {
	switch strings.TrimSpace(algorithm) {
	case "", xmlencRSAOAEP:
		return rsa.DecryptOAEP(sha1.New(), rand.Reader, privateKey, ciphertext, nil)
	case xmlencRSAOAEP11:
		digest := sha1.New()
		if digestMethod := samlFirstElementByTag(encryptedKey, "DigestMethod"); digestMethod != nil {
			if attr := digestMethod.SelectAttr("Algorithm"); attr != nil {
				switch attr.Value {
				case xmlencDigestSHA1:
					digest = sha1.New()
				case xmlencDigestSHA256:
					digest = sha256.New()
				default:
					return nil, fmt.Errorf("SAML encrypted key uses unsupported OAEP digest algorithm %q", attr.Value)
				}
			}
		}
		return rsa.DecryptOAEP(digest, rand.Reader, privateKey, ciphertext, nil)
	case xmlencRSAPKCS1:
		return rsa.DecryptPKCS1v15(rand.Reader, privateKey, ciphertext)
	default:
		return nil, fmt.Errorf("SAML encrypted key uses unsupported algorithm %q", algorithm)
	}
}

func decryptSAMLXMLData(ciphertext []byte, algorithm string, key []byte) ([]byte, error) {
	switch strings.TrimSpace(algorithm) {
	case xmlencAES128CBC, xmlencAES192CBC, xmlencAES256CBC:
		return decryptSAMLXMLAESCBC(ciphertext, key)
	case xmlencAES128GCM, xmlencAES192GCM, xmlencAES256GCM:
		return decryptSAMLXMLAESGCM(ciphertext, key)
	default:
		return nil, fmt.Errorf("SAML encrypted assertion uses unsupported data algorithm %q", algorithm)
	}
}

func decryptSAMLXMLAESCBC(ciphertext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("SAML encrypted assertion AES key is invalid: %w", err)
	}
	if len(ciphertext) < block.BlockSize()*2 || len(ciphertext)%block.BlockSize() != 0 {
		return nil, errors.New("SAML encrypted assertion AES-CBC payload is invalid")
	}
	iv := ciphertext[:block.BlockSize()]
	data := append([]byte(nil), ciphertext[block.BlockSize():]...)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(data, data)
	return pkcs7Unpad(data, block.BlockSize())
}

func decryptSAMLXMLAESGCM(ciphertext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("SAML encrypted assertion AES key is invalid: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("SAML encrypted assertion AES-GCM payload is invalid")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	data := ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, data, nil)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("SAML encrypted assertion padding is invalid")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, errors.New("SAML encrypted assertion padding is invalid")
	}
	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return nil, errors.New("SAML encrypted assertion padding is invalid")
		}
	}
	return data[:len(data)-padding], nil
}

func samlEncryptionAlgorithm(el *etree.Element) string {
	if method := samlFirstElementByTag(el, "EncryptionMethod"); method != nil {
		if attr := method.SelectAttr("Algorithm"); attr != nil {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

func samlCipherValue(el *etree.Element) ([]byte, error) {
	var cipherValue *etree.Element
	for _, child := range el.ChildElements() {
		if child.Tag != "CipherData" {
			continue
		}
		for _, cipherChild := range child.ChildElements() {
			if cipherChild.Tag == "CipherValue" {
				cipherValue = cipherChild
				break
			}
		}
		if cipherValue != nil {
			break
		}
	}
	if cipherValue == nil {
		cipherValue = samlFirstElementByTag(el, "CipherValue")
	}
	if cipherValue == nil {
		return nil, errors.New("SAML encrypted XML did not include CipherValue")
	}
	value := removePEMWhitespace(cipherValue.Text())
	if value == "" {
		return nil, errors.New("SAML encrypted XML included an empty CipherValue")
	}
	out, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("SAML encrypted XML CipherValue is invalid: %w", err)
	}
	return out, nil
}

func verifySAMLXMLSignature(xmlBytes []byte, cfg config.Config) error {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(xmlBytes); err != nil {
		return err
	}
	root := doc.Root()
	if root == nil {
		return errors.New("SAML response XML is empty")
	}
	if strings.TrimSpace(cfg.SAMLIDPCert) == "" && strings.TrimSpace(cfg.SAMLIDPCertFingerprint) == "" && strings.TrimSpace(cfg.SAMLIDPCertFingerprintValidator) == "" {
		return errors.New("SAML signed responses require SAML_IDP_CERT, SAML_IDP_CERT_FINGERPRINT, or SAML_IDP_CERT_FINGERPRINT_VALIDATOR")
	}
	var verified bool
	if hasXMLSignature(root) {
		if err := verifySAMLXMLElementSignature(root, cfg); err != nil {
			return err
		}
		verified = true
	}
	for _, assertion := range samlAssertionElements(root) {
		if !hasXMLSignature(assertion) {
			continue
		}
		if err := verifySAMLXMLElementSignature(assertion, cfg); err != nil {
			return err
		}
		verified = true
	}
	if !verified {
		return errors.New("SAML signed response did not include a verifiable Response or Assertion signature")
	}
	return nil
}

func verifySAMLXMLElementSignature(el *etree.Element, cfg config.Config) error {
	validationEl := samlElementWithInheritedNamespaces(el)
	roots, err := samlSignatureTrustedCertificates(validationEl, cfg)
	if err != nil {
		return err
	}
	ctx := dsig.NewDefaultValidationContext(&dsig.MemoryX509CertificateStore{Roots: roots})
	for _, attr := range []string{"ID", "Id", "id"} {
		if validationEl.SelectAttr(attr) != nil {
			ctx.IdAttribute = attr
			break
		}
	}
	if _, err := ctx.Validate(validationEl); err != nil {
		return fmt.Errorf("SAML XML signature validation failed: %w", err)
	}
	return nil
}

func samlElementWithInheritedNamespaces(el *etree.Element) *etree.Element {
	if el == nil {
		return nil
	}
	copyEl := el.Copy()
	var ancestors []*etree.Element
	for parent := el.Parent(); parent != nil; parent = parent.Parent() {
		ancestors = append(ancestors, parent)
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		for _, attr := range ancestors[i].Attr {
			if !samlIsNamespaceAttr(attr) || samlHasNamespaceAttr(copyEl, attr) {
				continue
			}
			if attr.Space == "" {
				copyEl.CreateAttr(attr.Key, attr.Value)
			} else {
				copyEl.CreateAttr(attr.Space+":"+attr.Key, attr.Value)
			}
		}
	}
	return copyEl
}

func samlIsNamespaceAttr(attr etree.Attr) bool {
	return attr.Key == "xmlns" || attr.Space == "xmlns"
}

func samlHasNamespaceAttr(el *etree.Element, attr etree.Attr) bool {
	if el == nil {
		return false
	}
	for _, existing := range el.Attr {
		if existing.Space == attr.Space && existing.Key == attr.Key {
			return true
		}
	}
	return false
}

func samlSignatureTrustedCertificates(el *etree.Element, cfg config.Config) ([]*x509.Certificate, error) {
	if roots, err := parseSAMLIDPCertificates(cfg.SAMLIDPCert); err != nil {
		return nil, err
	} else if len(roots) > 0 {
		if err := validateSAMLIDPCertFingerprintValidator(el, cfg); err != nil {
			return nil, err
		}
		return roots, nil
	}
	fingerprint := strings.TrimSpace(cfg.SAMLIDPCertFingerprint)
	if fingerprint == "" {
		return samlSignatureTrustedCertificatesFromValidator(el, cfg)
	}
	for _, cert := range samlSignatureKeyInfoCertificates(el) {
		if samlCertificateFingerprintMatches(cert, fingerprint) {
			if err := validateSAMLIDPCertFingerprintValidator(el, cfg); err != nil {
				return nil, err
			}
			return []*x509.Certificate{cert}, nil
		}
	}
	return nil, errors.New("SAML signature certificate fingerprint did not match SAML_IDP_CERT_FINGERPRINT")
}

func samlSignatureTrustedCertificatesFromValidator(el *etree.Element, cfg config.Config) ([]*x509.Certificate, error) {
	validator := strings.TrimSpace(cfg.SAMLIDPCertFingerprintValidator)
	if validator == "" {
		return nil, errors.New("SAML signed responses require SAML_IDP_CERT or SAML_IDP_CERT_FINGERPRINT")
	}
	for _, cert := range samlSignatureKeyInfoCertificates(el) {
		if samlCertificateFingerprintValidatorMatches(cert, validator) {
			return []*x509.Certificate{cert}, nil
		}
	}
	return nil, errors.New("SAML signature certificate fingerprint did not match SAML_IDP_CERT_FINGERPRINT_VALIDATOR")
}

func validateSAMLIDPCertFingerprintValidator(el *etree.Element, cfg config.Config) error {
	validator := strings.TrimSpace(cfg.SAMLIDPCertFingerprintValidator)
	if validator == "" {
		return nil
	}
	for _, cert := range samlSignatureKeyInfoCertificates(el) {
		if samlCertificateFingerprintValidatorMatches(cert, validator) {
			return nil
		}
	}
	return errors.New("SAML signature certificate fingerprint did not match SAML_IDP_CERT_FINGERPRINT_VALIDATOR")
}

func parseSAMLIDPCertificates(raw string) ([]*x509.Certificate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var certs []*x509.Certificate
	rest := []byte(raw)
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("SAML_IDP_CERT is invalid: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) > 0 {
		return certs, nil
	}
	der, err := base64.StdEncoding.DecodeString(removePEMWhitespace(raw))
	if err != nil {
		return nil, errors.New("SAML_IDP_CERT must be a PEM or base64 DER certificate")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("SAML_IDP_CERT is invalid: %w", err)
	}
	return []*x509.Certificate{cert}, nil
}

func samlSignatureKeyInfoCertificates(root *etree.Element) []*x509.Certificate {
	var certs []*x509.Certificate
	var walk func(*etree.Element)
	walk = func(el *etree.Element) {
		if el == nil {
			return
		}
		if el.Tag == "X509Certificate" {
			der, err := base64.StdEncoding.DecodeString(removePEMWhitespace(el.Text()))
			if err == nil {
				if cert, parseErr := x509.ParseCertificate(der); parseErr == nil {
					certs = append(certs, cert)
				}
			}
		}
		for _, child := range el.ChildElements() {
			walk(child)
		}
	}
	walk(root)
	return certs
}

func samlCertificateFingerprintMatches(cert *x509.Certificate, expected string) bool {
	normalized := normalizeCertificateFingerprint(expected)
	if normalized == "" {
		return false
	}
	sha1Sum := sha1.Sum(cert.Raw)
	if strings.EqualFold(hex.EncodeToString(sha1Sum[:]), normalized) {
		return true
	}
	sha256Sum := sha256.Sum256(cert.Raw)
	return strings.EqualFold(hex.EncodeToString(sha256Sum[:]), normalized)
}

func samlCertificateFingerprintValidatorMatches(cert *x509.Certificate, validator string) bool {
	validator = strings.TrimSpace(validator)
	if validator == "" {
		return false
	}
	sha1Sum := sha1.Sum(cert.Raw)
	railsFingerprint := colonizeFingerprint(hex.EncodeToString(sha1Sum[:]))
	if strings.Contains(validator, railsFingerprint) {
		return true
	}
	return samlCertificateFingerprintMatches(cert, validator)
}

func colonizeFingerprint(hexFingerprint string) string {
	hexFingerprint = strings.ToUpper(normalizeCertificateFingerprint(hexFingerprint))
	if len(hexFingerprint)%2 != 0 {
		return hexFingerprint
	}
	parts := make([]string, 0, len(hexFingerprint)/2)
	for i := 0; i < len(hexFingerprint); i += 2 {
		parts = append(parts, hexFingerprint[i:i+2])
	}
	return strings.Join(parts, ":")
}

func normalizeCertificateFingerprint(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'f') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func removePEMWhitespace(raw string) string {
	return strings.Join(strings.Fields(raw), "")
}

func hasXMLSignature(el *etree.Element) bool {
	if el == nil {
		return false
	}
	for _, child := range el.ChildElements() {
		if child.Tag == "Signature" {
			return true
		}
	}
	return false
}

func samlAssertionElements(root *etree.Element) []*etree.Element {
	return samlElementsByTag(root, "Assertion")
}

func samlFirstElementByTag(root *etree.Element, tag string) *etree.Element {
	for _, el := range samlElementsByTag(root, tag) {
		return el
	}
	return nil
}

func samlElementsByTag(root *etree.Element, tag string) []*etree.Element {
	var out []*etree.Element
	var walk func(*etree.Element)
	walk = func(el *etree.Element) {
		if el == nil {
			return
		}
		if el.Tag == tag {
			out = append(out, el)
		}
		for _, child := range el.ChildElements() {
			walk(child)
		}
	}
	walk(root)
	return out
}

func validateSAMLConditions(conditions samlConditions, cfg config.Config, now time.Time) error {
	drift := samlAllowedClockDrift(cfg.SAMLAllowedClockDrift)
	if notBefore := strings.TrimSpace(conditions.NotBefore); notBefore != "" {
		t, err := parseSAMLTime(notBefore)
		if err != nil {
			return fmt.Errorf("SAML assertion NotBefore is invalid: %w", err)
		}
		if now.Add(drift).Before(t) {
			return errors.New("SAML assertion is not yet valid")
		}
	}
	if notOnOrAfter := strings.TrimSpace(conditions.NotOnOrAfter); notOnOrAfter != "" {
		t, err := parseSAMLTime(notOnOrAfter)
		if err != nil {
			return fmt.Errorf("SAML assertion NotOnOrAfter is invalid: %w", err)
		}
		if !now.Add(-drift).Before(t) {
			return errors.New("SAML assertion has expired")
		}
	}
	return nil
}

func samlAllowedClockDrift(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if duration, err := time.ParseDuration(raw); err == nil && duration >= 0 {
		return duration
	}
	return 0
}

func parseSAMLTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", raw)
}

func (assertion samlAssertion) attributes() map[string]string {
	out := map[string]string{}
	for _, statement := range assertion.AttributeStatements {
		for _, attr := range statement.Attributes {
			value := ""
			for _, item := range attr.Values {
				if strings.TrimSpace(item.Value) != "" {
					value = strings.TrimSpace(item.Value)
					break
				}
			}
			for _, key := range []string{attr.Name, attr.FriendlyName} {
				key = strings.TrimSpace(key)
				if key != "" {
					if _, exists := out[key]; !exists {
						out[key] = value
					}
				}
			}
		}
	}
	return out
}

func samlAttr(attrs map[string]string, keys ...string) string {
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if value := strings.TrimSpace(attrs[key]); value != "" {
			return value
		}
	}
	return ""
}

func openIDConnectAuthorizationURL(cfg config.Config, state string, nonce string) (string, error) {
	if !cfg.OIDCEnabled {
		return "", errors.New("OIDC is not enabled")
	}
	u, err := openIDConnectClientEndpointURL(cfg, cfg.OIDCAuthEndpoint, "authorization")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", cfg.OIDCClientID)
	q.Set("redirect_uri", cfg.OIDCRedirectURI)
	q.Set("response_type", cfg.OIDCResponseType)
	if scope := openIDConnectScopeParam(cfg.OIDCScope); scope != "" {
		q.Set("scope", scope)
	}
	if strings.TrimSpace(state) != "" {
		q.Set("state", strings.TrimSpace(state))
	}
	if strings.TrimSpace(nonce) != "" {
		q.Set("nonce", strings.TrimSpace(nonce))
	}
	if cfg.OIDCResponseMode != "" || cfg.OIDCResponseModeSet {
		q.Set("response_mode", cfg.OIDCResponseMode)
	}
	if cfg.OIDCDisplay != "" || cfg.OIDCDisplaySet {
		q.Set("display", cfg.OIDCDisplay)
	}
	if cfg.OIDCPrompt != "" || cfg.OIDCPromptSet {
		q.Set("prompt", cfg.OIDCPrompt)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func openIDConnectLogoutURL(cfg config.Config) (string, error) {
	u, err := openIDConnectClientEndpointURL(cfg, cfg.OIDCEndSessionEndpoint, "end session")
	if err != nil {
		return "", err
	}
	if redirectURI := strings.TrimSpace(cfg.OIDCPostLogoutRedirectURI); redirectURI != "" {
		q := u.Query()
		q.Set("post_logout_redirect_uri", redirectURI)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func openIDConnectClientEndpointURL(cfg config.Config, raw string, label string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("OIDC %s endpoint is not configured", label)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("OIDC %s endpoint is invalid", label)
	}
	if u.IsAbs() {
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("OIDC %s endpoint is invalid", label)
		}
		return u, nil
	}
	host := strings.TrimSpace(cfg.OIDCHost)
	if host == "" {
		return nil, fmt.Errorf("OIDC %s endpoint is relative but OIDC host is not configured", label)
	}
	scheme := strings.TrimSpace(cfg.OIDCHTTPScheme)
	if port := strings.TrimSpace(cfg.OIDCPort); port != "" && !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, port)
	}
	path := u.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if scheme == "" {
		endpoint, err := url.Parse("//" + host + (&url.URL{Path: path, RawQuery: u.RawQuery}).String())
		if err != nil {
			return nil, fmt.Errorf("OIDC %s endpoint is invalid", label)
		}
		return endpoint, nil
	}
	return &url.URL{Scheme: scheme, Host: host, Path: path, RawQuery: u.RawQuery}, nil
}

func openIDConnectScopeParam(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, " ")
}

type openIDConnectTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
}

type openIDConnectIDTokenHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type openIDConnectJWKS struct {
	Keys []openIDConnectJWK `json:"keys"`
}

type openIDConnectJWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type omniauthAuthInfo struct {
	Provider      string
	UID           string
	Email         string
	EmailVerified bool
	Name          string
	FirstName     string
	LastName      string
	Nickname      string
	Image         string
	Location      string
	Phone         string
}

func (s *Server) openIDConnectUIDFromCallback(c *echo.Context) (string, error) {
	authInfo, err := s.openIDConnectAuthFromCallback(c)
	if err != nil {
		return "", err
	}
	return authInfo.UID, nil
}

func (s *Server) openIDConnectAuthFromCallback(c *echo.Context) (omniauthAuthInfo, error) {
	code := requestRawParamValue(c, "code")
	if code == "" {
		return omniauthAuthInfo{Provider: "openid_connect"}, nil
	}
	token, err := exchangeOpenIDConnectCode(c.Request().Context(), s.cfg, code)
	if err != nil {
		return omniauthAuthInfo{}, err
	}
	idTokenClaims := map[string]any{}
	if strings.TrimSpace(token.IDToken) != "" {
		if claims, err := verifyOpenIDConnectIDToken(c.Request().Context(), s.cfg, token.IDToken, time.Now().UTC()); err == nil {
			idTokenClaims = claims
		} else if strings.TrimSpace(s.cfg.OIDCJWKSURI) != "" || s.openIDConnectExpectedNonce(c) != "" {
			return omniauthAuthInfo{}, err
		}
	}
	if err := s.validateOpenIDConnectNonceClaim(c, idTokenClaims); err != nil {
		return omniauthAuthInfo{}, err
	}
	claims := map[string]any{}
	if strings.TrimSpace(token.AccessToken) != "" && strings.TrimSpace(s.cfg.OIDCUserInfoEndpoint) != "" {
		userInfo, err := fetchOpenIDConnectUserInfo(c.Request().Context(), s.cfg, token.AccessToken)
		if err != nil {
			return omniauthAuthInfo{}, err
		}
		for key, value := range userInfo {
			claims[key] = value
		}
	}
	for key, value := range idTokenClaims {
		if _, ok := claims[key]; !ok {
			claims[key] = value
		}
	}
	return omniauthAuthInfo{
		Provider:      "openid_connect",
		UID:           openIDConnectClaimString(claims, firstNonEmpty(s.cfg.OIDCUIDField, "sub")),
		Email:         openIDConnectEmailFromClaims(claims),
		EmailVerified: openIDConnectEmailVerified(claims, s.cfg),
		Name:          openIDConnectClaimString(claims, "name"),
		FirstName:     openIDConnectClaimString(claims, "given_name"),
		LastName:      openIDConnectClaimString(claims, "family_name"),
		Nickname:      openIDConnectClaimString(claims, "preferred_username"),
		Image:         openIDConnectClaimString(claims, "picture"),
	}, nil
}

func openIDConnectEmailFromClaims(claims map[string]any) string {
	return firstNonEmpty(openIDConnectClaimString(claims, "verified_email"), openIDConnectClaimString(claims, "email"))
}

func openIDConnectEmailVerified(claims map[string]any, cfg config.Config) bool {
	for _, key := range []string{"verified", "email_verified"} {
		if value, ok := claims[key]; ok && boolClaim(value) {
			return true
		}
	}
	return strings.TrimSpace(openIDConnectClaimString(claims, "verified_email")) != "" || cfg.OIDCSecurityAssumeEmailVerified
}

func (s *Server) openIDConnectExpectedNonce(c *echo.Context) string {
	_, nonce, ok := s.browserOIDCState(c)
	if !ok {
		return ""
	}
	return strings.TrimSpace(nonce)
}

func (s *Server) validateOpenIDConnectNonceClaim(c *echo.Context, claims map[string]any) error {
	expected := s.openIDConnectExpectedNonce(c)
	if expected == "" {
		return nil
	}
	actual := openIDConnectClaimString(claims, "nonce")
	if actual == "" {
		return errors.New("OIDC id_token nonce claim is missing")
	}
	if actual != expected {
		return errors.New("OIDC id_token nonce claim does not match")
	}
	return nil
}

func exchangeOpenIDConnectCode(ctx context.Context, cfg config.Config, code string) (openIDConnectTokenResponse, error) {
	endpoint, err := openIDConnectClientEndpointURL(cfg, cfg.OIDCTokenEndpoint, "token")
	if err != nil {
		return openIDConnectTokenResponse{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", cfg.OIDCRedirectURI)
	form.Set("client_id", cfg.OIDCClientID)
	if cfg.OIDCSendScopeToTokenEndpoint {
		if scope := openIDConnectScopeParam(cfg.OIDCScope); scope != "" {
			form.Set("scope", scope)
		}
	}
	authMethod := strings.TrimSpace(cfg.OIDCClientAuthMethod)
	if authMethod == "" {
		authMethod = "basic"
	}
	if authMethod == "post" || authMethod == "client_secret_post" {
		form.Set("client_secret", cfg.OIDCClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return openIDConnectTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if authMethod == "basic" || authMethod == "client_secret_basic" {
		req.SetBasicAuth(cfg.OIDCClientID, cfg.OIDCClientSecret)
	}
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return openIDConnectTokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return openIDConnectTokenResponse{}, errors.New("OIDC token endpoint returned non-success status")
	}
	var token openIDConnectTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return openIDConnectTokenResponse{}, err
	}
	if strings.TrimSpace(token.AccessToken) == "" && strings.TrimSpace(token.IDToken) == "" {
		return openIDConnectTokenResponse{}, errors.New("OIDC token endpoint response did not include an access_token or id_token")
	}
	return token, nil
}

func fetchOpenIDConnectUserInfo(ctx context.Context, cfg config.Config, accessToken string) (map[string]any, error) {
	endpoint, err := openIDConnectClientEndpointURL(cfg, cfg.OIDCUserInfoEndpoint, "userinfo")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, errors.New("OIDC userinfo endpoint returned non-success status")
	}
	claims := map[string]any{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func openIDConnectIDTokenClaims(idToken string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return nil, errors.New("OIDC id_token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	claims := map[string]any{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func verifyOpenIDConnectIDToken(ctx context.Context, cfg config.Config, idToken string, now time.Time) (map[string]any, error) {
	claims, err := openIDConnectIDTokenClaims(idToken)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.OIDCJWKSURI) == "" {
		return claims, nil
	}
	header, signingInput, signature, err := openIDConnectJWTParts(idToken)
	if err != nil {
		return nil, err
	}
	if err := verifyOpenIDConnectJWTSignature(ctx, cfg, header, signingInput, signature); err != nil {
		return nil, err
	}
	if err := validateOpenIDConnectRegisteredClaims(cfg, claims, now); err != nil {
		return nil, err
	}
	return claims, nil
}

func verifyOpenIDConnectJWTSignature(ctx context.Context, cfg config.Config, header openIDConnectIDTokenHeader, signingInput string, signature []byte) error {
	hash, digest, err := openIDConnectJWTDigest(header.Alg, signingInput)
	if err != nil {
		return err
	}
	if strings.HasPrefix(header.Alg, "HS") {
		if strings.TrimSpace(cfg.OIDCClientSecret) == "" {
			return errors.New("OIDC id_token HMAC verification requires OIDC_CLIENT_SECRET")
		}
		mac := hmac.New(hash.New, []byte(cfg.OIDCClientSecret))
		_, _ = mac.Write([]byte(signingInput))
		if !hmac.Equal(signature, mac.Sum(nil)) {
			return errors.New("OIDC id_token signature verification failed")
		}
		return nil
	}
	key, err := fetchOpenIDConnectJWK(ctx, cfg, header.Kid, header.Alg)
	if err != nil {
		return err
	}
	if strings.HasPrefix(header.Alg, "RS") {
		publicKey, err := key.rsaPublicKey()
		if err != nil {
			return err
		}
		if err := rsa.VerifyPKCS1v15(publicKey, hash, digest, signature); err != nil {
			return errors.New("OIDC id_token signature verification failed")
		}
		return nil
	}
	if strings.HasPrefix(header.Alg, "ES") {
		publicKey, size, err := key.ecdsaPublicKey()
		if err != nil {
			return err
		}
		if len(signature) != size*2 {
			return errors.New("OIDC id_token ECDSA signature has invalid length")
		}
		r := new(big.Int).SetBytes(signature[:size])
		s := new(big.Int).SetBytes(signature[size:])
		if !ecdsa.Verify(publicKey, digest, r, s) {
			return errors.New("OIDC id_token signature verification failed")
		}
		return nil
	}
	return errors.New("OIDC id_token uses unsupported signing algorithm")
}

func openIDConnectJWTDigest(alg, signingInput string) (crypto.Hash, []byte, error) {
	switch alg {
	case "RS256", "ES256", "HS256":
		digest := sha256.Sum256([]byte(signingInput))
		return crypto.SHA256, digest[:], nil
	case "RS384", "ES384", "HS384":
		digest := sha512.Sum384([]byte(signingInput))
		return crypto.SHA384, digest[:], nil
	case "RS512", "ES512", "HS512":
		digest := sha512.Sum512([]byte(signingInput))
		return crypto.SHA512, digest[:], nil
	default:
		return 0, nil, errors.New("OIDC id_token uses unsupported signing algorithm")
	}
}

func openIDConnectJWTParts(idToken string) (openIDConnectIDTokenHeader, string, []byte, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return openIDConnectIDTokenHeader{}, "", nil, errors.New("OIDC id_token is not a JWT")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return openIDConnectIDTokenHeader{}, "", nil, err
	}
	var header openIDConnectIDTokenHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return openIDConnectIDTokenHeader{}, "", nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return openIDConnectIDTokenHeader{}, "", nil, err
	}
	return header, parts[0] + "." + parts[1], signature, nil
}

func fetchOpenIDConnectJWK(ctx context.Context, cfg config.Config, kid, alg string) (openIDConnectJWK, error) {
	endpoint, err := openIDConnectClientEndpointURL(cfg, cfg.OIDCJWKSURI, "JWKS")
	if err != nil {
		return openIDConnectJWK{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return openIDConnectJWK{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return openIDConnectJWK{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return openIDConnectJWK{}, errors.New("OIDC JWKS endpoint returned non-success status")
	}
	var jwks openIDConnectJWKS
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&jwks); err != nil {
		return openIDConnectJWK{}, err
	}
	for _, key := range jwks.Keys {
		if strings.TrimSpace(kid) != "" && key.Kid != kid {
			continue
		}
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		if key.Alg != "" && key.Alg != alg {
			continue
		}
		if strings.HasPrefix(alg, "RS") && key.Kty != "RSA" {
			continue
		}
		if strings.HasPrefix(alg, "ES") && key.Kty != "EC" {
			continue
		}
		return key, nil
	}
	return openIDConnectJWK{}, errors.New("OIDC JWKS did not include a matching signing key")
}

func (key openIDConnectJWK) rsaPublicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, err
	}
	e := new(big.Int).SetBytes(eBytes).Int64()
	if e <= 0 || e > int64(^uint(0)>>1) {
		return nil, errors.New("OIDC JWK exponent is invalid")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e)}, nil
}

func (key openIDConnectJWK) ecdsaPublicKey() (*ecdsa.PublicKey, int, error) {
	var curve elliptic.Curve
	var size int
	switch key.Crv {
	case "P-256":
		curve, size = elliptic.P256(), 32
	case "P-384":
		curve, size = elliptic.P384(), 48
	case "P-521":
		curve, size = elliptic.P521(), 66
	default:
		return nil, 0, errors.New("OIDC JWK elliptic curve is unsupported")
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		return nil, 0, err
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
	if err != nil {
		return nil, 0, err
	}
	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	if !curve.IsOnCurve(x, y) {
		return nil, 0, errors.New("OIDC JWK elliptic curve point is invalid")
	}
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, size, nil
}

func validateOpenIDConnectRegisteredClaims(cfg config.Config, claims map[string]any, now time.Time) error {
	if issuer := strings.TrimSpace(cfg.OIDCIssuer); issuer != "" && openIDConnectClaimString(claims, "iss") != issuer {
		return errors.New("OIDC id_token issuer claim does not match")
	}
	if audience := strings.TrimSpace(cfg.OIDCClientID); audience != "" && !openIDConnectAudienceIncludes(claims["aud"], audience) {
		return errors.New("OIDC id_token audience claim does not include client id")
	}
	if exp := openIDConnectNumericClaim(claims["exp"]); exp > 0 && now.Unix() >= exp {
		return errors.New("OIDC id_token is expired")
	}
	return nil
}

func openIDConnectAudienceIncludes(value any, audience string) bool {
	switch typed := value.(type) {
	case string:
		return typed == audience
	case []any:
		for _, item := range typed {
			if itemString, ok := item.(string); ok && itemString == audience {
				return true
			}
		}
	}
	return false
}

func openIDConnectNumericClaim(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		i, _ := typed.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(typed, 10, 64)
		return i
	default:
		return 0
	}
}

func boolClaim(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return truthy(typed)
	case float64:
		return typed != 0
	case json.Number:
		i, _ := typed.Int64()
		return i != 0
	default:
		return false
	}
}

func openIDConnectClaimString(claims map[string]any, field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		field = "sub"
	}
	var value any = claims
	for _, part := range strings.Split(field, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[part]
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case []any:
		return normalizeOmniAuthUIDArray(typed)
	default:
		return ""
	}
}

func omniauthCallbackUID(c *echo.Context) string {
	return firstNonEmpty(
		c.QueryParam("uid"),
		c.FormValue("uid"),
		c.FormValue("omniauth[uid]"),
		c.FormValue("user[uid]"),
		normalizeOmniAuthUIDArray([]any{map[string]any{
			"uid":  c.FormValue("uid[0][uid]"),
			"user": c.FormValue("uid[0][user]"),
		}}),
		normalizeOmniAuthUIDArray([]any{map[string]any{
			"uid":  c.FormValue("omniauth[uid][0][uid]"),
			"user": c.FormValue("omniauth[uid][0][user]"),
		}}),
		normalizeOmniAuthUIDArray([]any{map[string]any{
			"uid":  c.FormValue("user[uid][0][uid]"),
			"user": c.FormValue("user[uid][0][user]"),
		}}),
	)
}

func normalizeOmniAuthUIDArray(values []any) string {
	if len(values) == 0 {
		return ""
	}
	first, ok := values[0].(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"uid", "user"} {
		switch typed := first[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		case json.Number:
			return typed.String()
		case float64:
			return strconv.FormatInt(int64(typed), 10)
		}
	}
	return ""
}

func (s *Server) omniauthCallbackStateValid(c *echo.Context) bool {
	expected, _, ok := s.browserOIDCState(c)
	if !ok || strings.TrimSpace(expected) == "" {
		return false
	}
	state := firstNonEmpty(c.QueryParam("state"), c.FormValue("state"))
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(state)), []byte(strings.TrimSpace(expected))) == 1
}

func omniauthUserEmailPresent(email string) bool {
	email = strings.TrimSpace(email)
	return email != "" && !strings.HasPrefix(email, "change@me")
}

func (s *Server) findOrCreateOmniAuthUser(auth omniauthAuthInfo, signedInResource *models.User) (*models.User, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	auth.Provider = strings.TrimSpace(auth.Provider)
	auth.UID = strings.TrimSpace(auth.UID)
	if auth.Provider == "" || auth.UID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var out *models.User
	var createdAccountID int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var identity models.Identity
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Preload("User").
			Where("provider = ? AND uid = ?", auth.Provider, auth.UID).
			First(&identity).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			identity = models.Identity{Provider: auth.Provider, UID: auth.UID, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&identity).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if user, shouldAttach, ok := omniauthSignedInOrIdentityUser(signedInResource, identity); ok {
			if shouldAttach {
				if err := tx.Model(&models.Identity{}).Where("id = ?", identity.ID).Updates(map[string]any{
					"user_id":    sql.NullInt64{Int64: user.ID, Valid: true},
					"updated_at": now,
				}).Error; err != nil {
					return err
				}
			}
			out = user
			return nil
		}
		if identity.UserID.Valid && identity.User.ID != 0 {
			if !userCanUseAuthenticatedAPI(identity.User) {
				return gorm.ErrRecordNotFound
			}
			out = &identity.User
			return nil
		}
		user, err := s.reattachOmniAuthUser(tx, auth)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			user, err = s.createUserForOmniAuth(tx, auth, now)
			if err == nil {
				createdAccountID = user.AccountID
			}
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&models.Identity{}).Where("id = ?", identity.ID).Updates(map[string]any{
			"user_id":    sql.NullInt64{Int64: user.ID, Valid: true},
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		out = user
		return nil
	})
	if err == nil && createdAccountID != 0 {
		s.triggerAccountWebhook("account.created", createdAccountID)
	}
	return out, err
}

func omniauthSignedInOrIdentityUser(signedInResource *models.User, identity models.Identity) (*models.User, bool, bool) {
	if signedInResource != nil && signedInResource.ID != 0 {
		return signedInResource, !identity.UserID.Valid, true
	}
	return nil, false, false
}

func (s *Server) findOmniAuthIdentityUser(provider string, uid string) (*models.User, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	provider = strings.TrimSpace(provider)
	uid = strings.TrimSpace(uid)
	if provider == "" || uid == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var identity models.Identity
	err := s.db.Preload("User").
		Where("provider = ? AND uid = ?", provider, uid).
		First(&identity).Error
	if err != nil {
		return nil, err
	}
	if !identity.UserID.Valid || identity.User.ID == 0 || !userCanUseAuthenticatedAPI(identity.User) {
		return nil, gorm.ErrRecordNotFound
	}
	return &identity.User, nil
}

func (s *Server) reattachOmniAuthUser(tx *gorm.DB, auth omniauthAuthInfo) (*models.User, error) {
	if os.Getenv("ALLOW_UNSAFE_AUTH_PROVIDER_REATTACH") != "true" || !auth.EmailVerified {
		return nil, gorm.ErrRecordNotFound
	}
	email := strings.ToLower(strings.TrimSpace(auth.Email))
	if email == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var user models.User
	if err := tx.Preload("Account").Where("lower(email) = ?", email).First(&user).Error; err != nil {
		return nil, err
	}
	var count int64
	if err := tx.Model(&models.Identity{}).Where("provider = ? AND user_id = ?", auth.Provider, user.ID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 || !userCanUseAuthenticatedAPI(user) {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}

func (s *Server) createUserForOmniAuth(tx *gorm.DB, auth omniauthAuthInfo, now time.Time) (*models.User, error) {
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		return nil, err
	}
	username, err := uniqueOmniAuthUsername(tx, auth.UID)
	if err != nil {
		return nil, err
	}
	account := models.Account{
		Username:    username,
		DisplayName: omniauthDisplayName(auth),
		PrivateKey:  sql.NullString{String: privateKey, Valid: true},
		PublicKey:   publicKey,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if supportedOmniAuthImageURL(auth.Image) {
		account.AvatarRemoteURL = sql.NullString{String: strings.TrimSpace(auth.Image), Valid: true}
	}
	if err := tx.Create(&account).Error; err != nil {
		return nil, err
	}
	stat := models.AccountStat{AccountID: account.ID, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&stat).Error; err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(auth.Email))
	if email == "" {
		email = "change@me-" + strings.TrimSpace(auth.UID) + "-" + strings.TrimSpace(auth.Provider) + ".com"
	}
	user := models.User{
		AccountID:         account.ID,
		Email:             email,
		EncryptedPassword: "",
		Locale:            railsUserLocaleValue(""),
		CreatedAt:         now,
		UpdatedAt:         now,
		Approved:          true,
	}
	if auth.EmailVerified {
		user.ConfirmedAt = sql.NullTime{Time: now, Valid: true}
	} else {
		confirmationToken := randomHex(16)
		user.ConfirmationToken = sql.NullString{String: deviseTokenForStorage(confirmationToken, deviseConfirmationTokenColumn, s.cfg.SecretKeyBase), Valid: true}
		user.ConfirmationSentAt = sql.NullTime{Time: now, Valid: true}
	}
	if err := tx.Create(&user).Error; err != nil {
		return nil, err
	}
	user.Account = &account
	return &user, nil
}

var omniauthUsernameInvalidChars = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func uniqueOmniAuthUsername(tx *gorm.DB, uid string) (string, error) {
	base := strings.Split(strings.TrimSpace(uid), "@")[0]
	base = omniauthUsernameInvalidChars.ReplaceAllString(base, "")
	base = railsAccountUsernameValue(base)
	if len(base) > 30 {
		base = base[:30]
	}
	if base == "" {
		base = "user"
	}
	username := base
	for i := 0; ; {
		var count int64
		if err := tx.Model(&models.Account{}).Where("lower(username) = lower(?) AND domain IS NULL", username).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return username, nil
		}
		i++
		username = base + "_" + strconv.Itoa(i)
		if len(username) > 30 {
			suffix := "_" + strconv.Itoa(i)
			username = base[:30-len(suffix)] + suffix
		}
	}
}

func omniauthDisplayName(auth omniauthAuthInfo) string {
	name := firstNonEmpty(auth.Name, strings.TrimSpace(auth.FirstName+" "+auth.LastName), auth.Nickname)
	if len(name) > 30 {
		return name[:30]
	}
	return name
}

func supportedOmniAuthImageURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && strings.TrimSpace(u.Host) != ""
}

func (s *Server) signInWithTwoFactorOTP(c *echo.Context, otpAttempt string) error {
	user, err := s.webauthnAttemptUser(c)
	if err != nil || !user.OTPRequiredForLogin {
		return c.Redirect(http.StatusFound, "/auth/sign_in?error="+url.QueryEscape(s.authInvalidSignInMessage(c, nil)))
	}
	if limited, err := s.consumeTwoFactorAttempt(c.Request().Context(), user.ID, time.Now().UTC()); err != nil {
		return err
	} else if limited {
		_ = s.clearBrowserTwoFactorAttempt(c)
		locale := s.webLocale(c, user)
		hasWebAuthn, _ := s.userHasWebauthnCredentials(user.ID)
		return c.HTML(http.StatusOK, twoFactorSignInHTML(s.packAssetPath("two_factor_authentication.js"), locale, hasWebAuthn, false, webT(locale, "users.rate_limited")))
	}
	if err := s.validateAndConsumeUserOTP(user, otpAttempt, time.Now().UTC()); err != nil {
		_ = s.recordLoginActivity(user.ID, "otp", false, "invalid_otp_token", c.RealIP(), c.Request().UserAgent())
		locale := s.webLocale(c, user)
		hasWebAuthn, _ := s.userHasWebauthnCredentials(user.ID)
		return c.HTML(http.StatusOK, twoFactorSignInHTML(s.packAssetPath("two_factor_authentication.js"), locale, hasWebAuthn, false, webT(locale, "users.invalid_otp_token")))
	}
	_, redirectTo, _ := s.browserTwoFactorAttempt(c)
	if err := s.clearBrowserTwoFactorAttempt(c); err != nil {
		return err
	}
	expireCookie(c, webauthnChallengeCookie, s.cfg.ForceSSL)
	expireCookie(c, webauthnAttemptUserCookie, s.cfg.ForceSSL)
	expireCookie(c, webauthnAttemptRedirectCookie, s.cfg.ForceSSL)
	return s.finishWebSignInWithMethod(c, user, redirectTo, "otp", "")
}

func (s *Server) signInWithWebAuthn(c *echo.Context) error {
	user, err := s.webauthnAttemptUser(c)
	if err != nil {
		return apiError(c, http.StatusUnauthorized, "WebAuthn challenge is missing or expired")
	}
	challenge, ok := s.browserWebAuthnChallenge(c, user.ID, "login")
	if !ok {
		return apiError(c, http.StatusUnauthorized, "WebAuthn challenge is missing or expired")
	}
	provider, err := s.webauthnProvider()
	if err != nil {
		return err
	}
	credentials, err := s.webauthnCredentialsForUser(user.ID)
	if err != nil {
		return err
	}
	webauthnUser, err := s.webauthnUser(user)
	if err != nil {
		return err
	}
	credentialRequest, _, err := webauthnCredentialRequest(c.Request())
	if err != nil {
		return err
	}
	credential, err := provider.FinishLogin(webauthnUser, webauthnLoginSession(user, challenge, webauthnRPID(s.cfg.WebDomain), credentials), credentialRequest)
	if err != nil {
		_ = s.recordLoginActivity(user.ID, "webauthn", false, "invalid_credential", c.RealIP(), c.Request().UserAgent())
		return apiError(c, http.StatusUnprocessableEntity, "WebAuthn credential is invalid")
	}
	if err := s.updateWebauthnCredentialSignCount(user.ID, credentials, credential.ID, int64(credential.Authenticator.SignCount)); err != nil {
		return err
	}
	_ = s.clearTwoFactorAttempts(c.Request().Context(), user.ID, time.Now().UTC())
	_, redirectTo, _ := s.browserTwoFactorAttempt(c)
	if err := s.establishWebSessionWithSignIn(c, user, "webauthn", ""); err != nil {
		return err
	}
	if err := s.clearBrowserTwoFactorAttempt(c); err != nil {
		return err
	}
	expireCookie(c, webauthnChallengeCookie, s.cfg.ForceSSL)
	expireCookie(c, webauthnAttemptUserCookie, s.cfg.ForceSSL)
	expireCookie(c, webauthnAttemptRedirectCookie, s.cfg.ForceSSL)
	return c.JSON(http.StatusOK, map[string]string{"redirect_path": s.afterSignInRedirectPath(user, redirectTo)})
}

func twoFactorSignInHTML(twoFactorScriptPath string, locale string, webauthnEnabled bool, preferWebAuthn bool, errors ...string) string {
	errorText := ""
	if len(errors) > 0 {
		errorText = errors[0]
	}
	script := ""
	if strings.TrimSpace(twoFactorScriptPath) != "" {
		script = `<script src="` + html.EscapeString(twoFactorScriptPath) + `" crossorigin="anonymous" defer></script>`
	}
	title := webT(locale, "settings.two_factor_authentication")
	var body strings.Builder
	if webauthnEnabled {
		webauthnClass := ""
		if !preferWebAuthn {
			webauthnClass = ` class="hidden"`
		}
		body.WriteString(`<form method="post" action="/auth/sign_in" id="webauthn-form"` + webauthnClass + `>
      <h3>` + html.EscapeString(webT(locale, "settings.webauthn_authentication")) + `</h3>
      <p class="lead">` + html.EscapeString(webT(locale, "auth.link_to_webauth")) + `</p>
      <button class="btn js-webauthn" type="submit">` + html.EscapeString(webT(locale, "auth.link_to_webauth")) + `</button>
      <p><a href="#" id="link-to-otp">` + html.EscapeString(webT(locale, "auth.link_to_otp")) + `</a></p>
    </form>`)
	}
	otpClass := ""
	if webauthnEnabled && preferWebAuthn {
		otpClass = ` class="hidden"`
	}
	body.WriteString(`<form method="post" action="/auth/sign_in" id="otp-authentication-form"` + otpClass + `>
      <p class="lead">` + html.EscapeString(webT(locale, "auth.link_to_otp")) + `</p>
      <input name="user[otp_attempt]" inputmode="numeric" autocomplete="one-time-code" required autofocus placeholder="` + html.EscapeString(webT(locale, "simple_form.labels.defaults.otp_attempt")) + `">
      <button type="submit">` + html.EscapeString(webT(locale, "auth.login")) + `</button>`)
	if webauthnEnabled {
		body.WriteString(`
      <p><a href="#" id="link-to-webauthn">` + html.EscapeString(webT(locale, "auth.link_to_webauth")) + `</a></p>`)
	}
	body.WriteString(`
    </form>
    <p class="flash-message hidden" id="unsupported-browser-message">` + html.EscapeString(settingsT(locale, "webauthn_credentials.not_supported", "This browser does not support security keys.")) + `</p>
    <p class="flash-message error hidden" id="security-key-error-message">` + html.EscapeString(settingsT(locale, "webauthn_credentials.authentication_failed", "Security key authentication failed.")) + `</p>`)
	return authShellHTML(title, "", errorText, body.String()+script, locale)
}

func (s *Server) finishWebSignIn(c *echo.Context, user *models.User, redirectTo string) error {
	if err := s.establishWebSession(c, user); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, s.afterSignInRedirectPath(user, redirectTo))
}

func (s *Server) finishWebSignInWithMethod(c *echo.Context, user *models.User, redirectTo string, method string, provider string) error {
	_ = s.clearTwoFactorAttempts(c.Request().Context(), user.ID, time.Now().UTC())
	if err := s.establishWebSessionWithSignIn(c, user, method, provider); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, s.afterSignInRedirectPath(user, redirectTo))
}

func (s *Server) consumeTwoFactorAttempt(ctx context.Context, userID int64, now time.Time) (bool, error) {
	key := twoFactorAttemptsRedisKey(redisConfig(s.cfg).prefix, userID, now)
	if key == "" {
		return false, nil
	}
	redisCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	value, err := s.redisCommand(redisCtx, "INCR", key)
	if err != nil {
		return false, err
	}
	if redisInt(value) == 1 {
		_, _ = s.redisCommand(redisCtx, "EXPIRE", key, strconv.FormatInt(int64(railsTwoFactorAttemptsTTL/time.Second), 10))
	}
	return redisInt(value) >= railsTwoFactorAttemptsLimit, nil
}

func (s *Server) clearTwoFactorAttempts(ctx context.Context, userID int64, now time.Time) error {
	key := twoFactorAttemptsRedisKey(redisConfig(s.cfg).prefix, userID, now)
	if key == "" {
		return nil
	}
	redisCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err := s.redisCommand(redisCtx, "DEL", key)
	return err
}

func twoFactorAttemptsRedisKey(prefix string, userID int64, now time.Time) string {
	if userID <= 0 {
		return ""
	}
	return prefix + "2fa_auth_attempts:" + strconv.FormatInt(userID, 10) + ":" + strconv.Itoa(now.UTC().Hour())
}

func (s *Server) afterSignInRedirectPath(user *models.User, redirectTo string) string {
	redirectTo = strings.TrimSpace(redirectTo)
	if redirectTo == "" {
		return "/"
	}
	path := safeRedirect(redirectTo)
	if path == "/about" || path == "/explore" {
		return "/"
	}
	if s.singleUserModeActive() && path == s.shortAccountPathForUser(user) {
		return "/"
	}
	return path
}

func (s *Server) singleUserModeActive() bool {
	if s == nil || !s.cfg.SingleUserMode {
		return false
	}
	if s.db == nil {
		return true
	}
	var count int64
	if err := s.db.Model(&models.Account{}).Where("id > 0").Limit(1).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func (s *Server) shortAccountPathForUser(user *models.User) string {
	if user == nil {
		return ""
	}
	if user.Account != nil && strings.TrimSpace(user.Account.Username) != "" && !user.Account.Domain.Valid {
		return "/@" + url.PathEscape(user.Account.Username)
	}
	if s == nil || s.db == nil || user.AccountID <= 0 {
		return ""
	}
	var account models.Account
	if err := s.db.Select("username, domain").Where("id = ?", user.AccountID).First(&account).Error; err != nil {
		return ""
	}
	if strings.TrimSpace(account.Username) == "" || account.Domain.Valid {
		return ""
	}
	return "/@" + url.PathEscape(account.Username)
}

func (s *Server) establishWebSession(c *echo.Context, user *models.User) error {
	return s.establishWebSessionWithSignIn(c, user, "password", "")
}

func (s *Server) establishWebSessionWithSignIn(c *echo.Context, user *models.User, method string, provider string) error {
	token, err := s.issueAccessToken(user, "read write follow push")
	if err != nil {
		return err
	}
	_ = s.recordSignInWithMethod(user, c.RealIP(), c.Request().UserAgent(), method, provider)
	if err := s.recordSessionActivation(user.ID, token.ID, c); err != nil {
		return err
	}
	return s.setSessionCookie(c, token.Token)
}

func (s *Server) signOut(c *echo.Context) error {
	if token := sessionToken(c); token != "" && s.db != nil {
		_ = s.revokeSessionToken(token)
	} else if s.db != nil {
		_ = s.revokeRailsSessionActivationForRequest(c)
	}
	clearSessionCookie(c, s.cfg.ForceSSL)
	return c.Redirect(http.StatusFound, s.signOutRedirectPath())
}

func (s *Server) signOutRedirectPath() string {
	if s != nil && s.cfg.OmniAuthOnly && s.cfg.OIDCEnabled {
		return "/auth/auth/openid_connect/logout"
	}
	return "/auth/sign_in"
}

func signOutRedirectPath() string {
	return (&Server{cfg: config.FromEnv()}).signOutRedirectPath()
}

func railsEnvTrue(name string) bool {
	return os.Getenv(name) == "true"
}

func (s *Server) suspiciousSignInDisabled() bool {
	return s != nil && s.cfg.SuspiciousSignInDisabled
}

func (s *Server) oauthToken(c *echo.Context) error {
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	if _, err := oauthRequestJSONPayload(c); err != nil {
		return oauthTokenErrorf(http.StatusBadRequest, "invalid_request", "Invalid request body")
	}

	grantType := oauthParam(c, "grant_type")
	if grantType == "" {
		return oauthTokenErrorf(http.StatusBadRequest, "invalid_request", "The grant type is missing")
	}
	switch grantType {
	case "authorization_code":
		token, err := s.exchangeAuthorizationCode(c)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, tokenResponse(token))
	case "password":
		app, hasApp, err := s.oauthApplicationFromOptionalTokenRequest(c)
		if err != nil {
			return invalidOAuthClientError()
		}
		user, err := s.authenticateUserPassword(oauthParam(c, "username"), oauthParam(c, "password"))
		if err != nil || user.OTPRequiredForLogin {
			return oauthTokenErrorf(http.StatusBadRequest, "invalid_grant", "The user credentials were incorrect")
		}
		scopes := oauthScopeParam(c, "read write follow push")
		if hasApp {
			scopes = normalizeRequestedScopes(scopes, app.Scopes)
		}
		token, err := s.issueAccessTokenForApplication(user, app, scopes)
		if err != nil {
			return err
		}
		_ = s.recordSignIn(user, c.RealIP(), c.Request().UserAgent())
		return c.JSON(http.StatusOK, tokenResponse(token))
	case "client_credentials":
		app, err := s.oauthApplicationFromTokenRequest(c)
		if err != nil {
			return invalidOAuthClientError()
		}
		scopes := normalizeRequestedScopes(oauthScopeParam(c, "read"), app.Scopes)
		token, err := s.issueApplicationTokenForApplication(app, scopes)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, tokenResponse(token))
	default:
		return oauthTokenErrorf(http.StatusBadRequest, "unsupported_grant_type", "Unsupported grant type")
	}
}

func (s *Server) oauthRevoke(c *echo.Context) error {
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	if _, err := oauthRequestJSONPayload(c); err != nil {
		return apiError(c, http.StatusBadRequest, "Invalid request body")
	}
	app, err := s.oauthApplicationFromTokenRequest(c)
	if err != nil {
		return apiError(c, http.StatusUnauthorized, "The client credentials were incorrect")
	}
	tokenValue := oauthRawParamValue(c, "token")
	if tokenValue == "" {
		return c.NoContent(http.StatusOK)
	}
	now := time.Now().UTC()
	var revokedTokenID int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var token models.OAuthAccessToken
		if err := tx.Where("token = ? OR refresh_token = ?", tokenValue, tokenValue).First(&token).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if !oauthRevocationMatchesApplication(token, tokenValue, app.ID) {
			return nil
		}
		revokedTokenID = token.ID
		if err := tx.Where("access_token_id = ?", token.ID).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		return tx.Model(&models.OAuthAccessToken{}).Where("id = ?", token.ID).Update("revoked_at", now).Error
	})
	if err != nil {
		return err
	}
	if revokedTokenID != 0 {
		s.publishAccessTokenKills([]int64{revokedTokenID})
	}
	return c.NoContent(http.StatusOK)
}

func (s *Server) oauthApplicationsForbidden(c *echo.Context) error {
	return c.NoContent(http.StatusForbidden)
}

func (s *Server) oauthTokenInfo(c *echo.Context) error {
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	tokenValue := requestToken(c)
	if strings.TrimSpace(tokenValue) == "" {
		return invalidOAuthTokenError()
	}
	var token models.OAuthAccessToken
	if err := s.db.Where("token = ? AND revoked_at IS NULL", tokenValue).First(&token).Error; err != nil {
		return invalidOAuthTokenError()
	}
	now := time.Now().UTC()
	if oauthAccessTokenExpired(token, now) {
		return invalidOAuthTokenError()
	}
	appUID := ""
	if token.ApplicationID.Valid {
		var app models.OAuthApplication
		if err := s.db.Select("uid").Where("id = ?", token.ApplicationID.Int64).First(&app).Error; err == nil {
			appUID = app.UID
		}
	}
	if err := s.trackAccessTokenUse(c, &token); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, tokenInfoResponse(token, appUID, now))
}

func oauthRevocationMatchesApplication(token models.OAuthAccessToken, tokenValue string, applicationID int64) bool {
	if tokenValue == "" || !token.ApplicationID.Valid || token.ApplicationID.Int64 != applicationID {
		return false
	}
	if token.Token == tokenValue {
		return true
	}
	return token.RefreshToken.Valid && token.RefreshToken.String == tokenValue
}

func (s *Server) oauthAuthorize(c *echo.Context) error {
	setOAuthAuthorizeCacheHeaders(c)
	setOAuthAuthorizeCSPHeaders(c, s.cfg)
	request, err := s.authorizationRequest(c)
	if err != nil {
		return err
	}
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	if request.App.Superapp {
		return s.redirectWithAuthorizationCode(c, request, user)
	}
	if !truthy(oauthRawParamValue(c, "force_login")) {
		hasReusableToken, err := s.authorizationRequestHasReusableToken(request, user)
		if err != nil {
			return err
		}
		if hasReusableToken {
			return s.redirectWithAuthorizationCode(c, request, user)
		}
	}
	return c.HTML(http.StatusOK, authorizationPage(request, s.webLocale(c, user)))
}

func (s *Server) oauthAuthorizeDecision(c *echo.Context) error {
	setOAuthAuthorizeCacheHeaders(c)
	setOAuthAuthorizeCSPHeaders(c, s.cfg)
	request, err := s.authorizationRequest(c)
	if err != nil {
		return err
	}
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	if oauthAuthorizationDenied(c) {
		return redirectOAuthError(c, request.RedirectURI, "access_denied", "The resource owner or authorization server denied the request.", request.State)
	}
	return s.redirectWithAuthorizationCode(c, request, user)
}

func setOAuthAuthorizeCacheHeaders(c *echo.Context) {
	setPrivateNoStoreCacheHeaders(c)
}

func setOAuthAuthorizeCSPHeaders(c *echo.Context, cfg config.Config) {
	c.Response().Header().Set("Content-Security-Policy", railsContentSecurityPolicyWithoutDirective(cfg, "form-action"))
}

func setPrivateNoStoreCacheHeaders(c *echo.Context) {
	c.Response().Header().Set("Cache-Control", "private, no-store")
}

func oauthAuthorizationDenied(c *echo.Context) bool {
	return c.Request().Method == http.MethodDelete || methodOverrideIs(c, "delete")
}

type authorizationRequestData struct {
	App                 *oauthApplication
	RedirectURI         string
	Scopes              string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
}

func (s *Server) authorizationRequest(c *echo.Context) (authorizationRequestData, error) {
	if s.db == nil {
		return authorizationRequestData{}, apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	if oauthRawParamValue(c, "response_type") != "code" {
		return authorizationRequestData{}, apiError(c, http.StatusBadRequest, "Unsupported response type")
	}
	clientID := oauthRawParamValue(c, "client_id")
	var app oauthApplication
	if err := s.db.Where("uid = ?", clientID).First(&app).Error; err != nil {
		return authorizationRequestData{}, apiError(c, http.StatusUnauthorized, "The client credentials were incorrect")
	}
	redirectURI := oauthRawParamValue(c, "redirect_uri")
	if redirectURI == "" {
		redirectURI = firstRedirectURI(app.RedirectURI)
	}
	if !redirectURIMatches(app.RedirectURI, redirectURI) {
		return authorizationRequestData{}, apiError(c, http.StatusBadRequest, "Redirect URI is invalid")
	}
	scopes := normalizeRequestedScopes(oauthScopeParam(c, "read"), app.Scopes)
	challenge := oauthRawParamValue(c, "code_challenge")
	method := oauthRawParamValue(c, "code_challenge_method")
	if method == "" {
		method = "plain"
	}
	if challenge != "" && !validPKCEChallenge(method, challenge) {
		return authorizationRequestData{}, apiError(c, http.StatusBadRequest, "Code challenge is invalid")
	}
	return authorizationRequestData{
		App:                 &app,
		RedirectURI:         redirectURI,
		Scopes:              scopes,
		State:               oauthRawParamValue(c, "state"),
		CodeChallenge:       challenge,
		CodeChallengeMethod: method,
	}, nil
}

func (s *Server) redirectWithAuthorizationCode(c *echo.Context, request authorizationRequestData, user *models.User) error {
	code := authorizationCodeToken(request.CodeChallengeMethod, request.CodeChallenge)
	grant := models.OAuthAccessGrant{
		Token:           code,
		ExpiresIn:       600,
		RedirectURI:     request.RedirectURI,
		CreatedAt:       time.Now().UTC(),
		Scopes:          models.NullSafeString(request.Scopes),
		ApplicationID:   request.App.ID,
		ResourceOwnerID: user.ID,
	}
	if err := s.db.Create(&grant).Error; err != nil {
		return err
	}
	if request.RedirectURI == "urn:ietf:wg:oauth:2.0:oob" {
		return c.HTML(http.StatusOK, authorizationCodePage(grant.Token, s.webLocale(c, user)))
	}
	u, err := url.Parse(request.RedirectURI)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Redirect URI is invalid")
	}
	q := u.Query()
	q.Set("code", grant.Token)
	if request.State != "" {
		q.Set("state", request.State)
	}
	u.RawQuery = q.Encode()
	return c.Redirect(http.StatusFound, u.String())
}

func (s *Server) authorizationRequestHasReusableToken(request authorizationRequestData, user *models.User) (bool, error) {
	if s == nil || s.db == nil || request.App == nil || user == nil {
		return false, nil
	}
	var existing []models.OAuthAccessToken
	err := s.db.Where("application_id = ? AND resource_owner_id = ? AND revoked_at IS NULL", request.App.ID, user.ID).
		Order("created_at DESC, id DESC").
		Find(&existing).Error
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	for i := range existing {
		if oauthAccessTokenReusable(existing[i], request.Scopes, now) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) exchangeAuthorizationCode(c *echo.Context) (*models.OAuthAccessToken, error) {
	app, err := s.oauthApplicationFromTokenRequest(c)
	if err != nil {
		return nil, invalidOAuthClientError()
	}
	code := oauthRawParamValue(c, "code")
	redirectURI := oauthRawParamValue(c, "redirect_uri")
	var grant models.OAuthAccessGrant
	if err := s.db.Where("token = ? AND application_id = ? AND revoked_at IS NULL", code, app.ID).First(&grant).Error; err != nil {
		return nil, oauthTokenErrorf(http.StatusBadRequest, "invalid_grant", "The authorization code is invalid")
	}
	if grant.RedirectURI != redirectURI || grantExpired(grant, time.Now().UTC()) {
		return nil, oauthTokenErrorf(http.StatusBadRequest, "invalid_grant", "The authorization code is invalid")
	}
	if !verifyPKCECode(grant.Token, oauthRawParamValue(c, "code_verifier")) {
		return nil, oauthTokenErrorf(http.StatusBadRequest, "invalid_grant", "Code verifier is invalid")
	}
	now := time.Now().UTC()
	token := &models.OAuthAccessToken{
		Token:           randomHex(32),
		CreatedAt:       now,
		Scopes:          grant.Scopes,
		ApplicationID:   sql.NullInt64{Int64: app.ID, Valid: true},
		ResourceOwnerID: sql.NullInt64{Int64: grant.ResourceOwnerID, Valid: true},
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(token).Error; err != nil {
			return err
		}
		return tx.Model(&models.OAuthAccessGrant{}).Where("id = ?", grant.ID).Update("revoked_at", now).Error
	})
	return token, err
}

func (s *Server) exchangeRefreshToken(c *echo.Context) (*models.OAuthAccessToken, error) {
	app, err := s.oauthApplicationFromTokenRequest(c)
	if err != nil {
		return nil, invalidOAuthClientError()
	}
	refreshToken := oauthRawParamValue(c, "refresh_token")
	var previous models.OAuthAccessToken
	if err := s.db.Where("refresh_token = ? AND application_id = ? AND revoked_at IS NULL", refreshToken, app.ID).First(&previous).Error; err != nil {
		return nil, oauthTokenErrorf(http.StatusBadRequest, "invalid_grant", "Refresh token is invalid")
	}
	if !previous.ResourceOwnerID.Valid {
		return nil, oauthTokenErrorf(http.StatusBadRequest, "invalid_grant", "Refresh token is invalid")
	}
	now := time.Now().UTC()
	token := &models.OAuthAccessToken{
		Token:           randomHex(32),
		RefreshToken:    sql.NullString{String: randomHex(32), Valid: true},
		CreatedAt:       now,
		Scopes:          previous.Scopes,
		ApplicationID:   sql.NullInt64{Int64: app.ID, Valid: true},
		ResourceOwnerID: previous.ResourceOwnerID,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(token).Error; err != nil {
			return err
		}
		return tx.Model(&models.OAuthAccessToken{}).Where("id = ?", previous.ID).Update("revoked_at", now).Error
	})
	if err == nil {
		s.publishAccessTokenKills([]int64{previous.ID})
	}
	return token, err
}

func (s *Server) authenticateUser(email string, password string) (*models.User, error) {
	return s.authenticateUserWithOTP(email, password, "")
}

func (s *Server) authenticateUserWithOTP(email string, password string, otpAttempt string) (*models.User, error) {
	user, err := s.authenticateUserPassword(email, password)
	if err != nil {
		return nil, err
	}
	if user.OTPRequiredForLogin {
		if err := s.validateAndConsumeUserOTP(user, otpAttempt, time.Now().UTC()); err != nil {
			return nil, gorm.ErrRecordNotFound
		}
		return user, nil
	}
	hasWebAuthn, err := s.userHasWebauthnCredentials(user.ID)
	if err != nil {
		return nil, err
	}
	if hasWebAuthn {
		return nil, gorm.ErrRecordNotFound
	}
	return user, nil
}

func (s *Server) authenticateUserPassword(email string, password string) (*models.User, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, gorm.ErrRecordNotFound
	}
	if s.cfg.LDAPEnabled {
		if user, err := s.authenticateUserLDAP(email, password); err == nil {
			return user, nil
		}
	}
	if s.cfg.PAMEnabled {
		if user, err := s.authenticateUserPAM(email, password); err == nil {
			return user, nil
		}
	}

	var user models.User
	err := s.db.Where("lower(email) = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	if user.Disabled || !user.Approved || !user.ConfirmedAt.Valid {
		return nil, gorm.ErrRecordNotFound
	}
	if !validBCryptPassword(user.EncryptedPassword, password) {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}

type ldapAuthAttributes struct {
	Username string
	Email    string
}

type pamAuthAttributes struct {
	Username string
	Email    string
}

var authenticatePAMCommand = func(cfg config.Config, login string, password string) (pamAuthAttributes, error) {
	username := pamUsernameFromLogin(login)
	if username == "" || password == "" {
		return pamAuthAttributes{}, gorm.ErrRecordNotFound
	}
	if err := runPAMCommand(cfg, username, password, "authenticate"); err != nil {
		return pamAuthAttributes{}, err
	}
	email := strings.ToLower(strings.TrimSpace(login))
	if !strings.Contains(email, "@") {
		suffix := strings.TrimSpace(cfg.PAMEmailDomain)
		if suffix == "" {
			return pamAuthAttributes{}, gorm.ErrRecordNotFound
		}
		email = username + "@" + suffix
	}
	return pamAuthAttributes{Username: username, Email: email}, nil
}

var pamControlledUsername = func(cfg config.Config, username string) bool {
	if !cfg.PAMEnabled || strings.TrimSpace(cfg.PAMControlledService) == "" || strings.TrimSpace(username) == "" {
		return false
	}
	return runPAMCommand(cfg, pamUsernameFromLogin(username), "", "account") == nil
}

func runPAMCommand(cfg config.Config, username string, password string, action string) error {
	username = pamUsernameFromLogin(username)
	if username == "" {
		return gorm.ErrRecordNotFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := strings.TrimSpace(cfg.PAMAuthCommand)
	if command == "" {
		command = "pamtester"
	}
	service := strings.TrimSpace(cfg.PAMDefaultService)
	if action == "account" && strings.TrimSpace(cfg.PAMControlledService) != "" {
		service = strings.TrimSpace(cfg.PAMControlledService)
	}
	if service == "" || strings.TrimSpace(action) == "" {
		return gorm.ErrRecordNotFound
	}
	cmd := exec.CommandContext(ctx, command, service, username, action)
	if password != "" {
		cmd.Stdin = strings.NewReader(password + "\n")
	}
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("pam %s failed: %s", action, msg)
		}
		return err
	}
	return nil
}

var authenticateLDAPBindSearch = func(cfg config.Config, email string, password string) (ldapAuthAttributes, error) {
	method := strings.TrimSpace(cfg.LDAPMethod)
	scheme := "ldap"
	if method == "simple_tls" {
		scheme = "ldaps"
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(cfg.LDAPHost, strconv.Itoa(cfg.LDAPPort))}
	tlsConfig := &tls.Config{ServerName: cfg.LDAPHost, InsecureSkipVerify: cfg.LDAPTLSNoVerify} //nolint:gosec // Mirrors Rails LDAP_TLS_NO_VERIFY.
	conn, err := ldap.DialURL(u.String(), ldap.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}), ldap.DialWithTLSConfig(tlsConfig))
	if err != nil {
		return ldapAuthAttributes{}, err
	}
	defer conn.Close()
	if method == "start_tls" {
		if err := conn.StartTLS(tlsConfig); err != nil {
			return ldapAuthAttributes{}, err
		}
	}
	if err := conn.Bind(cfg.LDAPBindDN, cfg.LDAPPassword); err != nil {
		return ldapAuthAttributes{}, err
	}
	filter := ldapSearchFilter(cfg, email)
	req := ldap.NewSearchRequest(
		cfg.LDAPBase,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1,
		10,
		false,
		filter,
		[]string{cfg.LDAPUID, cfg.LDAPMail},
		nil,
	)
	res, err := conn.Search(req)
	if err != nil {
		return ldapAuthAttributes{}, err
	}
	if len(res.Entries) == 0 {
		return ldapAuthAttributes{}, gorm.ErrRecordNotFound
	}
	entry := res.Entries[0]
	if err := conn.Bind(entry.DN, password); err != nil {
		return ldapAuthAttributes{}, err
	}
	attrs := ldapAuthAttributes{
		Username: strings.TrimSpace(entry.GetAttributeValue(cfg.LDAPUID)),
		Email:    strings.TrimSpace(entry.GetAttributeValue(cfg.LDAPMail)),
	}
	if attrs.Username == "" || attrs.Email == "" {
		return ldapAuthAttributes{}, gorm.ErrRecordNotFound
	}
	return attrs, nil
}

func (s *Server) authenticateUserLDAP(email string, password string) (*models.User, error) {
	attrs, err := authenticateLDAPBindSearch(s.cfg, email, password)
	if err != nil {
		return nil, err
	}
	username := ldapSafeUsername(attrs.Username, s.cfg)
	username = railsAccountUsernameValue(username)
	if username == "" || strings.TrimSpace(attrs.Email) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var user models.User
	err = s.db.Joins("JOIN accounts ON accounts.id = users.account_id").
		Where("LOWER(accounts.username) = ? AND accounts.domain IS NULL", strings.ToLower(username)).
		First(&user).Error
	if err == nil {
		if user.Disabled || !user.Approved || !user.ConfirmedAt.Valid {
			return nil, gorm.ErrRecordNotFound
		}
		return &user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	createdAccountID := int64(0)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		privateKey, publicKey, err := generateAccountKeyPair()
		if err != nil {
			return err
		}
		account := models.Account{
			Username:   username,
			PrivateKey: sql.NullString{String: privateKey, Valid: true},
			PublicKey:  publicKey,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.AccountStat{AccountID: account.ID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return err
		}
		user = models.User{
			AccountID:         account.ID,
			Email:             strings.ToLower(strings.TrimSpace(attrs.Email)),
			EncryptedPassword: "",
			Locale:            railsUserLocaleValue(""),
			CreatedAt:         now,
			UpdatedAt:         now,
			Approved:          true,
			ConfirmedAt:       sql.NullTime{Time: now, Valid: true},
			TimeZone:          railsUserTimeZoneValue(""),
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		user.Account = &account
		createdAccountID = account.ID
		return nil
	}); err != nil {
		return nil, err
	}
	s.triggerAccountWebhook("account.created", createdAccountID)
	return &user, nil
}

func (s *Server) authenticateUserPAM(login string, password string) (*models.User, error) {
	attrs, err := authenticatePAMCommand(s.cfg, login, password)
	if err != nil {
		return nil, err
	}
	username := pamUsernameFromLogin(attrs.Username)
	username = railsAccountUsernameValue(username)
	email := strings.ToLower(strings.TrimSpace(attrs.Email))
	if username == "" || email == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var user models.User
	query := s.db.Preload("Account")
	if strings.Contains(strings.TrimSpace(login), "@") {
		err = query.Where("lower(email) = ?", email).First(&user).Error
	} else {
		err = query.Joins("JOIN accounts ON accounts.id = users.account_id").
			Where("LOWER(accounts.username) = ? AND accounts.domain IS NULL", strings.ToLower(username)).
			First(&user).Error
	}
	if err == nil {
		if user.Disabled || !user.Approved || !user.ConfirmedAt.Valid {
			return nil, gorm.ErrRecordNotFound
		}
		return &user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	createdAccountID := int64(0)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		privateKey, publicKey, err := generateAccountKeyPair()
		if err != nil {
			return err
		}
		account := models.Account{
			Username:   username,
			PrivateKey: sql.NullString{String: privateKey, Valid: true},
			PublicKey:  publicKey,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.AccountStat{AccountID: account.ID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return err
		}
		user = models.User{
			AccountID:         account.ID,
			Email:             email,
			EncryptedPassword: "",
			Locale:            railsUserLocaleValue(""),
			CreatedAt:         now,
			UpdatedAt:         now,
			Approved:          true,
			ConfirmedAt:       sql.NullTime{Time: now, Valid: true},
			TimeZone:          railsUserTimeZoneValue(""),
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		user.Account = &account
		createdAccountID = account.ID
		return nil
	}); err != nil {
		return nil, err
	}
	s.triggerAccountWebhook("account.created", createdAccountID)
	return &user, nil
}

func pamUsernameFromLogin(login string) string {
	login = strings.ToLower(strings.TrimSpace(login))
	if i := strings.IndexByte(login, '@'); i >= 0 {
		login = login[:i]
	}
	login = omniauthUsernameInvalidChars.ReplaceAllString(login, "")
	if len(login) > 30 {
		login = login[:30]
	}
	return strings.Trim(login, "_")
}

func ldapSafeUsername(username string, cfg config.Config) string {
	username = strings.TrimSpace(username)
	if !cfg.LDAPUIDConversionEnabled || username == "" {
		return username
	}
	pairs := make([]string, 0, len(cfg.LDAPUIDConversionSearch)*2)
	for _, r := range cfg.LDAPUIDConversionSearch {
		pairs = append(pairs, string(r), cfg.LDAPUIDConversionReplace)
	}
	return strings.NewReplacer(pairs...).Replace(username)
}

func ldapSearchFilter(cfg config.Config, email string) string {
	return strings.NewReplacer(
		"%{uid}", cfg.LDAPUID,
		"%{mail}", cfg.LDAPMail,
		"%{email}", ldap.EscapeFilter(email),
	).Replace(cfg.LDAPSearchFilter)
}

func (s *Server) userHasWebauthnCredentials(userID int64) (bool, error) {
	var count int64
	if err := s.db.Model(&models.WebauthnCredential{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Server) updateWebauthnCredentialSignCount(userID int64, credentials []models.WebauthnCredential, credentialID []byte, signCount int64) error {
	for _, credential := range credentials {
		if decoded, ok := decodeWebauthnStoredBytes(credential.ExternalID); ok && bytes.Equal(decoded, credentialID) {
			return s.db.Model(&models.WebauthnCredential{}).Where("id = ? AND user_id = ?", credential.ID, userID).Update("sign_count", signCount).Error
		}
	}
	return gorm.ErrRecordNotFound
}

func (s *Server) issueAccessToken(user *models.User, scopes string) (*models.OAuthAccessToken, error) {
	token := &models.OAuthAccessToken{
		Token:           randomHex(32),
		CreatedAt:       time.Now().UTC(),
		Scopes:          models.NullSafeString(scopes),
		ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true},
	}
	return token, s.db.Create(token).Error
}

func (s *Server) issueAccessTokenForApplication(user *models.User, app *oauthApplication, scopes string) (*models.OAuthAccessToken, error) {
	if app == nil {
		return s.issueAccessToken(user, scopes)
	}
	var existing []models.OAuthAccessToken
	err := s.db.Where("application_id = ? AND resource_owner_id = ? AND revoked_at IS NULL", app.ID, user.ID).
		Order("created_at DESC, id DESC").
		Find(&existing).Error
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range existing {
		if oauthAccessTokenReusable(existing[i], scopes, now) {
			return &existing[i], nil
		}
	}
	token := &models.OAuthAccessToken{
		Token:           randomHex(32),
		CreatedAt:       now,
		Scopes:          models.NullSafeString(scopes),
		ApplicationID:   sql.NullInt64{Int64: app.ID, Valid: true},
		ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true},
	}
	return token, s.db.Create(token).Error
}

func (s *Server) issueApplicationToken(clientID string, clientSecret string, scopes string) (*models.OAuthAccessToken, error) {
	app, err := s.oauthApplicationFromCredentials(clientID, clientSecret)
	if err != nil {
		return nil, err
	}
	return s.issueApplicationTokenForApplication(app, scopes)
}

func (s *Server) issueApplicationTokenForApplication(app *oauthApplication, scopes string) (*models.OAuthAccessToken, error) {
	var existing []models.OAuthAccessToken
	err := s.db.Where("application_id = ? AND resource_owner_id IS NULL AND revoked_at IS NULL", app.ID).
		Order("created_at DESC, id DESC").
		Find(&existing).Error
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range existing {
		if oauthAccessTokenReusable(existing[i], scopes, now) {
			return &existing[i], nil
		}
	}
	token := &models.OAuthAccessToken{
		Token:         randomHex(32),
		CreatedAt:     now,
		Scopes:        models.NullSafeString(scopes),
		ApplicationID: sql.NullInt64{Int64: app.ID, Valid: true},
	}
	return token, s.db.Create(token).Error
}

func (s *Server) recordSessionActivation(userID int64, accessTokenID int64, c *echo.Context) error {
	if s.db == nil || userID == 0 || accessTokenID == 0 {
		return nil
	}
	now := time.Now().UTC()
	activation := sessionActivationForRequest(userID, accessTokenID, c.RealIP(), c.Request().UserAgent(), now)
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&activation).Error; err != nil {
			return err
		}
		return purgeOldSessionActivations(tx, userID, s.cfg.MaxSessionActivations)
	})
}

func sessionActivationForRequest(userID int64, accessTokenID int64, ip string, userAgent string, now time.Time) models.SessionActivation {
	trimmedIP := strings.TrimSpace(ip)
	return models.SessionActivation{
		SessionID:     randomHex(16),
		UserID:        userID,
		AccessTokenID: sql.NullInt64{Int64: accessTokenID, Valid: accessTokenID > 0},
		IP:            sql.NullString{String: trimmedIP, Valid: trimmedIP != ""},
		UserAgent:     userAgent,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func purgeOldSessionActivations(tx *gorm.DB, userID int64, keep int) error {
	if tx == nil || userID == 0 {
		return nil
	}
	if keep == -1 {
		return nil
	}
	if keep < 0 {
		keep = 0
	}
	var old []models.SessionActivation
	if err := tx.Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset(keep).
		Find(&old).Error; err != nil {
		return err
	}
	if len(old) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(old))
	var tokenIDs []int64
	var pushIDs []int64
	for _, activation := range old {
		ids = append(ids, activation.ID)
		if activation.AccessTokenID.Valid {
			tokenIDs = append(tokenIDs, activation.AccessTokenID.Int64)
		}
		if activation.WebPushSubscriptionID.Valid {
			pushIDs = append(pushIDs, activation.WebPushSubscriptionID.Int64)
		}
	}
	if len(pushIDs) > 0 {
		if err := tx.Where("id IN ?", pushIDs).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
	}
	if len(tokenIDs) > 0 {
		if err := tx.Where("id IN ?", tokenIDs).Delete(&models.OAuthAccessToken{}).Error; err != nil {
			return err
		}
	}
	return tx.Where("id IN ?", ids).Delete(&models.SessionActivation{}).Error
}

func (s *Server) revokeSessionToken(tokenValue string) error {
	if s.db == nil || strings.TrimSpace(tokenValue) == "" {
		return nil
	}
	now := time.Now().UTC()
	var revokedTokenID int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var token models.OAuthAccessToken
		if err := tx.Where("token = ? AND revoked_at IS NULL", tokenValue).First(&token).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		revokedTokenID = token.ID
		if err := tx.Where("access_token_id = ?", token.ID).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		if err := tx.Where("access_token_id = ?", token.ID).Delete(&models.SessionActivation{}).Error; err != nil {
			return err
		}
		return tx.Model(&models.OAuthAccessToken{}).Where("id = ?", token.ID).Update("revoked_at", now).Error
	})
	if err != nil {
		return err
	}
	s.publishAccessTokenKills([]int64{revokedTokenID})
	return nil
}

func (s *Server) oauthApplicationFromTokenRequest(c *echo.Context) (*oauthApplication, error) {
	clientID, clientSecret := basicClientCredentials(c)
	if clientID == "" {
		clientID = oauthRawParamValue(c, "client_id")
	}
	if clientSecret == "" {
		clientSecret = oauthRawParamValue(c, "client_secret")
	}
	return s.oauthApplicationFromCredentials(clientID, clientSecret)
}

func (s *Server) oauthApplicationFromOptionalTokenRequest(c *echo.Context) (*oauthApplication, bool, error) {
	clientID, clientSecret := basicClientCredentials(c)
	if clientID == "" {
		clientID = oauthRawParamValue(c, "client_id")
	}
	if clientSecret == "" {
		clientSecret = oauthRawParamValue(c, "client_secret")
	}
	if clientID == "" && clientSecret == "" {
		return nil, false, nil
	}
	app, err := s.oauthApplicationFromCredentials(clientID, clientSecret)
	return app, true, err
}

func (s *Server) oauthApplicationFromCredentials(clientID string, clientSecret string) (*oauthApplication, error) {
	var app oauthApplication
	if err := s.db.Where("uid = ?", clientID).First(&app).Error; err != nil {
		return nil, err
	}
	if app.Confidential && app.Secret != clientSecret {
		return nil, gorm.ErrRecordNotFound
	}
	return &app, nil
}

func oauthParam(c *echo.Context, keys ...string) string {
	payload, _ := oauthRequestJSONPayload(c)
	for _, key := range keys {
		if raw, ok := payload[key]; ok {
			if value := stringFromJSONValue(raw); value != "" {
				return value
			}
		}
		if value := strings.TrimSpace(c.FormValue(key)); value != "" {
			return value
		}
	}
	return ""
}

func oauthRawParamValue(c *echo.Context, key string) string {
	value, _ := oauthRawParam(c, key)
	return value
}

func oauthRawParam(c *echo.Context, key string) (string, bool) {
	payload, _ := oauthRequestJSONPayload(c)
	if raw, ok := payload[key]; ok {
		if value, ok := rawOAuthStringValue(raw); ok {
			return value, true
		}
	}
	req := c.Request()
	_ = req.ParseForm()
	if values, ok := req.PostForm[key]; ok {
		return lastValue(values), true
	}
	if values, ok := req.URL.Query()[key]; ok {
		return lastValue(values), true
	}
	return "", false
}

func rawOAuthStringValue(raw any) (string, bool) {
	switch value := raw.(type) {
	case string:
		return value, true
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				continue
			}
			items = append(items, text)
		}
		return strings.Join(items, " "), true
	default:
		return "", false
	}
}

func lastValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func oauthScopeParam(c *echo.Context, fallback string) string {
	return firstNonEmpty(oauthParam(c, "scope", "scopes"), fallback)
}

func oauthRequestJSONPayload(c *echo.Context) (map[string]any, error) {
	if cached := c.Get(oauthJSONPayloadKey); cached != nil {
		if payload, ok := cached.(map[string]any); ok {
			return payload, nil
		}
	}
	payload := map[string]any{}
	if !strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		c.Set(oauthJSONPayloadKey, payload)
		return payload, nil
	}
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return nil, err
	}
	c.Set(oauthJSONPayloadKey, payload)
	return payload, nil
}

func (s *Server) recordSignIn(user *models.User, ip string, userAgent string) error {
	return s.recordSignInWithMethod(user, ip, userAgent, "password", "")
}

func (s *Server) recordSignInWithMethod(user *models.User, ip string, userAgent string, method string, provider string) error {
	if s.db == nil || user == nil || user.ID == 0 {
		return nil
	}
	now := time.Now().UTC()
	ip = strings.TrimSpace(ip)
	method = strings.TrimSpace(method)
	if method == "" {
		method = "password"
	}
	if !validLoginActivityAuthenticationMethod(method) {
		return errors.New("login activity authentication_method is invalid")
	}
	provider = strings.TrimSpace(provider)
	suspicious := s.suspiciousSignIn(user, ip)
	updates := map[string]any{
		"sign_in_count":      gorm.Expr("sign_in_count + 1"),
		"last_sign_in_at":    user.CurrentSignInAt,
		"current_sign_in_at": now,
		"updated_at":         now,
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
			return err
		}
		activity := models.LoginActivity{
			UserID:               user.ID,
			AuthenticationMethod: sql.NullString{String: method, Valid: true},
			Provider:             sql.NullString{String: provider, Valid: provider != ""},
			Success:              sql.NullBool{Bool: true, Valid: true},
			IP:                   sql.NullString{String: ip, Valid: ip != ""},
			UserAgent:            sql.NullString{String: userAgent, Valid: userAgent != ""},
			CreatedAt:            sql.NullTime{Time: now, Valid: true},
		}
		return tx.Create(&activity).Error
	})
	if err != nil {
		return err
	}
	s.activityTrackerRecordUnique(context.Background(), "activity:logins", now, user.ID)
	s.regenerateHomeFeedForReturningUser(context.Background(), *user, now)
	if suspicious {
		return s.sendSuspiciousSignInMail(*user, ip, userAgent, now)
	}
	return nil
}

func (s *Server) suspiciousSignIn(user *models.User, ip string) bool {
	if s.db == nil || user == nil || s.suspiciousSignInDisabled() {
		return false
	}
	seenIPs, err := s.seenSignInIPs(user.ID)
	if err != nil {
		return false
	}
	return suspiciousSignInFromSeenIPs(*user, ip, seenIPs)
}

func (s *Server) seenSignInIPs(userID int64) ([]string, error) {
	var rows []struct {
		IP string `gorm:"column:ip"`
	}
	err := s.db.Raw(`
		SELECT ip::text AS ip FROM (
			SELECT sign_up_ip AS ip FROM users WHERE id = ? AND sign_up_ip IS NOT NULL
			UNION ALL
			SELECT ip FROM session_activations WHERE user_id = ? AND ip IS NOT NULL
			UNION ALL
			SELECT ip FROM login_activities WHERE user_id = ? AND success = TRUE AND ip IS NOT NULL
		) AS seen_ips
	`, userID, userID, userID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ips := make([]string, 0, len(rows))
	for _, row := range rows {
		ips = append(ips, row.IP)
	}
	return ips, nil
}

func suspiciousSignInFromSeenIPs(user models.User, remoteIP string, seenIPs []string) bool {
	if user.OTPRequiredForLogin || !user.CurrentSignInAt.Valid {
		return false
	}
	prefix, ok := signInTolerancePrefix(remoteIP)
	if !ok {
		return false
	}
	for _, seenIP := range seenIPs {
		addr, err := netip.ParseAddr(strings.TrimSpace(seenIP))
		if err == nil && prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func suspiciousSignInDisabled() bool {
	return (&Server{cfg: config.FromEnv()}).suspiciousSignInDisabled()
}

func signInTolerancePrefix(ip string) (netip.Prefix, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return netip.Prefix{}, false
	}
	bits := 16
	if addr.Is6() {
		bits = 64
	}
	return netip.PrefixFrom(addr, bits).Masked(), true
}

func (s *Server) setSessionCookie(c *echo.Context, token string) error {
	s.writeSessionCookie(c, token)
	if err := s.rotateBrowserSessionForLogin(c, token); err != nil {
		return err
	}
	expireCookie(c, railsSessionCookieName, s.cfg.ForceSSL)
	expireCookie(c, railsSessionIDCookieName, s.cfg.ForceSSL)
	return nil
}

func (s *Server) writeSessionCookie(c *echo.Context, token string) {
	c.Set(browserAuthTokenContextKey, token)
	http.SetCookie(c.Response(), &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(browserSessionLifetime.Seconds()),
		Expires:  time.Now().UTC().Add(browserSessionLifetime),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.ForceSSL,
	})
}

func expireCookie(c *echo.Context, name string, secure bool) {
	http.SetCookie(c.Response(), &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

func validBCryptPassword(encrypted string, password string) bool {
	hash := []byte(normalizeBCryptPrefix(encrypted))
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

func normalizeBCryptPrefix(hash string) string {
	if strings.HasPrefix(hash, "$2y$") {
		return "$2a$" + strings.TrimPrefix(hash, "$2y$")
	}
	return hash
}

func tokenResponse(token *models.OAuthAccessToken) map[string]any {
	out := map[string]any{
		"access_token": token.Token,
		"token_type":   "Bearer",
		"scope":        string(token.Scopes),
		"created_at":   token.CreatedAt.Unix(),
	}
	if token.RefreshToken.Valid {
		out["refresh_token"] = token.RefreshToken.String
	}
	if token.ExpiresIn.Valid {
		out["expires_in"] = token.ExpiresIn.Int64
	}
	return out
}

func tokenInfoResponse(token models.OAuthAccessToken, appUID string, now time.Time) map[string]any {
	var owner any
	if token.ResourceOwnerID.Valid {
		owner = token.ResourceOwnerID.Int64
	}
	out := map[string]any{
		"resource_owner_id": owner,
		"scope":             string(token.Scopes),
		"scopes":            strings.Fields(string(token.Scopes)),
		"expires_in":        nil,
		"created_at":        token.CreatedAt.Unix(),
	}
	if token.ExpiresIn.Valid {
		remaining := int64(token.CreatedAt.Add(time.Duration(token.ExpiresIn.Int64) * time.Second).Sub(now).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		out["expires_in"] = remaining
	}
	if appUID != "" {
		out["application"] = map[string]string{"uid": appUID}
	}
	return out
}

func oauthAccessTokenExpired(token models.OAuthAccessToken, now time.Time) bool {
	return token.ExpiresIn.Valid && token.CreatedAt.Add(time.Duration(token.ExpiresIn.Int64)*time.Second).Before(now)
}

type oauthTokenError struct {
	status      int
	code        string
	description string
}

func (e oauthTokenError) Error() string {
	return e.description
}

func oauthTokenErrorf(status int, code string, description string) error {
	return oauthTokenError{status: status, code: code, description: description}
}

func invalidOAuthClientError() error {
	return oauthTokenErrorf(http.StatusUnauthorized, "invalid_client", "The client credentials were incorrect")
}

func invalidOAuthTokenError() error {
	return oauthTokenErrorf(http.StatusUnauthorized, "invalid_token", "The access token is invalid")
}

func (s *Server) trackAccessTokenUse(c *echo.Context, token *models.OAuthAccessToken) error {
	if s == nil || s.db == nil || c == nil || token == nil || token.ID == 0 {
		return nil
	}
	now := time.Now().UTC()
	if !oauthAccessTokenNeedsLastUsedUpdate(*token, now) {
		return nil
	}
	updates := map[string]any{
		"last_used_at": now,
		"last_used_ip": strings.TrimSpace(c.RealIP()),
	}
	if err := s.db.Model(&models.OAuthAccessToken{}).Where("id = ?", token.ID).Updates(updates).Error; err != nil {
		return err
	}
	token.LastUsedAt = sql.NullTime{Time: now, Valid: true}
	token.LastUsedIP = sql.NullString{String: updates["last_used_ip"].(string), Valid: updates["last_used_ip"].(string) != ""}
	return nil
}

func oauthAccessTokenNeedsLastUsedUpdate(token models.OAuthAccessToken, now time.Time) bool {
	return !token.LastUsedAt.Valid || token.LastUsedAt.Time.Before(now.Add(-accessTokenUpdateEvery))
}

func htmlDocumentLang(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		locale = webDefaultLocaleValue()
	}
	return html.EscapeString(locale)
}

func sessionToken(c *echo.Context) string {
	cookie, err := c.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func safeRedirect(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "/home"
	}
	return value
}

func authorizationPage(request authorizationRequestData, locale string) string {
	title := oauthT(locale, "doorkeeper.authorizations.new.title", "Authorization required")
	prompt := oauthT(locale, "doorkeeper.authorizations.new.prompt_html", "%{client_name} would like permission to access your account. It is a third-party application. <strong>If you do not trust it, then you should not authorize it.</strong>", map[string]string{"client_name": "<strong>" + html.EscapeString(request.App.Name) + "</strong>"})
	reviewPermissions := oauthT(locale, "doorkeeper.authorizations.new.review_permissions", "Review permissions")
	authorize := oauthT(locale, "doorkeeper.authorizations.buttons.authorize", "Authorize")
	deny := oauthT(locale, "doorkeeper.authorizations.buttons.deny", "Deny")
	permissions := oauthAuthorizationPermissionsHTML(request.Scopes, locale)
	hiddenFields := oauthAuthorizationHiddenFields(request)
	body := `<div class="form-container simple_form"><div class="oauth-prompt">
    <h3>` + html.EscapeString(title) + `</h3>
    <p>` + prompt + `</p>
    <h3>` + html.EscapeString(reviewPermissions) + `</h3>
    ` + permissions + `
    <div class="actions">
      <form method="post" action="/oauth/authorize">` + hiddenFields + `<button type="submit">` + html.EscapeString(authorize) + `</button></form>
      <form method="post" action="/oauth/authorize"><input type="hidden" name="_method" value="delete">` + hiddenFields + `<button type="submit" class="negative">` + html.EscapeString(deny) + `</button></form>
    </div>
  </div></div>`
	return authShellHTML(title, "", "", body, locale)
}

func oauthAuthorizationHiddenFields(request authorizationRequestData) string {
	fields := []struct {
		Name  string
		Value string
	}{
		{"client_id", request.App.UID},
		{"redirect_uri", request.RedirectURI},
		{"state", request.State},
		{"response_type", "code"},
		{"scope", request.Scopes},
		{"code_challenge", request.CodeChallenge},
		{"code_challenge_method", request.CodeChallengeMethod},
	}
	var out strings.Builder
	for _, field := range fields {
		if field.Value == "" && (field.Name == "code_challenge" || field.Name == "code_challenge_method") {
			continue
		}
		out.WriteString(`<input type="hidden" name="` + field.Name + `" value="` + html.EscapeString(field.Value) + `">`)
	}
	return out.String()
}

func oauthAuthorizationPermissionsHTML(scopes string, locale string) string {
	var b strings.Builder
	b.WriteString(`<ul class="permissions-list">`)
	for _, scope := range groupedOAuthScopes(scopes) {
		b.WriteString(`<li class="permissions-list__item"><div class="permissions-list__item__icon"><i class="fa fa-check fa-fw"></i></div><div class="permissions-list__item__text"><div class="permissions-list__item__text__title">` + html.EscapeString(oauthGroupedScopeTitle(scope.Key, locale)) + `</div><div class="permissions-list__item__text__type">` + html.EscapeString(oauthGroupedScopeAccess(scope.Access, locale)) + `</div></div></li>`)
	}
	b.WriteString(`</ul>`)
	return b.String()
}

func authorizationCodePage(code string, locale string) string {
	title := oauthT(locale, "doorkeeper.authorizations.show.title", "Copy this authorization code and paste it to the application.")
	body := `<div class="form-container"><div class="flash-message simple_form"><p>` + html.EscapeString(title) + `</p><div class="input-copy"><div class="input-copy__wrapper"><input class="oauth-code" type="text" spellcheck="false" readonly value="` + html.EscapeString(code) + `"></div><button type="button">` + html.EscapeString(settingsT(locale, "generic.copy", "Copy")) + `</button></div></div></div>`
	return authShellHTML(title, "", "", body, locale)
}

func redirectOAuthError(c *echo.Context, redirectURI string, code string, description string, state string) error {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Redirect URI is invalid")
	}
	q := u.Query()
	q.Set("error", code)
	if description != "" {
		q.Set("error_description", description)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return c.Redirect(http.StatusFound, u.String())
}

func firstRedirectURI(values string) string {
	parts := strings.FieldsFunc(values, func(r rune) bool { return r == '\n' || r == '\r' || r == ' ' })
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func redirectURIMatches(allowed string, requested string) bool {
	for _, item := range strings.FieldsFunc(allowed, func(r rune) bool { return r == '\n' || r == '\r' || r == ' ' }) {
		if item == requested {
			return true
		}
	}
	return false
}

func normalizeRequestedScopes(requested string, allowed string) string {
	if strings.TrimSpace(allowed) == "" {
		return strings.TrimSpace(requested)
	}
	allowedSet := map[string]struct{}{}
	for _, scope := range strings.Fields(allowed) {
		allowedSet[scope] = struct{}{}
	}
	out := make([]string, 0)
	for _, scope := range strings.Fields(requested) {
		if _, ok := allowedSet[scope]; ok {
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		return "read"
	}
	return strings.Join(out, " ")
}

func oauthAccessTokenReusable(token models.OAuthAccessToken, requestedScopes string, now time.Time) bool {
	if token.RevokedAt.Valid {
		return false
	}
	if token.ExpiresIn.Valid && !token.CreatedAt.Add(time.Duration(token.ExpiresIn.Int64)*time.Second).After(now) {
		return false
	}
	return oauthScopeSetEqual(string(token.Scopes), requestedScopes)
}

func oauthScopeSetEqual(left string, right string) bool {
	leftScopes := strings.Fields(left)
	rightScopes := strings.Fields(right)
	if len(leftScopes) != len(rightScopes) {
		return false
	}
	seen := make(map[string]int, len(leftScopes))
	for _, scope := range leftScopes {
		seen[scope]++
	}
	for _, scope := range rightScopes {
		if seen[scope] == 0 {
			return false
		}
		seen[scope]--
	}
	return true
}

func basicClientCredentials(c *echo.Context) (string, string) {
	username, password, ok := c.Request().BasicAuth()
	if !ok {
		return "", ""
	}
	return username, password
}

func authorizationCodeToken(method string, challenge string) string {
	if challenge == "" {
		return randomHex(32)
	}
	return "pkce." + method + "." + base64.RawURLEncoding.EncodeToString([]byte(challenge)) + "." + randomHex(32)
}

func validPKCEChallenge(method string, challenge string) bool {
	if method != "plain" && method != "S256" {
		return false
	}
	length := len(challenge)
	return length >= 43 && length <= 128 && !strings.ContainsAny(challenge, " \t\r\n")
}

func verifyPKCECode(code string, verifier string) bool {
	if !strings.HasPrefix(code, "pkce.") {
		return true
	}
	parts := strings.SplitN(code, ".", 4)
	if len(parts) != 4 {
		return false
	}
	if !validPKCEVerifier(verifier) {
		return false
	}
	challengeBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	challenge := string(challengeBytes)
	switch parts[1] {
	case "plain":
		return verifier == challenge
	case "S256":
		digest := sha256.Sum256([]byte(verifier))
		return base64.RawURLEncoding.EncodeToString(digest[:]) == challenge
	default:
		return false
	}
}

func validPKCEVerifier(verifier string) bool {
	length := len(verifier)
	return length >= 43 && length <= 128 && !strings.ContainsAny(verifier, " \t\r\n")
}

func grantExpired(grant models.OAuthAccessGrant, now time.Time) bool {
	return grant.ExpiresIn > 0 && grant.CreatedAt.Add(time.Duration(grant.ExpiresIn)*time.Second).Before(now)
}
