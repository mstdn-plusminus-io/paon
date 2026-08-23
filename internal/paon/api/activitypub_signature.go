package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	activitySignatureExpirationWindow                = 12 * time.Hour
	activitySignatureClockSkew                       = time.Hour
	activitySignatureSourceStoplightFailureThreshold = 1
	activitySignatureSourceStoplightCooldown         = 5 * time.Minute
	activityHTTPConnectTimeout                       = 5 * time.Second
	activityHTTPReadTimeout                          = 10 * time.Second
	activityHTTPWriteTimeout                         = 10 * time.Second
	activityHTTPReadDeadline                         = 30 * time.Second
)

var (
	errActivitySignatureDomainNotAllowed = errors.New("domain is not allowed")
	errActivitySignatureFailed           = errors.New("activitypub signature verification failed")
	errActivitySignatureMalformed        = errors.New("malformed activitypub signature header")
	errActivitySignatureTemporary        = errors.New("temporary activitypub signature key resolution failure")
	errActivityPrivateNetworkAddress     = errors.New("remote host resolves to a private network address")
)

type activityHTTPAllowPrivateNetworkContextKey struct{}

func activityHTTPPrivateNetworkAllowed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	allowed, _ := ctx.Value(activityHTTPAllowPrivateNetworkContextKey{}).(bool)
	return allowed
}

// Keep this list aligned with Mastodon 4.3's private_address_check ranges.
// Addresses are normalized before matching so IPv4-mapped and IPv4-compatible
// IPv6 forms such as ::ffff:0.0.0.1 and ::127.0.0.1 cannot bypass IPv4 ranges.
var activityForbiddenAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("ff00::/8"),
}

var (
	defaultActivityHTTPClient        = activityHTTPClientFromConfig(config.Config{HTTPProxyURL: os.Getenv("http_proxy"), HTTPHiddenProxyURL: os.Getenv("http_hidden_proxy"), MaxRequestPoolSize: intFromEnvForActivityHTTP("MAX_REQUEST_POOL_SIZE", 512)})
	activityHTTPClient               = defaultActivityHTTPClient
	activityAllowAccessHiddenService = os.Getenv("ALLOW_ACCESS_TO_HIDDEN_SERVICE") == "true"
	activityPrivateAddressExceptions = parseActivityPrivateAddressExceptions(os.Getenv("ALLOWED_PRIVATE_ADDRESSES"))
	activityHTTPProxyConfigured      = strings.TrimSpace(os.Getenv("http_proxy")) != ""
	activityLookupIP                 = net.LookupIP
)

func configureActivityHTTPClient(cfg config.Config) {
	activityAllowAccessHiddenService = cfg.AllowAccessToHiddenService
	activityPrivateAddressExceptions = parseActivityPrivateAddressExceptions(cfg.AllowedPrivateAddresses)
	activityHTTPProxyConfigured = strings.TrimSpace(cfg.HTTPProxyURL) != ""
	if activityHTTPClient == defaultActivityHTTPClient {
		defaultActivityHTTPClient = activityHTTPClientFromConfig(cfg)
		activityHTTPClient = defaultActivityHTTPClient
	}
	if oidcHTTPClient == defaultOIDCHTTPClient {
		defaultOIDCHTTPClient = activityHTTPClientClone(10 * time.Second)
		oidcHTTPClient = defaultOIDCHTTPClient
	}
}

func activityHTTPClientClone(timeout time.Duration) *http.Client {
	if activityHTTPClient == nil {
		client := activityHTTPClientFromConfig(config.Config{})
		client.Timeout = timeout
		return client
	}
	client := *activityHTTPClient
	client.Timeout = timeout
	if client.CheckRedirect == nil {
		client.CheckRedirect = activityHTTPCheckRedirect
	}
	return &client
}

func activityHTTPClientFromConfig(cfg config.Config) *http.Client {
	client := &http.Client{
		Timeout:       activityHTTPReadDeadline,
		CheckRedirect: activityHTTPCheckRedirect,
	}
	transport := activityHTTPTransportFromConfig(cfg)
	if transport == nil {
		return client
	}
	if telemetry.Enabled() {
		client.Transport = telemetry.NewHTTPTransport(transport, "federation")
	} else {
		client.Transport = transport
	}
	return client
}

func intFromEnvForActivityHTTP(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	sign := 1
	if value[0] == '+' || value[0] == '-' {
		if value[0] == '-' {
			sign = -1
		}
		value = value[1:]
	}
	i := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	parsed, err := strconv.Atoi(value[:i])
	if err != nil {
		return 0
	}
	return sign * parsed
}

func activityHTTPTransportFromConfig(cfg config.Config) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Paon's proxy contract is configured explicitly through http_proxy and
	// http_hidden_proxy. Do not inherit Go's process-wide HTTP_PROXY/HTTPS_PROXY
	// handling, which would bypass the configured proxy allowlist below.
	transport.Proxy = nil
	transport.DialContext = activityHTTPDialContext(cfg)
	transport.TLSHandshakeTimeout = activityHTTPConnectTimeout
	transport.ResponseHeaderTimeout = activityHTTPReadTimeout
	transport.ExpectContinueTimeout = activityHTTPWriteTimeout
	if cfg.MaxRequestPoolSize > 0 {
		transport.MaxIdleConns = cfg.MaxRequestPoolSize
		transport.MaxIdleConnsPerHost = cfg.MaxRequestPoolSize
		transport.MaxConnsPerHost = cfg.MaxRequestPoolSize
	}
	if strings.TrimSpace(cfg.HTTPProxyURL) == "" && strings.TrimSpace(cfg.HTTPHiddenProxyURL) == "" {
		return transport
	}
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		raw := cfg.HTTPProxyURL
		if req != nil && req.URL != nil && activityHiddenServiceHost(req.URL.Hostname()) && strings.TrimSpace(cfg.HTTPHiddenProxyURL) != "" {
			raw = cfg.HTTPHiddenProxyURL
		}
		if strings.TrimSpace(raw) == "" {
			return nil, nil
		}
		return url.Parse(raw)
	}
	return transport
}

func activityHTTPDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   activityHTTPConnectTimeout,
		KeepAlive: 30 * time.Second,
	}
}

func activityHTTPDialContext(cfg config.Config) func(context.Context, string, string) (net.Conn, error) {
	dialer := activityHTTPDialer()
	proxyAddresses := activityHTTPProxyDialAddresses(cfg)
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		guardedDialer := *dialer
		guardedDialer.ControlContext = activityHTTPDialControl(address, proxyAddresses)
		return guardedDialer.DialContext(ctx, network, address)
	}
}

func activityHTTPDialControl(address string, proxyAddresses map[string]struct{}) func(context.Context, string, string, syscall.RawConn) error {
	if _, ok := proxyAddresses[activityHTTPDialAddressKey(address)]; ok {
		return nil
	}
	return func(ctx context.Context, _ string, resolvedAddress string, _ syscall.RawConn) error {
		if activityHTTPPrivateNetworkAllowed(ctx) {
			return nil
		}
		host, _, err := net.SplitHostPort(resolvedAddress)
		if err != nil {
			return fmt.Errorf("%w: %s", errActivityPrivateNetworkAddress, resolvedAddress)
		}
		if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
			host = host[:zone]
		}
		ip := net.ParseIP(host)
		if ip == nil || !activityIPAllowed(ip) {
			return fmt.Errorf("%w: %s", errActivityPrivateNetworkAddress, host)
		}
		return nil
	}
}

func activityHTTPProxyDialAddresses(cfg config.Config) map[string]struct{} {
	addresses := make(map[string]struct{}, 2)
	for _, raw := range []string{cfg.HTTPProxyURL, cfg.HTTPHiddenProxyURL} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		port := parsed.Port()
		if port == "" {
			switch strings.ToLower(parsed.Scheme) {
			case "http":
				port = "80"
			case "https":
				port = "443"
			default:
				continue
			}
		}
		addresses[activityHTTPDialAddressKey(net.JoinHostPort(parsed.Hostname(), port))] = struct{}{}
	}
	return addresses
}

func activityHTTPDialAddressKey(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(address))
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return net.JoinHostPort(host, port)
}

func (s *Server) verifyActivityPubSignature(c *echo.Context, body []byte) (*models.Account, error) {
	rawSignature := c.Request().Header.Get("Signature")
	if strings.TrimSpace(rawSignature) == "" {
		return nil, fmt.Errorf("request not signed")
	}
	if s.activityHTTPMessageSignaturesEnabled() && strings.TrimSpace(c.Request().Header.Get("Signature-Input")) != "" {
		return s.verifyActivityPubHTTPMessageSignature(c, body)
	}
	params, err := parseActivitySignature(rawSignature)
	if err != nil {
		return nil, err
	}
	algorithm := activitySignatureAlgorithm(params)
	if algorithm != "rsa-sha256" && algorithm != "hs2019" {
		return nil, fmt.Errorf("unsupported signature algorithm")
	}
	headers := activitySignedHeaders(params, algorithm)
	if err := validateActivitySignatureTime(c.Request(), params, algorithm); err != nil {
		return nil, err
	}
	if err := validateActivitySignatureStrength(c.Request(), headers); err != nil {
		return nil, err
	}
	if err := validateActivitySignaturePseudoHeaders(params, headers, algorithm); err != nil {
		return nil, err
	}
	if err := validateActivityDigest(c.Request(), headers, body); err != nil {
		return nil, err
	}
	account, err := s.activityPubActorFromKeyIDWithSourceStoplight(c, params["keyId"])
	if err != nil {
		return nil, activityPubSignatureActorResolutionError(err)
	}
	signature, err := decodeActivitySignatureParam(params["signature"])
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding")
	}
	publicKey, err := activityPublicKey(account.PublicKey)
	if err == nil {
		if verifyActivitySignedString(c.Request(), params, headers, publicKey, signature, true) == nil {
			return account, nil
		}
		if verifyActivitySignedString(c.Request(), params, headers, publicKey, signature, false) == nil {
			return account, nil
		}
	}
	refreshed, err := s.refreshActivityPubActorKeyWithSourceStoplight(c, params["keyId"], account)
	if err != nil {
		return nil, activityPubSignatureActorResolutionError(err)
	}
	if refreshed != nil && refreshed.PublicKey != "" && refreshed.PublicKey != account.PublicKey {
		publicKey, err = activityPublicKey(refreshed.PublicKey)
		if err != nil {
			return nil, err
		}
		if verifyActivitySignedString(c.Request(), params, headers, publicKey, signature, true) == nil {
			return refreshed, nil
		}
		if verifyActivitySignedString(c.Request(), params, headers, publicKey, signature, false) == nil {
			return refreshed, nil
		}
	}
	return nil, errActivitySignatureFailed
}

func (s *Server) requireActivityPubSignatureIfAuthorized(c *echo.Context) error {
	_, err := s.activityPubSignatureAccountIfAuthorized(c)
	return err
}

func (s *Server) activityPubSignatureAccountIfAuthorized(c *echo.Context) (*models.Account, error) {
	if s == nil || !s.authorizedFetchMode() {
		return nil, nil
	}
	appendVaryHeader(c, "Signature")
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	account, err := s.verifyActivityPubSignature(c, body)
	if err != nil {
		return nil, apiError(c, activityPubSignatureErrorStatus(err), err.Error())
	}
	return account, nil
}

func (s *Server) activityPubSignatureAccountForPublicFetch(c *echo.Context) (*models.Account, error) {
	if s == nil || s.authorizedFetchMode() {
		return s.activityPubSignatureAccountIfAuthorized(c)
	}
	if strings.TrimSpace(c.Request().Header.Get("Signature")) == "" {
		return nil, nil
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	account, err := s.verifyActivityPubSignature(c, body)
	if err != nil {
		return nil, nil
	}
	return account, nil
}

func activityPubSignatureErrorStatus(err error) int {
	if errors.Is(err, errActivitySignatureMalformed) {
		return http.StatusBadRequest
	}
	if errors.Is(err, errActivitySignatureTemporary) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, errActivitySignatureDomainNotAllowed) {
		return http.StatusForbidden
	}
	return http.StatusUnauthorized
}

func appendVaryHeader(c *echo.Context, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	header := c.Response().Header()
	current := header.Get("Vary")
	for _, part := range strings.Split(current, ",") {
		if strings.EqualFold(strings.TrimSpace(part), value) {
			return
		}
	}
	if strings.TrimSpace(current) == "" {
		header.Set("Vary", value)
		return
	}
	header.Set("Vary", current+", "+value)
}

func parseActivitySignature(raw string) (map[string]string, error) {
	params, err := parseActivitySignatureParamsStrict(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params["keyId"]) == "" || strings.TrimSpace(params["signature"]) == "" {
		return nil, fmt.Errorf("incompatible request signature")
	}
	return params, nil
}

func parseActivitySignatureParams(raw string) (map[string]string, error) {
	return parseActivitySignatureParamsWithTokenValidation(raw, false)
}

func parseActivitySignatureParamsStrict(raw string) (map[string]string, error) {
	return parseActivitySignatureParamsWithTokenValidation(raw, true)
}

func parseActivitySignatureParamsWithTokenValidation(raw string, strictTokens bool) (map[string]string, error) {
	if strings.HasPrefix(raw, "Signature ") {
		raw = strings.TrimPrefix(raw, "Signature ")
	}
	if activitySignatureHasOuterWhitespace(raw) {
		return nil, fmt.Errorf("error parsing signature parameters")
	}
	if raw == "" {
		return nil, fmt.Errorf("request not signed")
	}
	params := map[string]string{}
	parts, err := splitActivitySignatureParams(raw)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("error parsing signature parameters")
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, fmt.Errorf("error parsing signature parameters")
		}
		if strictTokens && !activitySignatureTokenValid(key) {
			return nil, fmt.Errorf("error parsing signature parameters")
		}
		if _, exists := params[key]; exists {
			return nil, fmt.Errorf("error parsing signature with duplicate keys")
		}
		quoted := strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2
		if !quoted && value == "" {
			return nil, fmt.Errorf("error parsing signature parameters")
		}
		if quoted {
			value = unquoteActivitySignatureParam(value[1 : len(value)-1])
		} else if strictTokens && (strings.Contains(value, `"`) || !activitySignatureTokenValid(value)) {
			return nil, fmt.Errorf("error parsing signature parameters")
		}
		params[key] = value
	}
	return params, nil
}

func activitySignatureHasOuterWhitespace(raw string) bool {
	var last rune
	seen := false
	for _, r := range raw {
		if !seen {
			seen = true
			if unicode.IsSpace(r) {
				return true
			}
		}
		last = r
	}
	if !seen {
		return false
	}
	return unicode.IsSpace(last)
}

func splitActivitySignatureParams(raw string) ([]string, error) {
	var out []string
	var b strings.Builder
	inQuote := false
	escaped := false
	for _, r := range raw {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\' && inQuote:
			b.WriteRune(r)
			escaped = true
		case r == '"':
			b.WriteRune(r)
			inQuote = !inQuote
		case r == ',' && !inQuote:
			part := strings.TrimSpace(b.String())
			if part == "" {
				return nil, fmt.Errorf("error parsing signature parameters")
			}
			out = append(out, part)
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	part := strings.TrimSpace(b.String())
	if part == "" && len(out) > 0 {
		return nil, fmt.Errorf("error parsing signature parameters")
	}
	if part != "" {
		out = append(out, part)
	}
	if inQuote || escaped {
		return nil, fmt.Errorf("error parsing signature parameters")
	}
	return out, nil
}

func unquoteActivitySignatureParam(raw string) string {
	var b strings.Builder
	escaped := false
	for _, r := range raw {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteRune('\\')
	}
	return b.String()
}

func activitySignatureTokenValid(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case strings.ContainsRune("!#$%&'*+.^_`|~-", r):
		default:
			return false
		}
	}
	return true
}

func decodeActivitySignatureParam(raw string) ([]byte, error) {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '+' || r == '/':
		default:
			continue
		}
		b.WriteRune(r)
	}
	value := b.String()
	switch len(value) % 4 {
	case 1:
		value = value[:len(value)-1]
	case 2:
		value += "=="
	case 3:
		value += "="
	}
	return base64.StdEncoding.DecodeString(value)
}

func activitySignedHeaders(params map[string]string, algorithm string) []string {
	defaultHeaders := "date"
	if algorithm == "hs2019" {
		defaultHeaders = "(created)"
	}
	raw, ok := params["headers"]
	if !ok {
		raw = defaultHeaders
	}
	raw = strings.ToLower(raw)
	return strings.Fields(raw)
}

func activitySignatureAlgorithm(params map[string]string) string {
	raw, ok := params["algorithm"]
	if !ok {
		raw = "hs2019"
	}
	return raw
}

func validateActivitySignatureStrength(req *http.Request, headers []string) error {
	hasDate := stringSliceContains(headers, "date") || stringSliceContains(headers, "(created)")
	hasTargetOrDigest := stringSliceContains(headers, "(request-target)") || stringSliceContains(headers, "digest")
	if !hasDate {
		return fmt.Errorf("mastodon requires the Date header or (created) pseudo-header to be signed")
	}
	if !hasTargetOrDigest {
		return fmt.Errorf("mastodon requires the Digest header or (request-target) pseudo-header to be signed")
	}
	if req.Method == http.MethodGet && !stringSliceContains(headers, "host") {
		return fmt.Errorf("mastodon requires the Host header to be signed when doing a GET request")
	}
	if req.Method == http.MethodPost && !stringSliceContains(headers, "digest") {
		return fmt.Errorf("mastodon requires the Digest header to be signed when doing a POST request")
	}
	return nil
}

func validateActivitySignaturePseudoHeaders(params map[string]string, headers []string, algorithm string) error {
	if stringSliceContains(headers, "(created)") {
		if algorithm != "hs2019" {
			return fmt.Errorf("invalid pseudo-header (created) for rsa-sha256")
		}
		if strings.TrimSpace(params["created"]) == "" {
			return fmt.Errorf("pseudo-header (created) used but corresponding argument missing")
		}
	}
	if stringSliceContains(headers, "(expires)") {
		if algorithm != "hs2019" {
			return fmt.Errorf("invalid pseudo-header (expires) for rsa-sha256")
		}
		if strings.TrimSpace(params["expires"]) == "" {
			return fmt.Errorf("pseudo-header (expires) used but corresponding argument missing")
		}
	}
	return nil
}

func validateActivitySignatureTime(req *http.Request, params map[string]string, algorithm string) error {
	var created time.Time
	var expires time.Time
	createdParam := params["created"]
	expiresParam := params["expires"]
	dateHeader := req.Header.Get("Date")
	if algorithm == "hs2019" && strings.TrimSpace(createdParam) != "" {
		created = time.Unix(railsInt64FromString(createdParam), 0).UTC()
	} else if strings.TrimSpace(dateHeader) != "" {
		parsed, err := http.ParseTime(dateHeader)
		if err != nil {
			return fmt.Errorf("invalid Date header")
		}
		created = parsed.UTC()
	}
	if strings.TrimSpace(expiresParam) != "" {
		expires = time.Unix(railsInt64FromString(expiresParam), 0).UTC()
	}
	if !created.IsZero() {
		if expires.IsZero() {
			expires = created.Add(5 * time.Minute)
		}
		maxExpires := created.Add(activitySignatureExpirationWindow)
		if expires.After(maxExpires) {
			expires = maxExpires
		}
	}
	now := time.Now().UTC()
	if (!created.IsZero() && created.After(now.Add(activitySignatureClockSkew))) || (!expires.IsZero() && now.After(expires.Add(activitySignatureClockSkew))) {
		return fmt.Errorf("signed request date outside acceptable time window")
	}
	return nil
}

func validateActivityDigest(req *http.Request, headers []string, body []byte) error {
	if !stringSliceContains(headers, "digest") {
		return nil
	}
	raw := req.Header.Get("Digest")
	if raw == "" {
		return fmt.Errorf("digest header missing")
	}
	var provided string
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(part, "=")
		if ok && strings.ToLower(key) == "sha-256" {
			provided = value
			break
		}
	}
	if provided == "" {
		return fmt.Errorf("mastodon only supports SHA-256 in Digest header")
	}
	sum := sha256.Sum256(body)
	if base64.StdEncoding.EncodeToString(sum[:]) != provided {
		decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(provided))
		if err != nil {
			return fmt.Errorf("invalid Digest value")
		}
		if len(decoded) != sha256.Size {
			return fmt.Errorf("invalid Digest value")
		}
		return fmt.Errorf("invalid Digest value")
	}
	return nil
}

func (s *Server) activityPubActorFromKeyID(keyID string) (*models.Account, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database is not available")
	}
	if disallowed, err := s.activityPubKeyIDDomainNotAllowed(keyID); err != nil || disallowed {
		if err != nil {
			return nil, err
		}
		return nil, errActivitySignatureDomainNotAllowed
	}
	var account models.Account
	query := s.db.Preload("AccountStat")
	resolvedAcct := ""
	localAcctKeyID := false
	if strings.HasPrefix(keyID, "acct:") {
		username, domain, ok := activityPubAcctKeyIDUsernameDomain(keyID)
		if !ok {
			return nil, fmt.Errorf("public key not found")
		}
		resolvedAcct = username + "@" + domain
		if activityHostWithNormalizedName(domain) == activityHostWithNormalizedName(s.cfg.LocalDomain) {
			localAcctKeyID = true
			query = query.Where("lower(username) = lower(?) AND domain IS NULL", username)
		} else {
			query = query.Where("lower(username) = lower(?) AND lower(domain) = lower(?)", username, domain)
		}
	} else {
		if s.localActivityURI(keyID) {
			return nil, fmt.Errorf("public key not found")
		}
		actorURI := keyID
		if before, _, ok := strings.Cut(keyID, "#"); ok {
			actorURI = before
		}
		query = query.Where("uri = ?", actorURI)
	}
	err := query.First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if strings.HasPrefix(keyID, "acct:") {
			if localAcctKeyID {
				return nil, fmt.Errorf("public key not found")
			}
			return s.fetchAndStoreActivityActorForAcct(resolvedAcct)
		}
		if strings.HasPrefix(keyID, "http://") || strings.HasPrefix(keyID, "https://") {
			return s.fetchAndStoreActivityActorForKeyID(keyID)
		}
		return nil, fmt.Errorf("public key not found")
	}
	return &account, err
}

func (s *Server) activityPubActorFromKeyIDWithSourceStoplight(c *echo.Context, keyID string) (*models.Account, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database is not available")
	}
	if disallowed, err := s.activityPubKeyIDDomainNotAllowed(keyID); err != nil || disallowed {
		if err != nil {
			return nil, err
		}
		return nil, errActivitySignatureDomainNotAllowed
	}
	if strings.HasPrefix(keyID, "acct:") {
		return activityPubSignatureSourceStoplightWrap(s, c, func() (*models.Account, error) {
			return s.activityPubActorFromKeyID(keyID)
		})
	}
	if s.localActivityURI(keyID) {
		return nil, fmt.Errorf("public key not found")
	}
	actorURI := keyID
	if before, _, ok := strings.Cut(keyID, "#"); ok {
		actorURI = before
	}
	var account models.Account
	err := s.db.Preload("AccountStat").Where("uri = ?", actorURI).First(&account).Error
	if err == nil {
		return &account, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if strings.HasPrefix(keyID, "http://") || strings.HasPrefix(keyID, "https://") {
		return activityPubSignatureSourceStoplightWrap(s, c, func() (*models.Account, error) {
			return s.fetchAndStoreActivityActorForKeyID(keyID)
		})
	}
	return nil, fmt.Errorf("public key not found")
}

func activityPubAcctKeyIDUsernameDomain(keyID string) (string, string, bool) {
	acct := strings.TrimSpace(strings.TrimPrefix(keyID, "acct:"))
	parts := strings.Split(acct, "@")
	if len(parts) < 2 {
		return "", "", false
	}
	username := parts[0]
	domain := normalizeDeliveryStatsHost(parts[1])
	if username == "" || domain == "" {
		return "", "", false
	}
	return username, domain, true
}

func (s *Server) refreshActivityPubActorKey(keyID string, account *models.Account) (*models.Account, error) {
	if account == nil || account.Local() {
		return account, nil
	}
	if disallowed, err := s.activityPubKeyIDDomainNotAllowed(keyID); err != nil || disallowed {
		if err != nil {
			return nil, err
		}
		return nil, errActivitySignatureDomainNotAllowed
	}
	return s.refreshKnownActivityPubActorOnlyKey(account)
}

func (s *Server) refreshActivityPubActorKeyWithSourceStoplight(c *echo.Context, keyID string, account *models.Account) (*models.Account, error) {
	if account == nil || account.Local() {
		return s.refreshActivityPubActorKey(keyID, account)
	}
	return activityPubSignatureSourceStoplightWrap(s, c, func() (*models.Account, error) {
		return s.refreshActivityPubActorKey(keyID, account)
	})
}

func activityPubSignatureSourceStoplightWrap(s *Server, c *echo.Context, fn func() (*models.Account, error)) (*models.Account, error) {
	source := activityPubSignatureSourceIP(c)
	if s.activityPubSignatureSourceStoplightOpen(source) {
		return nil, fmt.Errorf("fetching attempt skipped because of recent connection failure")
	}
	account, err := fn()
	if err != nil {
		s.trackActivityPubSignatureSourceStoplightFailure(source)
		return account, err
	}
	s.trackActivityPubSignatureSourceStoplightSuccess(source)
	return account, nil
}

func activityPubSignatureSourceIP(c *echo.Context) string {
	if c == nil {
		return ""
	}
	if ip := strings.TrimSpace((*c).RealIP()); ip != "" {
		return ip
	}
	req := (*c).Request()
	if req == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(req.RemoteAddr)
}

func activityPubSignatureSourceStoplightKey(prefix string, source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(source))
	return prefix + "activitypub:signature:source:stoplight:" + fmt.Sprintf("%x", sum[:])
}

func (s *Server) activityPubSignatureSourceStoplightOpen(source string) bool {
	if s == nil || s.db == nil {
		return false
	}
	key := activityPubSignatureSourceStoplightKey(redisConfig(s.cfg).prefix, source)
	if key == "" {
		return false
	}
	value, err := s.redisCommand(context.Background(), "GET", key)
	if err != nil {
		return false
	}
	return redisInt(value) >= activitySignatureSourceStoplightFailureThreshold
}

func (s *Server) trackActivityPubSignatureSourceStoplightFailure(source string) {
	if s == nil || s.db == nil {
		return
	}
	key := activityPubSignatureSourceStoplightKey(redisConfig(s.cfg).prefix, source)
	if key == "" {
		return
	}
	ctx := context.Background()
	if _, err := s.redisCommand(ctx, "INCR", key); err != nil {
		return
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.FormatInt(int64(activitySignatureSourceStoplightCooldown/time.Second), 10))
}

func (s *Server) trackActivityPubSignatureSourceStoplightSuccess(source string) {
	if s == nil || s.db == nil {
		return
	}
	key := activityPubSignatureSourceStoplightKey(redisConfig(s.cfg).prefix, source)
	if key == "" {
		return
	}
	_, _ = s.redisCommand(context.Background(), "DEL", key)
}

func (s *Server) activityPubKeyIDDomainNotAllowed(keyID string) (bool, error) {
	domain := activityPubKeyIDDomain(keyID)
	if domain == "" {
		return false, nil
	}
	return s.domainNotAllowed(domain)
}

func activityPubKeyIDDomain(keyID string) string {
	keyID = strings.TrimSpace(keyID)
	if strings.HasPrefix(keyID, "acct:") {
		acct := strings.TrimPrefix(keyID, "acct:")
		at := strings.LastIndex(acct, "@")
		if at < 0 || at == len(acct)-1 {
			return ""
		}
		domain := acct[at+1:]
		return normalizeDeliveryStatsHost(domain)
	}
	return activityPubNormalizedURIHost(keyID)
}

func (s *Server) fetchAndStoreActivityActorForAcct(acct string) (*models.Account, error) {
	return s.fetchAndStoreActivityActorForAcctDB(s.db, acct)
}

func (s *Server) fetchAndStoreActivityActorForAcctDB(database *gorm.DB, acct string) (*models.Account, error) {
	actorURL, resolvedAcct, err := fetchActivityActorURLAndAcctFromWebFinger(acct)
	if err != nil {
		return nil, err
	}
	acquired, releaseResolveLock, err := s.acquireActivityPubRedisLock(context.Background(), "resolve:"+resolvedAcct, 15*time.Minute)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("temporary problem resolving account")
	}
	defer releaseResolveLock()
	actor, err := s.fetchActivityActorAtURI(actorURL)
	if err != nil {
		return nil, err
	}
	if actor.ID != actorURL {
		return nil, fmt.Errorf("webfinger response does not loop back to actor")
	}
	if actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
		return nil, fmt.Errorf("public key not found")
	}
	account, err := s.upsertRemoteActivityActorDB(database, actor)
	if err != nil || account == nil {
		return account, err
	}
	if err := s.enqueueOrMergeDuplicateRemoteActivityPubAccounts(context.Background(), database, *account); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Server) fetchAndStoreActivityActorForKeyID(keyID string) (*models.Account, error) {
	actorURI := keyID
	if before, _, ok := strings.Cut(keyID, "#"); ok {
		actorURI = before
	}
	actor, err := s.fetchActivityActor(actorURI)
	if err != nil {
		actor, err = s.fetchActivityActorForPublicKeyDocument(keyID)
		if err != nil {
			return nil, err
		}
	}
	if actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
		return nil, fmt.Errorf("public key not found")
	}
	if actor.PublicKey.ID != "" && actor.PublicKey.ID != keyID && actor.PublicKey.Owner != keyID && actor.PublicKey.Owner != actor.ID {
		return nil, fmt.Errorf("public key not found")
	}
	if err := verifyRemoteActivityActorWebFinger(actor); err != nil {
		return nil, err
	}
	return s.upsertRemoteActivityActor(actor)
}

func (s *Server) refreshKnownActivityPubActorOnlyKey(account *models.Account) (*models.Account, error) {
	if account == nil || account.Local() || s.db == nil {
		return account, nil
	}
	actor, err := s.fetchActivityActorAtURI(account.URI)
	if err != nil {
		return nil, err
	}
	if actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
		return nil, fmt.Errorf("public key not found")
	}
	if actor.ID != account.URI {
		return nil, fmt.Errorf("actor id does not match")
	}
	now := time.Now().UTC()
	suspensionTransition := activityActorSuspensionTransitionFor(*account, actor.Suspended)
	suspendedAfterUpdate := activityActorSuspendedAfterRemoteUpdate(*account, actor.Suspended)
	updates := map[string]any{
		"updated_at":       now,
		"protocol":         1,
		"inbox_url":        actor.Inbox,
		"outbox_url":       actor.Outbox,
		"shared_inbox_url": actor.SharedInbox(),
		"followers_url":    actor.Followers,
		"url":              sql.NullString{String: firstActivityActorURL(actor.URL, actor.ID), Valid: firstActivityActorURL(actor.URL, actor.ID) != ""},
		"uri":              actor.ID,
		"actor_type":       sql.NullString{String: firstNonEmpty(actor.Type, "Person"), Valid: true},
	}
	if !suspendedAfterUpdate {
		updates["note"] = actor.Summary
		updates["display_name"] = actor.Name
		updates["locked"] = actor.ManuallyApprovesFollowers
		updates["memorial"] = actor.Memorial
		updates["discoverable"] = sql.NullBool{Bool: actor.Discoverable, Valid: true}
		updates["indexable"] = actor.Indexable
		updates["featured_collection_url"] = sql.NullString{String: actor.Featured, Valid: actor.Featured != ""}
		updates["also_known_as"] = models.StringArray(activityLimitedValueOrIDList(actor.AlsoKnownAs, 256))
		updates["attribution_domains"] = models.StringArray(activityLimitedStringList(actor.AttributionDomains, 256))
	}
	if actor.Published != "" {
		updates["created_at"] = activityActorPublishedAt(actor.Published, account.CreatedAt)
	}
	if !activityActorLocallySuspended(*account) {
		updates["public_key"] = actor.PublicKey.PublicKeyPem
	}
	for key, value := range activityActorSuspensionUpdatesForTransition(suspensionTransition, now) {
		updates[key] = value
	}
	if !suspendedAfterUpdate {
		fields := activityProfileFields(actor.Attachment)
		if fields == nil {
			fields = []profileField{}
		}
		encoded, _ := json.Marshal(fields)
		updates["fields"] = models.JSONValue(encoded)
	}
	keyChanged := false
	var customEmojiChanges []models.CustomEmoji
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if !suspendedAfterUpdate {
			skipMedia, err := activityPubActorSkipsMedia(tx, account)
			if err != nil {
				return err
			}
			if !skipMedia {
				changes, err := s.saveActivityPubActorCustomEmojis(tx, account, actor.Tags, now)
				if err != nil {
					return err
				}
				customEmojiChanges = append(customEmojiChanges, changes...)
			}
		}
		if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
			return err
		}
		if activityActorLocallySuspended(*account) {
			return nil
		}
		keyChanged = activityPubActorKeyChanged(account.PublicKey, actor.PublicKey.PublicKeyPem)
		return clearActivityPubActorTombstonesOnKeyChange(tx, account.ID, account.PublicKey, actor.PublicKey.PublicKeyPem)
	}); err != nil {
		return nil, err
	}
	s.invalidateCustomEmojiEntityCaches(context.Background(), customEmojiChanges)
	if err := s.applyActivityActorSuspensionTransitionEffects(context.Background(), s.db, account.ID, suspensionTransition); err != nil {
		return nil, err
	}
	refreshed := *account
	if err := s.db.Preload("AccountStat").Where("id = ?", account.ID).First(&refreshed).Error; err != nil {
		return nil, err
	}
	if keyChanged {
		if err := s.refollowLocalFollowersAfterActivityPubKeyChange(context.Background(), s.db, refreshed); err != nil {
			return nil, err
		}
	}
	return &refreshed, nil
}

func (s *Server) fetchActivityActorForPublicKeyDocument(keyID string) (remoteActivityActor, error) {
	resource, err := s.fetchActivityResourceWithRepresentative(keyID)
	if err != nil {
		return remoteActivityActor{}, err
	}
	resource, err = s.fetchActivityCanonicalPublicKeyResource(resource)
	if err != nil {
		return remoteActivityActor{}, err
	}
	if actor, err := s.parseRemoteActivityActor(resource.body); err == nil {
		return s.fetchActivityActorPublicKey(actor), nil
	}
	key, err := parseRemoteActivityPublicKey(resource.body)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor, err := s.fetchActivityActor(activityRawNonBlank(key.OwnerRaw, key.Owner))
	if err != nil {
		return remoteActivityActor{}, err
	}
	if activityRemoteActorPublicKeyRawID(actor.PublicKey) != activityRawNonBlank(key.IDRaw, key.ID) {
		return remoteActivityActor{}, fmt.Errorf("public key not found")
	}
	if actor.PublicKey.PublicKeyPem == "" {
		actor.PublicKey.PublicKeyPem = key.PublicKeyPem
	}
	if actor.PublicKey.ID == "" {
		actor.PublicKey.ID = key.ID
	}
	if actor.PublicKey.Owner == "" {
		actor.PublicKey.Owner = key.Owner
	}
	if actor.PublicKey.IDRaw == "" {
		actor.PublicKey.IDRaw = activityRawNonBlank(key.IDRaw, key.ID)
	}
	if actor.PublicKey.OwnerRaw == "" {
		actor.PublicKey.OwnerRaw = activityRawNonBlank(key.OwnerRaw, key.Owner)
	}
	return actor, nil
}

func fetchActivityActorForPublicKeyDocument(keyID string) (remoteActivityActor, error) {
	resource, err := fetchActivityResourceWithMetadata(keyID)
	if err != nil {
		return remoteActivityActor{}, err
	}
	resource, err = fetchActivityCanonicalPublicKeyResource(resource)
	if err != nil {
		return remoteActivityActor{}, err
	}
	if actor, err := parseRemoteActivityActor(resource.body); err == nil {
		return fetchActivityActorPublicKey(actor), nil
	}
	key, err := parseRemoteActivityPublicKey(resource.body)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor, err := fetchActivityActor(activityRawNonBlank(key.OwnerRaw, key.Owner))
	if err != nil {
		return remoteActivityActor{}, err
	}
	if activityRemoteActorPublicKeyRawID(actor.PublicKey) != activityRawNonBlank(key.IDRaw, key.ID) {
		return remoteActivityActor{}, fmt.Errorf("public key not found")
	}
	if actor.PublicKey.PublicKeyPem == "" {
		actor.PublicKey.PublicKeyPem = key.PublicKeyPem
	}
	if actor.PublicKey.ID == "" {
		actor.PublicKey.ID = key.ID
	}
	if actor.PublicKey.Owner == "" {
		actor.PublicKey.Owner = key.Owner
	}
	if actor.PublicKey.IDRaw == "" {
		actor.PublicKey.IDRaw = activityRawNonBlank(key.IDRaw, key.ID)
	}
	if actor.PublicKey.OwnerRaw == "" {
		actor.PublicKey.OwnerRaw = activityRawNonBlank(key.OwnerRaw, key.Owner)
	}
	return actor, nil
}

func (s *Server) fetchActivityCanonicalPublicKeyResource(resource fetchedActivityResource) (fetchedActivityResource, error) {
	canonicalURI, err := activityCanonicalPublicKeyResourceURI(resource.body)
	if err != nil {
		return fetchedActivityResource{}, err
	}
	return s.fetchActivityResourceWithRepresentative(canonicalURI)
}

func fetchActivityCanonicalPublicKeyResource(resource fetchedActivityResource) (fetchedActivityResource, error) {
	canonicalURI, err := activityCanonicalPublicKeyResourceURI(resource.body)
	if err != nil {
		return fetchedActivityResource{}, err
	}
	return fetchActivityResourceWithMetadata(canonicalURI)
}

func activityCanonicalPublicKeyResourceURI(body []byte) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	canonicalURI := activityJSONLDID(raw)
	if !activityActorURISupported(canonicalURI) {
		return "", fmt.Errorf("public key not found")
	}
	return canonicalURI, nil
}

func fetchActivityActorURLFromWebFinger(acct string) (string, error) {
	actorURL, _, err := fetchActivityActorURLAndAcctFromWebFinger(acct)
	return actorURL, err
}

func fetchActivityActorURLAndAcctFromWebFinger(acct string) (string, string, error) {
	return fetchActivityActorURLAndAcctFromWebFingerDepth(acct, 0)
}

func fetchActivityActorURLFromWebFingerDepth(acct string, depth int) (string, error) {
	actorURL, _, err := fetchActivityActorURLAndAcctFromWebFingerDepth(acct, depth)
	return actorURL, err
}

func fetchActivityActorURLAndAcctFromWebFingerDepth(acct string, depth int) (string, string, error) {
	if depth > 1 {
		return "", "", fmt.Errorf("too many webfinger redirects")
	}
	acct, domain, ok := normalizeActivityWebFingerAcct(acct)
	if !ok {
		return "", "", fmt.Errorf("public key not found")
	}
	if !activityFetchHostAllowed(domain) {
		return "", "", fmt.Errorf("remote host is not allowed")
	}
	resource := "acct:" + acct
	doc, err := fetchActivityWebFingerDocument(activityWebFingerURL(domain, resource))
	if status, ok := activityFetchStatus(err); ok && status == http.StatusNotFound {
		fallbackURL, fallbackErr := fetchActivityWebFingerHostMetaURL(domain, resource)
		if fallbackErr != nil {
			return "", "", fallbackErr
		}
		doc, err = fetchActivityWebFingerDocument(fallbackURL)
	}
	if err != nil {
		return "", "", err
	}
	subjectAcct, normalizedSubject, subjectOK := activityWebFingerSubjectAcct(doc.Subject)
	if !subjectOK || !strings.EqualFold(normalizedSubject, acct) {
		if depth == 0 && subjectOK {
			return fetchActivityActorURLAndAcctFromWebFingerDepth(subjectAcct, depth+1)
		}
		return "", "", fmt.Errorf("webfinger subject does not match")
	}
	for _, link := range doc.Links {
		if link.Rel == "self" && activityWebFingerSelfLinkActivityPubReady(link.Type) && link.Href != "" {
			return link.Href, normalizedSubject, nil
		}
	}
	return "", "", fmt.Errorf("public key not found")
}

func fetchActivityWebFingerDocument(endpoint string) (activityWebFinger, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return activityWebFinger{}, err
	}
	req.Header.Set("Accept", "application/jrd+json, application/json")
	resp, err := activityWebFingerHTTPClient().Do(req)
	if err != nil {
		return activityWebFinger{}, taskTargetError("webfinger fetch", "remote", remoteTaskTargetHost(endpoint), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return activityWebFinger{}, activityFetchHTTPError{StatusCode: resp.StatusCode, URL: endpoint}
	}
	body, err := readActivityResponseBodyWithRailsLimit(resp, "webfinger")
	if err != nil {
		return activityWebFinger{}, err
	}
	var doc activityWebFinger
	if err := json.Unmarshal(body, &doc); err != nil {
		return activityWebFinger{}, err
	}
	return doc, nil
}

func fetchActivityWebFingerHostMetaURL(domain string, resource string) (string, error) {
	endpoint := url.URL{Scheme: activityWebFingerScheme(domain), Host: domain, Path: "/.well-known/host-meta"}
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/xrd+xml, application/xml, text/xml")
	resp, err := activityWebFingerHTTPClient().Do(req)
	if err != nil {
		return "", taskTargetError("webfinger host-meta fetch", "remote", endpoint.Hostname(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", activityFetchHTTPError{StatusCode: resp.StatusCode, URL: endpoint.String()}
	}
	body, err := readActivityResponseBodyWithRailsLimit(resp, "host-meta")
	if err != nil {
		return "", err
	}
	template, err := activityWebFingerHostMetaTemplate(body)
	if err != nil {
		return "", err
	}
	webFingerURL := strings.ReplaceAll(template, "{uri}", resource)
	parsed, err := url.Parse(webFingerURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("invalid webfinger template")
	}
	if !activityFetchHostAllowed(parsed.Hostname()) {
		return "", fmt.Errorf("remote host is not allowed")
	}
	return webFingerURL, nil
}

func activityWebFingerHTTPClient() *http.Client {
	client := *activityHTTPClientClone(activityHTTPReadDeadline)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		if !activityRedirectAllowed(req, via) {
			return fmt.Errorf("remote host is not allowed")
		}
		return nil
	}
	return &client
}

func readActivityResponseBodyWithRailsLimit(resp *http.Response, label string) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("%s response body is missing", label)
	}
	if resp.ContentLength > maxActivityResourceBodySize {
		return nil, fmt.Errorf("%s body too large", label)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxActivityResourceBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxActivityResourceBodySize {
		return nil, fmt.Errorf("%s body too large", label)
	}
	return body, nil
}

func activityWebFingerHostMetaTemplate(body []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("invalid host-meta XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Link" || start.Name.Space != activityWebFingerHostMetaNamespace {
			continue
		}
		rel := ""
		template := ""
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "rel":
				rel = attr.Value
			case "template":
				template = attr.Value
			}
		}
		if rel == "lrdd" && template != "" {
			return template, nil
		}
	}
	return "", fmt.Errorf("host-meta without webfinger link")
}

func activityWebFingerSelfLinkActivityPubReady(linkType string) bool {
	switch linkType {
	case "application/activity+json", `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`:
		return true
	default:
		return false
	}
}

const activityWebFingerHostMetaNamespace = "http://docs.oasis-open.org/ns/xri/xrd-1.0"

func activityWebFingerURL(domain string, resource string) string {
	endpoint := url.URL{Scheme: activityWebFingerScheme(domain), Host: domain, Path: "/.well-known/webfinger"}
	query := endpoint.Query()
	query.Set("resource", resource)
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func activityWebFingerScheme(domain string) string {
	if strings.HasSuffix(domain, ".onion") {
		return "http"
	}
	return "https"
}

func normalizeActivityWebFingerAcct(acct string) (string, string, bool) {
	parts := strings.Split(strings.TrimSpace(acct), "@")
	if len(parts) < 2 {
		return "", "", false
	}
	username, domain := parts[0], parts[1]
	username = strings.TrimSpace(strings.TrimPrefix(username, "acct:"))
	domain = normalizeDeliveryStatsHost(domain)
	if username == "" || domain == "" {
		return "", "", false
	}
	return username + "@" + domain, domain, true
}

func activityWebFingerSubjectAcct(subject string) (string, string, bool) {
	if subject != strings.TrimSpace(subject) {
		return "", "", false
	}
	acct := strings.TrimPrefix(subject, "acct:")
	parts := strings.Split(acct, "@")
	if len(parts) < 2 {
		return "", "", false
	}
	username, domain := parts[0], parts[1]
	if username == "" || domain == "" || username != strings.TrimSpace(username) || domain != strings.TrimSpace(domain) {
		return "", "", false
	}
	domain = normalizeDeliveryStatsHost(domain)
	if domain == "" {
		return "", "", false
	}
	return acct, username + "@" + domain, true
}

func fetchActivityActor(actorURL string) (remoteActivityActor, error) {
	return fetchActivityActorDepth(actorURL, 0)
}

func (s *Server) fetchActivityActor(actorURL string) (remoteActivityActor, error) {
	return s.fetchActivityActorDepth(actorURL, 0)
}

func fetchActivityActorAtURI(actorURL string) (remoteActivityActor, error) {
	resource, err := fetchActivityResourceWithMetadata(actorURL)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor, err := parseRemoteActivityActor(resource.body)
	if err != nil {
		return remoteActivityActor{}, err
	}
	return fetchActivityActorPublicKey(actor), nil
}

func (s *Server) fetchActivityActorAtURI(actorURL string) (remoteActivityActor, error) {
	resource, err := s.fetchActivityResourceWithRepresentative(actorURL)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor, err := s.parseRemoteActivityActor(resource.body)
	if err != nil {
		return remoteActivityActor{}, err
	}
	return s.fetchActivityActorPublicKey(actor), nil
}

func fetchActivityActorDepth(actorURL string, depth int) (remoteActivityActor, error) {
	if depth > maxActivityFetchDepth {
		return remoteActivityActor{}, fmt.Errorf("remote actor fetch depth exceeded")
	}
	resource, err := fetchActivityResourceWithMetadata(actorURL)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor, err := parseRemoteActivityActor(resource.body)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor = fetchActivityActorPublicKey(actor)
	if actor.ID != "" && actor.ID != actorURL {
		return remoteActivityActor{}, fmt.Errorf("remote actor id mismatch")
	}
	return actor, nil
}

func (s *Server) fetchActivityActorDepth(actorURL string, depth int) (remoteActivityActor, error) {
	if depth > maxActivityFetchDepth {
		return remoteActivityActor{}, fmt.Errorf("remote actor fetch depth exceeded")
	}
	resource, err := s.fetchActivityResourceWithRepresentative(actorURL)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor, err := s.parseRemoteActivityActor(resource.body)
	if err != nil {
		return remoteActivityActor{}, err
	}
	actor = s.fetchActivityActorPublicKey(actor)
	if actor.ID != "" && actor.ID != actorURL {
		return remoteActivityActor{}, fmt.Errorf("remote actor id mismatch")
	}
	return actor, nil
}

func fetchActivityActorPublicKey(actor remoteActivityActor) remoteActivityActor {
	if actor.PublicKey.PublicKeyPem != "" || actor.PublicKey.ID == "" {
		return actor
	}
	resource, err := fetchActivityResourceWithMetadata(actor.PublicKey.ID)
	if err != nil {
		return actor
	}
	var raw map[string]any
	if err := json.Unmarshal(resource.body, &raw); err != nil {
		return actor
	}
	if graphKey := activityJSONLDGraphPublicKey(raw); graphKey != nil {
		raw = graphKey
	}
	if pemValue := activityJSONLDString(raw, "publicKeyPem"); pemValue != "" {
		actor.PublicKey.PublicKeyPem = pemValue
	}
	if owner := activityJSONLDObjectID(raw, "owner"); owner != "" {
		actor.PublicKey.Owner = owner
	}
	if ownerRaw := activityJSONLDValueOrID(activityJSONLDValue(raw, "owner")); ownerRaw != "" && actor.PublicKey.OwnerRaw == "" {
		actor.PublicKey.OwnerRaw = ownerRaw
	}
	if id := activityJSONLDID(raw); id != "" {
		actor.PublicKey.ID = id
	}
	if idRaw := activityJSONLDValueOrID(activityJSONLDValue(raw, "id")); idRaw != "" && actor.PublicKey.IDRaw == "" {
		actor.PublicKey.IDRaw = idRaw
	}
	return actor
}

func (s *Server) fetchActivityActorPublicKey(actor remoteActivityActor) remoteActivityActor {
	if actor.PublicKey.PublicKeyPem != "" || actor.PublicKey.ID == "" {
		return actor
	}
	resource, err := s.fetchActivityResourceWithRepresentative(actor.PublicKey.ID)
	if err != nil {
		return actor
	}
	var raw map[string]any
	if err := json.Unmarshal(resource.body, &raw); err != nil {
		return actor
	}
	if graphKey := activityJSONLDGraphPublicKey(raw); graphKey != nil {
		raw = graphKey
	}
	if pemValue := activityJSONLDString(raw, "publicKeyPem"); pemValue != "" {
		actor.PublicKey.PublicKeyPem = pemValue
	}
	if owner := activityJSONLDObjectID(raw, "owner"); owner != "" {
		actor.PublicKey.Owner = owner
	}
	if ownerRaw := activityJSONLDValueOrID(activityJSONLDValue(raw, "owner")); ownerRaw != "" && actor.PublicKey.OwnerRaw == "" {
		actor.PublicKey.OwnerRaw = ownerRaw
	}
	if id := activityJSONLDID(raw); id != "" {
		actor.PublicKey.ID = id
	}
	if idRaw := activityJSONLDValueOrID(activityJSONLDValue(raw, "id")); idRaw != "" && actor.PublicKey.IDRaw == "" {
		actor.PublicKey.IDRaw = idRaw
	}
	return actor
}

func (s *Server) fetchActivityResourceWithRepresentative(uri string) (fetchedActivityResource, error) {
	if s == nil {
		return fetchActivityResourceWithMetadata(uri)
	}
	signer := s.activityFetchSigner(nil)
	return fetchActivityResourceWithMetadataAndUserAgentSigned(uri, paonUserAgent(s.cfg), s, signer)
}

type remoteActivityPublicKey struct {
	ID           string
	IDRaw        string
	Owner        string
	OwnerRaw     string
	PublicKeyPem string
}

func parseRemoteActivityPublicKey(body []byte) (remoteActivityPublicKey, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return remoteActivityPublicKey{}, err
	}
	if !activityResourceSupportedContext(raw["@context"]) {
		return remoteActivityPublicKey{}, fmt.Errorf("unsupported activity context")
	}
	if graphKey := activityJSONLDGraphPublicKey(raw); graphKey != nil {
		raw = graphKey
	}
	key := remoteActivityPublicKey{
		ID:           activityJSONLDID(raw),
		IDRaw:        activityJSONLDValueOrID(activityJSONLDValue(raw, "id")),
		Owner:        activityJSONLDObjectID(raw, "owner"),
		OwnerRaw:     activityJSONLDValueOrID(activityJSONLDValue(raw, "owner")),
		PublicKeyPem: activityJSONLDString(raw, "publicKeyPem"),
	}
	if key.ID == "" || key.Owner == "" || key.PublicKeyPem == "" {
		return remoteActivityPublicKey{}, fmt.Errorf("public key not found")
	}
	return key, nil
}

func activityJSONLDGraphPublicKey(object map[string]any) map[string]any {
	items := activityJSONLDGraphMaps(object)
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if strings.TrimSpace(activityJSONLDString(item, "publicKeyPem")) != "" && activityJSONLDValueOrID(activityJSONLDValue(item, "owner")) != "" {
			return item
		}
	}
	return nil
}

func parseRemoteActivityActor(body []byte) (remoteActivityActor, error) {
	return parseRemoteActivityActorWithImageFetcher(body, fetchActivityActorImageObject)
}

func (s *Server) parseRemoteActivityActor(body []byte) (remoteActivityActor, error) {
	if s == nil {
		return parseRemoteActivityActor(body)
	}
	return parseRemoteActivityActorWithImageFetcher(body, s.fetchActivityActorImageObject)
}

func parseRemoteActivityActorWithImageFetcher(body []byte, imageFetcher func(string) any) (remoteActivityActor, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return remoteActivityActor{}, err
	}
	if !activityResourceSupportedContext(raw["@context"]) {
		return remoteActivityActor{}, fmt.Errorf("unsupported activity context")
	}
	if graphActor := activityJSONLDGraphActor(raw); graphActor != nil {
		raw = graphActor
	}
	typ := activityActorTypeValue(activityJSONLDTypes(raw))
	if !activityActorTypeSupported(typ) {
		return remoteActivityActor{}, fmt.Errorf("unsupported actor type")
	}
	actorID := activityJSONLDIDRaw(raw)
	if !activityActorURISupported(actorID) {
		return remoteActivityActor{}, fmt.Errorf("unsupported actor id")
	}
	inbox := activityActorCollectionURI(activityJSONLDValue(raw, "inbox"))
	if inbox == "" {
		return remoteActivityActor{}, fmt.Errorf("remote actor missing inbox")
	}
	publicKeyValue := activityJSONLDValue(raw, "publicKey")
	publicKey := activityRemoteActorPublicKey(publicKeyValue)
	featuredValue := activityJSONLDValue(raw, "featured")
	featuredTagsValue := activityJSONLDValue(raw, "featuredTags")
	sharedInboxURL := activityActorSharedInboxURLFromObject(raw)
	actor := remoteActivityActor{
		ID:                        actorID,
		Type:                      typ,
		PreferredUsername:         activityJSONLDString(raw, "preferredUsername"),
		Name:                      activityJSONLDString(raw, "name"),
		Summary:                   activityJSONLDString(raw, "summary"),
		Published:                 activityJSONLDString(raw, "published"),
		URL:                       activityJSONLDValue(raw, "url"),
		Inbox:                     inbox,
		Outbox:                    activityActorCollectionURI(activityJSONLDValue(raw, "outbox")),
		Following:                 activityActorCollectionURI(activityJSONLDValue(raw, "following")),
		Followers:                 activityActorCollectionURI(activityJSONLDValue(raw, "followers")),
		Featured:                  activityActorCollectionURI(featuredValue),
		FeaturedCollection:        activityCollectionInlinePage(featuredValue),
		FeaturedTags:              activityActorCollectionURI(featuredTagsValue),
		SharedInboxURL:            sharedInboxURL,
		ManuallyApprovesFollowers: activityBoolValue(activityJSONLDValue(raw, "manuallyApprovesFollowers")),
		Discoverable:              activityBoolValue(activityJSONLDValue(raw, "discoverable")),
		Indexable:                 activityBoolValue(activityJSONLDValue(raw, "indexable")),
		Memorial:                  activityBoolValue(activityJSONLDValue(raw, "memorial")),
		Suspended:                 activityRailsSuspendedTruthy(activityJSONLDValue(raw, "suspended")),
		MovedTo:                   activityJSONLDValue(raw, "movedTo"),
		AlsoKnownAs:               activityJSONLDValue(raw, "alsoKnownAs"),
		AttributionDomains:        activityJSONLDValue(raw, "attributionDomains"),
		Attachment:                activityJSONLDValue(raw, "attachment"),
		AvatarRemoteURL:           activityActorImageURLWithFetcher(activityJSONLDValue(raw, "icon"), imageFetcher),
		HeaderRemoteURL:           activityActorImageURLWithFetcher(activityJSONLDValue(raw, "image"), imageFetcher),
		Tags:                      activityRailsTagList(activityJSONLDValue(raw, "tag")),
		PublicKey:                 publicKey,
	}
	if strings.TrimSpace(actor.PreferredUsername) == "" {
		return remoteActivityActor{}, fmt.Errorf("remote actor missing preferredUsername")
	}
	if len([]rune(actor.PreferredUsername)) > 2048 {
		return remoteActivityActor{}, fmt.Errorf("remote actor preferredUsername exceeds 2048 characters")
	}
	actor.Name = activityTruncateRunes(actor.Name, 2048)
	actor.Summary = activityTruncateUTF8Bytes(actor.Summary, 20*1024)
	return actor, nil
}

func activityRemoteActorPublicKey(value any) struct {
	ID           string `json:"id"`
	IDRaw        string `json:"-"`
	Owner        string `json:"owner"`
	OwnerRaw     string `json:"-"`
	PublicKeyPem string `json:"publicKeyPem"`
} {
	value = activityJSONLDSingle(value)
	var key struct {
		ID           string `json:"id"`
		IDRaw        string `json:"-"`
		Owner        string `json:"owner"`
		OwnerRaw     string `json:"-"`
		PublicKeyPem string `json:"publicKeyPem"`
	}
	if id := activityPubObjectID(value); id != "" {
		key.ID = id
	}
	if idRaw := activityJSONLDValueOrID(value); idRaw != "" {
		key.IDRaw = idRaw
	}
	if object, ok := activityJSONLDSingle(value).(map[string]any); ok {
		if id := activityJSONLDID(object); id != "" {
			key.ID = id
		}
		if idRaw := activityJSONLDValueOrID(object); idRaw != "" {
			key.IDRaw = idRaw
		}
		key.Owner = activityJSONLDObjectID(object, "owner")
		key.OwnerRaw = activityJSONLDValueOrID(activityJSONLDValue(object, "owner"))
		key.PublicKeyPem = activityJSONLDString(object, "publicKeyPem")
	}
	return key
}

func activityRemoteActorPublicKeyRawID(key struct {
	ID           string `json:"id"`
	IDRaw        string `json:"-"`
	Owner        string `json:"owner"`
	OwnerRaw     string `json:"-"`
	PublicKeyPem string `json:"publicKeyPem"`
}) string {
	return activityRawNonBlank(key.IDRaw, key.ID)
}

func activityRawNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func activityActorImageURL(value any) string {
	return activityActorImageURLWithFetcher(value, fetchActivityActorImageObject)
}

func activityActorImageURLWithFetcher(value any, fetcher func(string) any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case string:
		if fetcher == nil {
			return ""
		}
		return activityActorImageURLWithFetcher(fetcher(typed), fetcher)
	case []any:
		items := activityJSONLDListItems(typed)
		if len(items) == 0 {
			return ""
		}
		return activityActorImageURLWithFetcher(items[0], fetcher)
	case map[string]any:
		if activityJSONLDType(typed) == "Image" {
			rawURL, expanded := activityJSONLDValueWithExpandedIRI(typed, "url")
			if url := activityAttachmentURLWithExpandedID(activityJSONLDSingle(rawURL), expanded); url != "" {
				return url
			}
		}
		return activityAttachmentURL(typed)
	}
	return ""
}

func fetchActivityActorImageObject(uri string) any {
	return fetchActivityActorImageObjectWithFetcher(uri, fetchActivityResourceWithMetadata)
}

func (s *Server) fetchActivityActorImageObject(uri string) any {
	if s == nil {
		return fetchActivityActorImageObject(uri)
	}
	return fetchActivityActorImageObjectWithFetcher(uri, s.fetchActivityResourceWithRepresentative)
}

func fetchActivityActorImageObjectWithFetcher(uri string, fetcher func(string) (fetchedActivityResource, error)) any {
	uri = activityPubHTTPURI(uri)
	if uri == "" {
		return nil
	}
	resource, err := fetcher(uri)
	if err != nil || !activityJSONContentType(resource.contentType) {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(resource.body, &raw); err != nil {
		return nil
	}
	return raw
}

func activityActorSharedInboxURL(endpoints any, fallback any) string {
	object, ok := endpoints.(map[string]any)
	if !ok {
		return activityActorCollectionURI(fallback)
	}
	return activityActorCollectionURI(activityJSONLDValue(object, "sharedInbox"))
}

func activityActorSharedInboxURLFromObject(object map[string]any) string {
	endpoints, expanded := activityJSONLDValueWithExpandedIRI(object, "endpoints")
	fallback := activityJSONLDValue(object, "sharedInbox")
	if !expanded {
		return activityActorSharedInboxURL(endpoints, fallback)
	}
	sawEndpointObject := false
	for _, item := range activityJSONLDListItems(endpoints) {
		endpointObject, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sawEndpointObject = true
		if uri := activityActorCollectionURI(activityJSONLDValue(endpointObject, "sharedInbox")); uri != "" {
			return uri
		}
	}
	if sawEndpointObject {
		return ""
	}
	return activityActorCollectionURI(fallback)
}

func activityActorCollectionURI(value any) string {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return ""
		}
		return activityActorCollectionURI(typed[0])
	case map[string]any:
		if id := stringValue(typed["id"]); id != "" {
			return activityActorCollectionURI(id)
		}
		return activityActorCollectionURI(stringValue(typed["@id"]))
	case string:
		if !activityActorCollectionURIValid(typed) {
			return ""
		}
		return typed
	default:
		return ""
	}
}

func activityActorCollectionURIValid(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func activityActorPublishedAt(raw string, fallback time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	publishedAt, ok := parseActivityPubTime(raw)
	if !ok {
		return fallback
	}
	return publishedAt
}

func activityActorURISupported(raw string) bool {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) != raw {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

type activityActorCollectionInfo struct {
	TotalItems sql.NullInt64
	HasFirst   bool
}

func fetchActivityActorCollectionInfo(uri string) activityActorCollectionInfo {
	return fetchActivityActorCollectionInfoWithFetcher(uri, fetchActivityResourceWithMetadata)
}

func (s *Server) fetchActivityActorCollectionInfo(uri string) activityActorCollectionInfo {
	if s == nil {
		return fetchActivityActorCollectionInfo(uri)
	}
	return fetchActivityActorCollectionInfoWithFetcher(uri, s.fetchActivityResourceWithRepresentative)
}

func fetchActivityActorCollectionInfoWithFetcher(uri string, fetcher func(string) (fetchedActivityResource, error)) activityActorCollectionInfo {
	uri = activityActorCollectionURI(uri)
	if uri == "" {
		return activityActorCollectionInfo{}
	}
	resource, err := fetcher(uri)
	if err != nil {
		return activityActorCollectionInfo{}
	}
	if !activityJSONContentType(resource.contentType) {
		return activityActorCollectionInfo{}
	}
	var raw map[string]any
	if err := json.Unmarshal(resource.body, &raw); err != nil {
		return activityActorCollectionInfo{}
	}
	return activityActorCollectionInfo{
		TotalItems: activityActorCollectionTotalItems(activityJSONLDValue(raw, "totalItems")),
		HasFirst:   activityActorCollectionHasFirst(activityJSONLDValue(raw, "first")),
	}
}

func activityActorCollectionTotalItems(value any) sql.NullInt64 {
	switch typed := activityJSONLDSingle(value).(type) {
	case int:
		return sql.NullInt64{Int64: int64(typed), Valid: true}
	case int64:
		return sql.NullInt64{Int64: typed, Valid: true}
	case float64:
		if typed == float64(int64(typed)) {
			return sql.NullInt64{Int64: int64(typed), Valid: true}
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return sql.NullInt64{Int64: parsed, Valid: true}
		}
	case map[string]any:
		return activityActorCollectionTotalItems(typed["@value"])
	}
	return sql.NullInt64{}
}

func activityActorCollectionHasFirst(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case bool:
		return typed
	default:
		return value != nil
	}
}

func activityActorCollectionStatUpdates(outboxInfo, followingInfo, followersInfo activityActorCollectionInfo) map[string]any {
	updates := map[string]any{}
	if outboxInfo.TotalItems.Valid {
		updates["statuses_count"] = outboxInfo.TotalItems.Int64
	}
	if followingInfo.TotalItems.Valid {
		updates["following_count"] = followingInfo.TotalItems.Int64
	}
	if followersInfo.TotalItems.Valid {
		updates["followers_count"] = followersInfo.TotalItems.Int64
	}
	return updates
}

func applyActivityActorCollectionStats(stat *models.AccountStat, outboxInfo, followingInfo, followersInfo activityActorCollectionInfo) {
	if outboxInfo.TotalItems.Valid {
		stat.StatusesCount = outboxInfo.TotalItems.Int64
	}
	if followingInfo.TotalItems.Valid {
		stat.FollowingCount = followingInfo.TotalItems.Int64
	}
	if followersInfo.TotalItems.Valid {
		stat.FollowersCount = followersInfo.TotalItems.Int64
	}
}

func activityActorTypeSupported(typ string) bool {
	switch typ {
	case "Application", "Group", "Organization", "Person", "Service":
		return true
	default:
		return false
	}
}

func activityFetchHostAllowed(host string) bool {
	if host == "" {
		return false
	}
	if activityHiddenServiceHost(host) {
		return activityAllowAccessHiddenService
	}
	if activityHTTPProxyConfigured {
		return true
	}
	ips, err := activityLookupIP(host)
	if err != nil {
		return true
	}
	for _, ip := range ips {
		if !activityIPAllowed(ip) {
			return false
		}
	}
	return true
}

func activityHTTPCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return http.ErrUseLastResponse
	}
	if req != nil && activityHTTPPrivateNetworkAllowed(req.Context()) {
		if !activityRedirectSchemeAllowed(req, via) {
			return fmt.Errorf("remote host is not allowed")
		}
		return nil
	}
	if !activityRedirectAllowed(req, via) {
		return fmt.Errorf("remote host is not allowed")
	}
	return nil
}

func activityRedirectAllowed(req *http.Request, via []*http.Request) bool {
	return activityRedirectSchemeAllowed(req, via) && activityFetchHostAllowed(req.URL.Hostname())
}

func activityRedirectSchemeAllowed(req *http.Request, via []*http.Request) bool {
	if req == nil || req.URL == nil || req.URL.Host == "" {
		return false
	}
	if len(via) > 0 {
		prev := via[len(via)-1]
		if prev == nil || prev.URL == nil || !railsOpenURIRedirectable(prev.URL.Scheme, req.URL.Scheme) {
			return false
		}
	} else if !railsOpenURIRedirectable(req.URL.Scheme, req.URL.Scheme) {
		return false
	}
	return true
}

func railsOpenURIRedirectable(fromScheme string, toScheme string) bool {
	from := strings.ToLower(fromScheme)
	to := strings.ToLower(toScheme)
	if from == "" || to == "" {
		return false
	}
	if from == to {
		return true
	}
	return railsOpenURIRedirectScheme(from) && railsOpenURIRedirectScheme(to)
}

func railsOpenURIRedirectScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "ftp":
		return true
	default:
		return false
	}
}

func activityIPAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip = normalizeActivityIP(ip)
	for _, allowed := range activityPrivateAddressExceptions {
		if allowed.Contains(ip) {
			return true
		}
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	for _, prefix := range activityForbiddenAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// normalizeActivityIP handles both IPv4-mapped (::ffff:a.b.c.d) and the
// deprecated IPv4-compatible (::a.b.c.d) representation before private-address
// policy is evaluated. net.IP.To4 covers both forms while netip.Addr.Unmap only
// handles the mapped form.
func normalizeActivityIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if ipv4 := activityEmbeddedIPv4(ip); ipv4 != nil {
		return ipv4
	}
	return ip
}

func activityEmbeddedIPv4(ip net.IP) net.IP {
	address := ip.To16()
	if address == nil {
		return nil
	}
	compatible := true
	for _, value := range address[:12] {
		if value != 0 {
			compatible = false
			break
		}
	}
	mapped := true
	for _, value := range address[:10] {
		if value != 0 {
			mapped = false
			break
		}
	}
	mapped = mapped && address[10] == 0xff && address[11] == 0xff
	if !compatible && !mapped {
		return nil
	}
	return net.IPv4(address[12], address[13], address[14], address[15])
}

func parseActivityPrivateAddressExceptions(raw string) []*net.IPNet {
	var out []*net.IPNet
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ip, network, err := net.ParseCIDR(item); err == nil {
			if ipv4 := activityEmbeddedIPv4(ip); ipv4 != nil {
				ones, bits := network.Mask.Size()
				if bits == net.IPv6len*8 {
					ones -= (net.IPv6len - net.IPv4len) * 8
				}
				if ones < 0 || ones > net.IPv4len*8 {
					continue
				}
				network.IP = ipv4
				network.Mask = net.CIDRMask(ones, net.IPv4len*8)
			} else {
				network.IP = ip
			}
			out = append(out, network)
			continue
		}
		if ip := net.ParseIP(item); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

func activityHiddenServiceHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	return strings.HasSuffix(host, ".onion") || strings.HasSuffix(host, ".i2p")
}

type activityWebFinger struct {
	Subject string `json:"subject"`
	Links   []struct {
		Rel  string `json:"rel"`
		Type string `json:"type"`
		Href string `json:"href"`
	} `json:"links"`
}

func (s *Server) upsertRemoteActivityActor(actor remoteActivityActor) (*models.Account, error) {
	return s.upsertRemoteActivityActorForRequest(actor, "")
}

func (s *Server) upsertRemoteActivityActorForRequest(actor remoteActivityActor, requestID string) (*models.Account, error) {
	return s.upsertRemoteActivityActorDBForRequest(s.db, actor, requestID)
}

func (s *Server) upsertRemoteActivityActorDB(database *gorm.DB, actor remoteActivityActor) (*models.Account, error) {
	return s.upsertRemoteActivityActorDBForRequest(database, actor, "")
}

func (s *Server) upsertRemoteActivityActorDBForRequest(database *gorm.DB, actor remoteActivityActor, requestID string) (*models.Account, error) {
	parsed, err := url.Parse(actor.ID)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid remote actor")
	}
	acquired, releaseAccountLock, err := s.acquireActivityPubRedisLock(context.Background(), "process_account:"+actor.ID, activityPubRedisLockDefaultTTL)
	if err != nil || !acquired {
		return nil, err
	}
	defer releaseAccountLock()
	username := railsAccountUsernameValue(firstNonEmpty(actor.PreferredUsername, pathBase(parsed.Path)))
	if username == "" || !railsRemoteUsernamePattern.MatchString(username) {
		return nil, fmt.Errorf("invalid remote actor")
	}
	domain := normalizeDeliveryStatsHost(parsed.Hostname())
	if domain == "" {
		return nil, fmt.Errorf("invalid remote actor")
	}
	now := time.Now().UTC()
	actorURL := firstActivityActorURL(actor.URL, actor.ID)
	createdAt := activityActorPublishedAt(actor.Published, now)
	domainBlock, err := activityPubDomainBlockForDomain(database, sql.NullString{String: domain, Valid: true})
	if err != nil {
		return nil, err
	}
	account := models.Account{
		Username:          username,
		Domain:            sql.NullString{String: domain, Valid: true},
		CreatedAt:         createdAt,
		UpdatedAt:         now,
		URI:               actor.ID,
		URL:               sql.NullString{String: actorURL, Valid: actorURL != ""},
		InboxURL:          actor.Inbox,
		OutboxURL:         actor.Outbox,
		SharedInboxURL:    actor.SharedInbox(),
		FollowersURL:      actor.Followers,
		FollowingURL:      actor.Following,
		Protocol:          1,
		ActorType:         sql.NullString{String: firstNonEmpty(actor.Type, "Person"), Valid: true},
		LastWebfingeredAt: sql.NullTime{Time: now, Valid: true},
	}
	if domainBlock != nil {
		severity, ok := domainBlock.SeverityInt()
		switch {
		case ok && severity == domainBlockSeverityCode("silence"):
			account.SilencedAt = sql.NullTime{Time: domainBlock.CreatedAt, Valid: true}
		case ok && severity == domainBlockSeverityCode("suspend"):
			account.SuspendedAt = sql.NullTime{Time: domainBlock.CreatedAt, Valid: true}
			account.SuspensionOrigin = sql.NullInt64{Int64: 0, Valid: true}
		}
	}
	if actor.Suspended && !account.SuspendedAt.Valid {
		account.SuspendedAt = sql.NullTime{Time: now, Valid: true}
		account.SuspensionOrigin = sql.NullInt64{Int64: 1, Valid: true}
	}
	if !activityActorLocallySuspended(account) {
		account.PublicKey = actor.PublicKey.PublicKeyPem
	}
	newAccountSuspended := account.SuspendedAt.Valid
	var outboxInfo activityActorCollectionInfo
	var followingInfo activityActorCollectionInfo
	var followersInfo activityActorCollectionInfo
	if !newAccountSuspended {
		movedToID, movedToSet := s.remoteActorMovedToAccountID(actor.MovedTo, requestID)
		outboxInfo = s.fetchActivityActorCollectionInfo(actor.Outbox)
		followingInfo = s.fetchActivityActorCollectionInfo(actor.Following)
		followersInfo = s.fetchActivityActorCollectionInfo(actor.Followers)
		account.Note = actor.Summary
		account.DisplayName = actor.Name
		account.HideCollections = sql.NullBool{Bool: !followingInfo.HasFirst || !followersInfo.HasFirst, Valid: true}
		account.Locked = actor.ManuallyApprovesFollowers
		account.Memorial = actor.Memorial
		account.Discoverable = sql.NullBool{Bool: actor.Discoverable, Valid: true}
		account.Indexable = actor.Indexable
		account.FeaturedCollectionURL = sql.NullString{String: actor.Featured, Valid: actor.Featured != ""}
		account.AlsoKnownAs = models.StringArray(activityLimitedValueOrIDList(actor.AlsoKnownAs, 256))
		account.AttributionDomains = models.StringArray(activityLimitedStringList(actor.AttributionDomains, 256))
		if movedToSet {
			account.MovedToAccountID = movedToID
		}
		fields := activityProfileFields(actor.Attachment)
		if fields == nil {
			fields = []profileField{}
		}
		encoded, _ := json.Marshal(fields)
		account.Fields = encoded
		skipMedia, err := activityPubActorSkipsMedia(database, &account)
		if err != nil {
			return nil, err
		}
		if !skipMedia {
			account.AvatarRemoteURL = sql.NullString{String: actor.AvatarRemoteURL, Valid: true}
			account.HeaderRemoteURL = actor.HeaderRemoteURL
		}
	}
	var suspensionTransition activityActorSuspensionTransition
	protocolChanged := false
	keyChanged := false
	createdAccount := false
	var previousAccount *models.Account
	finalSuspended := newAccountSuspended
	previousAvatarURL := ""
	previousHeaderURL := ""
	hadAvatarFile := false
	hadHeaderFile := false
	var customEmojiChanges []models.CustomEmoji
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := setActivityPubTransactionLockTimeout(tx); err != nil {
			return err
		}
		var existing models.Account
		err := tx.Where("uri = ?", actor.ID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = tx.Where("lower(username) = lower(?) AND lower(domain) = lower(?)", username, domain).First(&existing).Error
		}
		if err == nil {
			previous := existing
			previousAccount = &previous
			previousAvatarURL = existing.AvatarRemoteURL.String
			hadAvatarFile = existing.AvatarFileName.Valid && existing.AvatarFileName.String != ""
			previousHeaderURL = existing.HeaderRemoteURL
			hadHeaderFile = existing.HeaderFileName.Valid && existing.HeaderFileName.String != ""
			protocolChanged = existing.Protocol != account.Protocol
			suspensionTransition = activityActorSuspensionTransitionFor(existing, actor.Suspended)
			suspendedAfterUpdate := activityActorSuspendedAfterRemoteUpdate(existing, actor.Suspended)
			finalSuspended = suspendedAfterUpdate
			updates := map[string]any{
				"updated_at":          now,
				"uri":                 account.URI,
				"url":                 account.URL,
				"inbox_url":           account.InboxURL,
				"outbox_url":          account.OutboxURL,
				"shared_inbox_url":    account.SharedInboxURL,
				"followers_url":       account.FollowersURL,
				"protocol":            account.Protocol,
				"actor_type":          account.ActorType,
				"last_webfingered_at": account.LastWebfingeredAt,
			}
			if !activityActorLocallySuspended(existing) {
				updates["public_key"] = account.PublicKey
			}
			if actor.Published != "" {
				updates["created_at"] = account.CreatedAt
			}
			for key, value := range activityActorSuspensionUpdatesForTransition(suspensionTransition, now) {
				updates[key] = value
			}
			var skipMedia bool
			if !suspendedAfterUpdate {
				movedToID, movedToSet := s.remoteActorMovedToAccountID(actor.MovedTo, requestID)
				outboxInfo = s.fetchActivityActorCollectionInfo(actor.Outbox)
				followingInfo = s.fetchActivityActorCollectionInfo(actor.Following)
				followersInfo = s.fetchActivityActorCollectionInfo(actor.Followers)
				updates["note"] = account.Note
				updates["display_name"] = account.DisplayName
				updates["hide_collections"] = sql.NullBool{Bool: !followingInfo.HasFirst || !followersInfo.HasFirst, Valid: true}
				updates["locked"] = account.Locked
				updates["memorial"] = account.Memorial
				updates["discoverable"] = account.Discoverable
				updates["indexable"] = account.Indexable
				updates["featured_collection_url"] = account.FeaturedCollectionURL
				if movedToSet {
					updates["moved_to_account_id"] = movedToID
				}
				updates["also_known_as"] = account.AlsoKnownAs
				updates["attribution_domains"] = account.AttributionDomains
				updates["fields"] = models.JSONValue(account.Fields)
				var skipErr error
				skipMedia, skipErr = activityPubActorSkipsMedia(tx, &existing)
				if skipErr != nil {
					return skipErr
				}
				if !skipMedia {
					updates["avatar_remote_url"] = account.AvatarRemoteURL
					updates["header_remote_url"] = account.HeaderRemoteURL
					if actor.AvatarRemoteURL == "" || s.cfg.DisableRemoteMediaCache {
						clearActivityPubActorAvatarMediaUpdates(updates)
					}
					if actor.HeaderRemoteURL == "" || s.cfg.DisableRemoteMediaCache {
						clearActivityPubActorHeaderMediaUpdates(updates)
					}
				}
			}
			if err := tx.Model(&existing).Updates(updates).Error; err != nil {
				return err
			}
			if !activityActorLocallySuspended(existing) {
				keyChanged = activityPubActorKeyChanged(existing.PublicKey, account.PublicKey)
				if err := clearActivityPubActorTombstonesOnKeyChange(tx, existing.ID, existing.PublicKey, account.PublicKey); err != nil {
					return err
				}
			}
			if !suspendedAfterUpdate && !skipMedia {
				changes, err := s.saveActivityPubActorCustomEmojis(tx, &account, actor.Tags, now)
				if err != nil {
					return err
				}
				customEmojiChanges = append(customEmojiChanges, changes...)
			}
			account.ID = existing.ID
			if suspendedAfterUpdate {
				return nil
			}
			statUpdates := activityActorCollectionStatUpdates(outboxInfo, followingInfo, followersInfo)
			if len(statUpdates) > 0 {
				stat := models.AccountStat{AccountID: existing.ID, CreatedAt: now, UpdatedAt: now}
				if err := createActivityPubAccountStatIfMissing(tx, stat); err != nil {
					return err
				}
				statUpdates["updated_at"] = now
				if err := tx.Model(&models.AccountStat{}).Where("account_id = ?", existing.ID).Updates(statUpdates).Error; err != nil {
					return err
				}
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if !s.remoteActorSubdomainAllowed(domain) {
			return nil
		}
		if !s.remoteActorDiscoveryAllowed(requestID) {
			return nil
		}
		if err := tx.Omit(clause.Associations).Create(&account).Error; err != nil {
			return err
		}
		createdAccount = true
		if !newAccountSuspended {
			skipMedia, err := activityPubActorSkipsMedia(tx, &account)
			if err != nil {
				return err
			}
			if !skipMedia {
				changes, err := s.saveActivityPubActorCustomEmojis(tx, &account, actor.Tags, now)
				if err != nil {
					return err
				}
				customEmojiChanges = append(customEmojiChanges, changes...)
			}
		}
		if actor.Suspended && !(account.SuspendedAt.Valid && account.SuspensionOrigin.Valid && account.SuspensionOrigin.Int64 == 0) {
			suspensionTransition = activityActorSuspendedRemotely
		}
		if newAccountSuspended {
			return nil
		}
		stat := models.AccountStat{AccountID: account.ID, CreatedAt: now, UpdatedAt: now}
		applyActivityActorCollectionStats(&stat, outboxInfo, followingInfo, followersInfo)
		return createActivityPubAccountStatIfMissing(tx, stat)
	})
	if err != nil {
		return nil, err
	}
	if account.ID != 0 {
		var persisted models.Account
		if err := database.WithContext(context.Background()).Where("id = ?", account.ID).First(&persisted).Error; err == nil {
			account = persisted
			if createdAccount {
				_ = s.enqueueFASPAccountLifecycle(context.Background(), account, "new")
			} else if previousAccount != nil {
				_ = s.enqueueFASPAccountLifecycleUpdate(context.Background(), *previousAccount, account)
			}
		}
	}
	s.invalidateCustomEmojiEntityCaches(context.Background(), customEmojiChanges)
	if createdAccount {
		if err := s.materializeDomainControl(context.Background(), domain); err != nil {
			return nil, err
		}
	}
	if err := s.applyActivityActorSuspensionTransitionEffects(context.Background(), database, account.ID, suspensionTransition); err != nil {
		return nil, err
	}
	if protocolChanged {
		if err := s.applyActivityPubPostUpgrade(context.Background(), database, domain); err != nil {
			return nil, err
		}
	}
	if keyChanged {
		if err := s.refollowLocalFollowersAfterActivityPubKeyChange(context.Background(), database, account); err != nil {
			return nil, err
		}
	}
	if !finalSuspended {
		s.enqueueVerifyAccountLinksIfNeeded(context.Background(), account, now)
		if skipMedia, skipErr := activityPubActorSkipsMedia(database, &account); skipErr == nil && !skipMedia {
			if actor.AvatarRemoteURL != "" && (actor.AvatarRemoteURL != previousAvatarURL || !hadAvatarFile) {
				if err := s.downloadAndStoreRemoteAccountImage(context.Background(), account.ID, "avatar", actor.AvatarRemoteURL); err != nil {
					s.enqueueRedownloadAvatarTask(account.ID)
				}
			}
			if actor.HeaderRemoteURL != "" && (actor.HeaderRemoteURL != previousHeaderURL || !hadHeaderFile) {
				if err := s.downloadAndStoreRemoteAccountImage(context.Background(), account.ID, "header", actor.HeaderRemoteURL); err != nil {
					s.enqueueRedownloadHeaderTask(account.ID)
				}
			}
		}
		s.syncActivityPubFeaturedCollectionBestEffort(&account, actor.Featured, requestID, actor.FeaturedTags == "")
		if actor.FeaturedCollection != nil {
			if actor.FeaturedTags == "" {
				_ = s.syncRemoteFeaturedTags(&account, activityPubFeaturedTagNamesFromTags(actor.FeaturedCollection.Tags))
			}
			_ = s.syncRemoteStatusPinsFromActivityCollection(&account, *actor.FeaturedCollection, requestID)
		}
		s.syncActivityPubFeaturedTagsBestEffort(&account, actor.FeaturedTags)
	}
	return &account, nil
}

func createActivityPubAccountStatIfMissing(tx *gorm.DB, stat models.AccountStat) error {
	if tx == nil || stat.AccountID == 0 {
		return nil
	}
	return tx.Exec(`
		INSERT INTO account_stats (
			account_id, statuses_count, following_count, followers_count,
			created_at, updated_at, last_status_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account_id) DO NOTHING
	`,
		stat.AccountID,
		stat.StatusesCount,
		stat.FollowingCount,
		stat.FollowersCount,
		stat.CreatedAt,
		stat.UpdatedAt,
		stat.LastStatusAt,
	).Error
}

func setActivityPubTransactionLockTimeout(tx *gorm.DB) error {
	if tx == nil {
		return nil
	}
	return tx.Exec(`SELECT set_config('lock_timeout', '5s', true)`).Error
}

func (s *Server) applyActivityPubPostUpgrade(ctx context.Context, database *gorm.DB, domain string) error {
	if s != nil && s.enqueuePostUpgradeTask(domain) {
		return nil
	}
	return s.applyActivityPubPostUpgradeNow(ctx, database, domain)
}

func (s *Server) applyActivityPubPostUpgradeNow(ctx context.Context, database *gorm.DB, domain string) error {
	domain = normalizeDomain(domain)
	if database == nil || domain == "" {
		return nil
	}
	return database.WithContext(ctx).
		Model(&models.Account{}).
		Where("lower(domain) = lower(?)", domain).
		Where("protocol = ?", 0).
		Where("last_webfingered_at IS NOT NULL").
		Update("last_webfingered_at", nil).Error
}

func clearActivityPubActorTombstonesOnKeyChange(tx *gorm.DB, accountID int64, oldPublicKey, nextPublicKey string) error {
	if !activityPubActorKeyChanged(oldPublicKey, nextPublicKey) {
		return nil
	}
	return tx.Where("account_id = ?", accountID).Delete(&models.Tombstone{}).Error
}

func activityPubActorKeyChanged(oldPublicKey, nextPublicKey string) bool {
	return oldPublicKey != "" && oldPublicKey != nextPublicKey
}

type activityActorSuspensionTransition int

const (
	activityActorSuspensionUnchanged activityActorSuspensionTransition = iota
	activityActorSuspendedRemotely
	activityActorUnsuspendedRemotely
)

func activityActorSuspensionUpdates(existing models.Account, suspended bool, now time.Time) map[string]any {
	return activityActorSuspensionUpdatesForTransition(activityActorSuspensionTransitionFor(existing, suspended), now)
}

func activityActorLocallySuspended(existing models.Account) bool {
	return existing.SuspendedAt.Valid && existing.SuspensionOrigin.Valid && existing.SuspensionOrigin.Int64 == 0
}

func activityActorSuspendedAfterRemoteUpdate(existing models.Account, suspended bool) bool {
	if activityActorLocallySuspended(existing) {
		return true
	}
	return suspended
}

func activityActorSuspensionTransitionFor(existing models.Account, suspended bool) activityActorSuspensionTransition {
	if activityActorLocallySuspended(existing) {
		return activityActorSuspensionUnchanged
	}
	if suspended {
		if existing.SuspendedAt.Valid {
			return activityActorSuspensionUnchanged
		}
		return activityActorSuspendedRemotely
	}
	if existing.SuspendedAt.Valid {
		return activityActorUnsuspendedRemotely
	}
	return activityActorSuspensionUnchanged
}

func activityActorSuspensionUpdatesForTransition(transition activityActorSuspensionTransition, now time.Time) map[string]any {
	switch transition {
	case activityActorSuspendedRemotely:
		return map[string]any{
			"suspended_at":      sql.NullTime{Time: now, Valid: true},
			"suspension_origin": sql.NullInt64{Int64: 1, Valid: true},
		}
	case activityActorUnsuspendedRemotely:
		return map[string]any{
			"suspended_at":      nil,
			"suspension_origin": nil,
		}
	default:
		return nil
	}
}

func (s *Server) applyActivityActorSuspensionTransitionEffects(ctx context.Context, database *gorm.DB, accountID int64, transition activityActorSuspensionTransition) error {
	switch transition {
	case activityActorSuspendedRemotely:
		return s.enqueueAdminSuspensionOrRun(ctx, database, accountID)
	case activityActorUnsuspendedRemotely:
		return s.enqueueAdminUnsuspensionOrRun(database, accountID)
	default:
		return nil
	}
}

func (s *Server) remoteActorMovedToAccountID(value any, requestID string) (sql.NullInt64, bool) {
	movedToURI := activityJSONLDValueOrID(value)
	if strings.TrimSpace(movedToURI) == "" {
		return sql.NullInt64{}, true
	}
	account, err := s.activityActorForMovedToURI(movedToURI, requestID)
	if err != nil || account == nil || account.ID == 0 {
		return sql.NullInt64{}, true
	}
	return sql.NullInt64{Int64: account.ID, Valid: true}, true
}

func (s *Server) activityActorForMovedToURI(actorURI string, requestID string) (*models.Account, error) {
	if s == nil || s.db == nil || strings.TrimSpace(actorURI) == "" {
		return nil, nil
	}
	actorLookupURI := actorURI
	if before, _, ok := strings.Cut(actorLookupURI, "#"); ok {
		actorLookupURI = before
	}
	var account models.Account
	err := findActivityPubAccountByURIOrURL(s.db, actorURI, actorLookupURI, &account)
	if err == nil {
		return &account, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	actor, err := s.fetchActivityActor(actorLookupURI)
	if err != nil || actor.ID == "" || actor.PublicKey.PublicKeyPem == "" || activityPubObjectID(actor.MovedTo) != "" {
		return nil, nil
	}
	return s.upsertRemoteActivityActorForRequest(actor, requestID)
}

func activityPublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid public key")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("invalid public key")
}

func verifyActivitySignedString(req *http.Request, params map[string]string, headers []string, key *rsa.PublicKey, signature []byte, includeQuery bool) error {
	signedString := buildActivitySignedString(req, params, headers, includeQuery)
	sum := sha256.Sum256([]byte(signedString))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], signature)
}

func buildActivitySignedString(req *http.Request, params map[string]string, headers []string, includeQuery bool) string {
	lines := make([]string, 0, len(headers))
	for _, header := range headers {
		switch header {
		case "(request-target)":
			target := req.URL.EscapedPath()
			if target == "" {
				target = "/"
			}
			if includeQuery && req.URL.RawQuery != "" {
				target += "?" + req.URL.RawQuery
			}
			lines = append(lines, "(request-target): "+strings.ToLower(req.Method)+" "+target)
		case "(created)":
			lines = append(lines, "(created): "+params["created"])
		case "(expires)":
			lines = append(lines, "(expires): "+params["expires"])
		case "host":
			lines = append(lines, "host: "+req.Host)
		default:
			lines = append(lines, header+": "+req.Header.Get(canonicalActivityHeader(header)))
		}
	}
	return strings.Join(lines, "\n")
}

func canonicalActivityHeader(header string) string {
	parts := strings.Split(header, "-")
	for i, part := range parts {
		if part == "" {
			continue
		}
		part = strings.ToLower(part)
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "-")
}

func activityPubObjectID(value any) string {
	switch object := value.(type) {
	case string:
		return activityURIFromBearcap(object)
	case []any:
		for _, item := range object {
			if id := activityPubObjectID(item); id != "" {
				return id
			}
		}
		return ""
	case map[string]any:
		if raw, ok := object["@list"]; ok {
			return activityPubObjectID(raw)
		}
		if value, ok := object["id"]; ok {
			return activityPubObjectID(value)
		}
		if id := activityPubObjectID(object["@id"]); id != "" {
			return id
		}
		return activityPubObjectID(object["@value"])
	default:
		return ""
	}
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func activityDigestHeader(body []byte) string {
	sum := sha256.Sum256(body)
	return "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
}

func activityRequestTarget(method string, rawurl string, includeQuery bool) string {
	parsed, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	target := parsed.EscapedPath()
	if target == "" {
		target = "/"
	}
	if includeQuery && parsed.RawQuery != "" {
		target += "?" + parsed.RawQuery
	}
	return "(request-target): " + strings.ToLower(method) + " " + target
}

type remoteActivityActor struct {
	ID                        string `json:"id"`
	Type                      string `json:"type"`
	PreferredUsername         string `json:"preferredUsername"`
	Name                      string `json:"name"`
	Summary                   string `json:"summary"`
	Published                 string `json:"published"`
	URL                       any    `json:"url"`
	Inbox                     string `json:"inbox"`
	Outbox                    string `json:"outbox"`
	Following                 string `json:"following"`
	Followers                 string `json:"followers"`
	Featured                  string `json:"featured"`
	FeaturedCollection        *activityCollection
	FeaturedTags              string `json:"featuredTags"`
	SharedInboxURL            string `json:"sharedInbox"`
	ManuallyApprovesFollowers bool   `json:"manuallyApprovesFollowers"`
	Discoverable              bool   `json:"discoverable"`
	Indexable                 bool   `json:"indexable"`
	Memorial                  bool   `json:"memorial"`
	Suspended                 bool   `json:"suspended"`
	MovedTo                   any    `json:"movedTo"`
	AlsoKnownAs               any    `json:"alsoKnownAs"`
	AttributionDomains        any    `json:"attributionDomains"`
	Attachment                any    `json:"attachment"`
	AvatarRemoteURL           string
	HeaderRemoteURL           string
	Tags                      []activityTag
	PublicKey                 struct {
		ID           string `json:"id"`
		IDRaw        string `json:"-"`
		Owner        string `json:"owner"`
		OwnerRaw     string `json:"-"`
		PublicKeyPem string `json:"publicKeyPem"`
	} `json:"publicKey"`
	Endpoints map[string]any `json:"endpoints"`
}

func (a remoteActivityActor) SharedInbox() string {
	if a.Endpoints != nil {
		if value := activityActorSharedInboxURL(a.Endpoints, nil); value != "" {
			return value
		}
	}
	return a.SharedInboxURL
}

func firstActivityActorURL(value any, fallback string) string {
	candidate := firstActivityActorURLCandidate(value)
	if !activityActorURLAllowed(candidate, fallback) {
		return fallback
	}
	return candidate
}

func firstActivityActorURLCandidate(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		return activityStatusURLHref(typed, "text/html")
	case map[string]any:
		if _, ok := typed["@list"]; ok {
			return firstActivityActorURLCandidate(activityJSONLDListItems(typed))
		}
		if href := activityJSONLDString(typed, "href"); href != "" {
			return href
		}
		if urlValue := activityStatusURLHref(activityJSONLDValue(typed, "url"), "text/html"); urlValue != "" {
			return urlValue
		}
	}
	return ""
}

func activityActorURLAllowed(candidate string, actorID string) bool {
	if strings.TrimSpace(candidate) == "" {
		return false
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	actor, err := url.Parse(actorID)
	if err != nil || actor.Host == "" {
		return true
	}
	return strings.EqualFold(normalizeDeliveryStatsHost(parsed.Hostname()), normalizeDeliveryStatsHost(actor.Hostname()))
}

func pathBase(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}
