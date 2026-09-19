package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/i18n"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Server struct {
	echo              *echo.Echo
	cfg               config.Config
	browserSessionKey [32]byte
	db                *gorm.DB
	pgHeroStatsDB     *gorm.DB
	pgHeroOtherDB     *gorm.DB
	renderer          *web.Renderer
	webPushDeliverer  webPushDeliverFunc
	quoteStore        statusQuoteStore
	s3Storage         *s3SDKStorage
	asynqClient       *asynq.Client
	asynqInspector    asynqInspectorClient
	asynqTaskRetryer  asynqTaskRetryer
	streamMetrics     streamingMetricState
	ipBlockMu         sync.Mutex
	noAccessIPBlocks  []models.IPBlock
	noAccessIPCached  time.Time
	postCommitMu      sync.Mutex
	postCommitWG      sync.WaitGroup
	postCommitClosed  bool
}

type oauthApplication struct {
	ID           int64                 `gorm:"primaryKey;column:id"`
	Name         string                `gorm:"column:name"`
	UID          string                `gorm:"column:uid"`
	Secret       string                `gorm:"column:secret"`
	RedirectURI  string                `gorm:"column:redirect_uri"`
	Scopes       string                `gorm:"column:scopes"`
	CreatedAt    time.Time             `gorm:"column:created_at"`
	UpdatedAt    time.Time             `gorm:"column:updated_at"`
	Website      models.NullSafeString `gorm:"column:website"`
	Confidential bool                  `gorm:"column:confidential"`
	Superapp     bool                  `gorm:"column:superapp"`
	OwnerType    sql.NullString        `gorm:"column:owner_type"`
	OwnerID      sql.NullInt64         `gorm:"column:owner_id"`
}

func (oauthApplication) TableName() string { return "oauth_applications" }

var errApplicationTokenRequiresUser = errors.New("application token requires authenticated user")

var (
	railsAPIDeleteStatusPattern = regexp.MustCompile(`^/api/v1/statuses/\d+$`)
	railsAPIUnreblogPattern     = regexp.MustCompile(`^/api/v1/statuses/\d+/unreblog$`)
	railsAPIMediaPattern        = regexp.MustCompile(`^/api/v\d+/media$`)
	statusLengthURLPattern      = regexp.MustCompile(`(^|[\s(])(?:(?:https?|dat|dweb|ipfs|ipns|ssb|gopher|gemini)://[^\s<]+|xmpp:[^\s<]+|magnet:\?[^\s<]+)`)
	statusLengthRemoteMention   = regexp.MustCompile(`(^|[^A-Za-z0-9_])@([A-Za-z0-9_]+)@([A-Za-z0-9.-]+)`)
)

type statusUpdatePayload struct {
	Status          string
	HasStatus       bool
	MediaIDs        []string
	HasMediaIDs     bool
	MediaAttributes []mediaAttributePayload
	Sensitive       bool
	HasSensitive    bool
	SpoilerText     string
	HasSpoilerText  bool
	Language        string
	HasLanguage     bool
	Poll            *pollUpdatePayload
	HasPoll         bool
}

type statusCreatePayload struct {
	statusUpdatePayload
	Visibility         string
	InReplyToID        string
	ScheduledAt        string
	QuoteID            string
	AllowedMentions    []string
	HasAllowedMentions bool
	ApplicationID      sql.NullInt64
}

type reblogPayload struct {
	Visibility string
}

type mediaAttributePayload struct {
	ID          string
	Description *string
	Focus       *string
}

type pollUpdatePayload struct {
	Options    []string `json:"options"`
	Multiple   bool     `json:"multiple"`
	HideTotals bool     `json:"hide_totals"`
	ExpiresIn  int      `json:"expires_in"`
}

const (
	pollMinExpirationSeconds = 5 * 60
	pollMaxExpirationSeconds = 2_629_746
)

type unexpectedMentionsError struct {
	accounts []models.Account
}

func (e unexpectedMentionsError) Error() string {
	return "Post would be sent to unexpected accounts"
}

type treeIDRow struct {
	ID int64 `gorm:"column:id"`
}

const (
	statusContextLimit               = 4096
	anonymousAncestorsLimit          = 40
	anonymousDescendantsLimit        = 60
	anonymousDescendantsDepthLimit   = 20
	railsUnauthenticatedAPILimit     = 300
	railsUnauthenticatedAPIPeriod    = 5 * time.Minute
	railsAuthenticatedAPILimit       = 300_000
	railsAuthenticatedAPIPeriod      = 5 * time.Minute
	railsPerTokenAPILimit            = 30_000
	railsPerTokenAPIPeriod           = 5 * time.Minute
	railsAPIMediaLimit               = 30_000
	railsAPIMediaPeriod              = 30 * time.Minute
	railsAPISignupLimit              = 5
	railsAPISignupPeriod             = 30 * time.Minute
	railsAPIApplicationLimit         = 5
	railsAPIApplicationPeriod        = 10 * time.Minute
	railsAuthenticatedPagingLimit    = 300_000
	railsAuthenticatedPagingPeriod   = 15 * time.Minute
	railsUnauthenticatedPagingLimit  = 300
	railsUnauthenticatedPagingPeriod = 15 * time.Minute
	railsAPIDeleteLimit              = 30_000
	railsAPIDeletePeriod             = 30 * time.Minute
	railsMediaProxyLimit             = 30_000
	railsMediaProxyPeriod            = 10 * time.Minute
	railsAuthAttemptIPLimit          = 25
	railsAuthAttemptIPPeriod         = 5 * time.Minute
	railsAuthEmailLimit              = 5
	railsPasswordResetEmailPeriod    = 30 * time.Minute
	railsAuthSetupEmailPeriod        = 10 * time.Minute
	railsLoginAttemptEmailLimit      = 25
	railsLoginAttemptEmailPeriod     = time.Hour
	railsPasswordChangeAccountLimit  = 10
	railsPasswordChangeAccountPeriod = 10 * time.Minute
	railsFollowsFamilyLimit          = 4_000
	railsFollowsFamilyPeriod         = 24 * time.Hour
	railsReportsFamilyLimit          = 400
	railsReportsFamilyPeriod         = 24 * time.Hour
	railsStatusFamilyLimit           = 3_000
	railsStatusFamilyPeriod          = 3 * time.Hour
	railsStatusReplyNotFoundMessage  = "The post you are trying to reply to does not appear to exist."
	tagTimelineLimitPerMode          = 4
)

func NewServer(cfg config.Config, database *gorm.DB) (*Server, error) {
	configureActivityHTTPClient(cfg)
	configureS3HTTPClient(cfg)
	s3Storage, err := newS3SDKStorage(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	renderer, err := web.NewRenderer(cfg)
	if err != nil {
		return nil, err
	}

	e := echo.New()
	e.IPExtractor = railsTrustedProxyIPExtractor(cfg)
	e.HTTPErrorHandler = handleAPIError
	e.Pre(headMethodMiddleware)
	e.Pre(apiTrailingSlashMiddleware)
	e.Pre(railsFormContentTypeMiddleware)
	e.Pre(methodOverrideMiddleware)
	e.Use(requestIDMiddleware)
	e.Use(accessLogMiddleware(cfg))
	e.Use(corsMiddleware)
	e.Use(hostAuthorizationMiddleware(cfg))
	e.Use(forceSSLMiddleware(cfg))
	e.Use(apiRateLimitHeadersMiddleware)
	e.Use(securityHeadersMiddleware)
	e.Use(contentSecurityPolicyMiddleware(cfg))
	e.Use(staticFileServerHeadersMiddleware(cfg))
	e.Use(statsDMiddleware(cfg))
	e.Use(privateNoStoreWebMiddleware(cfg))
	e.Use(encodedAtMiddleware)

	quoteStore, err := newStatusQuoteStore(cfg, nil)
	if err != nil {
		return nil, err
	}
	pgHeroStatsDB, err := paondb.OpenPgHeroStats(cfg)
	if err != nil {
		return nil, err
	}
	pgHeroOtherDB, err := paondb.OpenPgHeroOther(cfg)
	if err != nil {
		return nil, err
	}
	browserSessionKey, err := deriveBrowserSessionKey(cfg)
	if err != nil {
		return nil, err
	}
	asynqTaskRetryer, err := newRedisAsynqTaskRetryer(cfg)
	if err != nil {
		return nil, err
	}
	server := &Server{echo: e, cfg: cfg, browserSessionKey: browserSessionKey, db: database, pgHeroStatsDB: pgHeroStatsDB, pgHeroOtherDB: pgHeroOtherDB, renderer: renderer, quoteStore: quoteStore, s3Storage: s3Storage, asynqClient: asynq.NewClient(asynqRedisOpt(cfg)), asynqInspector: asynq.NewInspector(asynqRedisOpt(cfg)), asynqTaskRetryer: asynqTaskRetryer}
	e.HTTPErrorHandler = server.handleHTTPError
	setAppAssets(renderer)
	i18nStore := i18n.NewStore(localesDirFor(cfg.PublicDir))
	i18nStore.Preload("en", i18n.NormalizeLocale(cfg.Locale()))
	setWebI18n(i18nStore)
	setWebDefaultLocale(i18n.NormalizeLocale(cfg.Locale()))
	e.Use(server.ipBlockBlocklistMiddleware)
	e.Use(server.rackAttackThrottleMiddleware)
	e.Use(server.apiAuthenticationGateMiddleware)
	e.Use(server.browserSecurityMiddleware)
	server.routes()
	return server, nil
}

func (s *Server) Start(addr string) error {
	return s.echo.Start(addr)
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closePostCommitWorkers(30 * time.Second)
	var closeErrors []error
	if s.asynqClient != nil {
		closeErrors = append(closeErrors, s.asynqClient.Close())
	}
	if s.asynqInspector != nil {
		closeErrors = append(closeErrors, s.asynqInspector.Close())
	}
	if s.asynqTaskRetryer != nil {
		closeErrors = append(closeErrors, s.asynqTaskRetryer.Close())
	}
	return errors.Join(closeErrors...)
}

func (s *Server) StartContext(ctx context.Context, addr string) error {
	listenNetwork := strings.TrimSpace(s.cfg.ListenNetwork)
	if listenNetwork == "" {
		listenNetwork = "tcp"
	}
	if strings.HasPrefix(listenNetwork, "unix") {
		_ = os.Remove(addr)
		defer func() { _ = os.Remove(addr) }()
	}
	startConfig := echo.StartConfig{
		Address:         addr,
		ListenerNetwork: listenNetwork,
		GracefulTimeout: 10 * time.Second,
		BeforeServeFunc: func(server *http.Server) error {
			server.IdleTimeout = s.cfg.PersistentTimeout
			return nil
		},
	}
	if s.cfg.ProxyProtocolV1 {
		ln, err := (&net.ListenConfig{}).Listen(ctx, listenNetwork, addr)
		if err != nil {
			return err
		}
		startConfig.Listener = proxyProtocolV1Listener{Listener: ln}
	}
	return startConfig.Start(ctx, s.echo)
}

func railsTrustedProxyIPExtractor(cfg config.Config) echo.IPExtractor {
	ranges := parseTrustedProxyIPRanges(cfg.TrustedProxyIP)
	useRailsDefaultTrustedProxies := strings.TrimSpace(cfg.TrustedProxyIP) == ""
	return func(req *http.Request) string {
		direct := requestRemoteIP(req)
		if direct == "" {
			return ""
		}
		if xff := req.Header.Values(echo.HeaderXForwardedFor); len(xff) > 0 {
			ips := append(strings.Split(strings.Join(xff, ","), ","), direct)
			for i := len(ips) - 1; i >= 0; i-- {
				raw := strings.Trim(strings.TrimSpace(ips[i]), "[]")
				ip := net.ParseIP(raw)
				if ip == nil {
					return direct
				}
				if !railsTrustedProxyIP(ip, ranges, useRailsDefaultTrustedProxies) {
					return ip.String()
				}
			}
			return strings.Trim(strings.TrimSpace(ips[0]), "[]")
		}
		if realIP := strings.Trim(strings.TrimSpace(req.Header.Get(echo.HeaderXRealIP)), "[]"); realIP != "" {
			if ip := net.ParseIP(realIP); ip != nil {
				if directIP := net.ParseIP(direct); directIP != nil && railsTrustedProxyIP(directIP, ranges, useRailsDefaultTrustedProxies) {
					return ip.String()
				}
			}
		}
		return direct
	}
}

func requestRemoteIP(req *http.Request) string {
	if req == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err == nil {
		return host
	}
	if net.ParseIP(req.RemoteAddr) != nil {
		return req.RemoteAddr
	}
	return ""
}

func railsTrustedProxyIP(ip net.IP, ranges []*net.IPNet, useDefault bool) bool {
	if ip == nil {
		return false
	}
	for _, trusted := range ranges {
		if trusted.Contains(ip) {
			return true
		}
	}
	if !useDefault {
		return false
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
}

func parseTrustedProxyIPRanges(raw string) []*net.IPNet {
	var out []*net.IPNet
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ip, network, err := net.ParseCIDR(item); err == nil {
			network.IP = ip
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

func privateNoStoreWebMiddleware(cfg config.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			path := c.Request().URL.Path
			if railsPrivateNoStoreControllerPath(cfg, path) {
				setPrivateNoStoreCacheHeaders(c)
				if path == "/api" || strings.HasPrefix(path, "/api/") {
					appendVaryHeader(c, "Authorization")
				}
			}
			return next(c)
		}
	}
}

func railsPrivateNoStoreWebPath(path string) bool {
	return railsPrivateNoStoreControllerPath(config.Config{}, path)
}

func railsPrivateNoStoreControllerPath(cfg config.Config, path string) bool {
	return strings.TrimSpace(path) != "" && !railsPublicStaticPath(cfg, path)
}

func (s *Server) registerWebAppRoutes(paths ...string) {
	for _, path := range paths {
		s.echo.GET(path, s.webApp)
		s.echo.HEAD(path, s.webApp)
		if webAppRouteAcceptsOptionalFormat(path) {
			s.echo.GET(path+".:format", s.webApp)
			s.echo.HEAD(path+".:format", s.webApp)
		}
	}
}

func webAppRouteAcceptsOptionalFormat(path string) bool {
	return path != "/" && !strings.Contains(path, "*")
}

type headResponseWriter struct {
	http.ResponseWriter
}

func (w headResponseWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

func (w headResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func headMethodMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		if req.Method != http.MethodHead {
			return next(c)
		}
		req.Method = http.MethodGet
		c.SetResponse(headResponseWriter{ResponseWriter: c.Response()})
		return next(c)
	}
}

func encodedAtMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		requestURI := c.Request().URL.RequestURI()
		if strings.HasPrefix(strings.ToLower(requestURI), "/%40") {
			return c.Redirect(http.StatusMovedPermanently, "/@"+strings.TrimPrefix(requestURI, "/%40"))
		}
		return next(c)
	}
}

func requestIDMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		requestID := sanitizedRequestID(c.Request().Header.Get("X-Request-Id"))
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Set("request_id", requestID)
		setHeaderIfAbsent(c.Response().Header(), "X-Request-Id", requestID)
		return next(c)
	}
}

func hostAuthorizationMiddleware(cfg config.Config) echo.MiddlewareFunc {
	allowedHosts := railsAllowedHosts(cfg)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if len(allowedHosts) == 0 || railsHostAuthorizationExcluded(req) {
				return next(c)
			}
			forwardedHost := ""
			if values := strings.Split(req.Header.Get("X-Forwarded-Host"), ","); len(values) > 0 {
				forwardedHost = strings.TrimSpace(values[len(values)-1])
			}
			if railsHostAllowed(req.Host, allowedHosts) && (forwardedHost == "" || railsHostAllowed(forwardedHost, allowedHosts)) {
				return next(c)
			}
			return c.NoContent(http.StatusForbidden)
		}
	}
}

func railsHostAuthorizationExcluded(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	return req.URL.Path == "/health" || req.URL.Path == "/health/ready"
}

func railsAllowedHosts(cfg config.Config) map[string]struct{} {
	allowed := make(map[string]struct{})
	if cfg.RailsEnv == "test" {
		return allowed
	}
	add := func(host string) {
		if host != "" {
			allowed[strings.ToLower(host)] = struct{}{}
		}
	}
	if cfg.RailsEnv == "development" {
		add(".localhost")
		add(".test")
		allowed[railsDevelopmentAnyIPHost] = struct{}{}
		for _, host := range cfg.RailsDevelopmentHosts {
			add(host)
		}
	}
	add(cfg.LocalDomain)
	add(cfg.WebDomain)
	for _, host := range cfg.AlternateDomains {
		add(host)
	}
	return allowed
}

const railsDevelopmentAnyIPHost = "\x00rails-development-any-ip"

func railsHostAllowed(host string, allowed map[string]struct{}) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return false
	}
	hostname := railsRequestHostname(host)
	if _, allowIP := allowed[railsDevelopmentAnyIPHost]; allowIP && net.ParseIP(hostname) != nil {
		return true
	}
	for configured := range allowed {
		if configured == railsDevelopmentAnyIPHost || configured == "" {
			continue
		}
		if strings.HasPrefix(configured, ".") {
			suffix := strings.TrimPrefix(configured, ".")
			if hostname == suffix || strings.HasSuffix(hostname, "."+suffix) {
				return true
			}
			continue
		}
		if configuredHost, configuredPort, err := net.SplitHostPort(configured); err == nil && configuredHost != "" && configuredPort != "" {
			if host == configured {
				return true
			}
			continue
		}
		if configuredIP := net.ParseIP(strings.Trim(configured, "[]")); configuredIP != nil {
			if requestIP := net.ParseIP(hostname); requestIP != nil && configuredIP.Equal(requestIP) {
				return true
			}
			continue
		}
		if hostname == configured {
			return true
		}
	}
	return false
}

func railsRequestHostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return strings.Trim(strings.ToLower(h), "[]")
	}
	return strings.Trim(strings.ToLower(host), "[]")
}

const noAccessIPBlockCacheTTL = 30 * time.Second

func (s *Server) ipBlockBlocklistMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if s == nil || s.db == nil {
			return next(c)
		}
		blocked, err := s.noAccessIPBlocked(c.Request().Context(), c.RealIP(), time.Now().UTC())
		if err != nil {
			return err
		}
		if blocked {
			rackAttackLogHit("blacklist", c)
			return c.NoContent(http.StatusForbidden)
		}
		return next(c)
	}
}

func (s *Server) noAccessIPBlocked(ctx context.Context, remoteIP string, now time.Time) (bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(remoteIP) == "" {
		return false, nil
	}
	blocks, err := s.cachedNoAccessIPBlocks(ctx, now)
	if err != nil {
		return false, err
	}
	return noAccessIPBlockedForBlocks(remoteIP, blocks), nil
}

func (s *Server) cachedNoAccessIPBlocks(ctx context.Context, now time.Time) ([]models.IPBlock, error) {
	s.ipBlockMu.Lock()
	defer s.ipBlockMu.Unlock()
	if !s.noAccessIPCached.IsZero() && now.Sub(s.noAccessIPCached) < noAccessIPBlockCacheTTL {
		return append([]models.IPBlock(nil), s.noAccessIPBlocks...), nil
	}
	var blocks []models.IPBlock
	if err := s.db.WithContext(ctx).
		Where("severity = ?", 9999).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Find(&blocks).Error; err != nil {
		return nil, err
	}
	s.noAccessIPBlocks = append([]models.IPBlock(nil), blocks...)
	s.noAccessIPCached = now
	return append([]models.IPBlock(nil), blocks...), nil
}

func noAccessIPBlockedForBlocks(remoteIP string, blocks []models.IPBlock) bool {
	for _, block := range blocks {
		if block.Severity == 9999 && ipMatchesBlock(remoteIP, block.IP) {
			return true
		}
	}
	return false
}

func forceSSLMiddleware(cfg config.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if !cfg.ForceSSL || railsForceSSLExcluded(req) {
				return next(c)
			}
			if !requestIsHTTPS(req) {
				target := url.URL{
					Scheme:   "https",
					Host:     req.Host,
					Path:     req.URL.EscapedPath(),
					RawQuery: req.URL.RawQuery,
				}
				return c.Redirect(http.StatusMovedPermanently, target.String())
			}
			setHeaderIfAbsent(c.Response().Header(), "Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			return next(c)
		}
	}
}

func railsForceSSLExcluded(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if strings.HasPrefix(req.URL.Path, "/health") {
		return true
	}
	host := strings.ToLower(req.Host)
	if idx := strings.LastIndex(host, "@"); idx >= 0 {
		host = host[idx+1:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.HasSuffix(host, ".onion") || strings.HasSuffix(host, ".i2p")
}

func requestIsHTTPS(req *http.Request) bool {
	return req != nil && (req.TLS != nil || strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https"))
}

func securityHeadersMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		header := c.Response().Header()
		setHeaderIfAbsent(header, "Server", "Mastodon")
		setHeaderIfAbsent(header, "X-Frame-Options", "DENY")
		setHeaderIfAbsent(header, "X-Content-Type-Options", "nosniff")
		setHeaderIfAbsent(header, "X-XSS-Protection", "0")
		setHeaderIfAbsent(header, "Referrer-Policy", "same-origin")
		return next(c)
	}
}

func contentSecurityPolicyMiddleware(cfg config.Config) echo.MiddlewareFunc {
	policy := railsContentSecurityPolicy(cfg)
	apiPolicy := railsAPIContentSecurityPolicy()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if railsAPIContentSecurityPolicyPath(c.Request().URL.Path) {
				setHeaderIfAbsent(c.Response().Header(), "Content-Security-Policy", apiPolicy)
			} else if policy != "" {
				setHeaderIfAbsent(c.Response().Header(), "Content-Security-Policy", policy)
			}
			return next(c)
		}
	}
}

func railsAPIContentSecurityPolicyPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

func railsAPIContentSecurityPolicy() string {
	return "default-src 'none'; frame-ancestors 'none'; form-action 'none'"
}

func railsContentSecurityPolicy(cfg config.Config) string {
	assetsHost := firstNonEmptyString(cfg.CDNHost, cfg.BaseURL())
	mediaHost := firstNonEmptyString(strings.TrimRight(cfg.CSPMediaHost, "/"), strings.TrimRight(cfg.StorageHost, "/"), assetsHost)
	streamingHost := cfg.StreamingBaseURL()
	connectHosts := []string{assetsHost, mediaHost, streamingHost}
	connectHosts = append(connectHosts, railsShakapackerDevServerURLs(cfg)...)
	formAction := "form-action 'self'"
	if ssoFormActionURL := strings.TrimSpace(cfg.SSOFormActionURL); ssoFormActionURL != "" {
		formAction += " " + ssoFormActionURL
	}
	scriptSrc := "script-src 'self' 'unsafe-inline' 'unsafe-eval' " + assetsHost
	if !railsDevelopmentEnv(cfg) {
		scriptSrc += " 'wasm-unsafe-eval'"
	}
	directives := []string{
		"base-uri 'none'",
		"default-src 'none'",
		"frame-ancestors 'none'",
		"font-src 'self' " + assetsHost,
		"img-src 'self' https: data: blob: " + assetsHost,
		"style-src 'self' 'unsafe-inline' " + assetsHost,
		"media-src 'self' https: data: " + assetsHost,
		"frame-src 'self' https:",
		"manifest-src 'self' " + assetsHost,
		formAction,
		"child-src 'self' blob: " + assetsHost,
		"worker-src 'self' blob: " + assetsHost,
		"connect-src 'self' data: blob: " + strings.Join(uniqueNonEmptyStrings(connectHosts), " "),
		scriptSrc,
	}
	return strings.Join(directives, "; ")
}

func railsDevelopmentEnv(cfg config.Config) bool {
	if cfg.RailsEnv != "" {
		return cfg.RailsEnv == "development"
	}
	return railsEnvNameFromProcess() == "development"
}

func railsShakapackerDevServerURLs(cfg config.Config) []string {
	if !railsDevelopmentEnv(cfg) {
		return nil
	}
	host := strings.TrimSpace(cfg.ShakapackerDevServerPublic)
	suffix := "://" + host
	if cfg.ShakapackerDevServerHTTPS {
		return []string{"wss" + suffix, "https" + suffix}
	}
	return []string{"ws" + suffix, "http" + suffix}
}

func railsContentSecurityPolicyWithoutDirective(cfg config.Config, directive string) string {
	directive = strings.TrimSpace(directive)
	if directive == "" {
		return railsContentSecurityPolicy(cfg)
	}
	parts := strings.Split(railsContentSecurityPolicy(cfg), ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, directive+" ") {
			continue
		}
		out = append(out, part)
	}
	return strings.Join(out, "; ")
}

func railsContentSecurityPolicyDirective(cfg config.Config, directive string) string {
	directive = strings.TrimSpace(directive)
	if directive == "" {
		return ""
	}
	for _, part := range strings.Split(railsContentSecurityPolicy(cfg), ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, directive+" ") {
			return part
		}
	}
	return ""
}

func staticFileServerHeadersMiddleware(cfg config.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			path := c.Request().URL.Path
			if !railsPublicStaticPath(cfg, path) {
				return next(c)
			}
			header := c.Response().Header()
			header.Set("X-Content-Type-Options", "nosniff")
			switch {
			case strings.HasPrefix(path, "/sw.js"):
				header.Set("Cache-Control", "public, max-age=604800, must-revalidate")
			case railsPaperclipStaticPath(cfg, path):
				header.Set("Cache-Control", "public, max-age=2419200, immutable")
				header.Set("Content-Security-Policy", "default-src 'none'; form-action 'none'")
			default:
				header.Set("Cache-Control", "public, max-age=2419200, must-revalidate")
			}
			err := next(c)
			if err != nil {
				// A watch rebuild can briefly remove a hashed pack before its
				// replacement and manifest are published. Never let a proxy or
				// browser retain that transient error as an immutable asset.
				header.Set("Cache-Control", "no-store")
				header.Del("ETag")
				header.Del("Last-Modified")
			}
			return err
		}
	}
}

func railsPublicStaticPath(cfg config.Config, path string) bool {
	if path == "" {
		return false
	}
	if railsPaperclipStaticPath(cfg, path) {
		return true
	}
	for _, prefix := range []string{"/packs/", "/assets/", "/emoji/", "/avatars/", "/headers/", "/sounds/", "/ocr/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	if strings.HasPrefix(path, "/android-chrome-") || strings.HasPrefix(path, "/apple-touch-icon") {
		return true
	}
	switch path {
	case "/500.html", "/badge.png", "/favicon.ico", "/inert.css", "/oops.gif", "/oops.png", "/robots.txt", "/embed.js", "/sw.js", "/sw.js.map", "/web-push-icon_expand.png", "/web-push-icon_favourite.png", "/web-push-icon_reblog.png":
		return true
	default:
		return false
	}
}

func railsPaperclipStaticPath(cfg config.Config, path string) bool {
	root := strings.TrimRight(strings.TrimSpace(cfg.PaperclipRootURL), "/")
	if root == "" {
		root = "/system"
	}
	if strings.HasPrefix(root, "http://") || strings.HasPrefix(root, "https://") {
		return false
	}
	root = "/" + strings.Trim(root, "/")
	return path == root || strings.HasPrefix(path, root+"/")
}

func railsRemoteInteractionHelperCSP(cfg config.Config) string {
	directives := []string{
		"default-src 'none'",
		"form-action 'none'",
		"frame-ancestors 'self'",
		"connect-src https:",
	}
	if scriptSrc := railsContentSecurityPolicyDirective(cfg, "script-src"); scriptSrc != "" {
		directives = append(directives, scriptSrc)
	}
	return strings.Join(directives, "; ")
}

func railsLetterOpenerCSP() string {
	return strings.Join([]string{
		"child-src 'self'",
		"connect-src 'none'",
		"frame-ancestors 'self'",
		"frame-src 'self'",
		"script-src 'unsafe-inline'",
		"style-src 'unsafe-inline'",
		"worker-src 'none'",
	}, "; ")
}

func uniqueNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sanitizedRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(value), 255))
	for i := 0; i < len(value) && b.Len() < 255; i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' {
			b.WriteByte(ch)
		}
	}
	return b.String()
}

type rateLimitCandidate struct {
	limit  int
	period time.Duration
}

type rackAttackThrottleCandidate struct {
	name     string
	limit    int
	period   time.Duration
	identity string
}

func apiRateLimitHeadersMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if limit, period, ok := railsAPIRateLimit(c.Request()); ok {
			setHeaderIfAbsent(c.Response().Header(), "X-RateLimit-Limit", strconv.Itoa(limit))
			setHeaderIfAbsent(c.Response().Header(), "X-RateLimit-Remaining", strconv.Itoa(limit))
			setHeaderIfAbsent(c.Response().Header(), "X-RateLimit-Reset", railsRateLimitReset(time.Now().UTC(), period))
		}
		return next(c)
	}
}

func setHeaderIfAbsent(header http.Header, key string, value string) {
	if header.Get(key) == "" {
		header.Set(key, value)
	}
}

func railsAPIRateLimit(req *http.Request) (int, time.Duration, bool) {
	if req == nil {
		return 0, 0, false
	}
	if strings.HasPrefix(req.URL.Path, "/media_proxy") {
		return railsMediaProxyLimit, railsMediaProxyPeriod, true
	}
	if !railsAPICORSPath(req.URL.Path) {
		return 0, 0, false
	}
	authenticated := railsAuthenticatedAPIRequest(req)
	candidates := make([]rateLimitCandidate, 0, 4)
	if authenticated {
		candidates = append(candidates,
			rateLimitCandidate{limit: railsAuthenticatedAPILimit, period: railsAuthenticatedAPIPeriod},
			rateLimitCandidate{limit: railsPerTokenAPILimit, period: railsPerTokenAPIPeriod},
		)
	} else {
		candidates = append(candidates, rateLimitCandidate{limit: railsUnauthenticatedAPILimit, period: railsUnauthenticatedAPIPeriod})
	}
	if authenticated && req.Method == http.MethodPost && railsAPIMediaPattern.MatchString(strings.ToLower(req.URL.Path)) {
		candidates = append(candidates, rateLimitCandidate{limit: railsAPIMediaLimit, period: railsAPIMediaPeriod})
	}
	if !authenticated && req.Method == http.MethodPost && req.URL.Path == "/api/v1/accounts" {
		candidates = append(candidates, rateLimitCandidate{limit: railsAPISignupLimit, period: railsAPISignupPeriod})
	}
	if !authenticated && req.Method == http.MethodPost && req.URL.Path == "/api/v1/apps" {
		candidates = append(candidates, rateLimitCandidate{limit: railsAPIApplicationLimit, period: railsAPIApplicationPeriod})
	}
	if railsPagingRequest(req) {
		if authenticated {
			candidates = append(candidates, rateLimitCandidate{limit: railsAuthenticatedPagingLimit, period: railsAuthenticatedPagingPeriod})
		} else {
			candidates = append(candidates, rateLimitCandidate{limit: railsUnauthenticatedPagingLimit, period: railsUnauthenticatedPagingPeriod})
		}
	}
	if authenticated && railsAPIDeleteRequest(req) {
		candidates = append(candidates, rateLimitCandidate{limit: railsAPIDeleteLimit, period: railsAPIDeletePeriod})
	}
	if len(candidates) == 0 {
		return 0, 0, false
	}
	mostLimited := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.limit < mostLimited.limit {
			mostLimited = candidate
		}
	}
	return mostLimited.limit, mostLimited.period, true
}

func (s *Server) rackAttackThrottleMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		now := time.Now().UTC()
		accessToken := s.rackAttackAccessToken(c.Request(), now)
		webUserID := s.rackAttackWebSessionUserID(c)
		candidates := rackAttackThrottleCandidatesForSession(c.Request(), c.RealIP(), accessToken, webUserID)
		if c.Request().Method == http.MethodPost && railsRackAttackPathMatches(c.Request().URL.Path, "/auth/sign_in") && rackAttackFormValue(c.Request(), "user[email]") == "" {
			if pendingUserID, _, ok := s.browserTwoFactorAttempt(c); ok {
				candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_login_attempts/email", limit: railsLoginAttemptEmailLimit, period: railsLoginAttemptEmailPeriod, identity: strconv.FormatInt(pendingUserID, 10)})
			}
		}
		for _, candidate := range candidates {
			exceeded, err := s.consumeRackAttackThrottle(c.Request().Context(), candidate, now)
			if err != nil {
				continue
			}
			if exceeded {
				rackAttackLogHit("throttle", c)
				return rackAttackThrottledResponse(c, candidate, now, s.webLocale(c, nil))
			}
		}
		return next(c)
	}
}

func rackAttackLogHit(matchType string, c *echo.Context) {
	if line := rackAttackLogLine(matchType, c); line != "" {
		log.Print(line)
	}
}

func rackAttackLogLine(matchType string, c *echo.Context) string {
	matchType = strings.TrimSpace(matchType)
	if matchType == "" || c == nil || c.Request() == nil {
		return ""
	}
	req := c.Request()
	fullPath := ""
	if req.URL != nil {
		fullPath = req.URL.RequestURI()
	}
	return fmt.Sprintf("Rate limit hit (%s): %s %s %s", matchType, c.RealIP(), req.Method, fullPath)
}

func rackAttackThrottleCandidates(req *http.Request, realIP string) []rackAttackThrottleCandidate {
	if railsAuthenticatedAPIRequest(req) {
		identity := throttleableRemoteIP(realIP)
		if req == nil || identity == "" {
			return nil
		}
		path := req.URL.Path
		candidates := make([]rackAttackThrottleCandidate, 0, 3)
		if strings.HasPrefix(path, "/media_proxy") {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_media_proxy", limit: railsMediaProxyLimit, period: railsMediaProxyPeriod, identity: identity})
		}
		if req.Method == http.MethodPost && path == "/api/v1/accounts" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_api_sign_up", limit: railsAPISignupLimit, period: railsAPISignupPeriod, identity: identity})
		}
		if req.Method == http.MethodPost && path == "/api/v1/apps" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_oauth_application_registrations/ip", limit: railsAPIApplicationLimit, period: railsAPIApplicationPeriod, identity: identity})
		}
		return candidates
	}
	return rackAttackThrottleCandidatesForToken(req, realIP, nil)
}

func rackAttackThrottleCandidatesForToken(req *http.Request, realIP string, accessToken *models.OAuthAccessToken) []rackAttackThrottleCandidate {
	return rackAttackThrottleCandidatesForSession(req, realIP, accessToken, 0)
}

func rackAttackThrottleCandidatesForSession(req *http.Request, realIP string, accessToken *models.OAuthAccessToken, webUserID int64) []rackAttackThrottleCandidate {
	if req == nil {
		return nil
	}
	identity := throttleableRemoteIP(realIP)
	if identity == "" {
		return nil
	}
	path := req.URL.Path
	userIdentity, tokenIdentity := rackAttackTokenIdentities(accessToken)
	candidates := make([]rackAttackThrottleCandidate, 0, 8)
	if railsAPICORSPath(path) {
		if userIdentity != "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_authenticated_api", limit: railsAuthenticatedAPILimit, period: railsAuthenticatedAPIPeriod, identity: userIdentity})
		}
		if tokenIdentity != "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_per_token_api", limit: railsPerTokenAPILimit, period: railsPerTokenAPIPeriod, identity: tokenIdentity})
		}
		if userIdentity == "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_unauthenticated_api", limit: railsUnauthenticatedAPILimit, period: railsUnauthenticatedAPIPeriod, identity: identity})
		}
	}
	if req.Method == http.MethodPost && railsAPIMediaPattern.MatchString(strings.ToLower(path)) && userIdentity != "" {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_api_media", limit: railsAPIMediaLimit, period: railsAPIMediaPeriod, identity: userIdentity})
	}
	if strings.HasPrefix(path, "/media_proxy") {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_media_proxy", limit: railsMediaProxyLimit, period: railsMediaProxyPeriod, identity: identity})
	}
	if req.Method == http.MethodPost && path == "/api/v1/accounts" {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_api_sign_up", limit: railsAPISignupLimit, period: railsAPISignupPeriod, identity: identity})
	}
	if railsPagingRequest(req) {
		if userIdentity != "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_authenticated_paging", limit: railsAuthenticatedPagingLimit, period: railsAuthenticatedPagingPeriod, identity: userIdentity})
		} else {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_unauthenticated_paging", limit: railsUnauthenticatedPagingLimit, period: railsUnauthenticatedPagingPeriod, identity: identity})
		}
	}
	if railsAPIDeleteRequest(req) && userIdentity != "" {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_api_delete", limit: railsAPIDeleteLimit, period: railsAPIDeletePeriod, identity: userIdentity})
	}
	if req.Method == http.MethodPost && path == "/api/v1/apps" {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_oauth_application_registrations/ip", limit: railsAPIApplicationLimit, period: railsAPIApplicationPeriod, identity: identity})
	}
	candidates = append(candidates, rackAttackHTMLThrottleCandidates(req, identity, userIdentity, webUserID)...)
	return candidates
}

func rackAttackHTMLThrottleCandidates(req *http.Request, ipIdentity string, tokenUserIdentity string, webUserID int64) []rackAttackThrottleCandidate {
	if req == nil || ipIdentity == "" {
		return nil
	}
	path := req.URL.Path
	email := rackAttackFormValue(req, "user[email]")
	webUserIdentity := ""
	if webUserID > 0 {
		webUserIdentity = strconv.FormatInt(webUserID, 10)
	}
	candidates := make([]rackAttackThrottleCandidate, 0, 8)
	if req.Method == http.MethodPost && railsRackAttackPathMatches(path, "/auth") {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_sign_up_attempts/ip", limit: railsAuthAttemptIPLimit, period: railsAuthAttemptIPPeriod, identity: ipIdentity})
	}
	if req.Method == http.MethodPost && railsRackAttackPathMatches(path, "/auth/password") {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_password_resets/ip", limit: railsAuthAttemptIPLimit, period: railsAuthAttemptIPPeriod, identity: ipIdentity})
		if email != "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_password_resets/email", limit: railsAuthEmailLimit, period: railsPasswordResetEmailPeriod, identity: email})
		}
	}
	if (req.Method == http.MethodPost && (railsRackAttackPathMatches(path, "/auth/confirmation") || path == "/api/v1/emails/confirmations")) ||
		((req.Method == http.MethodPut || req.Method == http.MethodPatch) && railsRackAttackPathMatches(path, "/auth/setup")) {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_email_confirmations/ip", limit: railsAuthAttemptIPLimit, period: railsAuthAttemptIPPeriod, identity: ipIdentity})
	}
	if req.Method == http.MethodPost && railsRackAttackPathMatches(path, "/auth/confirmation") && email != "" {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_email_confirmations/email", limit: railsAuthEmailLimit, period: railsPasswordResetEmailPeriod, identity: email})
	} else if req.Method == http.MethodPost && path == "/api/v1/emails/confirmations" && tokenUserIdentity != "" {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_email_confirmations/email", limit: railsAuthEmailLimit, period: railsPasswordResetEmailPeriod, identity: tokenUserIdentity})
	}
	if (req.Method == http.MethodPut || req.Method == http.MethodPatch) && railsRackAttackPathMatches(path, "/auth/setup") {
		if email != "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_auth_setup/email", limit: railsAuthEmailLimit, period: railsAuthSetupEmailPeriod, identity: email})
		}
		if webUserIdentity != "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_auth_setup/account", limit: railsAuthEmailLimit, period: railsAuthSetupEmailPeriod, identity: webUserIdentity})
		}
	}
	if req.Method == http.MethodPost && railsRackAttackPathMatches(path, "/auth/sign_in") {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_login_attempts/ip", limit: railsAuthAttemptIPLimit, period: railsAuthAttemptIPPeriod, identity: ipIdentity})
		if email != "" {
			candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_login_attempts/email", limit: railsLoginAttemptEmailLimit, period: railsLoginAttemptEmailPeriod, identity: email})
		}
	}
	if (req.Method == http.MethodPut || req.Method == http.MethodPatch) && (railsRackAttackPathMatches(path, "/auth") || railsRackAttackPathMatches(path, "/auth/password")) && webUserIdentity != "" {
		candidates = append(candidates, rackAttackThrottleCandidate{name: "throttle_password_change/account", limit: railsPasswordChangeAccountLimit, period: railsPasswordChangeAccountPeriod, identity: webUserIdentity})
	}
	return candidates
}

func railsRackAttackPathMatches(path string, otherPath string) bool {
	return path == otherPath || strings.HasPrefix(path, otherPath+".")
}

func rackAttackFormValue(req *http.Request, key string) string {
	if req == nil || key == "" {
		return ""
	}
	return strings.TrimSpace(req.FormValue(key))
}

func rackAttackTokenIdentities(accessToken *models.OAuthAccessToken) (string, string) {
	if accessToken == nil || accessToken.ID == 0 {
		return "", ""
	}
	tokenIdentity := strconv.FormatInt(accessToken.ID, 10)
	if accessToken.ResourceOwnerID.Valid && accessToken.ResourceOwnerID.Int64 > 0 {
		return strconv.FormatInt(accessToken.ResourceOwnerID.Int64, 10), tokenIdentity
	}
	return "", tokenIdentity
}

func (s *Server) rackAttackAccessToken(req *http.Request, now time.Time) *models.OAuthAccessToken {
	if s == nil || s.db == nil || req == nil {
		return nil
	}
	tokenValue := rackAttackRequestToken(req)
	if tokenValue == "" {
		return nil
	}
	var accessToken models.OAuthAccessToken
	err := s.db.Select("id", "resource_owner_id", "expires_in", "created_at").
		Where("token = ? AND revoked_at IS NULL", tokenValue).
		First(&accessToken).Error
	if err != nil || oauthAccessTokenExpired(accessToken, now) {
		return nil
	}
	return &accessToken
}

func (s *Server) rackAttackWebSessionUserID(c *echo.Context) int64 {
	if s == nil || s.db == nil || c == nil {
		return 0
	}
	sessionID, ok := s.railsSessionIDFromCookie(c)
	if !ok {
		sessionID, ok = s.railsSessionIDFromEncryptedSession(c)
	}
	if !ok || sessionID == "" {
		return 0
	}
	var activation models.SessionActivation
	if err := s.db.Select("user_id").Where("session_id = ?", sessionID).First(&activation).Error; err != nil {
		return 0
	}
	return activation.UserID
}

func rackAttackRequestToken(req *http.Request) string {
	if req == nil {
		return ""
	}
	value := req.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	if token := requestRawQueryParamValue(req, "access_token"); token != "" {
		return token
	}
	if token := requestRawQueryParamValue(req, "bearer_token"); token != "" {
		return token
	}
	return ""
}

func throttleableRemoteIP(raw string) string {
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if raw == "" {
		return ""
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return raw
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

func (s *Server) consumeRackAttackThrottle(ctx context.Context, candidate rackAttackThrottleCandidate, now time.Time) (bool, error) {
	periodSeconds := int64(candidate.period / time.Second)
	if candidate.limit <= 0 || periodSeconds <= 0 || candidate.identity == "" {
		return false, nil
	}
	key := rackAttackThrottleRedisKey(redisConfig(s.cfg).prefix, candidate, now)
	redisCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	value, err := s.redisCommand(redisCtx, "INCR", key)
	if err != nil {
		return false, err
	}
	if redisInt(value) == 1 {
		_, _ = s.redisCommand(redisCtx, "EXPIRE", key, strconv.FormatInt(periodSeconds, 10))
	}
	return redisInt(value) > int64(candidate.limit), nil
}

func rackAttackThrottleRedisKey(prefix string, candidate rackAttackThrottleCandidate, now time.Time) string {
	periodSeconds := int64(candidate.period / time.Second)
	bucket := int64(0)
	if periodSeconds > 0 {
		bucket = now.Unix() / periodSeconds
	}
	return prefix + "paon:rack_attack:" + candidate.name + ":" + candidate.identity + ":" + strconv.FormatInt(bucket, 10)
}

func rackAttackThrottledResponse(c *echo.Context, candidate rackAttackThrottleCandidate, now time.Time, locale string) error {
	header := c.Response().Header()
	header.Set(echo.HeaderContentType, echo.MIMEApplicationJSONCharsetUTF8)
	header.Set("X-RateLimit-Limit", strconv.Itoa(candidate.limit))
	header.Set("X-RateLimit-Remaining", "0")
	header.Set("X-RateLimit-Reset", railsRateLimitReset(now, candidate.period))
	message := webT(locale, "errors.429")
	if message == "" || message == "errors.429" {
		message = "Too many requests"
	}
	return c.JSON(http.StatusTooManyRequests, map[string]string{"error": message})
}

func railsAuthenticatedAPIRequest(req *http.Request) bool {
	if req.Header.Get("Authorization") != "" {
		return true
	}
	return requestHasTokenParam(req)
}

func railsPagingRequest(req *http.Request) bool {
	query := req.URL.Query()
	for _, key := range []string{"page", "min_id", "max_id", "since_id"} {
		if query.Get(key) != "" {
			return true
		}
	}
	return false
}

func railsAPIDeleteRequest(req *http.Request) bool {
	if req.Method == http.MethodDelete && railsAPIDeleteStatusPattern.MatchString(req.URL.Path) {
		return true
	}
	if req.Method == http.MethodPost && railsAPIUnreblogPattern.MatchString(req.URL.Path) {
		return true
	}
	return false
}

func railsRateLimitReset(now time.Time, period time.Duration) string {
	periodSeconds := int64(period / time.Second)
	if periodSeconds <= 0 {
		return now.Format("2006-01-02T15:04:05.000000Z")
	}
	offset := periodSeconds - (now.Unix() % periodSeconds)
	return now.Add(time.Duration(offset) * time.Second).Format("2006-01-02T15:04:05.000000Z")
}

func setStatusFamilyRateLimitHeaders(c *echo.Context, remaining int) {
	setRateLimitFamilyHeaders(c, railsStatusFamilyLimit, railsStatusFamilyPeriod, remaining)
}

func setFollowsFamilyRateLimitHeaders(c *echo.Context, remaining int) {
	setRateLimitFamilyHeaders(c, railsFollowsFamilyLimit, railsFollowsFamilyPeriod, remaining)
}

func setReportsFamilyRateLimitHeaders(c *echo.Context, remaining int) {
	setRateLimitFamilyHeaders(c, railsReportsFamilyLimit, railsReportsFamilyPeriod, remaining)
}

func setRateLimitFamilyHeaders(c *echo.Context, limit int, period time.Duration, remaining int) {
	if remaining < 0 {
		remaining = 0
	}
	c.Response().Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	c.Response().Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	c.Response().Header().Set("X-RateLimit-Reset", railsRateLimitReset(time.Now().UTC(), period))
}

func methodOverrideMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		if req.Method == http.MethodPost {
			if method := normalizedOverrideMethod(req.Header.Get("X-HTTP-Method-Override")); method != "" {
				cacheURLEncodedPostForm(req)
				req.Method = method
				return next(c)
			}
			if method := formOverrideMethod(req); method != "" {
				req.Method = method
			}
		}
		return next(c)
	}
}

func apiTrailingSlashMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		path := req.URL.Path
		if len(path) > 1 && strings.HasPrefix(path, "/api/") && strings.HasSuffix(path, "/") {
			req.URL.Path = strings.TrimRight(path, "/")
			if req.URL.RawPath != "" {
				req.URL.RawPath = strings.TrimRight(req.URL.RawPath, "/")
			}
		}
		return next(c)
	}
}

func railsFormContentTypeMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		if railsMultipartFormMissingBoundary(req.Header.Get(echo.HeaderContentType)) {
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		}
		return next(c)
	}
}

func railsMultipartFormMissingBoundary(contentType string) bool {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	}
	return strings.EqualFold(mediaType, echo.MIMEMultipartForm) && strings.TrimSpace(params["boundary"]) == ""
}

func formOverrideMethod(req *http.Request) string {
	values, ok := cacheURLEncodedPostForm(req)
	if !ok {
		return ""
	}
	return normalizedOverrideMethod(values.Get("_method"))
}

func cacheURLEncodedPostForm(req *http.Request) (url.Values, bool) {
	if req == nil || req.Body == nil || !strings.HasPrefix(strings.ToLower(req.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		return nil, false
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, false
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, false
	}
	req.PostForm = values
	req.Form = make(url.Values, len(values)+len(req.URL.Query()))
	for key, queryValues := range req.URL.Query() {
		req.Form[key] = append([]string(nil), queryValues...)
	}
	for key, postValues := range values {
		req.Form[key] = append(append([]string(nil), postValues...), req.Form[key]...)
	}
	return values, true
}

func normalizedOverrideMethod(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
		return strings.ToUpper(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func corsMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		if strings.TrimSpace(req.Header.Get("Origin")) == "" {
			return next(c)
		}
		path := req.URL.Path
		if railsPublicCORSPath(path, req.Method) {
			c.Response().Header().Set("Access-Control-Allow-Origin", "*")
			c.Response().Header().Set("Access-Control-Allow-Methods", "GET")
			c.Response().Header().Set("Access-Control-Allow-Headers", corsAllowHeaders(req))
			if req.Method == http.MethodOptions {
				return c.NoContent(http.StatusNoContent)
			}
		}
		if railsAPICORSPath(path) {
			c.Response().Header().Set("Access-Control-Allow-Origin", "*")
			c.Response().Header().Set("Access-Control-Allow-Methods", "POST, PUT, DELETE, GET, PATCH, OPTIONS")
			c.Response().Header().Set("Access-Control-Allow-Headers", corsAllowHeaders(req))
			c.Response().Header().Set("Access-Control-Expose-Headers", "Link, X-RateLimit-Reset, X-RateLimit-Limit, X-RateLimit-Remaining, X-Request-Id")
			if req.Method == http.MethodOptions {
				return c.NoContent(http.StatusNoContent)
			}
		}
		if railsOAuthTokenCORSPath(path, req.Method) {
			c.Response().Header().Set("Access-Control-Allow-Origin", "*")
			c.Response().Header().Set("Access-Control-Allow-Methods", "POST")
			c.Response().Header().Set("Access-Control-Allow-Headers", corsAllowHeaders(req))
			if req.Method == http.MethodOptions {
				return c.NoContent(http.StatusNoContent)
			}
		}
		return next(c)
	}
}

func railsPublicCORSPath(path string, method string) bool {
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		return false
	}
	return strings.HasPrefix(path, "/.well-known/") ||
		accountWebCORSPath(path) ||
		userActorCORSPath(path)
}

func railsAPICORSPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

func railsOAuthTokenCORSPath(path string, method string) bool {
	return path == "/oauth/token" && (method == http.MethodPost || method == http.MethodOptions)
}

func accountWebCORSPath(path string) bool {
	if !strings.HasPrefix(path, "/@") || strings.Contains(strings.TrimPrefix(path, "/@"), "/") {
		return false
	}
	return strings.TrimPrefix(path, "/@") != ""
}

func userActorCORSPath(path string) bool {
	if !strings.HasPrefix(path, "/users/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/users/")
	return rest != "" && !strings.Contains(rest, "/")
}

func corsAllowHeaders(req *http.Request) string {
	headers := strings.TrimSpace(req.Header.Get("Access-Control-Request-Headers"))
	if headers == "" {
		return "*"
	}
	return headers
}

func (s *Server) apiAuthenticationGateMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		path := c.Path()
		if (path == "/api" || strings.HasPrefix(path, "/api/")) && path != "/api/oembed" {
			if err := s.requireAuthenticatedAPIIfDisallowed(c); err != nil {
				return err
			}
		}
		return next(c)
	}
}

func (s *Server) routes() {
	e := s.echo

	e.GET("/health", s.health)
	e.GET("/health.:format", s.health)
	e.GET("/health/ready", s.ready)
	e.GET("/metrics", s.streamingMetrics)
	e.GET("/letter_opener", s.letterOpenerPage)
	e.GET("/letter_opener/*", s.letterOpenerPage)
	e.GET("/manifest", s.manifest)
	e.GET("/manifest.json", s.manifest)
	e.GET("/manifest.:format", s.manifest)
	e.GET("/intent", s.intent)
	e.GET("/intent.json", s.intent)
	e.GET("/intent.:format", s.intent)
	e.GET("/privacy-policy.:format", s.privacyPolicy)
	e.GET("/privacy-policy", s.privacyPolicy)
	e.GET("/api/oembed", s.oEmbed)
	e.GET("/.well-known/nodeinfo", s.nodeInfoDiscovery)
	e.GET("/.well-known/nodeinfo.json", s.nodeInfoDiscovery)
	e.GET("/.well-known/nodeinfo.:format", s.nodeInfoDiscovery)
	e.GET("/.well-known/host-meta", s.hostMeta)
	e.GET("/.well-known/host-meta.xml", s.hostMeta)
	e.GET("/.well-known/host-meta.:format", s.hostMeta)
	e.GET("/.well-known/webfinger", s.webfinger)
	e.GET("/.well-known/webfinger.json", s.webfinger)
	e.GET("/.well-known/webfinger.:format", s.webfinger)
	e.GET("/.well-known/change-password", redirectTo("/auth/edit", http.StatusMovedPermanently))
	e.GET("/.well-known/change-password.json", redirectTo("/auth/edit", http.StatusMovedPermanently))
	e.GET("/.well-known/change-password.:format", redirectTo("/auth/edit", http.StatusMovedPermanently))
	e.GET("/.well-known/proxy", s.remoteInteractionRedirect)
	e.GET("/.well-known/proxy.json", s.remoteInteractionRedirectJSON)
	e.GET("/.well-known/proxy.:format", s.remoteInteractionRedirectFormat)
	e.GET("/nodeinfo/2.0", s.nodeInfo)
	e.GET("/nodeinfo/2.0.json", s.nodeInfo)
	e.GET("/nodeinfo/2.0.:format", s.nodeInfo)
	e.GET("/custom.css.:format", s.customCSS)
	e.GET("/custom.css", s.customCSS)
	e.GET("/media/:id/player.:format", s.publicMediaPlayer)
	e.GET("/media/:id/player", s.publicMediaPlayer)
	e.GET("/media/:id.:format", s.publicMedia)
	e.GET("/media/:id", s.publicMedia)
	e.GET("/media_proxy/:id/*", s.mediaProxy)
	e.GET("/media_proxy/:id", s.mediaProxy)
	e.GET("/download_proxy/:id/*", s.downloadProxy)
	e.GET("/download_proxy/:id", s.downloadProxy)
	e.GET("/backups/:id/download", s.downloadBackup)
	e.GET("/tags/:name.:format", s.publicTag)
	e.GET("/tags/:name", s.publicTag)
	e.GET("/emojis/:id.:format", s.publicEmoji)
	e.GET("/emojis/:id", s.publicEmoji)
	e.GET("/actor.json", s.activityPubInstanceActor)
	e.GET("/actor.:format", s.activityPubInstanceActor)
	e.GET("/actor", s.activityPubInstanceActor)
	e.GET("/actor/outbox.json", s.activityPubInstanceOutbox)
	e.GET("/actor/outbox.:format", s.activityPubInstanceOutbox)
	e.GET("/actor/outbox", s.activityPubInstanceOutbox)
	e.POST("/actor/inbox.json", s.activityPubInbox)
	e.POST("/actor/inbox.:format", s.activityPubInbox)
	e.POST("/actor/inbox", s.activityPubInbox)
	e.GET("/users/:username/outbox.json", s.activityPubOutbox)
	e.GET("/users/:username/outbox.:format", s.activityPubOutbox)
	e.GET("/users/:username/outbox", s.activityPubOutbox)
	e.GET("/users/:username/followers.json", s.activityPubFollowers)
	e.GET("/users/:username/followers.:format", s.activityPubFollowersFormat)
	e.GET("/users/:username/followers", s.activityPubFollowersOrWebRedirect)
	e.GET("/users/:username/following.json", s.activityPubFollowing)
	e.GET("/users/:username/following.:format", s.activityPubFollowingFormat)
	e.GET("/users/:username/following", s.activityPubFollowingOrWebRedirect)
	e.GET("/users/:username/followers_synchronization.json", s.activityPubFollowersSynchronization)
	e.GET("/users/:username/followers_synchronization.:format", s.activityPubFollowersSynchronization)
	e.GET("/users/:username/followers_synchronization", s.activityPubFollowersSynchronization)
	e.POST("/users/:username/claim.json", s.activityPubClaim)
	e.POST("/users/:username/claim.:format", s.activityPubClaim)
	e.POST("/users/:username/claim", s.activityPubClaim)
	e.GET("/users/:username/collections/:id.json", s.activityPubCollection)
	e.GET("/users/:username/collections/:id.:format", s.activityPubCollection)
	e.GET("/users/:username/collections/:id", s.activityPubCollection)
	e.GET("/users/:username/statuses/:id/replies.json", s.activityPubReplies)
	e.GET("/users/:username/statuses/:id/replies.:format", s.activityPubReplies)
	e.GET("/users/:username/statuses/:id/replies", s.activityPubReplies)
	e.GET("/users/:username/statuses/:id/embed.:format", s.statusEmbed)
	e.GET("/users/:username/statuses/:id/embed", s.statusEmbed)
	e.GET("/users/:username/statuses/:id.json", s.activityPubStatus)
	e.GET("/users/:username/statuses/:id.:format", s.activityPubStatusUnsupportedFormat)
	e.GET("/users/:username/statuses/:id", s.activityPubStatusOrWebRedirect)
	e.GET("/users/:username/statuses/:id/activity.json", s.activityPubStatusActivity)
	e.GET("/users/:username/statuses/:id/activity.:format", s.activityPubStatusActivity)
	e.GET("/users/:username/statuses/:id/activity", s.activityPubStatusActivity)
	e.POST("/users/:username/inbox.json", s.activityPubInbox)
	e.POST("/users/:username/inbox.:format", s.activityPubInbox)
	e.POST("/users/:username/inbox", s.activityPubInbox)
	e.GET("/users/:username.json", s.activityPubActor)
	e.GET("/users/:username.rss", s.publicAccount)
	e.GET("/users/:username.:format", s.activityPubActorOrWebRedirect)
	e.GET("/users/:username", s.activityPubActorOrWebRedirect)
	e.POST("/inbox.json", s.activityPubInbox)
	e.POST("/inbox.:format", s.activityPubInbox)
	e.POST("/inbox", s.activityPubInbox)
	e.GET("/invite/:invite_code.:format", s.publicInvite)
	e.GET("/invite/:invite_code", s.publicInvite)
	e.GET("/unsubscribe.:format", s.unsubscribePage)
	e.GET("/unsubscribe", s.unsubscribePage)
	e.POST("/unsubscribe.:format", s.createUnsubscribe)
	e.POST("/unsubscribe", s.createUnsubscribe)
	e.GET("/auth/setup.:format", s.authSetupPage)
	e.GET("/auth/setup", s.authSetupPage)
	e.POST("/auth/setup.:format", s.notFound)
	e.POST("/auth/setup", s.notFound)
	e.PUT("/auth/setup.:format", s.updateAuthSetup)
	e.PUT("/auth/setup", s.updateAuthSetup)
	e.PATCH("/auth/setup.:format", s.updateAuthSetup)
	e.PATCH("/auth/setup", s.updateAuthSetup)
	e.GET("/auth/sign_up", s.registrationForm)
	e.GET("/auth/cancel", s.cancelUserRegistration)
	e.POST("/auth", s.createWebRegistration)
	e.GET("/auth/edit", s.authEditPage)
	e.POST("/auth/edit", s.notFound)
	e.PUT("/auth", s.updateUserRegistration)
	e.PATCH("/auth", s.updateUserRegistration)
	e.DELETE("/auth", s.destroyUserRegistration)
	e.POST("/auth/challenge.:format", s.createAuthChallenge)
	e.POST("/auth/challenge", s.createAuthChallenge)
	e.GET("/auth/sessions/security_key_options.:format", s.webauthnOptions)
	e.GET("/auth/sessions/security_key_options", s.webauthnOptions)
	e.POST("/auth/captcha_confirmation.:format", s.confirmCaptcha)
	e.POST("/auth/captcha_confirmation", s.confirmCaptcha)
	e.GET("/auth/confirmation/new", s.newAuthConfirmation)
	e.GET("/auth/confirmation", s.showAuthConfirmation)
	e.POST("/auth/confirmation", s.createAuthConfirmation)
	e.GET("/auth/password/new", s.newAuthPassword)
	e.GET("/auth/password/edit", s.editAuthPassword)
	e.POST("/auth/password", s.postAuthPassword)
	e.PUT("/auth/password", s.updateAuthPassword)
	e.PATCH("/auth/password", s.updateAuthPassword)
	e.GET("/auth/sign_in", s.signInForm)
	e.POST("/auth/sign_in", s.signIn)
	e.POST("/auth/sign_out", s.notFound)
	e.DELETE("/auth/sign_out", s.signOut)
	e.GET("/auth/auth/:provider", s.omniauthProviderEntry)
	e.POST("/auth/auth/:provider", s.omniauthProviderEntry)
	e.GET("/auth/auth/:provider/callback", s.omniauthCallback)
	e.POST("/auth/auth/:provider/callback", s.omniauthCallback)
	e.GET("/auth/auth/:provider/logout", s.omniauthLogout)
	e.GET("/oauth/authorize", s.oauthAuthorize)
	e.POST("/oauth/authorize", s.oauthAuthorizeDecision)
	e.DELETE("/oauth/authorize", s.oauthAuthorizeDecision)
	e.POST("/oauth/token", s.oauthToken)
	e.GET("/oauth/token/info", s.oauthTokenInfo)
	e.POST("/oauth/revoke", s.oauthRevoke)
	e.GET("/oauth/applications", s.oauthApplicationsForbidden)
	e.GET("/oauth/applications.:format", s.oauthApplicationsForbidden)
	e.POST("/oauth/applications", s.oauthApplicationsForbidden)
	e.POST("/oauth/applications.:format", s.oauthApplicationsForbidden)
	e.GET("/oauth/applications/new", s.oauthApplicationsForbidden)
	e.GET("/oauth/applications/new.:format", s.oauthApplicationsForbidden)
	e.GET("/oauth/applications/:id/edit", s.oauthApplicationsForbidden)
	e.GET("/oauth/applications/:id/edit.:format", s.oauthApplicationsForbidden)
	e.GET("/oauth/applications/:id", s.oauthApplicationsForbidden)
	e.GET("/oauth/applications/:id.:format", s.oauthApplicationsForbidden)
	e.PATCH("/oauth/applications/:id", s.oauthApplicationsForbidden)
	e.PATCH("/oauth/applications/:id.:format", s.oauthApplicationsForbidden)
	e.PUT("/oauth/applications/:id", s.oauthApplicationsForbidden)
	e.PUT("/oauth/applications/:id.:format", s.oauthApplicationsForbidden)
	e.DELETE("/oauth/applications/:id", s.oauthApplicationsForbidden)
	e.DELETE("/oauth/applications/:id.:format", s.oauthApplicationsForbidden)
	e.GET("/oauth/authorized_applications", s.oauthAuthorizedApplications)
	e.DELETE("/oauth/authorized_applications/:id", s.destroyOAuthAuthorizedApplication)
	e.POST("/oauth/authorized_applications/:id", s.notFound)
	e.GET("/authorize_follow", s.remoteInteractionRedirect)
	e.GET("/authorize_follow.json", s.remoteInteractionRedirectJSON)
	e.GET("/authorize_follow.:format", s.remoteInteractionRedirectFormat)
	e.GET("/authorize_interaction", s.authorizeInteraction)
	e.GET("/authorize_interaction.json", s.authorizeInteraction)
	e.GET("/authorize_interaction.:format", s.authorizeInteraction)
	e.GET("/remote_interaction_helper.:format", s.remoteInteractionHelper)
	e.GET("/remote_interaction_helper", s.remoteInteractionHelper)

	e.StaticFS("/packs", os.DirFS(filepath.Join(s.cfg.PublicDir, "packs")))
	e.StaticFS("/assets", os.DirFS(filepath.Join(s.cfg.PublicDir, "assets")))
	e.StaticFS("/emoji", os.DirFS(filepath.Join(s.cfg.PublicDir, "emoji")))
	systemFS := os.DirFS(s.cfg.SystemAssetPath())
	e.StaticFS("/system", systemFS)
	if rootURL := strings.TrimRight(strings.TrimSpace(s.cfg.PaperclipRootURL), "/"); rootURL != "" && rootURL != "/system" && !strings.HasPrefix(rootURL, "http://") && !strings.HasPrefix(rootURL, "https://") {
		e.StaticFS("/"+strings.Trim(rootURL, "/"), systemFS)
	}
	e.StaticFS("/avatars", os.DirFS(filepath.Join(s.cfg.PublicDir, "avatars")))
	e.StaticFS("/headers", os.DirFS(filepath.Join(s.cfg.PublicDir, "headers")))
	e.StaticFS("/sounds", os.DirFS(filepath.Join(s.cfg.PublicDir, "sounds")))
	e.StaticFS("/ocr", os.DirFS(filepath.Join(s.cfg.PublicDir, "ocr")))
	publicFS := os.DirFS(s.cfg.PublicDir)
	e.FileFS("/500.html", "500.html", publicFS)
	e.FileFS("/badge.png", "badge.png", publicFS)
	e.FileFS("/favicon.ico", "favicon.ico", publicFS)
	e.FileFS("/inert.css", "inert.css", publicFS)
	e.FileFS("/oops.gif", "oops.gif", publicFS)
	e.FileFS("/oops.png", "oops.png", publicFS)
	e.FileFS("/robots.txt", "robots.txt", publicFS)
	e.FileFS("/embed.js", "embed.js", publicFS)
	e.FileFS("/sw.js", "packs/sw.js", publicFS)
	e.FileFS("/sw.js.map", "packs/sw.js.map", publicFS)
	e.FileFS("/web-push-icon_expand.png", "web-push-icon_expand.png", publicFS)
	e.FileFS("/web-push-icon_favourite.png", "web-push-icon_favourite.png", publicFS)
	e.FileFS("/web-push-icon_reblog.png", "web-push-icon_reblog.png", publicFS)
	for _, size := range []string{"36", "48", "72", "96", "144", "192", "256", "384", "512"} {
		e.GET("/android-chrome-"+size+"x"+size+".png", s.androidChromeIcon)
	}
	for _, size := range []string{"57", "60", "72", "76", "114", "120", "144", "152", "167", "180", "1024"} {
		e.GET("/apple-touch-icon-"+size+"x"+size+".png", s.appleTouchIcon)
		e.GET("/apple-touch-icon-"+size+"x"+size+"-precomposed.png", s.appleTouchIcon)
	}
	e.GET("/apple-touch-icon.png", s.appleTouchIcon)
	e.GET("/apple-touch-icon-precomposed.png", s.appleTouchIcon)

	e.GET("/api/v1/instance", s.instanceV1)
	e.GET("/api/v2/instance", s.instanceV2)
	e.GET("/api/v1/instance/translation_languages", s.translationLanguages)
	e.GET("/api/v1/instance/extended_description", s.instanceExtendedDescription)
	e.GET("/api/v1/instance/privacy_policy", s.instancePrivacyPolicy)
	e.GET("/api/v1/instance/domain_blocks", s.instanceDomainBlocks)
	e.GET("/api/v1/instance/peers", s.instancePeers)
	e.GET("/api/v1/instance/rules", s.instanceRules)
	e.GET("/api/v1/instance/languages", s.instanceLanguages)
	e.GET("/api/v1/instance/activity", s.instanceActivity)
	e.GET("/api/v2/instance_stats/:domain", s.instanceStatsV2)
	e.GET("/api/v1/peers/search", s.peerSearch)
	e.GET("/api/v1/custom_emojis", s.customEmojis)
	e.GET("/api/v1/preferences", s.preferences)
	e.GET("/api/v1/streaming/health", s.streamingHealth)
	e.GET("/api/v1/streaming", s.streaming)
	e.GET("/api/v1/streaming/*", s.streaming)
	e.GET("/api/v1/admin/accounts", s.adminAccounts)
	e.GET("/api/v1/admin/accounts/:id", s.showAdminAccount)
	e.DELETE("/api/v1/admin/accounts/:id", s.destroyAdminAccount)
	e.POST("/api/v1/admin/accounts/:id/enable", s.enableAdminAccount)
	e.POST("/api/v1/admin/accounts/:id/approve", s.approveAdminAccount)
	e.POST("/api/v1/admin/accounts/:id/reject", s.rejectAdminAccount)
	e.POST("/api/v1/admin/accounts/:id/unsensitive", s.unsensitiveAdminAccount)
	e.POST("/api/v1/admin/accounts/:id/unsilence", s.unsilenceAdminAccount)
	e.POST("/api/v1/admin/accounts/:id/unsuspend", s.unsuspendAdminAccount)
	e.POST("/api/v1/admin/accounts/:account_id/action", s.adminAccountAction)
	e.GET("/api/v2/admin/accounts", s.adminAccounts)
	e.GET("/api/v1/admin/reports", s.adminReports)
	e.GET("/api/v1/admin/reports/:id", s.showAdminReport)
	e.PUT("/api/v1/admin/reports/:id", s.updateAdminReport)
	e.PATCH("/api/v1/admin/reports/:id", s.updateAdminReport)
	e.POST("/api/v1/admin/reports/:id/assign_to_self", s.assignAdminReportToSelf)
	e.POST("/api/v1/admin/reports/:id/unassign", s.unassignAdminReport)
	e.POST("/api/v1/admin/reports/:id/reopen", s.reopenAdminReport)
	e.POST("/api/v1/admin/reports/:id/resolve", s.resolveAdminReport)
	e.GET("/api/v1/admin/tags", s.adminTags)
	e.GET("/api/v1/admin/tags/:id", s.showAdminTag)
	e.PUT("/api/v1/admin/tags/:id", s.updateAdminTag)
	e.PATCH("/api/v1/admin/tags/:id", s.updateAdminTag)
	e.GET("/api/v1/admin/trends/tags", s.adminTrendTags)
	e.POST("/api/v1/admin/trends/tags/:id/approve", s.approveAdminTrendTag)
	e.POST("/api/v1/admin/trends/tags/:id/reject", s.rejectAdminTrendTag)
	e.GET("/api/v1/admin/trends/links", s.adminTrendLinks)
	e.POST("/api/v1/admin/trends/links/:id/approve", s.approveAdminTrendLink)
	e.POST("/api/v1/admin/trends/links/:id/reject", s.rejectAdminTrendLink)
	e.GET("/api/v1/admin/trends/statuses", s.adminTrendStatuses)
	e.POST("/api/v1/admin/trends/statuses/:id/approve", s.approveAdminTrendStatus)
	e.POST("/api/v1/admin/trends/statuses/:id/reject", s.rejectAdminTrendStatus)
	e.GET("/api/v1/admin/trends/links/publishers", s.adminPreviewCardProviders)
	e.POST("/api/v1/admin/trends/links/publishers/:id/approve", s.approveAdminPreviewCardProvider)
	e.POST("/api/v1/admin/trends/links/publishers/:id/reject", s.rejectAdminPreviewCardProvider)
	e.POST("/api/v1/admin/measures", s.adminMeasures)
	e.POST("/api/v1/admin/dimensions", s.adminDimensions)
	e.POST("/api/v1/admin/retention", s.adminRetention)
	e.GET("/api/v1/admin/domain_allows", s.adminDomainAllows)
	e.POST("/api/v1/admin/domain_allows", s.createAdminDomainAllow)
	e.GET("/api/v1/admin/domain_allows/:id", s.showAdminDomainAllow)
	e.DELETE("/api/v1/admin/domain_allows/:id", s.deleteAdminDomainAllow)
	e.GET("/api/v1/admin/domain_blocks", s.adminDomainBlocks)
	e.POST("/api/v1/admin/domain_blocks", s.createAdminDomainBlock)
	e.GET("/api/v1/admin/domain_blocks/:id", s.showAdminDomainBlock)
	e.PUT("/api/v1/admin/domain_blocks/:id", s.updateAdminDomainBlock)
	e.PATCH("/api/v1/admin/domain_blocks/:id", s.updateAdminDomainBlock)
	e.DELETE("/api/v1/admin/domain_blocks/:id", s.deleteAdminDomainBlock)
	e.GET("/api/v1/admin/email_domain_blocks", s.adminEmailDomainBlocks)
	e.POST("/api/v1/admin/email_domain_blocks", s.createAdminEmailDomainBlock)
	e.GET("/api/v1/admin/email_domain_blocks/:id", s.showAdminEmailDomainBlock)
	e.DELETE("/api/v1/admin/email_domain_blocks/:id", s.deleteAdminEmailDomainBlock)
	e.GET("/api/v1/admin/canonical_email_blocks", s.adminCanonicalEmailBlocks)
	e.POST("/api/v1/admin/canonical_email_blocks", s.createAdminCanonicalEmailBlock)
	e.POST("/api/v1/admin/canonical_email_blocks/test", s.testAdminCanonicalEmailBlocks)
	e.GET("/api/v1/admin/canonical_email_blocks/:id", s.showAdminCanonicalEmailBlock)
	e.DELETE("/api/v1/admin/canonical_email_blocks/:id", s.deleteAdminCanonicalEmailBlock)
	e.GET("/api/v1/admin/ip_blocks", s.adminIPBlocks)
	e.POST("/api/v1/admin/ip_blocks", s.createAdminIPBlock)
	e.GET("/api/v1/admin/ip_blocks/:id", s.showAdminIPBlock)
	e.PUT("/api/v1/admin/ip_blocks/:id", s.updateAdminIPBlock)
	e.PATCH("/api/v1/admin/ip_blocks/:id", s.updateAdminIPBlock)
	e.DELETE("/api/v1/admin/ip_blocks/:id", s.deleteAdminIPBlock)
	e.GET("/api/v1/announcements", s.announcements)
	e.POST("/api/v1/announcements/:id/dismiss", s.dismissAnnouncement)
	e.PUT("/api/v1/announcements/:id/reactions/:name", s.addAnnouncementReaction)
	e.PATCH("/api/v1/announcements/:id/reactions/:name", s.addAnnouncementReaction)
	e.DELETE("/api/v1/announcements/:id/reactions/:name", s.removeAnnouncementReaction)
	e.GET("/api/v1/trends", s.trendingTags)
	e.GET("/api/v1/trends/tags", s.trendingTags)
	e.GET("/api/v1/trends/links", s.trendingLinks)
	e.GET("/api/v1/trends/statuses", s.trendingStatuses)
	e.GET("/api/v2/suggestions", s.suggestionsV2)
	e.GET("/api/v1/suggestions", s.suggestionsV1)
	e.DELETE("/api/v1/suggestions/:id", s.deleteSuggestion)
	e.GET("/api/v1/scheduled_statuses", s.scheduledStatuses)
	e.GET("/api/v1/scheduled_statuses/:id", s.showScheduledStatus)
	e.PUT("/api/v1/scheduled_statuses/:id", s.updateScheduledStatus)
	e.PATCH("/api/v1/scheduled_statuses/:id", s.updateScheduledStatus)
	e.DELETE("/api/v1/scheduled_statuses/:id", s.deleteScheduledStatus)
	e.GET("/api/v1/directory", s.directory)
	e.GET("/api/v1/markers", s.markers)
	e.POST("/api/v1/markers", s.updateMarkers)
	e.GET("/api/v1/notifications", s.notifications)
	e.GET("/api/v1/notifications/:id", s.showNotification)
	e.POST("/api/v1/notifications/clear", s.clearNotifications)
	e.POST("/api/v1/notifications/:id/dismiss", s.dismissNotification)
	e.POST("/api/v1/reports", s.createReport)
	e.GET("/api/v1/conversations", s.conversations)
	e.POST("/api/v1/conversations/:id/read", s.readConversation)
	e.POST("/api/v1/conversations/:id/unread", s.unreadConversation)
	e.DELETE("/api/v1/conversations/:id", s.deleteConversation)
	e.GET("/api/v1/blocks", s.blocks)
	e.GET("/api/v1/mutes", s.mutes)
	e.GET("/api/v1/endorsements", s.endorsements)
	e.GET("/api/v1/domain_blocks", s.domainBlocks)
	e.POST("/api/v1/domain_blocks", s.createDomainBlock)
	e.DELETE("/api/v1/domain_blocks", s.deleteDomainBlock)
	e.GET("/api/v1/follow_requests", s.followRequests)
	e.POST("/api/v1/follow_requests/:id/authorize", s.authorizeFollowRequest)
	e.POST("/api/v1/follow_requests/:id/reject", s.rejectFollowRequest)
	e.DELETE("/api/v1/profile/avatar", s.deleteProfileAvatar)
	e.DELETE("/api/v1/profile/header", s.deleteProfileHeader)

	e.POST("/api/v1/apps", s.createApp)
	e.GET("/api/v1/apps/verify_credentials", s.verifyAppCredentials)
	e.POST("/api/v1/accounts", s.createAccount)
	e.POST("/api/v1/emails/confirmations", s.createEmailConfirmation)
	e.GET("/api/v1/emails/check_confirmation", s.checkEmailConfirmation)
	e.GET("/api/v1/accounts/verify_credentials", s.verifyCredentials)
	e.PATCH("/api/v1/accounts/update_credentials", s.updateCredentials)
	e.GET("/api/v1/accounts/search", s.searchAccounts)
	e.GET("/api/v1/accounts/lookup", s.lookupAccount)
	e.GET("/api/v1/accounts/relationships", s.accountRelationships)
	e.GET("/api/v1/accounts/familiar_followers", s.familiarFollowers)
	e.GET("/api/v1/accounts/:id/lists", s.accountLists)
	e.GET("/api/v1/accounts/:id/identity_proofs", s.identityProofs)
	e.GET("/api/v1/accounts/:id/featured_tags", s.accountFeaturedTags)
	e.GET("/api/v1/accounts/:id", s.getAccount)
	e.GET("/api/v1/accounts/:id/statuses", s.accountStatuses)
	e.GET("/api/v1/accounts/:id/followers", s.accountFollowers)
	e.GET("/api/v1/accounts/:id/following", s.accountFollowing)
	e.POST("/api/v1/accounts/:id/follow", s.followAccount)
	e.POST("/api/v1/accounts/:id/unfollow", s.unfollowAccount)
	e.POST("/api/v1/accounts/:id/remove_from_followers", s.removeFromFollowers)
	e.POST("/api/v1/accounts/:id/block", s.blockAccount)
	e.POST("/api/v1/accounts/:id/unblock", s.unblockAccount)
	e.POST("/api/v1/accounts/:id/mute", s.muteAccount)
	e.POST("/api/v1/accounts/:id/unmute", s.unmuteAccount)
	e.POST("/api/v1/accounts/:id/note", s.noteAccount)
	e.POST("/api/v1/accounts/:id/pin", s.pinAccount)
	e.POST("/api/v1/accounts/:id/unpin", s.unpinAccount)

	e.GET("/api/v1/statuses/:id", s.getStatus)
	e.DELETE("/api/v1/statuses/:id", s.deleteStatus)
	e.PUT("/api/v1/statuses/:id", s.updateStatus)
	e.PATCH("/api/v1/statuses/:id", s.updateStatus)
	e.POST("/api/v1/statuses", s.createStatus)
	e.GET("/api/v1/statuses/:id/context", s.statusContext)
	e.GET("/api/v1/statuses/:id/source", s.statusSource)
	e.GET("/api/v1/statuses/:id/history", s.statusHistory)
	e.GET("/api/v1/statuses/:id/favourited_by", s.favouritedBy)
	e.GET("/api/v1/statuses/:id/reblogged_by", s.rebloggedBy)
	e.POST("/api/v1/statuses/:id/favourite", s.favouriteStatus)
	e.POST("/api/v1/statuses/:id/unfavourite", s.unfavouriteStatus)
	e.POST("/api/v1/statuses/:id/bookmark", s.bookmarkStatus)
	e.POST("/api/v1/statuses/:id/unbookmark", s.unbookmarkStatus)
	e.POST("/api/v1/statuses/:id/pin", s.pinStatus)
	e.POST("/api/v1/statuses/:id/unpin", s.unpinStatus)
	e.POST("/api/v1/statuses/:id/mute", s.muteStatus)
	e.POST("/api/v1/statuses/:id/unmute", s.unmuteStatus)
	e.POST("/api/v1/statuses/:id/translate", s.translateStatus)
	e.POST("/api/v1/statuses/:id/reblog", s.reblogStatus)
	e.POST("/api/v1/statuses/:id/unreblog", s.unreblogStatus)
	e.POST("/api/v1/polls", s.createPoll)
	e.GET("/api/v1/polls/:id", s.getPoll)
	e.POST("/api/v1/polls/:id/votes", s.votePoll)
	e.GET("/api/v1/push/subscription", s.showPushSubscription)
	e.POST("/api/v1/push/subscription", s.createPushSubscription)
	e.PUT("/api/v1/push/subscription", s.updatePushSubscription)
	e.PATCH("/api/v1/push/subscription", s.updatePushSubscription)
	e.DELETE("/api/v1/push/subscription", s.deletePushSubscription)
	e.POST("/api/v1/media", s.createMedia)
	e.POST("/api/v2/media", s.createMediaV2)
	e.GET("/api/v1/media/:id", s.showMedia)
	e.PUT("/api/v1/media/:id", s.updateMedia)
	e.PATCH("/api/v1/media/:id", s.updateMedia)

	e.GET("/api/v1/timelines/public", s.publicTimeline)
	e.GET("/api/v1/timelines/home", s.homeTimeline)
	e.GET("/api/v1/timelines/tag/:tag", s.tagTimeline)
	e.GET("/api/v1/timelines/list/:id", s.listTimeline)
	e.GET("/api/v1/tags/:name", s.showTag)
	e.POST("/api/v1/tags/:name/follow", s.followTag)
	e.POST("/api/v1/tags/:name/unfollow", s.unfollowTag)
	e.GET("/api/v1/followed_tags", s.followedTags)
	e.GET("/api/v1/featured_tags", s.featuredTags)
	e.POST("/api/v1/featured_tags", s.createFeaturedTag)
	e.DELETE("/api/v1/featured_tags/:id", s.deleteFeaturedTag)
	e.GET("/api/v1/featured_tags/suggestions", s.featuredTagSuggestions)
	e.GET("/api/v1/favourites", s.favourites)
	e.GET("/api/v1/bookmarks", s.bookmarks)
	e.GET("/api/v1/filters", s.v1Filters)
	e.POST("/api/v1/filters", s.createV1Filter)
	e.GET("/api/v1/filters/:id", s.showV1Filter)
	e.PUT("/api/v1/filters/:id", s.updateV1Filter)
	e.PATCH("/api/v1/filters/:id", s.updateV1Filter)
	e.DELETE("/api/v1/filters/:id", s.deleteV1Filter)
	e.GET("/api/v2/search", s.search)
	e.GET("/api/v2/filters", s.filters)
	e.POST("/api/v2/filters", s.createFilter)
	e.GET("/api/v2/filters/:id", s.showFilter)
	e.PUT("/api/v2/filters/:id", s.updateFilter)
	e.PATCH("/api/v2/filters/:id", s.updateFilter)
	e.DELETE("/api/v2/filters/:id", s.deleteFilter)
	e.GET("/api/v2/filters/:filter_id/keywords", s.filterKeywords)
	e.POST("/api/v2/filters/:filter_id/keywords", s.createFilterKeyword)
	e.GET("/api/v2/filters/keywords/:id", s.showFilterKeyword)
	e.PUT("/api/v2/filters/keywords/:id", s.updateFilterKeyword)
	e.PATCH("/api/v2/filters/keywords/:id", s.updateFilterKeyword)
	e.DELETE("/api/v2/filters/keywords/:id", s.deleteFilterKeyword)
	e.GET("/api/v2/filters/:filter_id/statuses", s.filterStatuses)
	e.POST("/api/v2/filters/:filter_id/statuses", s.createFilterStatus)
	e.GET("/api/v2/filters/statuses/:id", s.showFilterStatus)
	e.DELETE("/api/v2/filters/statuses/:id", s.deleteFilterStatus)
	e.GET("/api/v1/lists", s.lists)
	e.POST("/api/v1/lists", s.createList)
	e.GET("/api/v1/lists/:id", s.showList)
	e.PUT("/api/v1/lists/:id", s.updateList)
	e.PATCH("/api/v1/lists/:id", s.updateList)
	e.DELETE("/api/v1/lists/:id", s.deleteList)
	e.GET("/api/v1/lists/:id/accounts", s.listAccounts)
	e.POST("/api/v1/lists/:id/accounts", s.addListAccounts)
	e.DELETE("/api/v1/lists/:id/accounts", s.removeListAccounts)
	e.GET("/api/web/embeds/:id", s.webEmbed)
	e.POST("/api/web/push_subscriptions", s.apiWebCSRF(s.createWebPushSubscription))
	e.PUT("/api/web/push_subscriptions/:id", s.apiWebCSRF(s.updateWebPushSubscription))
	e.PUT("/api/web/push_subscriptions/:id/update", s.apiWebCSRF(s.updateWebPushSubscription))
	e.PUT("/api/web/settings", s.apiWebCSRF(s.updateWebSettings))
	e.PATCH("/api/web/settings", s.apiWebCSRF(s.updateWebSettings))

	e.GET("/instance-stats/:domain.:format", s.instanceStatsPage)
	e.GET("/instance-stats/:domain", s.instanceStatsPage)
	e.HEAD("/instance-stats/:domain.:format", s.instanceStatsPage)
	e.HEAD("/instance-stats/:domain", s.instanceStatsPage)
	e.GET("/about.:format", s.about)
	e.GET("/about", s.about)
	s.registerWebAppRoutes(
		"/",
		"/home",
		"/getting-started",
		"/keyboard-shortcuts",
		"/public",
		"/public/local",
		"/public/remote",
		"/conversations",
		"/lists",
		"/lists/*",
		"/notifications",
		"/favourites",
		"/bookmarks",
		"/pinned",
		"/start",
		"/directory",
		"/publish",
		"/statuses/new",
		"/follow_requests",
		"/blocks",
		"/domain_blocks",
		"/mutes",
		"/followed_tags",
		"/statuses/*",
	)
	e.GET("/statuses_cleanup.:format", s.statusesCleanupPage)
	e.GET("/statuses_cleanup", s.statusesCleanupPage)
	e.PUT("/statuses_cleanup.:format", s.updateStatusesCleanup)
	e.PUT("/statuses_cleanup", s.updateStatusesCleanup)
	e.PATCH("/statuses_cleanup.:format", s.updateStatusesCleanup)
	e.PATCH("/statuses_cleanup", s.updateStatusesCleanup)
	e.POST("/statuses_cleanup.:format", s.notFound)
	e.POST("/statuses_cleanup", s.notFound)
	e.GET("/filters.:format", s.webFiltersPage)
	e.GET("/filters", s.webFiltersPage)
	e.POST("/filters.:format", s.createWebFilter)
	e.POST("/filters", s.createWebFilter)
	e.GET("/filters/new.:format", s.newWebFilter)
	e.GET("/filters/new", s.newWebFilter)
	e.GET("/filters/:id/edit.:format", s.editWebFilter)
	e.GET("/filters/:id/edit", s.editWebFilter)
	e.PUT("/filters/:id.:format", optionalFormatPathParam("id", s.updateWebFilter))
	e.PUT("/filters/:id", s.updateWebFilter)
	e.PATCH("/filters/:id.:format", optionalFormatPathParam("id", s.updateWebFilter))
	e.PATCH("/filters/:id", s.updateWebFilter)
	e.DELETE("/filters/:id.:format", optionalFormatPathParam("id", s.destroyWebFilter))
	e.DELETE("/filters/:id", s.destroyWebFilter)
	e.POST("/filters/:id.:format", optionalFormatPathParam("id", s.notFound))
	e.POST("/filters/:id", s.notFound)
	e.GET("/filters/:filter_id/statuses.:format", s.webFilterStatusesPage)
	e.GET("/filters/:filter_id/statuses", s.webFilterStatusesPage)
	e.POST("/filters/:filter_id/statuses/batch.:format", s.batchWebFilterStatuses)
	e.POST("/filters/:filter_id/statuses/batch", s.batchWebFilterStatuses)
	e.GET("/sidekiq", s.sidekiqPage)
	e.GET("/sidekiq/stats", s.sidekiqStats)
	e.GET("/sidekiq/*", s.sidekiqPage)
	e.GET("/asynq", s.sidekiqPage)
	e.GET("/asynq/stats", s.sidekiqStats)
	e.POST("/asynq/tasks/retry", s.retryAsynqTask)
	e.POST("/asynq/tasks/retry_all", s.retryAllAsynqTasks)
	e.POST("/asynq/tasks/delete_all", s.deleteAllArchivedAsynqTasks)
	e.GET("/asynq/*", s.sidekiqPage)
	e.GET("/pghero", s.pgHeroPage)
	e.GET("/pghero/*", s.pgHeroPage)
	e.GET("/admin", redirectTo("/admin/dashboard", http.StatusFound))
	e.GET("/admin.json", redirectTo("/admin/dashboard", http.StatusFound))
	e.GET("/admin.:format", redirectTo("/admin/dashboard", http.StatusFound))
	e.GET("/admin/dashboard.:format", s.adminDashboardPage)
	e.GET("/admin/dashboard", s.adminDashboardPage)
	e.GET("/admin/settings", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings.json", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings.:format", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings/edit", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings/edit.json", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings/edit.:format", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings/branding.:format", s.adminSettingsBrandingPage)
	e.GET("/admin/settings/branding", s.adminSettingsBrandingPage)
	e.PUT("/admin/settings/branding.:format", s.updateAdminSettingsBranding)
	e.PUT("/admin/settings/branding", s.updateAdminSettingsBranding)
	e.PATCH("/admin/settings/branding.:format", s.updateAdminSettingsBranding)
	e.PATCH("/admin/settings/branding", s.updateAdminSettingsBranding)
	e.POST("/admin/settings/branding.:format", s.notFound)
	e.POST("/admin/settings/branding", s.notFound)
	e.GET("/admin/settings/registrations.:format", s.adminSettingsRegistrationsPage)
	e.GET("/admin/settings/registrations", s.adminSettingsRegistrationsPage)
	e.PUT("/admin/settings/registrations.:format", s.updateAdminSettingsRegistrations)
	e.PUT("/admin/settings/registrations", s.updateAdminSettingsRegistrations)
	e.PATCH("/admin/settings/registrations.:format", s.updateAdminSettingsRegistrations)
	e.PATCH("/admin/settings/registrations", s.updateAdminSettingsRegistrations)
	e.POST("/admin/settings/registrations.:format", s.notFound)
	e.POST("/admin/settings/registrations", s.notFound)
	e.GET("/admin/settings/discovery.:format", s.adminSettingsDiscoveryPage)
	e.GET("/admin/settings/discovery", s.adminSettingsDiscoveryPage)
	e.PUT("/admin/settings/discovery.:format", s.updateAdminSettingsDiscovery)
	e.PUT("/admin/settings/discovery", s.updateAdminSettingsDiscovery)
	e.PATCH("/admin/settings/discovery.:format", s.updateAdminSettingsDiscovery)
	e.PATCH("/admin/settings/discovery", s.updateAdminSettingsDiscovery)
	e.POST("/admin/settings/discovery.:format", s.notFound)
	e.POST("/admin/settings/discovery", s.notFound)
	e.GET("/admin/settings/about.:format", s.adminSettingsAboutPage)
	e.GET("/admin/settings/about", s.adminSettingsAboutPage)
	e.PUT("/admin/settings/about.:format", s.updateAdminSettingsAbout)
	e.PUT("/admin/settings/about", s.updateAdminSettingsAbout)
	e.PATCH("/admin/settings/about.:format", s.updateAdminSettingsAbout)
	e.PATCH("/admin/settings/about", s.updateAdminSettingsAbout)
	e.POST("/admin/settings/about.:format", s.notFound)
	e.POST("/admin/settings/about", s.notFound)
	e.GET("/admin/settings/appearance.:format", s.adminSettingsAppearancePage)
	e.GET("/admin/settings/appearance", s.adminSettingsAppearancePage)
	e.PUT("/admin/settings/appearance.:format", s.updateAdminSettingsAppearance)
	e.PUT("/admin/settings/appearance", s.updateAdminSettingsAppearance)
	e.PATCH("/admin/settings/appearance.:format", s.updateAdminSettingsAppearance)
	e.PATCH("/admin/settings/appearance", s.updateAdminSettingsAppearance)
	e.POST("/admin/settings/appearance.:format", s.notFound)
	e.POST("/admin/settings/appearance", s.notFound)
	e.GET("/admin/settings/content_retention.:format", s.adminSettingsContentRetentionPage)
	e.GET("/admin/settings/content_retention", s.adminSettingsContentRetentionPage)
	e.PUT("/admin/settings/content_retention.:format", s.updateAdminSettingsContentRetention)
	e.PUT("/admin/settings/content_retention", s.updateAdminSettingsContentRetention)
	e.PATCH("/admin/settings/content_retention.:format", s.updateAdminSettingsContentRetention)
	e.PATCH("/admin/settings/content_retention", s.updateAdminSettingsContentRetention)
	e.POST("/admin/settings/content_retention.:format", s.notFound)
	e.POST("/admin/settings/content_retention", s.notFound)
	e.DELETE("/admin/site_uploads/:id.:format", optionalFormatPathParam("id", s.destroyAdminSiteUpload))
	e.DELETE("/admin/site_uploads/:id", s.destroyAdminSiteUpload)
	e.POST("/admin/site_uploads/:id", s.notFound)
	e.GET("/admin/invites.:format", s.adminInvitesPage)
	e.GET("/admin/invites", s.adminInvitesPage)
	e.POST("/admin/invites.:format", s.createAdminInvite)
	e.POST("/admin/invites", s.createAdminInvite)
	e.POST("/admin/invites/deactivate_all.:format", s.deactivateAllAdminInvites)
	e.POST("/admin/invites/deactivate_all", s.deactivateAllAdminInvites)
	e.DELETE("/admin/invites/:id.:format", optionalFormatPathParam("id", s.destroyAdminInvite))
	e.DELETE("/admin/invites/:id", s.destroyAdminInvite)
	e.POST("/admin/invites/:id", s.notFound)
	e.GET("/admin/rules.:format", s.adminRulesPage)
	e.GET("/admin/rules", s.adminRulesPage)
	e.POST("/admin/rules.:format", s.createAdminRule)
	e.POST("/admin/rules", s.createAdminRule)
	e.GET("/admin/rules/:id/edit.:format", optionalFormatPathParam("id", s.editAdminRulePage))
	e.GET("/admin/rules/:id/edit", s.editAdminRulePage)
	e.PUT("/admin/rules/:id.:format", optionalFormatPathParam("id", s.updateAdminRule))
	e.PUT("/admin/rules/:id", s.updateAdminRule)
	e.PATCH("/admin/rules/:id.:format", optionalFormatPathParam("id", s.updateAdminRule))
	e.PATCH("/admin/rules/:id", s.updateAdminRule)
	e.DELETE("/admin/rules/:id.:format", optionalFormatPathParam("id", s.destroyAdminRule))
	e.DELETE("/admin/rules/:id", s.destroyAdminRule)
	e.POST("/admin/rules/:id", s.notFound)
	e.GET("/admin/roles.:format", s.adminRolesPage)
	e.GET("/admin/roles", s.adminRolesPage)
	e.GET("/admin/roles/new.:format", s.newAdminRolePage)
	e.GET("/admin/roles/new", s.newAdminRolePage)
	e.POST("/admin/roles.:format", s.createAdminRole)
	e.POST("/admin/roles", s.createAdminRole)
	e.GET("/admin/roles/:id/edit.:format", optionalFormatPathParam("id", s.editAdminRolePage))
	e.GET("/admin/roles/:id/edit", s.editAdminRolePage)
	e.PUT("/admin/roles/:id.:format", optionalFormatPathParam("id", s.updateAdminRole))
	e.PUT("/admin/roles/:id", s.updateAdminRole)
	e.PATCH("/admin/roles/:id.:format", optionalFormatPathParam("id", s.updateAdminRole))
	e.PATCH("/admin/roles/:id", s.updateAdminRole)
	e.DELETE("/admin/roles/:id.:format", optionalFormatPathParam("id", s.destroyAdminRole))
	e.DELETE("/admin/roles/:id", s.destroyAdminRole)
	e.POST("/admin/roles/:id", s.notFound)
	e.GET("/admin/users/:user_id/role.:format", s.adminUserRolePage)
	e.GET("/admin/users/:user_id/role", s.adminUserRolePage)
	e.PUT("/admin/users/:user_id/role.:format", s.updateAdminUserRole)
	e.PUT("/admin/users/:user_id/role", s.updateAdminUserRole)
	e.PATCH("/admin/users/:user_id/role.:format", s.updateAdminUserRole)
	e.PATCH("/admin/users/:user_id/role", s.updateAdminUserRole)
	e.POST("/admin/users/:user_id/role", s.notFound)
	e.DELETE("/admin/users/:user_id/two_factor_authentication.:format", s.destroyAdminUserTwoFactor)
	e.DELETE("/admin/users/:user_id/two_factor_authentication", s.destroyAdminUserTwoFactor)
	e.POST("/admin/users/:user_id/two_factor_authentication", s.notFound)
	e.GET("/admin/software_updates.:format", s.adminSoftwareUpdatesPage)
	e.GET("/admin/software_updates", s.adminSoftwareUpdatesPage)
	e.GET("/admin/tags/:id.:format", optionalFormatPathParam("id", s.adminTagPage))
	e.GET("/admin/tags/:id", s.adminTagPage)
	e.PATCH("/admin/tags/:id.:format", optionalFormatPathParam("id", s.updateAdminTagWeb))
	e.PATCH("/admin/tags/:id", s.updateAdminTagWeb)
	e.PUT("/admin/tags/:id.:format", optionalFormatPathParam("id", s.updateAdminTagWeb))
	e.PUT("/admin/tags/:id", s.updateAdminTagWeb)
	e.POST("/admin/tags/:id", s.notFound)
	e.GET("/admin/trends/tags.:format", s.adminTrendsTagsPage)
	e.GET("/admin/trends/tags", s.adminTrendsTagsPage)
	e.POST("/admin/trends/tags/batch.:format", s.batchAdminTrendsTags)
	e.POST("/admin/trends/tags/batch", s.batchAdminTrendsTags)
	e.GET("/admin/trends/statuses.:format", s.adminTrendsStatusesPage)
	e.GET("/admin/trends/statuses", s.adminTrendsStatusesPage)
	e.POST("/admin/trends/statuses/batch.:format", s.batchAdminTrendsStatuses)
	e.POST("/admin/trends/statuses/batch", s.batchAdminTrendsStatuses)
	e.GET("/admin/trends/links.:format", s.adminTrendsLinksPage)
	e.GET("/admin/trends/links", s.adminTrendsLinksPage)
	e.POST("/admin/trends/links/batch.:format", s.batchAdminTrendsLinks)
	e.POST("/admin/trends/links/batch", s.batchAdminTrendsLinks)
	e.GET("/admin/trends/links/publishers.:format", s.adminTrendsLinkPublishersPage)
	e.GET("/admin/trends/links/publishers", s.adminTrendsLinkPublishersPage)
	e.POST("/admin/trends/links/publishers/batch.:format", s.batchAdminTrendsLinkPublishers)
	e.POST("/admin/trends/links/publishers/batch", s.batchAdminTrendsLinkPublishers)
	e.GET("/admin/follow_recommendations.:format", s.adminFollowRecommendationsPage)
	e.GET("/admin/follow_recommendations", s.adminFollowRecommendationsPage)
	e.PATCH("/admin/follow_recommendations.:format", s.updateAdminFollowRecommendations)
	e.PATCH("/admin/follow_recommendations", s.updateAdminFollowRecommendations)
	e.PUT("/admin/follow_recommendations.:format", s.updateAdminFollowRecommendations)
	e.PUT("/admin/follow_recommendations", s.updateAdminFollowRecommendations)
	e.POST("/admin/follow_recommendations.:format", s.notFound)
	e.POST("/admin/follow_recommendations", s.notFound)
	e.GET("/admin/warning_presets.:format", s.adminWarningPresetsPage)
	e.GET("/admin/warning_presets", s.adminWarningPresetsPage)
	e.POST("/admin/warning_presets.:format", s.createAdminWarningPreset)
	e.POST("/admin/warning_presets", s.createAdminWarningPreset)
	e.GET("/admin/warning_presets/:id/edit.:format", optionalFormatPathParam("id", s.editAdminWarningPresetPage))
	e.GET("/admin/warning_presets/:id/edit", s.editAdminWarningPresetPage)
	e.PUT("/admin/warning_presets/:id.:format", optionalFormatPathParam("id", s.updateAdminWarningPreset))
	e.PUT("/admin/warning_presets/:id", s.updateAdminWarningPreset)
	e.PATCH("/admin/warning_presets/:id.:format", optionalFormatPathParam("id", s.updateAdminWarningPreset))
	e.PATCH("/admin/warning_presets/:id", s.updateAdminWarningPreset)
	e.DELETE("/admin/warning_presets/:id.:format", optionalFormatPathParam("id", s.destroyAdminWarningPreset))
	e.DELETE("/admin/warning_presets/:id", s.destroyAdminWarningPreset)
	e.POST("/admin/warning_presets/:id", s.notFound)
	e.GET("/admin/announcements.:format", s.adminAnnouncementsPage)
	e.GET("/admin/announcements", s.adminAnnouncementsPage)
	e.GET("/admin/announcements/new.:format", s.newAdminAnnouncementPage)
	e.GET("/admin/announcements/new", s.newAdminAnnouncementPage)
	e.POST("/admin/announcements.:format", s.createAdminAnnouncement)
	e.POST("/admin/announcements", s.createAdminAnnouncement)
	e.GET("/admin/announcements/:id/edit.:format", optionalFormatPathParam("id", s.editAdminAnnouncementPage))
	e.GET("/admin/announcements/:id/edit", s.editAdminAnnouncementPage)
	e.PUT("/admin/announcements/:id.:format", optionalFormatPathParam("id", s.updateAdminAnnouncement))
	e.PUT("/admin/announcements/:id", s.updateAdminAnnouncement)
	e.PATCH("/admin/announcements/:id.:format", optionalFormatPathParam("id", s.updateAdminAnnouncement))
	e.PATCH("/admin/announcements/:id", s.updateAdminAnnouncement)
	e.POST("/admin/announcements/:id/publish.:format", s.publishAdminAnnouncement)
	e.POST("/admin/announcements/:id/publish", s.publishAdminAnnouncement)
	e.POST("/admin/announcements/:id/unpublish.:format", s.unpublishAdminAnnouncement)
	e.POST("/admin/announcements/:id/unpublish", s.unpublishAdminAnnouncement)
	e.DELETE("/admin/announcements/:id.:format", optionalFormatPathParam("id", s.destroyAdminAnnouncement))
	e.DELETE("/admin/announcements/:id", s.destroyAdminAnnouncement)
	e.POST("/admin/announcements/:id", s.notFound)
	e.GET("/admin/relays.:format", s.adminRelaysPage)
	e.GET("/admin/relays", s.adminRelaysPage)
	e.GET("/admin/relays/new.:format", s.newAdminRelayPage)
	e.GET("/admin/relays/new", s.newAdminRelayPage)
	e.POST("/admin/relays.:format", s.createAdminRelay)
	e.POST("/admin/relays", s.createAdminRelay)
	e.POST("/admin/relays/:id/enable.:format", s.enableAdminRelay)
	e.POST("/admin/relays/:id/enable", s.enableAdminRelay)
	e.POST("/admin/relays/:id/disable.:format", s.disableAdminRelay)
	e.POST("/admin/relays/:id/disable", s.disableAdminRelay)
	e.DELETE("/admin/relays/:id.:format", optionalFormatPathParam("id", s.destroyAdminRelay))
	e.DELETE("/admin/relays/:id", s.destroyAdminRelay)
	e.POST("/admin/relays/:id", s.notFound)
	e.GET("/admin/webhooks.:format", s.adminWebhooksPage)
	e.GET("/admin/webhooks", s.adminWebhooksPage)
	e.GET("/admin/webhooks/new.:format", s.newAdminWebhookPage)
	e.GET("/admin/webhooks/new", s.newAdminWebhookPage)
	e.POST("/admin/webhooks.:format", s.createAdminWebhook)
	e.POST("/admin/webhooks", s.createAdminWebhook)
	e.GET("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.showAdminWebhookPage))
	e.GET("/admin/webhooks/:id", s.showAdminWebhookPage)
	e.GET("/admin/webhooks/:id/edit.:format", optionalFormatPathParam("id", s.editAdminWebhookPage))
	e.GET("/admin/webhooks/:id/edit", s.editAdminWebhookPage)
	e.PUT("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.updateAdminWebhook))
	e.PUT("/admin/webhooks/:id", s.updateAdminWebhook)
	e.PATCH("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.updateAdminWebhook))
	e.PATCH("/admin/webhooks/:id", s.updateAdminWebhook)
	e.POST("/admin/webhooks/:id/enable.:format", s.enableAdminWebhook)
	e.POST("/admin/webhooks/:id/enable", s.enableAdminWebhook)
	e.POST("/admin/webhooks/:id/disable.:format", s.disableAdminWebhook)
	e.POST("/admin/webhooks/:id/disable", s.disableAdminWebhook)
	e.POST("/admin/webhooks/:webhook_id/secret/rotate.:format", s.rotateAdminWebhookSecret)
	e.POST("/admin/webhooks/:webhook_id/secret/rotate", s.rotateAdminWebhookSecret)
	e.DELETE("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.destroyAdminWebhook))
	e.DELETE("/admin/webhooks/:id", s.destroyAdminWebhook)
	e.POST("/admin/webhooks/:id", s.notFound)
	e.GET("/admin/action_logs.:format", s.adminActionLogsPage)
	e.GET("/admin/action_logs", s.adminActionLogsPage)
	e.GET("/admin/accounts.:format", s.adminAccountsPage)
	e.GET("/admin/accounts", s.adminAccountsPage)
	e.POST("/admin/accounts/batch.:format", s.batchAdminAccountsWeb)
	e.POST("/admin/accounts/batch", s.batchAdminAccountsWeb)
	e.GET("/admin/accounts/:id.:format", optionalFormatPathParam("id", s.adminAccountPage))
	e.GET("/admin/accounts/:id", s.adminAccountPage)
	e.DELETE("/admin/accounts/:id.:format", optionalFormatPathParam("id", s.destroyAdminAccountWeb))
	e.DELETE("/admin/accounts/:id", s.destroyAdminAccountWeb)
	e.POST("/admin/accounts/:id", s.notFound)
	e.POST("/admin/accounts/:id/enable.:format", s.enableAdminAccountWeb)
	e.POST("/admin/accounts/:id/enable", s.enableAdminAccountWeb)
	e.POST("/admin/accounts/:id/unsensitive.:format", s.unsensitiveAdminAccountWeb)
	e.POST("/admin/accounts/:id/unsensitive", s.unsensitiveAdminAccountWeb)
	e.POST("/admin/accounts/:id/unsilence.:format", s.unsilenceAdminAccountWeb)
	e.POST("/admin/accounts/:id/unsilence", s.unsilenceAdminAccountWeb)
	e.POST("/admin/accounts/:id/unsuspend.:format", s.unsuspendAdminAccountWeb)
	e.POST("/admin/accounts/:id/unsuspend", s.unsuspendAdminAccountWeb)
	e.POST("/admin/accounts/:id/redownload.:format", s.redownloadAdminAccountWeb)
	e.POST("/admin/accounts/:id/redownload", s.redownloadAdminAccountWeb)
	e.POST("/admin/accounts/:id/remove_avatar.:format", s.removeAvatarAdminAccountWeb)
	e.POST("/admin/accounts/:id/remove_avatar", s.removeAvatarAdminAccountWeb)
	e.POST("/admin/accounts/:id/remove_header.:format", s.removeHeaderAdminAccountWeb)
	e.POST("/admin/accounts/:id/remove_header", s.removeHeaderAdminAccountWeb)
	e.POST("/admin/accounts/:id/memorialize.:format", s.memorializeAdminAccountWeb)
	e.POST("/admin/accounts/:id/memorialize", s.memorializeAdminAccountWeb)
	e.POST("/admin/accounts/:id/approve.:format", s.approveAdminAccountWeb)
	e.POST("/admin/accounts/:id/approve", s.approveAdminAccountWeb)
	e.POST("/admin/accounts/:id/reject.:format", s.rejectAdminAccountWeb)
	e.POST("/admin/accounts/:id/reject", s.rejectAdminAccountWeb)
	e.POST("/admin/accounts/:id/unblock_email.:format", s.unblockEmailAdminAccountWeb)
	e.POST("/admin/accounts/:id/unblock_email", s.unblockEmailAdminAccountWeb)
	e.GET("/admin/accounts/:account_id/action/new.:format", s.newAdminAccountActionPage)
	e.GET("/admin/accounts/:account_id/action/new", s.newAdminAccountActionPage)
	e.POST("/admin/accounts/:account_id/action.:format", s.createAdminAccountActionWeb)
	e.POST("/admin/accounts/:account_id/action", s.createAdminAccountActionWeb)
	e.GET("/admin/accounts/:account_id/statuses.:format", s.adminAccountStatusesPage)
	e.GET("/admin/accounts/:account_id/statuses", s.adminAccountStatusesPage)
	e.GET("/admin/accounts/:account_id/statuses/:id.:format", optionalFormatPathParam("id", s.adminAccountStatusPage))
	e.GET("/admin/accounts/:account_id/statuses/:id", s.adminAccountStatusPage)
	e.POST("/admin/accounts/:account_id/statuses/batch.:format", s.batchAdminAccountStatusesWeb)
	e.POST("/admin/accounts/:account_id/statuses/batch", s.batchAdminAccountStatusesWeb)
	e.GET("/admin/accounts/:account_id/relationships.:format", s.adminAccountRelationshipsPage)
	e.GET("/admin/accounts/:account_id/relationships", s.adminAccountRelationshipsPage)
	e.GET("/admin/accounts/:account_id/change_email.:format", s.adminAccountChangeEmailPage)
	e.GET("/admin/accounts/:account_id/change_email", s.adminAccountChangeEmailPage)
	e.PUT("/admin/accounts/:account_id/change_email.:format", s.updateAdminAccountChangeEmail)
	e.PUT("/admin/accounts/:account_id/change_email", s.updateAdminAccountChangeEmail)
	e.PATCH("/admin/accounts/:account_id/change_email.:format", s.updateAdminAccountChangeEmail)
	e.PATCH("/admin/accounts/:account_id/change_email", s.updateAdminAccountChangeEmail)
	e.POST("/admin/accounts/:account_id/change_email", s.notFound)
	e.POST("/admin/accounts/:account_id/reset.:format", s.resetAdminAccountPasswordWeb)
	e.POST("/admin/accounts/:account_id/reset", s.resetAdminAccountPasswordWeb)
	e.POST("/admin/accounts/:account_id/confirmation.:format", s.confirmAdminAccountWeb)
	e.POST("/admin/accounts/:account_id/confirmation", s.confirmAdminAccountWeb)
	e.POST("/admin/accounts/:account_id/confirmation/resend.:format", s.resendAdminAccountConfirmationWeb)
	e.POST("/admin/accounts/:account_id/confirmation/resend", s.resendAdminAccountConfirmationWeb)
	e.GET("/admin/disputes/appeals.:format", s.adminAppealsPage)
	e.GET("/admin/disputes/appeals", s.adminAppealsPage)
	e.POST("/admin/disputes/appeals/:id/approve.:format", s.approveAdminAppealWeb)
	e.POST("/admin/disputes/appeals/:id/approve", s.approveAdminAppealWeb)
	e.POST("/admin/disputes/appeals/:id/reject.:format", s.rejectAdminAppealWeb)
	e.POST("/admin/disputes/appeals/:id/reject", s.rejectAdminAppealWeb)
	e.GET("/admin/custom_emojis.:format", s.adminCustomEmojisPage)
	e.GET("/admin/custom_emojis", s.adminCustomEmojisPage)
	e.GET("/admin/custom_emojis/new.:format", s.newAdminCustomEmojiPage)
	e.GET("/admin/custom_emojis/new", s.newAdminCustomEmojiPage)
	e.POST("/admin/custom_emojis.:format", s.createAdminCustomEmojiWeb)
	e.POST("/admin/custom_emojis", s.createAdminCustomEmojiWeb)
	e.POST("/admin/custom_emojis/batch.:format", s.batchAdminCustomEmojisWeb)
	e.POST("/admin/custom_emojis/batch", s.batchAdminCustomEmojisWeb)
	e.GET("/admin/ip_blocks.:format", s.adminIPBlocksPage)
	e.GET("/admin/ip_blocks", s.adminIPBlocksPage)
	e.GET("/admin/ip_blocks/new.:format", s.newAdminIPBlockPage)
	e.GET("/admin/ip_blocks/new", s.newAdminIPBlockPage)
	e.POST("/admin/ip_blocks.:format", s.createAdminIPBlockWeb)
	e.POST("/admin/ip_blocks", s.createAdminIPBlockWeb)
	e.POST("/admin/ip_blocks/batch.:format", s.batchAdminIPBlocks)
	e.POST("/admin/ip_blocks/batch", s.batchAdminIPBlocks)
	e.GET("/admin/email_domain_blocks.:format", s.adminEmailDomainBlocksPage)
	e.GET("/admin/email_domain_blocks", s.adminEmailDomainBlocksPage)
	e.GET("/admin/email_domain_blocks/new.:format", s.newAdminEmailDomainBlockPage)
	e.GET("/admin/email_domain_blocks/new", s.newAdminEmailDomainBlockPage)
	e.POST("/admin/email_domain_blocks.:format", s.createAdminEmailDomainBlockWeb)
	e.POST("/admin/email_domain_blocks", s.createAdminEmailDomainBlockWeb)
	e.POST("/admin/email_domain_blocks/batch.:format", s.batchAdminEmailDomainBlocks)
	e.POST("/admin/email_domain_blocks/batch", s.batchAdminEmailDomainBlocks)
	e.GET("/admin/domain_allows/new.:format", s.newAdminDomainAllowPage)
	e.GET("/admin/domain_allows/new", s.newAdminDomainAllowPage)
	e.POST("/admin/domain_allows.:format", s.createAdminDomainAllowWeb)
	e.POST("/admin/domain_allows", s.createAdminDomainAllowWeb)
	e.DELETE("/admin/domain_allows/:id.:format", optionalFormatPathParam("id", s.destroyAdminDomainAllowWeb))
	e.DELETE("/admin/domain_allows/:id", s.destroyAdminDomainAllowWeb)
	e.POST("/admin/domain_allows/:id", s.notFound)
	e.GET("/admin/domain_blocks/new.:format", s.newAdminDomainBlockPage)
	e.GET("/admin/domain_blocks/new", s.newAdminDomainBlockPage)
	e.POST("/admin/domain_blocks.:format", s.createAdminDomainBlockWeb)
	e.POST("/admin/domain_blocks", s.createAdminDomainBlockWeb)
	e.POST("/admin/domain_blocks/batch.:format", s.batchAdminDomainBlocks)
	e.POST("/admin/domain_blocks/batch", s.batchAdminDomainBlocks)
	e.GET("/admin/domain_blocks/:id/edit.:format", optionalFormatPathParam("id", s.editAdminDomainBlockPage))
	e.GET("/admin/domain_blocks/:id/edit", s.editAdminDomainBlockPage)
	e.PUT("/admin/domain_blocks/:id.:format", optionalFormatPathParam("id", s.updateAdminDomainBlockWeb))
	e.PUT("/admin/domain_blocks/:id", s.updateAdminDomainBlockWeb)
	e.PATCH("/admin/domain_blocks/:id.:format", optionalFormatPathParam("id", s.updateAdminDomainBlockWeb))
	e.PATCH("/admin/domain_blocks/:id", s.updateAdminDomainBlockWeb)
	e.DELETE("/admin/domain_blocks/:id.:format", optionalFormatPathParam("id", s.destroyAdminDomainBlockWeb))
	e.DELETE("/admin/domain_blocks/:id", s.destroyAdminDomainBlockWeb)
	e.POST("/admin/domain_blocks/:id", s.notFound)
	e.GET("/admin/export_domain_allows/new.:format", s.newAdminExportDomainAllowsPage)
	e.GET("/admin/export_domain_allows/new", s.newAdminExportDomainAllowsPage)
	e.GET("/admin/export_domain_allows/export.csv", s.exportAdminDomainAllowsCSV)
	e.POST("/admin/export_domain_allows/import.:format", s.importAdminDomainAllowsCSV)
	e.POST("/admin/export_domain_allows/import", s.importAdminDomainAllowsCSV)
	e.GET("/admin/export_domain_blocks/new.:format", s.newAdminExportDomainBlocksPage)
	e.GET("/admin/export_domain_blocks/new", s.newAdminExportDomainBlocksPage)
	e.GET("/admin/export_domain_blocks/export.csv", s.exportAdminDomainBlocksCSV)
	e.POST("/admin/export_domain_blocks/import.:format", s.importAdminDomainBlocksCSV)
	e.POST("/admin/export_domain_blocks/import", s.importAdminDomainBlocksCSV)
	e.GET("/admin/instances.html", s.adminInstancesPage)
	e.GET("/admin/instances", s.adminInstancesPage)
	e.GET("/admin/instances/:id", s.showAdminInstancePage)
	e.DELETE("/admin/instances/:id", s.destroyAdminInstance)
	e.POST("/admin/instances/:id", s.notFound)
	e.POST("/admin/instances/:id/clear_delivery_errors.html", s.clearAdminInstanceDeliveryErrors)
	e.POST("/admin/instances/:id/clear_delivery_errors", s.clearAdminInstanceDeliveryErrors)
	e.POST("/admin/instances/:id/restart_delivery.html", s.restartAdminInstanceDelivery)
	e.POST("/admin/instances/:id/restart_delivery", s.restartAdminInstanceDelivery)
	e.POST("/admin/instances/:id/stop_delivery.html", s.stopAdminInstanceDelivery)
	e.POST("/admin/instances/:id/stop_delivery", s.stopAdminInstanceDelivery)
	e.GET("/admin/reports.:format", s.adminReportsPage)
	e.GET("/admin/reports", s.adminReportsPage)
	e.GET("/admin/reports/:id.:format", optionalFormatPathParam("id", s.adminReportPage))
	e.GET("/admin/reports/:id", s.adminReportPage)
	e.POST("/admin/reports/:id.:format", optionalFormatPathParam("id", s.notFound))
	e.POST("/admin/reports/:id", s.notFound)
	e.PUT("/admin/reports/:id.:format", optionalFormatPathParam("id", s.notFound))
	e.PUT("/admin/reports/:id", s.notFound)
	e.PATCH("/admin/reports/:id.:format", optionalFormatPathParam("id", s.notFound))
	e.PATCH("/admin/reports/:id", s.notFound)
	e.POST("/admin/reports/:id/assign_to_self.:format", s.assignAdminReportToSelfWeb)
	e.POST("/admin/reports/:id/assign_to_self", s.assignAdminReportToSelfWeb)
	e.POST("/admin/reports/:id/unassign.:format", s.unassignAdminReportWeb)
	e.POST("/admin/reports/:id/unassign", s.unassignAdminReportWeb)
	e.POST("/admin/reports/:id/reopen.:format", s.reopenAdminReportWeb)
	e.POST("/admin/reports/:id/reopen", s.reopenAdminReportWeb)
	e.POST("/admin/reports/:id/resolve.:format", s.resolveAdminReportWeb)
	e.POST("/admin/reports/:id/resolve", s.resolveAdminReportWeb)
	e.POST("/admin/reports/:report_id/actions/preview.:format", s.previewAdminReportActionWeb)
	e.POST("/admin/reports/:report_id/actions/preview", s.previewAdminReportActionWeb)
	e.POST("/admin/reports/:report_id/actions.:format", s.createAdminReportActionWeb)
	e.POST("/admin/reports/:report_id/actions", s.createAdminReportActionWeb)
	e.POST("/admin/account_moderation_notes.:format", s.createAdminAccountModerationNoteWeb)
	e.POST("/admin/account_moderation_notes", s.createAdminAccountModerationNoteWeb)
	e.DELETE("/admin/account_moderation_notes/:id.:format", optionalFormatPathParam("id", s.destroyAdminAccountModerationNoteWeb))
	e.DELETE("/admin/account_moderation_notes/:id", s.destroyAdminAccountModerationNoteWeb)
	e.POST("/admin/account_moderation_notes/:id", s.notFound)
	e.POST("/admin/report_notes.:format", s.createAdminReportNoteWeb)
	e.POST("/admin/report_notes", s.createAdminReportNoteWeb)
	e.DELETE("/admin/report_notes/:id.:format", optionalFormatPathParam("id", s.destroyAdminReportNoteWeb))
	e.DELETE("/admin/report_notes/:id", s.destroyAdminReportNoteWeb)
	e.POST("/admin/report_notes/:id", s.notFound)
	e.GET("/relationships.:format", s.relationshipsPage)
	e.GET("/relationships", s.relationshipsPage)
	e.PATCH("/relationships.:format", s.updateRelationshipsPage)
	e.PATCH("/relationships", s.updateRelationshipsPage)
	e.PUT("/relationships.:format", s.updateRelationshipsPage)
	e.PUT("/relationships", s.updateRelationshipsPage)
	e.POST("/relationships.:format", s.notFound)
	e.POST("/relationships", s.notFound)
	e.GET("/disputes/strikes.:format", s.disputeStrikesPage)
	e.GET("/disputes/strikes", s.disputeStrikesPage)
	e.GET("/disputes/strikes/:id.:format", optionalFormatPathParam("id", s.disputeStrikePage))
	e.GET("/disputes/strikes/:id", s.disputeStrikePage)
	e.POST("/disputes/strikes/:strike_id/appeal.:format", s.createDisputeAppeal)
	e.POST("/disputes/strikes/:strike_id/appeal", s.createDisputeAppeal)
	e.GET("/invites.:format", s.invitesPage)
	e.GET("/invites", s.invitesPage)
	e.POST("/invites.:format", s.createInvite)
	e.POST("/invites", s.createInvite)
	e.DELETE("/invites/:id.:format", optionalFormatPathParam("id", s.destroyInvite))
	e.DELETE("/invites/:id", s.destroyInvite)
	e.POST("/invites/:id.:format", s.notFound)
	e.POST("/invites/:id", s.notFound)
	e.GET("/share.:format", s.share)
	e.GET("/share", s.share)
	e.GET("/settings", redirectTo("/settings/profile", http.StatusMovedPermanently))
	e.GET("/settings.json", redirectTo("/settings/profile", http.StatusMovedPermanently))
	e.GET("/settings.:format", redirectTo("/settings/profile", http.StatusMovedPermanently))
	e.GET("/settings/profile.:format", s.settingsPage)
	e.GET("/settings/profile", s.settingsPage)
	e.PUT("/settings/profile.:format", s.updateSettingsProfile)
	e.PUT("/settings/profile", s.updateSettingsProfile)
	e.PATCH("/settings/profile.:format", s.updateSettingsProfile)
	e.PATCH("/settings/profile", s.updateSettingsProfile)
	e.POST("/settings/profile.:format", s.notFound)
	e.POST("/settings/profile", s.notFound)
	e.DELETE("/settings/profile/pictures/:id.:format", optionalFormatPathParam("id", s.destroySettingsProfilePicture))
	e.DELETE("/settings/profile/pictures/:id", s.destroySettingsProfilePicture)
	e.POST("/settings/profile/pictures/:id.:format", s.notFound)
	e.POST("/settings/profile/pictures/:id", s.notFound)
	e.GET("/settings/preferences", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))
	e.GET("/settings/preferences.json", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))
	e.GET("/settings/preferences.:format", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))
	e.GET("/settings/preferences/appearance.:format", s.settingsPage)
	e.GET("/settings/preferences/appearance", s.settingsPage)
	e.PUT("/settings/preferences/appearance.:format", s.updateSettingsPreferences)
	e.PUT("/settings/preferences/appearance", s.updateSettingsPreferences)
	e.PATCH("/settings/preferences/appearance.:format", s.updateSettingsPreferences)
	e.PATCH("/settings/preferences/appearance", s.updateSettingsPreferences)
	e.POST("/settings/preferences/appearance.:format", s.notFound)
	e.POST("/settings/preferences/appearance", s.notFound)
	e.GET("/settings/preferences/notifications.:format", s.settingsPage)
	e.GET("/settings/preferences/notifications", s.settingsPage)
	e.PUT("/settings/preferences/notifications.:format", s.updateSettingsPreferences)
	e.PUT("/settings/preferences/notifications", s.updateSettingsPreferences)
	e.PATCH("/settings/preferences/notifications.:format", s.updateSettingsPreferences)
	e.PATCH("/settings/preferences/notifications", s.updateSettingsPreferences)
	e.POST("/settings/preferences/notifications.:format", s.notFound)
	e.POST("/settings/preferences/notifications", s.notFound)
	e.GET("/settings/preferences/other.:format", s.settingsPage)
	e.GET("/settings/preferences/other", s.settingsPage)
	e.PUT("/settings/preferences/other.:format", s.updateSettingsPreferences)
	e.PUT("/settings/preferences/other", s.updateSettingsPreferences)
	e.PATCH("/settings/preferences/other.:format", s.updateSettingsPreferences)
	e.PATCH("/settings/preferences/other", s.updateSettingsPreferences)
	e.POST("/settings/preferences/other.:format", s.notFound)
	e.POST("/settings/preferences/other", s.notFound)
	e.GET("/settings/privacy.:format", s.settingsPage)
	e.GET("/settings/privacy", s.settingsPage)
	e.PUT("/settings/privacy.:format", s.updateSettingsPrivacy)
	e.PUT("/settings/privacy", s.updateSettingsPrivacy)
	e.PATCH("/settings/privacy.:format", s.updateSettingsPrivacy)
	e.PATCH("/settings/privacy", s.updateSettingsPrivacy)
	e.POST("/settings/privacy.:format", s.notFound)
	e.POST("/settings/privacy", s.notFound)
	e.GET("/settings/export.:format", s.settingsPage)
	e.GET("/settings/export", s.settingsPage)
	e.POST("/settings/export.:format", s.createBackup)
	e.POST("/settings/export", s.createBackup)
	e.GET("/settings/exports/follows.csv", s.exportFollowsCSV)
	e.GET("/settings/exports/blocks.csv", s.exportBlocksCSV)
	e.GET("/settings/exports/mutes.csv", s.exportMutesCSV)
	e.GET("/settings/exports/lists.csv", s.exportListsCSV)
	e.GET("/settings/exports/domain_blocks.csv", s.exportDomainBlocksCSV)
	e.GET("/settings/exports/bookmarks.csv", s.exportBookmarksCSV)
	e.GET("/settings/imports.:format", s.settingsImportsPage)
	e.GET("/settings/imports", s.settingsImportsPage)
	e.POST("/settings/imports.:format", s.createSettingsImport)
	e.POST("/settings/imports", s.createSettingsImport)
	e.GET("/settings/imports/:id.:format", optionalFormatPathParam("id", s.showSettingsImport))
	e.GET("/settings/imports/:id", s.showSettingsImport)
	e.DELETE("/settings/imports/:id.:format", optionalFormatPathParam("id", s.destroySettingsImport))
	e.DELETE("/settings/imports/:id", s.destroySettingsImport)
	e.POST("/settings/imports/:id.:format", s.notFound)
	e.POST("/settings/imports/:id", s.notFound)
	e.POST("/settings/imports/:id/confirm.:format", optionalFormatPathParam("id", s.confirmSettingsImport))
	e.POST("/settings/imports/:id/confirm", s.confirmSettingsImport)
	e.GET("/settings/imports/:id/failures.:format", optionalFormatPathParam("id", s.settingsImportFailuresCSV))
	e.GET("/settings/imports/:id/failures", s.settingsImportFailuresCSV)
	e.GET("/settings/imports/:id/failures.csv", s.settingsImportFailuresCSV)
	e.GET("/settings/applications.:format", s.settingsApplicationsPage)
	e.GET("/settings/applications", s.settingsApplicationsPage)
	e.POST("/settings/applications.:format", s.createSettingsApplication)
	e.POST("/settings/applications", s.createSettingsApplication)
	e.GET("/settings/applications/new.:format", s.newSettingsApplication)
	e.GET("/settings/applications/new", s.newSettingsApplication)
	e.GET("/settings/applications/:id.:format", optionalFormatPathParam("id", s.showSettingsApplication))
	e.GET("/settings/applications/:id", s.showSettingsApplication)
	e.PUT("/settings/applications/:id.:format", optionalFormatPathParam("id", s.updateSettingsApplication))
	e.PUT("/settings/applications/:id", s.updateSettingsApplication)
	e.PATCH("/settings/applications/:id.:format", optionalFormatPathParam("id", s.updateSettingsApplication))
	e.PATCH("/settings/applications/:id", s.updateSettingsApplication)
	e.DELETE("/settings/applications/:id.:format", optionalFormatPathParam("id", s.destroySettingsApplication))
	e.DELETE("/settings/applications/:id", s.destroySettingsApplication)
	e.POST("/settings/applications/:id.:format", s.notFound)
	e.POST("/settings/applications/:id", s.notFound)
	e.POST("/settings/applications/:id/regenerate.:format", optionalFormatPathParam("id", s.regenerateSettingsApplicationToken))
	e.POST("/settings/applications/:id/regenerate", s.regenerateSettingsApplicationToken)
	e.GET("/settings/delete.:format", s.settingsDeletePage)
	e.GET("/settings/delete", s.settingsDeletePage)
	e.DELETE("/settings/delete.:format", s.destroyOwnAccount)
	e.DELETE("/settings/delete", s.destroyOwnAccount)
	e.POST("/settings/delete.:format", s.notFound)
	e.POST("/settings/delete", s.notFound)
	e.GET("/settings/migration.:format", s.settingsMigrationPage)
	e.GET("/settings/migration", s.settingsMigrationPage)
	e.POST("/settings/migration.:format", s.createSettingsMigration)
	e.POST("/settings/migration", s.createSettingsMigration)
	e.GET("/settings/migration/redirect/new.:format", s.newSettingsMigrationRedirect)
	e.GET("/settings/migration/redirect/new", s.newSettingsMigrationRedirect)
	e.POST("/settings/migration/redirect.:format", s.createSettingsMigrationRedirect)
	e.POST("/settings/migration/redirect", s.createSettingsMigrationRedirect)
	e.DELETE("/settings/migration/redirect.:format", s.destroySettingsMigrationRedirect)
	e.DELETE("/settings/migration/redirect", s.destroySettingsMigrationRedirect)
	e.GET("/settings/verification.:format", s.settingsVerificationPage)
	e.GET("/settings/verification", s.settingsVerificationPage)
	e.GET("/settings/aliases.:format", s.settingsAliasesPage)
	e.GET("/settings/aliases", s.settingsAliasesPage)
	e.POST("/settings/aliases.:format", s.createSettingsAlias)
	e.POST("/settings/aliases", s.createSettingsAlias)
	e.DELETE("/settings/aliases/:id.:format", optionalFormatPathParam("id", s.destroySettingsAlias))
	e.DELETE("/settings/aliases/:id", s.destroySettingsAlias)
	e.POST("/settings/aliases/:id.:format", s.notFound)
	e.POST("/settings/aliases/:id", s.notFound)
	e.GET("/settings/featured_tags.:format", s.settingsFeaturedTagsPage)
	e.GET("/settings/featured_tags", s.settingsFeaturedTagsPage)
	e.POST("/settings/featured_tags.:format", s.createSettingsFeaturedTag)
	e.POST("/settings/featured_tags", s.createSettingsFeaturedTag)
	e.DELETE("/settings/featured_tags/:id.:format", optionalFormatPathParam("id", s.destroySettingsFeaturedTag))
	e.DELETE("/settings/featured_tags/:id", s.destroySettingsFeaturedTag)
	e.POST("/settings/featured_tags/:id.:format", s.notFound)
	e.POST("/settings/featured_tags/:id", s.notFound)
	e.GET("/settings/login_activities.:format", s.settingsLoginActivitiesPage)
	e.GET("/settings/login_activities", s.settingsLoginActivitiesPage)
	e.DELETE("/settings/sessions/:id.:format", optionalFormatPathParam("id", s.destroySettingsSession))
	e.DELETE("/settings/sessions/:id", s.destroySettingsSession)
	e.POST("/settings/sessions/:id.:format", s.notFound)
	e.POST("/settings/sessions/:id", s.notFound)
	e.GET("/settings/two_factor_authentication_methods.:format", s.settingsPage)
	e.GET("/settings/two_factor_authentication_methods", s.settingsPage)
	e.POST("/settings/two_factor_authentication_methods/disable.:format", s.disableSettingsTwoFactor)
	e.POST("/settings/two_factor_authentication_methods/disable", s.disableSettingsTwoFactor)
	e.GET("/settings/otp_authentication.:format", s.settingsOTPAuthenticationPage)
	e.GET("/settings/otp_authentication", s.settingsOTPAuthenticationPage)
	e.POST("/settings/otp_authentication.:format", s.createSettingsOTPAuthentication)
	e.POST("/settings/otp_authentication", s.createSettingsOTPAuthentication)
	e.GET("/settings/two_factor_authentication/confirmation/new.:format", s.newSettingsTwoFactorConfirmation)
	e.GET("/settings/two_factor_authentication/confirmation/new", s.newSettingsTwoFactorConfirmation)
	e.POST("/settings/two_factor_authentication/confirmation.:format", s.createSettingsTwoFactorConfirmation)
	e.POST("/settings/two_factor_authentication/confirmation", s.createSettingsTwoFactorConfirmation)
	e.POST("/settings/two_factor_authentication/recovery_codes.:format", s.createSettingsRecoveryCodes)
	e.POST("/settings/two_factor_authentication/recovery_codes", s.createSettingsRecoveryCodes)
	e.GET("/settings/security_keys.:format", s.settingsSecurityKeysPage)
	e.GET("/settings/security_keys", s.settingsSecurityKeysPage)
	e.GET("/settings/security_keys/new.:format", s.newSettingsSecurityKey)
	e.GET("/settings/security_keys/new", s.newSettingsSecurityKey)
	e.GET("/settings/security_keys/options.:format", s.settingsSecurityKeyOptions)
	e.GET("/settings/security_keys/options", s.settingsSecurityKeyOptions)
	e.POST("/settings/security_keys.:format", s.createSettingsSecurityKey)
	e.POST("/settings/security_keys", s.createSettingsSecurityKey)
	e.DELETE("/settings/security_keys/:id.:format", optionalFormatPathParam("id", s.destroySettingsSecurityKey))
	e.DELETE("/settings/security_keys/:id", s.destroySettingsSecurityKey)
	e.POST("/settings/security_keys/:id.:format", s.notFound)
	e.POST("/settings/security_keys/:id", s.notFound)
	e.GET("/terms", redirectTo("/privacy-policy", http.StatusMovedPermanently))
	e.GET("/terms.json", redirectTo("/privacy-policy", http.StatusMovedPermanently))
	e.GET("/terms.:format", redirectTo("/privacy-policy", http.StatusMovedPermanently))
	e.GET("/about/more", redirectTo("/about", http.StatusMovedPermanently))
	e.GET("/about/more.json", redirectTo("/about", http.StatusMovedPermanently))
	e.GET("/about/more.:format", redirectTo("/about", http.StatusMovedPermanently))
	s.registerWebAppRoutes(
		"/explore",
		"/explore/*",
		"/search",
		"/@:username/:id/reblogs",
		"/@:username/:id/favourites",
		"/statuses",
		"/deck",
		"/deck/*",
	)
	e.GET("/@:username", s.publicAccount)
	e.GET("/@:username.:format", s.publicAccount)
	e.GET("/@:username/with_replies", s.publicAccountWithReplies)
	e.GET("/@:username/with_replies.json", s.publicAccountWithReplies)
	e.GET("/@:username/with_replies.rss", s.publicAccountWithReplies)
	e.GET("/@:username/with_replies.:format", s.publicAccountWithReplies)
	e.GET("/@:username/media", s.publicAccountMedia)
	e.GET("/@:username/media.json", s.publicAccountMedia)
	e.GET("/@:username/media.rss", s.publicAccountMedia)
	e.GET("/@:username/media.:format", s.publicAccountMedia)
	e.GET("/@:username/tagged/:tag", s.publicAccountTagged)
	e.GET("/@:username/tagged/:tag.:format", s.publicAccountTagged)
	e.GET("/@:username/followers", s.publicAccountFollowers)
	e.GET("/@:username/followers.json", s.publicAccountFollowersJSON)
	e.GET("/@:username/followers.:format", s.publicAccountFollowers)
	e.GET("/@:username/following", s.publicAccountFollowing)
	e.GET("/@:username/following.json", s.publicAccountFollowingJSON)
	e.GET("/@:username/following.:format", s.publicAccountFollowing)
	e.GET("/@:username/:id/embed.:format", s.statusEmbed)
	e.GET("/@:username/:id/embed", s.statusEmbed)
	e.GET("/@:username/:id.:format", s.publicStatus)
	e.GET("/@:username/:id", s.publicStatus)
	e.GET("/@:username_with_domain/*", s.webApp)
	e.HEAD("/@:username_with_domain/*", s.webApp)
	e.GET("/web", s.webRedirect)
	e.GET("/web/*", s.webRedirect)
	e.GET("/:encoded_at", s.encodedAtRedirect)
	e.GET("/:encoded_at/*", s.encodedAtRedirect)

	e.POST("/", s.notFound)
	e.PUT("/", s.notFound)
	e.PATCH("/", s.notFound)
	e.DELETE("/", s.notFound)
	e.RouteNotFound("/*", s.notFound)
}

func (s *Server) health(c *echo.Context) error {
	return c.String(http.StatusOK, "OK")
}

func (s *Server) ready(c *echo.Context) error {
	if s == nil || s.db == nil {
		return c.String(http.StatusServiceUnavailable, "database unavailable")
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return c.String(http.StatusServiceUnavailable, "database unavailable")
	}
	if err := sqlDB.Ping(); err != nil {
		return c.String(http.StatusServiceUnavailable, "database unavailable")
	}
	if err := paondb.SchemaAvailable(s.db); err != nil {
		return c.String(http.StatusServiceUnavailable, "schema unavailable")
	}
	if err := RedisAvailable(c.Request().Context(), s.cfg); err != nil {
		return c.String(http.StatusServiceUnavailable, "redis unavailable")
	}
	if s.cfg.MeiliEnabled {
		if err := MeiliAvailable(c.Request().Context(), s.cfg); err != nil {
			return c.String(http.StatusServiceUnavailable, "meilisearch unavailable")
		}
	}
	if err := web.ValidatePublicAssets(s.cfg); err != nil {
		return c.String(http.StatusServiceUnavailable, "public assets unavailable")
	}
	if err := web.ValidateServerRenderedLocales(s.cfg); err != nil {
		return c.String(http.StatusServiceUnavailable, "server-rendered locales unavailable")
	}
	return c.String(http.StatusOK, "OK")
}

func (s *Server) webApp(c *echo.Context) error {
	return s.webAppWithOptions(c, nil)
}

func (s *Server) webAppWithOptions(c *echo.Context, configure func(*web.AppOptions, *models.User)) error {
	c.Response().Header().Set("Vary", "Accept, Accept-Language, Cookie")
	setPublicRESTCacheIfDefault(c, 15)
	account, token, user, _ := s.currentAccountForWeb(c)
	options := s.webAppOptions(c)
	if user != nil && s.userCanUseAPI(*user) {
		s.applyWebAppUserOptions(&options, user)
	} else if user != nil && account != nil {
		options.DisabledAccount = account
		options.MovedToAccount = s.movedToAccountFor(account)
		account = nil
		token = ""
	} else if account != nil {
		options.MovedToAccount = s.movedToAccountFor(account)
	}
	if redirectPath := s.webAppPermalinkRedirectPath(c.Request().URL.Path, account, user); redirectPath != "" {
		return c.Redirect(http.StatusFound, redirectPath)
	}
	if composeRouteAcceptsQuery(c.Request().URL.Path) {
		options.ComposeText = shareTextFromQuery(c)
		options.ComposeVisibility = composeVisibilityFromQuery(c)
	}
	if configure != nil {
		configure(&options, user)
	}
	html, err := s.renderer.AppHTML(c.Request().URL.Path, account, token, options)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, html)
}

func (s *Server) webAppPermalinkRedirectPath(path string, account *models.Account, user *models.User) string {
	if s == nil || s.db == nil {
		return ""
	}
	if user != nil && account != nil && !account.MovedToAccountID.Valid {
		return ""
	}
	return s.permalinkRedirectPath(path)
}

func (s *Server) permalinkRedirectPath(path string) string {
	segments := permalinkPathSegments(path)
	first, second := "", ""
	if len(segments) > 0 {
		first = segments[0]
	}
	if len(segments) > 1 {
		second = segments[1]
	}
	if strings.HasPrefix(first, "@") && permalinkRecordIDCandidate(second) {
		return s.permalinkStatusURL(second)
	}
	if first == "statuses" && permalinkRecordIDCandidate(second) {
		return s.permalinkStatusURL(second)
	}
	if strings.HasPrefix(first, "@") {
		return s.permalinkAccountURLByName(first)
	}
	if first == "accounts" && permalinkRecordIDCandidate(second) {
		return s.permalinkAccountURLByID(second)
	}
	if strings.HasPrefix(path, "/deck") {
		return strings.TrimPrefix(path, "/deck")
	}
	return ""
}

func permalinkPathSegments(path string) []string {
	path = strings.TrimPrefix(path, "/deck")
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func permalinkRecordIDCandidate(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func (s *Server) permalinkStatusURL(rawID string) string {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id == 0 || s == nil || s.db == nil {
		return ""
	}
	var status models.Status
	if err := s.db.Preload("Account").Where("id = ?", id).First(&status).Error; err != nil {
		return ""
	}
	if status.Visibility != 0 && status.Visibility != 1 {
		return ""
	}
	if status.Account.Local() {
		return ""
	}
	return permalinkRemoteURL(status.URL)
}

func (s *Server) permalinkAccountURLByID(rawID string) string {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id == 0 || s == nil || s.db == nil {
		return ""
	}
	var account models.Account
	if err := s.db.Where("id = ?", id).First(&account).Error; err != nil {
		return ""
	}
	if account.Local() {
		return ""
	}
	return permalinkRemoteURL(account.URL)
}

func (s *Server) permalinkAccountURLByName(name string) string {
	if s == nil || s.db == nil {
		return ""
	}
	username, domain, _ := strings.Cut(strings.TrimPrefix(name, "@"), "@")
	query := s.db.Where("lower(username) = ?", strings.ToLower(username))
	if domain == "" || webfingerLocalHostRaw(domain, s.cfg.LocalDomain, s.cfg.WebDomain, s.cfg.AlternateDomains) {
		query = query.Where("domain IS NULL")
	} else {
		query = query.Where("lower(domain) = ?", strings.ToLower(domain))
	}
	var account models.Account
	if err := query.First(&account).Error; err != nil {
		return ""
	}
	if account.Local() {
		return ""
	}
	return permalinkRemoteURL(account.URL)
}

func permalinkRemoteURL(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	if strings.HasPrefix(value.String, "http://") || strings.HasPrefix(value.String, "https://") {
		return value.String
	}
	return ""
}

func (s *Server) privacyPolicy(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Accept, Accept-Language, Cookie")
	setPublicRESTCacheIfDefault(c, 15)
	account, token, user, _ := s.currentAccountForWeb(c)
	options := s.webAppOptions(c)
	if user != nil && s.userCanUseAPI(*user) {
		s.applyWebAppUserOptions(&options, user)
	} else if user != nil && account != nil {
		options.DisabledAccount = account
		options.MovedToAccount = s.movedToAccountFor(account)
		account = nil
		token = ""
	} else if account != nil {
		options.MovedToAccount = s.movedToAccountFor(account)
	}
	privacyTitle := settingsT(s.webLocale(c, user), "privacy_policy.title", "Privacy Policy")
	if strings.TrimSpace(options.SiteTitle) != "" {
		options.DocumentTitle = privacyTitle + " - " + options.SiteTitle
	} else {
		options.DocumentTitle = privacyTitle
	}
	html, err := s.renderer.AppHTML(c.Request().URL.Path, account, token, options)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, html)
}

func (s *Server) about(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Accept, Accept-Language, Cookie")
	setPublicRESTCacheIfDefault(c, 15)
	account, token, user, _ := s.currentAccountForWeb(c)
	options := s.webAppOptions(c)
	if user != nil && s.userCanUseAPI(*user) {
		s.applyWebAppUserOptions(&options, user)
	} else if user != nil && account != nil {
		options.DisabledAccount = account
		options.MovedToAccount = s.movedToAccountFor(account)
		account = nil
		token = ""
	} else if account != nil {
		options.MovedToAccount = s.movedToAccountFor(account)
	}
	aboutTitle := settingsT(s.webLocale(c, user), "about.title", "About")
	if strings.TrimSpace(options.SiteTitle) != "" {
		options.DocumentTitle = aboutTitle + " - " + options.SiteTitle
	} else {
		options.DocumentTitle = aboutTitle
	}
	html, err := s.renderer.AppHTML(c.Request().URL.Path, account, token, options)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, html)
}

func (s *Server) instanceStatsPage(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "max-age=180, public")
	c.Response().Header().Set("Vary", "Accept, Accept-Language, Cookie")
	account, token, user, _ := s.currentAccountForWeb(c)
	options := s.webAppOptions(c)
	if user != nil && s.userCanUseAPI(*user) {
		s.applyWebAppUserOptions(&options, user)
	} else if user != nil && account != nil {
		options.DisabledAccount = account
		options.MovedToAccount = s.movedToAccountFor(account)
		account = nil
		token = ""
	} else if account != nil {
		options.MovedToAccount = s.movedToAccountFor(account)
	}
	aboutTitle := settingsT(s.webLocale(c, user), "about.title", "About")
	if strings.TrimSpace(options.SiteTitle) != "" {
		options.DocumentTitle = aboutTitle + " - " + options.SiteTitle
	} else {
		options.DocumentTitle = aboutTitle
	}
	html, err := s.renderer.AppHTML(c.Request().URL.Path, account, token, options)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, html)
}

func redirectTo(path string, status int) echo.HandlerFunc {
	return func(c *echo.Context) error {
		return c.Redirect(status, withRawQuery(path, c.Request().URL.RawQuery))
	}
}

func (s *Server) webRedirect(c *echo.Context) error {
	path := "/" + c.Param("*")
	if path == "/" {
		return c.Redirect(http.StatusFound, withRawQuery("/", c.Request().URL.RawQuery))
	}
	return c.Redirect(http.StatusFound, withRawQuery(path, c.Request().URL.RawQuery))
}

func (s *Server) encodedAtRedirect(c *echo.Context) error {
	requestURI := c.Request().URL.RequestURI()
	if !strings.HasPrefix(strings.ToLower(requestURI), "/%40") {
		return s.notFound(c)
	}
	target := "/@" + strings.TrimPrefix(requestURI, "/%40")
	return c.Redirect(http.StatusMovedPermanently, target)
}

func (s *Server) remoteInteractionRedirect(c *echo.Context) error {
	return c.Redirect(http.StatusMovedPermanently, withRawQuery("/authorize_interaction", c.Request().URL.RawQuery))
}

func (s *Server) remoteInteractionRedirectJSON(c *echo.Context) error {
	return c.Redirect(http.StatusMovedPermanently, withQueryFormat("/authorize_interaction", c.Request().URL.RawQuery, "json"))
}

func (s *Server) remoteInteractionRedirectFormat(c *echo.Context) error {
	format := c.Param("format")
	if format == "" {
		return s.remoteInteractionRedirect(c)
	}
	return c.Redirect(http.StatusMovedPermanently, withQueryFormat("/authorize_interaction", c.Request().URL.RawQuery, format))
}

func (s *Server) authorizeInteraction(c *echo.Context) error {
	account, _, err := s.currentAccount(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	raw := interactionResource(c)
	if raw == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if path, ok := localInteractionPath(s.cfg.BaseURL(), raw); ok {
		return c.Redirect(http.StatusFound, path)
	}
	path, err := s.resolveInteractionPath(raw, account)
	if err != nil {
		return err
	}
	if path != "" {
		return c.Redirect(http.StatusFound, path)
	}
	return apiError(c, http.StatusNotFound, "Record not found")
}

func (s *Server) remoteInteractionHelper(c *echo.Context) error {
	html, err := s.renderer.RemoteInteractionHelperHTML()
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400")
	c.Response().Header().Set("X-Frame-Options", "SAMEORIGIN")
	c.Response().Header().Set("Referrer-Policy", "no-referrer")
	c.Response().Header().Set("Content-Security-Policy", railsRemoteInteractionHelperCSP(s.cfg))
	return c.HTML(http.StatusOK, html)
}

func interactionResource(c *echo.Context) string {
	raw := c.QueryParam("uri")
	if raw == "" {
		raw = c.QueryParam("acct")
	}
	return strings.TrimPrefix(raw, "acct:")
}

func localInteractionPath(baseURL string, raw string) (string, bool) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Host, base.Host) {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) == 1 && strings.HasPrefix(parts[0], "@") {
		return "/" + parts[0], true
	}
	if len(parts) == 2 && strings.HasPrefix(parts[0], "@") && parts[1] != "" {
		return "/" + parts[0] + "/" + parts[1], true
	}
	if len(parts) == 2 && parts[0] == "users" && parts[1] != "" {
		return "/@" + parts[1], true
	}
	if len(parts) == 4 && parts[0] == "users" && parts[2] == "statuses" && parts[1] != "" && parts[3] != "" {
		return "/@" + parts[1] + "/" + parts[3], true
	}
	return "", false
}

func accountWebPath(account models.Account) string {
	return "/@" + pathAcct(account.Acct())
}

func statusWebPath(status models.Status) string {
	return accountWebPath(status.Account) + "/" + strconv.FormatInt(status.ID, 10)
}

func pathAcct(acct string) string {
	return strings.ReplaceAll(url.PathEscape(acct), "%40", "@")
}

func withRawQuery(path string, rawQuery string) string {
	if rawQuery == "" {
		return path
	}
	return path + "?" + rawQuery
}

func withQueryFormat(path string, rawQuery string, format string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		if rawQuery == "" {
			return path + "?format=" + url.QueryEscape(format)
		}
		return path + "?" + rawQuery + "&format=" + url.QueryEscape(format)
	}
	values.Set("format", format)
	return withRawQuery(path, values.Encode())
}

func (s *Server) share(c *echo.Context) error {
	account, token, err := s.currentAccount(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	options := s.webAppOptions(c)
	options.MovedToAccount = s.movedToAccountFor(account)
	options.ComposeVisibility = composeVisibilityFromQuery(c)
	html, err := s.renderer.ShareHTML(account, token, shareTextFromQuery(c), options)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, html)
}

func (s *Server) webAppOptions(c *echo.Context) web.AppOptions {
	options := web.AppOptions{
		SiteTitle:         s.settingRawValue("site_title", s.cfg.Title),
		SiteTitleSet:      true,
		RegistrationsOpen: s.registrationsOpen(),
		ServerSettings:    s.initialStateServerSettings(),
		IncludeCSRFMeta:   s.cfg.SSORedirect != "",
	}
	if mascot, _ := s.instanceSiteUpload("mascot"); mascot != nil {
		options.MascotURL = serializer.SiteUploadFileURL(s.cfg, *mascot, "original")
	}
	if s.cfg.SingleUserMode {
		options.OwnerAccount, _ = s.initialStateOwnerAccount()
	}
	user, token, err := s.currentUser(c)
	if err != nil {
		return options
	}
	s.applyWebAppUserOptions(&options, user)
	options.PushSubscription = s.webPushSubscriptionForToken(token, user.ID)
	return options
}

func (s *Server) applyWebAppUserOptions(options *web.AppOptions, user *models.User) {
	if options == nil || user == nil {
		return
	}
	options.User = user
	options.Settings = s.webSettingsForUser(user.ID)
	options.Role, options.EveryoneRole = s.initialStateUserRole(user)
	if s.userCanUseAPI(*user) {
		options.AdminAccount, _ = s.instanceContactAccount()
	}
	if s.softwareUpdateCheckEnabled() && s.userCan(user, rolePermissionViewDevops) {
		if pending, err := s.criticalSoftwareUpdatesPending(); err == nil {
			options.CriticalUpdatesPending = &pending
		}
	}
}

func (s *Server) movedToAccountFor(account *models.Account) *models.Account {
	if s.db == nil || account == nil || !account.MovedToAccountID.Valid {
		return nil
	}
	var moved models.Account
	if err := accountSerializerPreloads(s.db).Where("id = ?", account.MovedToAccountID.Int64).First(&moved).Error; err != nil {
		return nil
	}
	return &moved
}

func (s *Server) initialStateUserRole(user *models.User) (*models.UserRole, *models.UserRole) {
	if s.db == nil || user == nil {
		return nil, nil
	}
	everyone, _ := s.userRoleByID(-99)
	if user.RoleID.Valid {
		if role, err := s.userRoleByID(user.RoleID.Int64); err == nil {
			return role, everyone
		}
	}
	return everyone, everyone
}

func (s *Server) userRoleByID(id int64) (*models.UserRole, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var role models.UserRole
	err := s.db.Where("id = ?", id).First(&role).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && id == -99 {
		return &models.UserRole{
			ID:          -99,
			Permissions: 1 << 16,
			Position:    -1,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return &role, nil
}

func (s *Server) initialStateOwnerAccount() (*models.Account, error) {
	if s.db == nil {
		return nil, nil
	}
	var account models.Account
	err := s.db.Preload("AccountStat").
		Where("domain IS NULL").
		Where("suspended_at IS NULL").
		Where("id > 0").
		Order("id ASC").
		First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *Server) webSettingsForUser(userID int64) map[string]any {
	settings := map[string]any{}
	if s.db == nil || userID <= 0 {
		return settings
	}
	var setting models.WebSetting
	if err := s.db.Where("user_id = ?", userID).First(&setting).Error; err != nil {
		return settings
	}
	if len(setting.Data) == 0 {
		return settings
	}
	return decodeWebSettings(setting.Data)
}

func (s *Server) settingsPage(c *echo.Context) error {
	if err := requireHTMLOnlyOptionalFormat(c); err != nil {
		return err
	}
	var user *models.User
	var account *models.Account
	var err error
	if settingsPageSkipsFunctional(c.Request().URL.Path) {
		user, _, err = s.currentUserIncludingDisabled(c)
		if err != nil {
			return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
		}
		setPrivateNoStoreCacheHeaders(c)
		account, err = s.accountForUser(user)
		if err != nil {
			return err
		}
	} else {
		account, _, user, err = s.requireFunctionalAccountForWeb(c)
		if err != nil {
			return webAuthResponseError(err)
		}
		setPrivateNoStoreCacheHeaders(c)
	}
	permissions, err := s.computedUserPermissions(user)
	if err != nil {
		return err
	}
	if c.Request().URL.Path == "/settings/two_factor_authentication_methods" && !user.OTPRequiredForLogin {
		params := url.Values{}
		for _, key := range []string{"notice", "error"} {
			if value := strings.TrimSpace(c.QueryParam(key)); value != "" {
				params.Set(key, value)
			}
		}
		if encoded := params.Encode(); encoded != "" {
			return c.Redirect(http.StatusFound, "/settings/otp_authentication?"+encoded)
		}
		return c.Redirect(http.StatusFound, "/settings/otp_authentication")
	}
	if html, ok, err := s.settingsHTMLWithOptions(c.Request().URL.Path, *user, *account, settingsHTMLOptions{
		Notice:                     c.QueryParam("notice"),
		ErrorText:                  c.QueryParam("error"),
		Permissions:                permissions,
		SoftwareUpdateCheckEnabled: s.softwareUpdateCheckEnabled(),
		Functional:                 webUserFunctional(*user, false),
		FunctionalOrMoved:          webUserFunctional(*user, true),
		LimitedFederationMode:      s.cfg.LimitedFederationMode,
		ApplicationName:            firstNonEmpty(s.settingStringValue("site_title", s.cfg.Title), s.cfg.Title),
	}); ok {
		if err != nil {
			return err
		}
		return c.HTML(http.StatusOK, html)
	}
	html, err := s.renderer.SettingsHTML(c.Request().URL.Path, account, s.settingStringValue("site_title", s.cfg.Title))
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, html)
}

func settingsPageSkipsFunctional(path string) bool {
	switch path {
	case "/settings/export", "/settings/two_factor_authentication_methods":
		return true
	default:
		return false
	}
}

func (s *Server) intent(c *echo.Context) error {
	rawURI := c.QueryParam("uri")
	parsed, err := url.Parse(rawURI)
	if err != nil || parsed.Scheme != "web+mastodon" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	switch parsed.Host {
	case "follow":
		acct := strings.TrimPrefix(parsed.Query().Get("uri"), "acct:")
		if acct == "" {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return c.Redirect(http.StatusFound, "/authorize_interaction?uri="+url.QueryEscape(acct))
	case "share":
		return c.Redirect(http.StatusFound, shareIntentPath(parsed.Query()))
	default:
		return apiError(c, http.StatusNotFound, "Record not found")
	}
}

func shareIntentPath(values url.Values) string {
	raw, ok := values["text"]
	if !ok {
		return "/share"
	}
	query := url.Values{}
	query.Set("text", lastFormValue(url.Values{"text": raw}, "text"))
	return "/share?" + query.Encode()
}

func shareTextFromQuery(c *echo.Context) string {
	parts := make([]string, 0, 3)
	for _, name := range []string{"title", "text", "url"} {
		value := strings.TrimSpace(c.QueryParam(name))
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

func composeVisibilityFromQuery(c *echo.Context) string {
	switch strings.ToLower(strings.TrimSpace(c.QueryParam("visibility"))) {
	case "public", "unlisted", "private", "direct":
		return strings.ToLower(strings.TrimSpace(c.QueryParam("visibility")))
	default:
		return ""
	}
}

func composeRouteAcceptsQuery(path string) bool {
	return path == "/publish" || path == "/statuses/new"
}

func (s *Server) notFound(c *echo.Context) error {
	if c.Request().URL.Path == "/api" || strings.HasPrefix(c.Request().URL.Path, "/api/") {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if publicRequestHasFormat(c, "json") || acceptsJSON(c.Request().Header.Get("Accept")) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Not Found"})
	}
	locale := s.webLocale(c, nil)
	message := settingsT(locale, "errors.404", "The page you are looking for isn't here.")
	title := firstNonEmpty(s.settingStringValue("site_title", s.cfg.Title), s.cfg.Title)
	page := errorPageHTML(http.StatusNotFound, locale, title, "default", message)
	return c.HTML(http.StatusNotFound, page)
}

func acceptsJSON(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mediaType, q, _ := parseAcceptEntry(part)
		if q <= 0 {
			continue
		}
		switch mediaType {
		case "application/json", "application/activity+json", "application/ld+json", "application/jrd+json", "application/jsonrequest", "text/x-json":
			return true
		}
	}
	return false
}

func errorPageHTML(status int, locale string, siteTitle string, theme string, message string) string {
	if strings.TrimSpace(locale) == "" {
		locale = webDefaultLocaleValue()
	}
	pageTitle, content := localizedErrorText(locale, status)
	if status == 0 && strings.TrimSpace(message) != "" {
		pageTitle = message
		content = message
	}
	title := pageTitle
	if strings.TrimSpace(siteTitle) != "" {
		title += " - " + siteTitle
	}
	assets := currentAppAssets()
	themeCSS := assets.themes[normalizedWebTheme(theme)]
	if strings.TrimSpace(themeCSS) == "" {
		themeCSS = assets.themeCSS
	}
	return `<!DOCTYPE html>
<html lang="` + html.EscapeString(locale) + `">
  <head>
    <meta content="text/html; charset=UTF-8" http-equiv="Content-Type">
    <meta charset="utf-8">
    <title>` + html.EscapeString(title) + `</title>
    <meta content="width=device-width,initial-scale=1" name="viewport">
	<link rel="stylesheet" media="all" href="` + html.EscapeString(assets.commonCSS) + `" crossorigin="anonymous">
	<link rel="stylesheet" media="all" href="` + html.EscapeString(themeCSS) + `" crossorigin="anonymous">
	<script src="` + html.EscapeString(assets.commonJS) + `" crossorigin="anonymous" defer></script>
	<script src="` + html.EscapeString(assets.errorJS) + `" crossorigin="anonymous" defer></script>
  </head>
  <body class="error">
    <div class="dialog">
      <div class="dialog__illustration">
        <img alt="` + html.EscapeString(siteTitle) + `" src="/oops.png">
      </div>
      <div class="dialog__message">
		<h1>` + html.EscapeString(content) + `</h1>
      </div>
    </div>
  </body>
</html>`
}

func localizedErrorText(locale string, status int) (string, string) {
	fallback := http.StatusText(status)
	if fallback == "" {
		fallback = "Error"
	}
	switch status {
	case http.StatusBadRequest:
		message := settingsT(locale, "errors.400", "The request you submitted was invalid or malformed.")
		return message, message
	case http.StatusForbidden:
		message := settingsT(locale, "errors.403", "You don't have permission to view this page.")
		return message, message
	case http.StatusNotFound:
		message := settingsT(locale, "errors.404", "The page you are looking for isn't here.")
		return message, message
	case http.StatusNotAcceptable:
		message := settingsT(locale, "errors.406", "This page is not available in the requested format.")
		return message, message
	case http.StatusGone:
		message := settingsT(locale, "errors.410", "The page you were looking for doesn't exist here anymore.")
		return message, message
	case http.StatusUnprocessableEntity:
		return settingsT(locale, "errors.422.title", "Security verification failed"), settingsT(locale, "errors.422.content", "Security verification failed. Are you blocking cookies?")
	case http.StatusTooManyRequests:
		message := settingsT(locale, "errors.429", "Too many requests")
		return message, message
	case http.StatusInternalServerError:
		return settingsT(locale, "errors.500.title", "This page is not correct"), settingsT(locale, "errors.500.content", "We're sorry, but something went wrong on our end.")
	case http.StatusServiceUnavailable:
		message := settingsT(locale, "errors.503", "The page could not be served due to a temporary server failure.")
		return message, message
	default:
		return fallback, fallback
	}
}

func (s *Server) publicRESTCacheEvenIfAuthenticated(c *echo.Context, maxAgeSeconds int) {
	if s == nil || s.disallowUnauthenticatedAPIAccess() {
		return
	}
	c.Response().Header().Set("Cache-Control", "max-age="+strconv.Itoa(maxAgeSeconds)+", public, stale-while-revalidate=30, stale-if-error=86400")
}

func publicRESTCacheIfUnauthenticated(c *echo.Context, maxAgeSeconds int) {
	if requestHasAuthenticationState(c.Request()) {
		return
	}
	c.Response().Header().Set("Cache-Control", "max-age="+strconv.Itoa(maxAgeSeconds)+", public, stale-while-revalidate=30, stale-if-error=86400")
}

func setPublicRESTCacheIfDefault(c *echo.Context, maxAgeSeconds int) {
	cacheControl := strings.TrimSpace(c.Response().Header().Get("Cache-Control"))
	if cacheControl == "" || cacheControl == "private, no-store" {
		publicRESTCacheIfUnauthenticated(c, maxAgeSeconds)
	}
}

func publicHTMLCacheIfUnauthenticated(c *echo.Context, maxAgeSeconds int, staleIfErrorSeconds int) {
	if requestHasAuthenticationState(c.Request()) {
		return
	}
	value := "max-age=" + strconv.Itoa(maxAgeSeconds) + ", public"
	if staleIfErrorSeconds > 0 {
		value += ", stale-while-revalidate=30, stale-if-error=" + strconv.Itoa(staleIfErrorSeconds)
	}
	c.Response().Header().Set("Cache-Control", value)
}

func requestHasAuthenticationState(req *http.Request) bool {
	if strings.TrimSpace(req.Header.Get("Authorization")) != "" {
		return true
	}
	if requestHasTokenParam(req) {
		return true
	}
	for _, name := range []string{sessionCookieName, railsSessionCookieName, railsSessionIDCookieName} {
		cookie, err := req.Cookie(name)
		if err == nil && strings.TrimSpace(cookie.Value) != "" {
			return true
		}
	}
	return false
}

func requestHasTokenParam(req *http.Request) bool {
	return requestRawQueryParamValue(req, "access_token") != "" || requestRawQueryParamValue(req, "bearer_token") != ""
}

func requestRawQueryParamValue(req *http.Request, key string) string {
	if req == nil || req.URL == nil || key == "" {
		return ""
	}
	return lastValue(req.URL.Query()[key])
}

func (s *Server) instanceV1(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	stats := s.instanceStats()
	metadata := s.instanceMetadata()
	rules, err := s.instanceRuleModels()
	if err != nil {
		return err
	}
	metadata.Rules = rules
	instance := serializer.InstanceFromConfigWithOptions(s.cfg, stats, s.instanceRegistrationOptions(), nil, metadata)
	approvalRequired, _ := instance.Registrations["approval_required"].(bool)
	registrationsEnabled, _ := instance.Registrations["enabled"].(bool)
	invitesEnabled, err := s.instanceInvitesEnabled()
	if err != nil {
		return err
	}
	var contactAccount any
	if metadata.ContactAccount != nil {
		contactAccount = serializer.AccountFromModel(s.cfg, *metadata.ContactAccount)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"uri":               instance.Domain,
		"title":             instance.Title,
		"short_description": instance.Description,
		"description":       metadata.Description,
		"email":             metadata.ContactEmail,
		"version":           instance.Version,
		"actual_version":    instance.ActualVersion,
		"urls":              map[string]string{"streaming_api": s.cfg.StreamingBaseURL()},
		"stats":             s.instanceV1Stats(),
		"thumbnail":         serializer.InstanceV1ThumbnailFromSiteUpload(s.cfg, metadata.Thumbnail, metadata.PreviewImageURL),
		"languages":         instance.Languages,
		"registrations":     registrationsEnabled,
		"approval_required": approvalRequired,
		"invites_enabled":   invitesEnabled,
		"configuration":     instanceV1Configuration(instance.Configuration),
		"feature_quote":     instance.FeatureQuote,
		"contact_account":   contactAccount,
		"rules":             instance.Rules,
	})
}

func instanceV1Configuration(configuration map[string]any) map[string]any {
	out := make(map[string]any, len(configuration))
	for key, value := range configuration {
		if key == "urls" || key == "translation" {
			continue
		}
		out[key] = value
	}
	return out
}

func (s *Server) instanceInvitesEnabled() (bool, error) {
	if s == nil || s.db == nil {
		return true, nil
	}
	everyone, err := s.userRoleByID(-99)
	if err != nil {
		return false, err
	}
	return everyone.Permissions&rolePermissionInviteUsers == rolePermissionInviteUsers, nil
}

func (s *Server) instanceV2(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	activeMonth, err := s.instanceActiveMonthUsers(c.Request().Context(), time.Now().UTC())
	if err != nil {
		return err
	}
	metadata := s.instanceMetadata()
	rules, err := s.instanceRuleModels()
	if err != nil {
		return err
	}
	metadata.Rules = rules
	return c.JSON(http.StatusOK, serializer.InstanceFromConfigWithOptions(s.cfg, s.instanceStats(), s.instanceRegistrationOptions(), &activeMonth, metadata))
}

func (s *Server) instanceMetadata() serializer.InstanceMetadata {
	contactAccount, _ := s.instanceContactAccount()
	thumbnail, _ := s.instanceSiteUpload("thumbnail")
	return serializer.InstanceMetadata{
		Title:            s.settingRawValue("site_title", s.cfg.Title),
		TitleSet:         true,
		ShortDescription: s.settingRawValue("site_short_description", ""),
		Description:      s.settingRawValue("site_description", ""),
		ContactEmail:     s.settingRawValue("site_contact_email", ""),
		ContactAccount:   contactAccount,
		Thumbnail:        thumbnail,
		PreviewImageURL:  s.packAssetURL("media/images/preview.png"),
		StatusPageURL:    s.settingRawValue("status_page_url", ""),
	}
}

func (s *Server) instanceSiteUpload(name string) (*models.SiteUpload, error) {
	if s.db == nil {
		return nil, nil
	}
	var upload models.SiteUpload
	err := s.db.Where("var = ?", name).First(&upload).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &upload, nil
}

func (s *Server) packAssetURL(name string) string {
	return s.cfg.BaseURL() + s.packAssetPath(name)
}

func (s *Server) packAssetPath(name string) string {
	path := ""
	if s.renderer != nil {
		path = s.renderer.Asset(name)
	}
	if path == "" {
		path = web.FallbackPackAssetPath(name)
	}
	return path
}

func (s *Server) androidChromeIcon(c *echo.Context) error {
	name := strings.TrimPrefix(c.Request().URL.Path, "/")
	assetName := "media/icons/" + name
	path := ""
	if s.renderer != nil {
		path = s.renderer.Asset(assetName)
	}
	if path == "" {
		path = "/packs/" + assetName
	}
	return c.Redirect(http.StatusFound, path)
}

func (s *Server) appleTouchIcon(c *echo.Context) error {
	name := strings.TrimPrefix(c.Request().URL.Path, "/")
	if strings.HasSuffix(name, "-precomposed.png") {
		name = strings.TrimSuffix(name, "-precomposed.png") + ".png"
	}
	if name == "apple-touch-icon.png" {
		name = "apple-touch-icon-180x180.png"
	}
	assetName := "media/icons/" + name
	path := ""
	if s.renderer != nil {
		path = s.renderer.Asset(assetName)
	}
	if path == "" {
		path = "/packs/" + assetName
	}
	return c.Redirect(http.StatusFound, path)
}

func (s *Server) instanceContactAccount() (*models.Account, error) {
	acct := strings.TrimPrefix(strings.TrimSpace(s.settingStringValue("site_contact_username", "")), "@")
	username, domain, _ := strings.Cut(acct, "@")
	if username == "" {
		return nil, nil
	}
	if webfingerLocalHostRaw(domain, s.cfg.LocalDomain, s.cfg.WebDomain, s.cfg.AlternateDomains) {
		domain = ""
	}
	account, err := s.findAccountByUsernameDomainTx(s.db, username, domain)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Server) instanceRegistrationOptions() serializer.InstanceRegistrationOptions {
	return serializer.InstanceRegistrationOptions{
		Mode:          normalizeRegistrationsMode(s.settingValue("registrations_mode", "none")),
		ClosedMessage: s.settingValue("closed_registrations_message", ""),
		SignUpURL:     s.cfg.SSOAccountSignUpURL,
		SignUpURLSet:  s.cfg.SSOAccountSignUpURLSet,
	}
}

func (s *Server) initialStateServerSettings() *serializer.InitialStateServerSettings {
	settings := serializer.DefaultInitialStateServerSettings()
	settings.ProfileDirectory = s.settingBoolValue("profile_directory", settings.ProfileDirectory)
	settings.TrendsEnabled = s.settingBoolValue("trends", settings.TrendsEnabled)
	settings.TimelinePreview = s.settingBoolValue("timeline_preview", settings.TimelinePreview)
	settings.ActivityAPIEnabled = s.settingBoolValue("activity_api_enabled", settings.ActivityAPIEnabled)
	settings.TrendsAsLandingPage = s.settingBoolValue("trends_as_landing_page", settings.TrendsAsLandingPage)
	settings.StatusPageURL = s.settingStringValue("status_page_url", settings.StatusPageURL)
	settings.AutoPlayGIF = s.settingOptionalBoolValue("auto_play_gif")
	settings.DisplayMedia = s.settingOptionalStringValue("display_media")
	settings.ReduceMotion = s.settingOptionalBoolValue("reduce_motion")
	settings.UseBlurhash = s.settingOptionalBoolValue("use_blurhash")
	settings.CropImages = s.settingOptionalBoolValue("crop_images")
	return &settings
}

func (s *Server) registrationsOpen() bool {
	return !s.cfg.SingleUserMode && normalizeRegistrationsMode(s.settingValue("registrations_mode", "none")) != "none"
}

func normalizeRegistrationsMode(mode string) string {
	mode = normalizeSettingScalar(mode)
	switch mode {
	case "open", "approved":
		return mode
	default:
		return "none"
	}
}

func (s *Server) translationLanguages(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	return c.JSON(http.StatusOK, s.translationLanguageMap(c.Request().Context()))
}

func (s *Server) instanceStats() map[string]string {
	counts := s.instanceStatCounts()
	return map[string]string{
		"user_count":   strconv.FormatInt(counts.UserCount, 10),
		"status_count": strconv.FormatInt(counts.StatusCount, 10),
		"domain_count": strconv.FormatInt(counts.DomainCount, 10),
	}
}

func (s *Server) instanceV1Stats() map[string]int64 {
	counts := s.instanceStatCounts()
	return map[string]int64{
		"user_count":   counts.UserCount,
		"status_count": counts.StatusCount,
		"domain_count": counts.DomainCount,
	}
}

type instanceStatCounts struct {
	UserCount   int64
	StatusCount int64
	DomainCount int64
}

func (s *Server) instanceStatCounts() instanceStatCounts {
	stats := instanceStatCounts{}
	if s.db == nil {
		return stats
	}
	_ = s.db.Model(&models.User{}).
		Joins("JOIN accounts ON accounts.id = users.account_id").
		Where("users.confirmed_at IS NOT NULL").
		Where("accounts.suspended_at IS NULL").
		Count(&stats.UserCount).Error
	_ = s.db.Model(&models.Account{}).
		Select("COALESCE(SUM(account_stats.statuses_count), 0)").
		Joins("JOIN account_stats ON account_stats.account_id = accounts.id").
		Where("accounts.domain IS NULL").
		Scan(&stats.StatusCount).Error
	_ = s.db.Model(&models.Instance{}).Count(&stats.DomainCount).Error
	return stats
}

func (s *Server) instanceActiveMonthUsers(ctx context.Context, now time.Time) (int64, error) {
	return s.instanceActiveUsers(ctx, now, 4)
}

func (s *Server) instanceActiveUsers(ctx context.Context, now time.Time, weeks int) (int64, error) {
	if weeks <= 0 {
		return 0, nil
	}
	start := now.AddDate(0, 0, -7*weeks)
	active, err := s.activityTrackerUniqueSum(ctx, "activity:logins", start, now)
	if err == nil {
		return active, nil
	}
	if s.db == nil {
		return 0, nil
	}
	var count int64
	err = s.db.Model(&models.User{}).
		Joins("JOIN accounts ON accounts.id = users.account_id").
		Where("users.confirmed_at IS NOT NULL").
		Where("accounts.suspended_at IS NULL").
		Where("current_sign_in_at >= ? AND current_sign_in_at < ?", start, now).
		Count(&count).Error
	return count, err
}

func (s *Server) customEmojis(c *echo.Context) error {
	if s.disallowUnauthenticatedAPIAccess() {
		c.Response().Header().Set("Vary", "Authorization")
	}
	if err := s.requireAuthenticatedAPIIfDisallowed(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	if s.db == nil {
		return c.JSON(http.StatusOK, []serializer.CustomEmoji{})
	}
	var emojis []models.CustomEmoji
	if err := s.db.Preload("Category").
		Where("domain IS NULL AND disabled = false AND visible_in_picker = true").
		Order("shortcode ASC").
		Find(&emojis).Error; err != nil {
		return err
	}
	out := make([]serializer.CustomEmoji, 0, len(emojis))
	for _, emoji := range emojis {
		out = append(out, serializer.CustomEmojiFromModel(s.cfg, emoji))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) preferences(c *echo.Context) error {
	user, _, err := s.requireUserScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	account, err := s.accountForUser(user)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.PreferencesFromModel(s.cfg, *user, *account))
}

func (s *Server) emptyArray(c *echo.Context) error {
	return c.JSON(http.StatusOK, []any{})
}

func (s *Server) emptyObject(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{})
}

func (s *Server) verifyCredentials(c *echo.Context) error {
	user, _, err := s.requireUserScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	account, err := s.accountForUser(user)
	if err != nil {
		return err
	}
	count, err := s.followRequestsCount(account.ID)
	if err != nil {
		return err
	}
	role, everyone := s.initialStateUserRole(user)
	return c.JSON(http.StatusOK, serializer.CredentialAccountFromModelWithRole(s.cfg, *account, *user, count, role, everyone))
}

func (s *Server) getAccount(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil {
		return err
	}
	account, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if accountHiddenFromAccountsShow(account) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.AccountFromModel(s.cfg, *account))
}

func accountHiddenFromAccountsShow(account *models.Account) bool {
	if account == nil || !account.Local() {
		return false
	}
	user := account.User
	if user.ID == 0 {
		return true
	}
	return !user.Approved || !user.ConfirmedAt.Valid
}

func (s *Server) lookupAccount(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil {
		return err
	}
	acct := c.QueryParam("acct")
	account, err := s.findAccountByLookupAcct(acct)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.AccountFromModel(s.cfg, *account))
}

func (s *Server) searchAccounts(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	q := normalizeSearchQuery(c.QueryParam("q"))
	limitValue := limit(c, 40, 80)
	offsetValue := intParam(c.QueryParam("offset"), 0)
	resolve := truthy(c.QueryParam("resolve"))
	following := truthy(c.QueryParam("following"))
	if limitValue < 1 {
		return c.JSON(http.StatusOK, []any{})
	}
	var accounts []models.Account
	exactAccount, err := s.resolveAccountSearchExact(q, account, resolve, following, offsetValue)
	if err != nil {
		return err
	}
	if exactAccount != nil {
		accounts = append(accounts, *exactAccount)
	}
	var excludeAccountID int64
	if exactAccount != nil {
		excludeAccountID = exactAccount.ID
	}
	nonExactLimit := accountSearchNonExactLimit(q, account, limitValue, exactAccount)
	if q != "" && nonExactLimit > 0 {
		meiliAccounts, usedMeili, err := s.accountSearchMeiliResults(c.Request().Context(), q, account, following, nonExactLimit, offsetValue, excludeAccountID)
		if err != nil {
			return err
		}
		if usedMeili {
			accounts = append(accounts, meiliAccounts...)
		} else {
			searchResults, err := s.accountSearchDatabaseResults(q, account, following, nonExactLimit, offsetValue, excludeAccountID)
			if err != nil {
				return err
			}
			accounts = append(accounts, searchResults...)
		}
	}
	out := make([]serializer.Account, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, serializer.AccountFromModel(s.cfg, account))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) getStatus(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	status, account, err := s.findVisibleStatusForRequest(c, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.hydrateStatusRelationship(status, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "thread"))
}

func (s *Server) publicTimeline(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	if err := s.requireTimelinePreviewAccess(c); err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	current, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}
	localOnly, remoteOnly := timelineLocationParams(c)
	query := s.publicTimelineStatusQuery().
		Where("statuses.reply = false OR statuses.in_reply_to_account_id = statuses.account_id").
		Where("statuses.reblog_of_id IS NULL")
	if localOnly {
		query = query.Where("(statuses.local = true OR statuses.uri IS NULL)")
	}
	if remoteOnly {
		query = query.Where("statuses.local = false AND statuses.uri IS NOT NULL")
	}
	query = applyPublicTimelineAccountFilters(query, current, localOnly)
	query = applyOnlyMediaFilter(c, query)
	return s.statusList(c, query)
}

func (s *Server) homeTimeline(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	account, _, err := s.requireAccountScope(c, "read", "read:statuses")
	if err != nil {
		return err
	}
	query := s.homeTimelineQuery(account)
	statusCode := http.StatusOK
	if s.homeFeedRegenerating(c.Request().Context(), account.ID) {
		statusCode = http.StatusPartialContent
	}
	return s.statusListWithStatus(c, query, statusCode)
}

func (s *Server) homeFeedRegenerating(ctx context.Context, accountID int64) bool {
	if s == nil || accountID == 0 {
		return false
	}
	value, err := s.redisCommand(ctx, "EXISTS", redisConfig(s.cfg).prefix+"account:"+strconv.FormatInt(accountID, 10)+":regeneration")
	if err != nil {
		return false
	}
	switch typed := value.(type) {
	case int64:
		return typed > 0
	case int:
		return typed > 0
	case string:
		count, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return err == nil && count > 0
	default:
		return false
	}
}

func (s *Server) directTimeline(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:statuses")
	if err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	query := s.visibleStatusQuery(account).Where("statuses.visibility = ?", 3)
	return s.statusList(c, query)
}

func (s *Server) tagTimeline(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	if err := s.requireTimelinePreviewAccess(c); err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	current, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}
	tag := normalizedSearchTagName(c.Param("tag"))
	anyTags := tagTimelineParamValuesWithInitial(c, []string{tag}, "any", "any[]")
	localOnly, remoteOnly := timelineLocationParams(c)
	query := s.publicTimelineStatusQuery().
		Where(statusHasAnyTagSQL(), anyTags)
	if localOnly {
		query = query.Where("(statuses.local = true OR statuses.uri IS NULL)")
	}
	if remoteOnly {
		query = query.Where("statuses.local = false AND statuses.uri IS NOT NULL")
	}
	query = applyPublicTimelineAccountFilters(query, current, localOnly)
	query = applyOnlyMediaFilter(c, query)
	query = applyTagTimelineFilters(c, query)
	return s.statusList(c, query)
}

func (s *Server) requireTimelinePreviewAccess(c *echo.Context) error {
	if s.timelinePreviewEnabled() {
		return nil
	}
	if _, _, err := s.requireAccount(c); err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "This method requires an authenticated user")
	}
	return nil
}

func (s *Server) timelinePreviewEnabled() bool {
	return s.settingBoolValue("timeline_preview", true)
}

func (s *Server) accountStatuses(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if target.SuspendedAt.Valid {
		return c.JSON(http.StatusOK, []any{})
	}

	current, _, _ := s.currentAccount(c)
	if current != nil && current.ID != target.ID {
		blocked, err := s.accountBlocksAccountOrDomain(target.ID, current)
		if err != nil {
			return err
		}
		if blocked {
			return c.JSON(http.StatusOK, []any{})
		}
	}
	query := s.visibleStatusQuery(current).Where("statuses.account_id = ?", target.ID)
	if truthy(c.QueryParam("exclude_reblogs")) {
		query = query.Where("statuses.reblog_of_id IS NULL")
	}
	if truthy(c.QueryParam("exclude_replies")) {
		query = query.Where("(statuses.reply = false OR statuses.in_reply_to_account_id = statuses.account_id)")
	}
	if truthy(c.QueryParam("pinned")) {
		query = query.
			Joins("JOIN status_pins ON status_pins.status_id = statuses.id AND status_pins.account_id = ?", target.ID).
			Order("status_pins.created_at DESC")
	}
	if tagged, ok := accountStatusTagQueryValue(c.QueryParam("tagged")); ok {
		query = query.Where(statusHasTagSQL(), tagged)
	}
	if accountStatusReblogsMayOccur(c) && current != nil && current.ID != target.ID {
		query = applyAccountStatusReblogFilters(query, current)
	}
	query = applyOnlyMediaFilterForAccount(c, query, target.ID)
	return s.statusList(c, query)
}

func (s *Server) homeTimelineQuery(account *models.Account) *gorm.DB {
	query := s.statusQuery().
		Joins("LEFT JOIN follows ON follows.target_account_id = statuses.account_id AND follows.account_id = ?", account.ID).
		Where("statuses.deleted_at IS NULL").
		Where("(statuses.account_id = ? OR (follows.id IS NOT NULL AND statuses.visibility IN ?))", account.ID, []int{0, 1, 2})
	return applyHomeTimelineFilters(query, account)
}

func applyHomeTimelineFilters(query *gorm.DB, account *models.Account) *gorm.DB {
	if account == nil || account.ID == 0 {
		return query
	}
	return query.
		Where(`(
			statuses.account_id = ?
			OR statuses.reply = false
			OR (
				statuses.in_reply_to_id IS NOT NULL
				AND statuses.in_reply_to_account_id IS NOT NULL
			)
		)`, account.ID).
		Where(`(
			statuses.account_id = ?
			OR NOT EXISTS (
				SELECT 1
				FROM lists home_exclusive_lists
				JOIN list_accounts home_exclusive_list_accounts
				  ON home_exclusive_list_accounts.list_id = home_exclusive_lists.id
				WHERE home_exclusive_lists.account_id = ?
				  AND home_exclusive_lists.exclusive = true
				  AND home_exclusive_list_accounts.account_id = statuses.account_id
			)
		)`, account.ID, account.ID).
		Where(`(
			statuses.account_id = ?
			OR array_length(follows.languages, 1) IS NULL
			OR statuses.language IS NULL
			OR statuses.language = ANY(follows.languages)
		)`, account.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks home_blocks
			WHERE home_blocks.account_id = ?
			  AND (
				home_blocks.target_account_id = statuses.account_id
				OR EXISTS (
					SELECT 1 FROM mentions home_status_mentions
					WHERE home_status_mentions.status_id = statuses.id
					  AND home_status_mentions.silent = false
					  AND home_status_mentions.account_id = home_blocks.target_account_id
				)
				OR EXISTS (
					SELECT 1 FROM statuses home_reblog_status
					WHERE home_reblog_status.id = statuses.reblog_of_id
					  AND home_reblog_status.account_id = home_blocks.target_account_id
				)
				OR EXISTS (
					SELECT 1 FROM mentions home_reblog_mentions
					WHERE home_reblog_mentions.status_id = statuses.reblog_of_id
					  AND home_reblog_mentions.silent = false
					  AND home_reblog_mentions.account_id = home_blocks.target_account_id
				)
			  )
		)`, account.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM mutes home_mutes
			WHERE home_mutes.account_id = ?
			  AND (
				home_mutes.target_account_id = statuses.account_id
				OR EXISTS (
					SELECT 1 FROM mentions home_status_mute_mentions
					WHERE home_status_mute_mentions.status_id = statuses.id
					  AND home_status_mute_mentions.silent = false
					  AND home_status_mute_mentions.account_id = home_mutes.target_account_id
				)
				OR EXISTS (
					SELECT 1 FROM statuses home_reblog_mute_status
					WHERE home_reblog_mute_status.id = statuses.reblog_of_id
					  AND home_reblog_mute_status.account_id = home_mutes.target_account_id
				)
				OR EXISTS (
					SELECT 1 FROM mentions home_reblog_mute_mentions
					WHERE home_reblog_mute_mentions.status_id = statuses.reblog_of_id
					  AND home_reblog_mute_mentions.silent = false
					  AND home_reblog_mute_mentions.account_id = home_mutes.target_account_id
				)
			  )
		)`, account.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks home_blocked_by
			WHERE home_blocked_by.target_account_id = ?
			  AND home_blocked_by.account_id = statuses.account_id
		)`, account.ID).
		Where(`(
			statuses.account_id = ?
			OR statuses.reply = false
			OR statuses.in_reply_to_account_id IS NULL
			OR statuses.in_reply_to_account_id = ?
			OR statuses.in_reply_to_account_id = statuses.account_id
			OR EXISTS (
				SELECT 1 FROM follows home_reply_follows
				WHERE home_reply_follows.account_id = ?
				  AND home_reply_follows.target_account_id = statuses.in_reply_to_account_id
			)
		)`, account.ID, account.ID, account.ID).
		Where(`(
			statuses.account_id = ?
			OR statuses.reblog_of_id IS NULL
			OR follows.show_reblogs = true
		)`, account.ID).
		Where(`(
			statuses.reblog_of_id IS NULL
			OR NOT EXISTS (
				SELECT 1
				FROM statuses home_reblog
				JOIN accounts home_reblog_accounts ON home_reblog_accounts.id = home_reblog.account_id
				WHERE home_reblog.id = statuses.reblog_of_id
				  AND (
					EXISTS (
						SELECT 1 FROM blocks home_reblog_blocked_by
						WHERE home_reblog_blocked_by.target_account_id = ?
						  AND home_reblog_blocked_by.account_id = home_reblog.account_id
					)
					OR EXISTS (
						SELECT 1 FROM account_domain_blocks home_reblog_domain_blocks
						WHERE home_reblog_domain_blocks.account_id = ?
						  AND home_reblog_accounts.domain IS NOT NULL
						  AND lower(home_reblog_domain_blocks.domain) = lower(home_reblog_accounts.domain)
					)
				  )
			)
		)`, account.ID, account.ID)
}

func (s *Server) accountBlocksAccountOrDomain(accountID int64, current *models.Account) (bool, error) {
	if current == nil || current.ID == 0 {
		return false, nil
	}
	var count int64
	if err := s.db.Model(&models.Block{}).
		Where("account_id = ? AND target_account_id = ?", accountID, current.ID).
		Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if !current.Domain.Valid || strings.TrimSpace(current.Domain.String) == "" {
		return false, nil
	}
	if err := s.db.Model(&models.AccountDomainBlock{}).
		Where("account_id = ? AND lower(domain) = ?", accountID, strings.ToLower(current.Domain.String)).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func accountStatusReblogsMayOccur(c *echo.Context) bool {
	return !truthy(c.QueryParam("exclude_reblogs")) &&
		!truthy(c.QueryParam("only_media")) &&
		strings.TrimSpace(c.QueryParam("tagged")) == ""
}

func accountStatusTagQueryValue(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	return strings.ToLower(normalizedSearchTagName(raw)), true
}

func applyAccountStatusReblogFilters(query *gorm.DB, current *models.Account) *gorm.DB {
	if current == nil || current.ID == 0 {
		return query
	}
	return query.Where(`(
		statuses.reblog_of_id IS NULL
		OR NOT EXISTS (
			SELECT 1
			FROM statuses account_status_reblogs
			JOIN accounts account_status_reblog_accounts
			  ON account_status_reblog_accounts.id = account_status_reblogs.account_id
			WHERE account_status_reblogs.id = statuses.reblog_of_id
			  AND (
				EXISTS (
					SELECT 1 FROM blocks account_status_reblog_blocks
					WHERE account_status_reblog_blocks.account_id = ?
					AND account_status_reblog_blocks.target_account_id = account_status_reblogs.account_id
				)
				OR EXISTS (
					SELECT 1 FROM blocks account_status_reblog_blocked_by
					WHERE account_status_reblog_blocked_by.target_account_id = ?
					AND account_status_reblog_blocked_by.account_id = account_status_reblogs.account_id
				)
				OR EXISTS (
					SELECT 1 FROM mutes account_status_reblog_mutes
					WHERE account_status_reblog_mutes.account_id = ?
					AND account_status_reblog_mutes.target_account_id = account_status_reblogs.account_id
				)
				OR EXISTS (
					SELECT 1 FROM account_domain_blocks account_status_reblog_domain_blocks
					WHERE account_status_reblog_domain_blocks.account_id = ?
					AND account_status_reblog_accounts.domain IS NOT NULL
					AND lower(account_status_reblog_domain_blocks.domain) = lower(account_status_reblog_accounts.domain)
				)
			  )
		)
	)`, current.ID, current.ID, current.ID, current.ID)
}

func (s *Server) publicTimelineStatusQuery() *gorm.DB {
	return s.statusQuery().
		Joins("JOIN accounts timeline_accounts ON timeline_accounts.id = statuses.account_id").
		Where("statuses.visibility = 0 AND statuses.deleted_at IS NULL").
		Where("timeline_accounts.suspended_at IS NULL AND timeline_accounts.silenced_at IS NULL")
}

func timelineLocationParams(c *echo.Context) (bool, bool) {
	local := truthy(c.QueryParam("local"))
	remote := truthy(c.QueryParam("remote"))
	return local && !remote, remote && !local
}

func applyPublicTimelineAccountFilters(query *gorm.DB, current *models.Account, localOnly bool) *gorm.DB {
	if current == nil || current.ID == 0 {
		return query
	}
	query = query.
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks timeline_blocks
			WHERE timeline_blocks.account_id = ?
			AND timeline_blocks.target_account_id = statuses.account_id
		)`, current.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks timeline_blocked_by
			WHERE timeline_blocked_by.target_account_id = ?
			AND timeline_blocked_by.account_id = statuses.account_id
		)`, current.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM mutes timeline_mutes
			WHERE timeline_mutes.account_id = ?
			AND timeline_mutes.target_account_id = statuses.account_id
		)`, current.ID)
	if !localOnly {
		query = query.Where(`NOT EXISTS (
			SELECT 1 FROM account_domain_blocks timeline_domain_blocks
			WHERE timeline_domain_blocks.account_id = ?
			AND timeline_accounts.domain IS NOT NULL
			AND lower(timeline_domain_blocks.domain) = lower(timeline_accounts.domain)
		)`, current.ID)
	}
	if len(current.User.ChosenLanguages) > 0 {
		query = query.Where("(statuses.language IS NULL OR statuses.language IN ?)", []string(current.User.ChosenLanguages))
	}
	return query
}

func applyOnlyMediaFilter(c *echo.Context, query *gorm.DB) *gorm.DB {
	if !truthy(c.QueryParam("only_media")) {
		return query
	}
	return query.Where(`EXISTS (
		SELECT 1 FROM media_attachments timeline_media
		WHERE timeline_media.status_id = statuses.id
	)`)
}

func applyOnlyMediaFilterForAccount(c *echo.Context, query *gorm.DB, accountID int64) *gorm.DB {
	if !truthy(c.QueryParam("only_media")) {
		return query
	}
	return query.Where(`EXISTS (
		SELECT 1 FROM media_attachments timeline_media
		WHERE timeline_media.status_id = statuses.id
		  AND timeline_media.account_id = ?
	)`, accountID)
}

func applyTagTimelineFilters(c *echo.Context, query *gorm.DB) *gorm.DB {
	for _, tag := range tagTimelineParamValues(c, "all", "all[]") {
		query = query.Where(statusHasTagSQL(), tag)
	}
	for _, tag := range tagTimelineParamValues(c, "none", "none[]") {
		query = query.Where("NOT "+statusHasTagSQL(), tag)
	}
	return query
}

func statusHasTagSQL() string {
	return `EXISTS (
		SELECT 1
		FROM statuses_tags timeline_statuses_tags
		JOIN tags timeline_tags ON timeline_tags.id = timeline_statuses_tags.tag_id
		WHERE timeline_statuses_tags.status_id = statuses.id
		  AND lower(timeline_tags.name) = ?
	)`
}

func statusHasAnyTagSQL() string {
	return `EXISTS (
		SELECT 1
		FROM statuses_tags timeline_statuses_tags
		JOIN tags timeline_tags ON timeline_tags.id = timeline_statuses_tags.tag_id
		WHERE timeline_statuses_tags.status_id = statuses.id
		  AND lower(timeline_tags.name) IN ?
	)`
}

func tagTimelineParamValues(c *echo.Context, keys ...string) []string {
	return tagTimelineParamValuesWithInitial(c, nil, keys...)
}

func tagTimelineParamValuesWithInitial(c *echo.Context, initial []string, keys ...string) []string {
	values := c.QueryParams()
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range initial {
		tag := normalizedSearchTagName(value)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
		if len(out) >= tagTimelineLimitPerMode {
			return out
		}
	}
	for _, key := range keys {
		for _, value := range values[key] {
			tag := normalizedSearchTagName(value)
			if tag == "" {
				continue
			}
			if _, ok := seen[tag]; ok {
				continue
			}
			seen[tag] = struct{}{}
			out = append(out, tag)
			if len(out) >= tagTimelineLimitPerMode {
				return out
			}
		}
	}
	return out
}

func (s *Server) favourites(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:favourites")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	return s.statusJoinList(c, account, "favourites")
}

func (s *Server) bookmarks(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:bookmarks")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	return s.statusJoinList(c, account, "bookmarks")
}

func (s *Server) statusContext(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	status, account, err := s.findVisibleStatusForRequest(c, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	ancestorsLimit := statusContextLimit
	descendantsLimit := statusContextLimit
	descendantsDepthLimit := -1
	if account == nil {
		ancestorsLimit = anonymousAncestorsLimit
		descendantsLimit = anonymousDescendantsLimit
		descendantsDepthLimit = anonymousDescendantsDepthLimit
	}

	ancestors, err := s.statusAncestors(*status, ancestorsLimit, account)
	if err != nil {
		return err
	}
	descendants, err := s.statusDescendants(*status, descendantsLimit, descendantsDepthLimit, account)
	if err != nil {
		return err
	}
	if err := s.hydrateStatusRelationships(ancestors, account); err != nil {
		return err
	}
	if err := s.hydrateStatusRelationships(descendants, account); err != nil {
		return err
	}

	out := serializer.Context{
		Ancestors:   serializeStatusesWithFilterContext(s.cfg, ancestors, account, s.accountFilters(account), "thread"),
		Descendants: serializeStatusesWithFilterContext(s.cfg, descendants, account, s.accountFilters(account), "thread"),
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) statusAncestors(status models.Status, limitValue int, account *models.Account) ([]models.Status, error) {
	if !status.InReplyToID.Valid || limitValue <= 0 {
		return []models.Status{}, nil
	}

	var rows []treeIDRow
	err := s.db.Raw(`
		WITH RECURSIVE search_tree(id, in_reply_to_id, path) AS (
			SELECT id, in_reply_to_id, ARRAY[id]
			FROM statuses
			WHERE id = ?
		UNION ALL
			SELECT statuses.id, statuses.in_reply_to_id, path || statuses.id
			FROM search_tree
			JOIN statuses ON statuses.id = search_tree.in_reply_to_id
			WHERE NOT statuses.id = ANY(path)
		)
		SELECT id
		FROM search_tree
		ORDER BY array_length(path, 1) DESC
		LIMIT ?
	`, status.InReplyToID.Int64, limitValue).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.statusesByTreeIDs(treeRowIDs(rows), false, account)
}

func (s *Server) statusDescendants(status models.Status, limitValue int, depthLimit int, account *models.Account) ([]models.Status, error) {
	if limitValue <= 0 {
		return []models.Status{}, nil
	}
	if depthLimit == 0 {
		depthLimit = statusContextLimit
	}

	var rows []treeIDRow
	err := s.db.Raw(`
		WITH RECURSIVE search_tree(id, path, depth) AS (
			SELECT id, ARRAY[id], 1
			FROM statuses
			WHERE id = ?
		UNION ALL
			SELECT statuses.id, path || statuses.id, depth + 1
			FROM search_tree
			JOIN statuses ON statuses.in_reply_to_id = search_tree.id
			WHERE (? < 0 OR depth < ?)
			  AND NOT statuses.id = ANY(path)
		)
		SELECT id
		FROM search_tree
		WHERE id <> ?
		ORDER BY path
		LIMIT ?
	`, status.ID, depthLimit, depthLimit+1, status.ID, limitValue).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.statusesByTreeIDs(treeRowIDs(rows), true, account)
}

func treeRowIDs(rows []treeIDRow) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func (s *Server) statusesByTreeIDs(ids []int64, promoteSelfReplies bool, account *models.Account) ([]models.Status, error) {
	if len(ids) == 0 {
		return []models.Status{}, nil
	}

	var statuses []models.Status
	query := applyStatusContextFilterQuery(s.visibleStatusQuery(account).Where("statuses.id IN ?", ids), account)
	if err := query.Find(&statuses).Error; err != nil {
		return nil, err
	}
	sortStatusesByIDs(statuses, ids)
	if promoteSelfReplies {
		promoteSelfReplyStatuses(statuses)
	}
	return statuses, nil
}

func sortStatusesByIDs(statuses []models.Status, ids []int64) {
	positions := make(map[int64]int, len(ids))
	for idx, id := range ids {
		positions[id] = idx
	}
	sort.SliceStable(statuses, func(i, j int) bool {
		return positions[statuses[i].ID] < positions[statuses[j].ID]
	})
}

func promoteSelfReplyStatuses(statuses []models.Status) {
	insertAt := -1
	for i, status := range statuses {
		if !status.InReplyToAccountID.Valid || status.InReplyToAccountID.Int64 != status.AccountID {
			insertAt = i
			break
		}
	}
	if insertAt < 0 {
		return
	}
	for i := insertAt + 1; i < len(statuses); i++ {
		status := statuses[i]
		if !status.InReplyToAccountID.Valid || status.InReplyToAccountID.Int64 != status.AccountID {
			continue
		}
		copy(statuses[insertAt+1:i+1], statuses[insertAt:i])
		statuses[insertAt] = status
		insertAt++
	}
}

func (s *Server) createStatus(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	setStatusFamilyRateLimitHeaders(c, railsStatusFamilyLimit-1)

	payload, err := parseStatusCreatePayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	payload.ApplicationID = s.requestApplicationID(c)
	if !validStatusVisibility(payload.Visibility) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Visibility is invalid")
	}
	sensitive := statusSensitiveForCreate(payload.statusUpdatePayload, *account)
	payload.Sensitive = sensitive
	payload.HasSensitive = true
	applyCreateSpoilerTextFallback(&payload.statusUpdatePayload)
	text := payload.Status
	mediaIDs := compactMediaIDs(payload.MediaIDs)
	if submittedMediaIDsPresent(payload.MediaIDs) && submittedMediaIDsCount(payload.MediaIDs) > s.maxMediaAttachments() {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Too many media attachments")
	}
	hasPoll := payload.HasPoll && payload.Poll != nil
	var quote *models.Status
	if strings.TrimSpace(payload.QuoteID) != "" {
		quote, err = s.findQuoteStatusForAccount(account, payload.QuoteID)
		if err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		text = statusTextWithQuoteURL(text, s.quoteStatusURL(*quote))
	}
	normalizeStatusContents(&text, &payload.SpoilerText)
	if strings.TrimSpace(text) == "" && len(mediaIDs) == 0 && !hasPoll {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Text can't be blank")
	}
	if statusLengthTooLong(text, payload.SpoilerText, s.maxStatusChars()) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Text is too long")
	}
	if err := validateStatusDisallowedHashtags(c.Request().Context(), s.db, text); err != nil {
		return err
	}
	if hasPoll && submittedMediaIDsPresent(payload.MediaIDs) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Media attachments can't be attached to polls")
	}
	var scheduledAt time.Time
	if strings.TrimSpace(payload.ScheduledAt) != "" {
		scheduledAt, err = parseScheduledAt(payload.ScheduledAt)
		if err != nil {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Scheduled at is invalid")
		}
	}
	now := time.Now().UTC()
	scheduled := !scheduledAt.IsZero() && scheduledAt.After(now)
	idempotencyKey := cleanIdempotencyKey(c.Request().Header.Get("Idempotency-Key"))
	if id, ok := s.statusIdempotencyDuplicate(c.Request().Context(), account.ID, idempotencyKey); ok {
		if scheduled {
			if status, err := s.findScheduledStatus(id, account.ID); err == nil {
				return c.JSON(http.StatusOK, serializer.ScheduledStatusFromModel(s.cfg, status))
			}
		} else if status, err := s.findStatus(id); err == nil && status.AccountID == account.ID {
			if err := s.hydrateStatusRelationship(status, account); err != nil {
				return err
			}
			return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))
		}
	}
	if scheduled {
		if err := s.validateScheduledStatus(c.Request().Context(), account.ID, scheduledAt, now); err != nil {
			return err
		}
	}
	if hasPoll {
		if err := validatePollPayload(payload.Poll, now); err != nil {
			return err
		}
	}

	visibility := s.statusVisibility(*account, payload.Visibility)
	language := s.statusLanguageForAccount(payload.Language, sql.NullString{}, *account)

	var replyTo *models.Status
	if strings.TrimSpace(payload.InReplyToID) != "" {
		replyTo, err = s.findVisibleStatusForAccount(account, payload.InReplyToID)
		if err != nil {
			return apiError(c, http.StatusNotFound, railsStatusReplyNotFoundMessage)
		}
		replyTo, err = s.railsStatusReplyTarget(replyTo)
		if err != nil {
			return err
		}
	}
	if scheduled {
		return s.createScheduledStatusFromPayload(c, account, payload, mediaIDs, scheduledAt, idempotencyKey)
	}

	status := models.Status{
		Text:          text,
		CreatedAt:     now,
		UpdatedAt:     now,
		AccountID:     account.ID,
		Local:         sql.NullBool{Bool: true, Valid: true},
		Visibility:    visibility,
		Sensitive:     sensitive,
		SpoilerText:   payload.SpoilerText,
		Language:      language,
		ApplicationID: payload.ApplicationID,
	}
	if replyTo != nil {
		status.InReplyToID = sql.NullInt64{Int64: replyTo.ID, Valid: true}
		status.InReplyToAccountID = railsStatusReplyAccountID(account.ID, replyTo)
		status.Reply = true
		status.ConversationID = replyTo.ConversationID
	}

	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	var conversationIDs []int64
	var indexedTagIDs []int64
	rateLimitRecorded, err := s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyStatuses, now)
	if err != nil {
		return err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.ensureStatusConversation(tx, &status, now); err != nil {
			return err
		}
		if err := tx.Create(&status).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.StatusStat{StatusID: status.ID, RepliesCount: 0, ReblogsCount: 0, FavouritesCount: 0}).Error; err != nil {
			return err
		}
		mediaIntIDs := mediaIDsToInt64Array(mediaIDs)
		acceptedMediaIDs, err := updateStatusMedia(tx, account.ID, status.ID, mediaIDs, mediaIntIDs, payload.MediaAttributes, true)
		if err != nil {
			return err
		}
		if len(mediaIDs) > 0 {
			if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("ordered_media_attachment_ids", acceptedMediaIDs).Error; err != nil {
				return err
			}
		}
		if hasPoll {
			pollID, _, err := updateStatusPoll(tx, account.ID, status.ID, payload.Poll, now)
			if err != nil {
				return err
			}
			if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("poll_id", pollID).Error; err != nil {
				return err
			}
		}
		mentions, err := s.saveStatusMentionsFromTextAndCollectAccounts(tx, status.ID, account.ID, text, now)
		if err != nil {
			return err
		}
		if unexpected := unexpectedMentionAccounts(mentions.Accounts, payload.AllowedMentions, payload.HasAllowedMentions); len(unexpected) > 0 {
			return unexpectedMentionsError{accounts: unexpected}
		}
		updatedConversationIDs, err := s.addDirectStatusToConversations(tx, status, mentions.Accounts)
		if err != nil {
			return err
		}
		conversationIDs = append(conversationIDs, updatedConversationIDs...)
		notificationIDs = append(notificationIDs, mentions.NotificationIDs...)
		notificationPayloads = append(notificationPayloads, mentions.NotificationPayloads...)
		tagIDs, err := saveStatusTagsFromText(tx, status.ID, text, now)
		if err != nil {
			return err
		}
		indexedTagIDs = append(indexedTagIDs, tagIDs...)
		if err := refreshFeaturedTagStatsForStatusTags(tx, account.ID, status.Visibility, tagIDs, now); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if rateLimitRecorded {
			s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyStatuses, now)
		}
		var unexpected unexpectedMentionsError
		if errors.As(err, &unexpected) {
			return c.JSON(http.StatusUnprocessableEntity, map[string]any{
				"error":               unexpected.Error(),
				"unexpected_accounts": serializeAccounts(s.cfg, unexpected.accounts),
			})
		}
		if mediaErr := mediaAttachmentValidationAPIError(c, err); mediaErr != nil {
			return mediaErr
		}
		return err
	}

	created, err := s.findStatus(strconv.FormatInt(status.ID, 10))
	if err != nil {
		return err
	}
	if statusCountsTowardAccountStats(created.Visibility) {
		created.Account.AccountStat.StatusesCount++
		created.Account.AccountStat.LastStatusAt = sql.NullTime{Time: created.CreatedAt, Valid: true}
	}
	s.applyStatusQuote(created, quote)
	response := statusWithFilterContext(s.cfg, *created, account, s.accountFilters(account), "public")
	requestID, _ := c.Get("request_id").(string)
	responseErr := c.JSON(http.StatusOK, response)
	s.startLocalStatusCreatePostCommit(localStatusCreatePostCommit{
		RequestID:            requestID,
		Status:               *created,
		Account:              *account,
		ReplyTo:              replyTo,
		Quote:                quote,
		NotificationIDs:      notificationIDs,
		NotificationPayloads: notificationPayloads,
		ConversationIDs:      conversationIDs,
		IndexedTagIDs:        indexedTagIDs,
		IdempotencyKey:       idempotencyKey,
		CreatedAt:            now,
	})
	return responseErr
}

func (s *Server) findQuoteStatusForAccount(account *models.Account, quoteID string) (*models.Status, error) {
	quoteID = strings.TrimSpace(quoteID)
	if quoteID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	quote, err := s.findVisibleStatusForAccount(account, quoteID)
	if err != nil {
		return nil, err
	}
	if quote.ReblogOfID.Valid {
		reblog, err := s.findVisibleStatusForAccount(account, strconv.FormatInt(quote.ReblogOfID.Int64, 10))
		if err != nil {
			return nil, err
		}
		quote = reblog
	}
	return quote, nil
}

func statusTextWithQuoteURL(text string, quoteURL string) string {
	return strings.TrimRight(text, "\r\n") + "\n\nRE: " + quoteURL
}

func statusTextWithExistingQuoteURL(text string, quoteURL string) string {
	quoteURL = strings.TrimSpace(quoteURL)
	if quoteURL == "" || strings.HasSuffix(text, quoteURL) {
		return text
	}
	return statusTextWithQuoteURL(text, quoteURL)
}

func normalizeStatusContents(text *string, spoilerText *string) {
	if text != nil {
		*text = strings.TrimSpace(*text)
	}
	if spoilerText != nil {
		*spoilerText = strings.TrimSpace(*spoilerText)
	}
}

func (s *Server) quoteStatusURL(status models.Status) string {
	if status.URL.Valid && strings.TrimSpace(status.URL.String) != "" {
		return status.URL.String
	}
	if status.Account.Local() {
		return s.cfg.BaseURL() + "/@" + url.PathEscape(status.Account.Username) + "/" + strconv.FormatInt(status.ID, 10)
	}
	if status.URI.Valid && strings.TrimSpace(status.URI.String) != "" {
		return status.URI.String
	}
	return s.cfg.BaseURL() + "/users/" + url.PathEscape(status.Account.Username) + "/statuses/" + strconv.FormatInt(status.ID, 10)
}

func (s *Server) quoteStatusURI(status models.Status) string {
	if status.URI.Valid && strings.TrimSpace(status.URI.String) != "" {
		return status.URI.String
	}
	return s.cfg.BaseURL() + "/users/" + url.PathEscape(status.Account.Username) + "/statuses/" + strconv.FormatInt(status.ID, 10)
}

func (s *Server) localStatusURI(account models.Account, statusID int64) string {
	return s.cfg.BaseURL() + "/users/" + url.PathEscape(account.Username) + "/statuses/" + strconv.FormatInt(statusID, 10)
}

func (s *Server) storeLocalStatusURI(tx *gorm.DB, status *models.Status, account models.Account, _ time.Time) error {
	if tx == nil || status == nil || status.ID == 0 || strings.TrimSpace(account.Username) == "" {
		return nil
	}
	if status.URI.Valid && strings.TrimSpace(status.URI.String) != "" {
		return nil
	}
	uri := s.localStatusURI(account, status.ID)
	if err := tx.Model(&models.Status{}).Where("id = ? AND uri IS NULL", status.ID).Update("uri", uri).Error; err != nil {
		return err
	}
	status.URI = sql.NullString{String: uri, Valid: true}
	return nil
}

func (s *Server) runLocalStatusAfterCreateCommitEffects(db *gorm.DB, status *models.Status, account models.Account, reblogTarget *models.Status, replyTo *models.Status, updateStatistics func()) error {
	if db == nil || status == nil {
		return nil
	}

	s.triggerStatusWebhook("status.created", status.ID)
	if statusCountsTowardAccountStats(status.Visibility) {
		if err := upsertAccountStatForStatus(db, account.ID, status.Visibility, status.CreatedAt); err != nil {
			return err
		}
		if reblogTarget != nil {
			if err := incrementStatusStatCounter(db, reblogTarget.ID, statusStatCounterReblogs, 1); err != nil {
				return err
			}
		}
		if replyTo != nil && statusCountsTowardReplyStats(status.Visibility) {
			if err := incrementStatusStatCounter(db, replyTo.ID, statusStatCounterReplies, 1); err != nil {
				return err
			}
		}
	}
	if err := s.storeLocalStatusURI(db, status, account, status.CreatedAt); err != nil {
		return err
	}
	if updateStatistics != nil {
		updateStatistics()
	}
	return nil
}

func (s *Server) createScheduledStatusFromPayload(c *echo.Context, account *models.Account, payload statusCreatePayload, mediaIDs []string, scheduledAt time.Time, idempotencyKey string) error {
	now := time.Now().UTC()
	if strings.TrimSpace(payload.Visibility) == "" {
		payload.Visibility = serializer.UserDefaultPrivacy(userSettingsForAccount(*account), *account)
	}
	applyCreateSpoilerTextFallback(&payload.statusUpdatePayload)
	if payload.HasPoll && payload.Poll != nil {
		if err := validatePollPayload(payload.Poll, now); err != nil {
			return err
		}
	}
	params, err := scheduledStatusParamsFromPayload(payload, mediaIDs)
	if err != nil {
		return err
	}
	scheduled := models.ScheduledStatus{
		AccountID:   models.ScheduledStatusAccountID(account.ID),
		ScheduledAt: sql.NullTime{Time: scheduledAt.UTC(), Valid: true},
		Params:      params,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&scheduled).Error; err != nil {
			return err
		}
		return attachMediaToScheduledStatus(tx, account.ID, scheduled.ID, mediaIDs, now)
	}); err != nil {
		if mediaErr := mediaAttachmentValidationAPIError(c, err); mediaErr != nil {
			return mediaErr
		}
		return err
	}
	created, err := s.findScheduledStatus(strconv.FormatInt(scheduled.ID, 10), account.ID)
	if err != nil {
		return err
	}
	s.rememberStatusIdempotency(c.Request().Context(), account.ID, idempotencyKey, created.ID)
	return c.JSON(http.StatusOK, serializer.ScheduledStatusFromModel(s.cfg, created))
}

func (s *Server) requestApplicationID(c *echo.Context) sql.NullInt64 {
	token, err := s.currentAccessToken(c)
	if err != nil || token == nil || !token.ApplicationID.Valid {
		return sql.NullInt64{}
	}
	return token.ApplicationID
}

func (s *Server) updateStatus(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	setStatusFamilyRateLimitHeaders(c, railsStatusFamilyLimit-1)
	status, err := s.findStatus(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if status.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is outside the authorized account")
	}
	if status.ReblogOfID.Valid {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You cannot edit boosts")
	}

	payload, err := parseStatusUpdatePayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	applyUpdateSpoilerTextFallback(&payload)
	nextText := status.Text
	if payload.HasStatus {
		nextText = statusTextWithExistingQuoteURL(payload.Status, status.QuoteOriginalURL.String)
		nextText = strings.TrimSpace(nextText)
	}
	nextSpoilerText := status.SpoilerText
	if payload.HasSpoilerText {
		nextSpoilerText = strings.TrimSpace(payload.SpoilerText)
	}
	if (payload.HasStatus || payload.HasSpoilerText) && statusLengthTooLong(nextText, nextSpoilerText, s.maxStatusChars()) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Text is too long")
	}
	if payload.HasStatus {
		if err := validateStatusDisallowedHashtags(c.Request().Context(), s.db, nextText); err != nil {
			return err
		}
	}
	if payload.HasMediaIDs {
		rawMediaIDs := payload.MediaIDs
		if submittedMediaIDsPresent(payload.MediaIDs) && submittedMediaIDsCount(payload.MediaIDs) > s.maxMediaAttachments() {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Too many media attachments")
		}
		payload.MediaIDs = compactMediaIDs(payload.MediaIDs)
		if payload.HasPoll && payload.Poll != nil && submittedMediaIDsPresent(rawMediaIDs) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Media attachments can't be attached to polls")
		}
	}
	if payload.HasPoll && payload.Poll != nil {
		if err := validatePollPayload(payload.Poll, time.Now().UTC()); err != nil {
			return err
		}
	}
	nextLanguage := s.statusLanguageForAccount(payload.Language, status.Language, *account)
	if !statusUpdateHasSignificantChanges(*status, payload, nextText, nextSpoilerText, nextLanguage) {
		if err := s.hydrateStatusRelationship(status, account); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))
	}

	now := time.Now().UTC()
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	var indexedTagIDs []int64
	rateLimitRecorded, err := s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyStatuses, now)
	if err != nil {
		return err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var editCount int64
		if err := tx.Model(&models.StatusEdit{}).Where("status_id = ?", status.ID).Count(&editCount).Error; err != nil {
			return err
		}
		if editCount == 0 {
			previous := statusSnapshotEdit(*status)
			if err := tx.Omit("Status", "Account", "OrderedMediaAttachments").Create(&previous).Error; err != nil {
				return err
			}
		}

		updates := map[string]any{"updated_at": now, "edited_at": now}
		if payload.HasStatus {
			updates["text"] = nextText
			status.Text = nextText
		}
		if payload.HasSpoilerText {
			updates["spoiler_text"] = nextSpoilerText
			status.SpoilerText = nextSpoilerText
		}
		if payload.HasSensitive || payload.HasSpoilerText {
			sensitive := statusSensitiveValue(payload)
			updates["sensitive"] = sensitive
			status.Sensitive = sensitive
		}
		updates["language"] = nextLanguage
		status.Language = nextLanguage
		if payload.HasMediaIDs {
			mediaIntIDs := mediaIDsToInt64Array(payload.MediaIDs)
			acceptedMediaIDs, err := updateStatusMedia(tx, account.ID, status.ID, payload.MediaIDs, mediaIntIDs, payload.MediaAttributes, false)
			if err != nil {
				return err
			}
			updates["ordered_media_attachment_ids"] = acceptedMediaIDs
			status.OrderedMediaAttachmentIDs = acceptedMediaIDs
		}
		if payload.HasPoll {
			pollID, pollOptions, err := updateStatusPoll(tx, account.ID, status.ID, payload.Poll, now)
			if err != nil {
				return err
			}
			updates["poll_id"] = pollID
			status.PollID = pollID
			if payload.Poll != nil {
				status.Poll = &models.Poll{Options: pollOptions}
			} else {
				status.Poll = nil
			}
		}

		if err := tx.Model(&models.Status{}).Where("id = ? AND account_id = ?", status.ID, account.ID).Updates(updates).Error; err != nil {
			return err
		}
		if payload.HasStatus {
			if err := deleteStatusPreviewCardLinks(tx, status.ID); err != nil {
				return err
			}
			mentions, err := s.updateStatusMentionsFromTextAndCollectAccounts(tx, status.ID, account.ID, nextText, now)
			if err != nil {
				return err
			}
			notificationIDs = append(notificationIDs, mentions.NotificationIDs...)
			notificationPayloads = append(notificationPayloads, mentions.NotificationPayloads...)
			tagIDs, err := replaceStatusTagsFromText(tx, status.ID, nextText, now)
			if err != nil {
				return err
			}
			indexedTagIDs = append(indexedTagIDs, tagIDs...)
			if err := refreshFeaturedTagStatsForStatusTags(tx, account.ID, status.Visibility, tagIDs, now); err != nil {
				return err
			}
		}

		updated, err := loadStatusForSnapshot(tx, status.ID)
		if err != nil {
			return err
		}
		current := statusSnapshotEdit(*updated)
		current.AccountID = sql.NullInt64{Int64: account.ID, Valid: true}
		current.CreatedAt = now
		current.UpdatedAt = now
		return tx.Omit("Status", "Account", "OrderedMediaAttachments").Create(&current).Error
	}); err != nil {
		if rateLimitRecorded {
			s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyStatuses, now)
		}
		if mediaErr := mediaAttachmentValidationAPIError(c, err); mediaErr != nil {
			return mediaErr
		}
		return err
	}
	if payload.HasMediaIDs || payload.HasPoll {
		s.invalidateStatusCache(c.Request().Context(), status.ID)
	}

	updated, err := s.findStatus(c.Param("id"))
	if err != nil {
		return err
	}
	if payload.HasPoll {
		s.schedulePollExpirationFinalCheck(updated.Poll)
	}
	if err := s.hydrateStatusRelationship(updated, account); err != nil {
		return err
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(c.Request().Context(), notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	s.meiliIndexStatusBestEffort(c.Request().Context(), updated.ID)
	s.meiliIndexTagsBestEffort(c.Request().Context(), indexedTagIDs)
	_ = s.fanOutStatusUpdateToLocalRecipients(c.Request().Context(), s.db, *updated)
	s.publishStatusUpdateEvent("status.update", *updated)
	s.publishNotificationIDs(notificationIDs)
	s.fetchLinkCardForStatusAsync(updated.ID)
	_ = s.enqueueOrDeliverStatusUpdateDistribution(*updated)
	s.triggerStatusWebhook("status.updated", updated.ID)
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *updated, account, s.accountFilters(account), "public"))
}

const localStatusDeleteEnqueueTimeout = time.Second

func (s *Server) deleteStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	status, err := s.findStatus(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if status.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is outside the authorized account")
	}
	if err := s.refreshStatusAccount(status); err != nil {
		return err
	}
	removal := asynqRemovalPayload{StatusID: status.ID, Redraft: true}
	enqueueCtx, cancel := context.WithTimeout(c.Request().Context(), localStatusDeleteEnqueueTimeout)
	defer cancel()
	if err := s.enqueueRemovalTaskContext(enqueueCtx, removal, asynq.TaskID(removalTaskID(status.ID))); err != nil {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	status.DeletedAt = sql.NullTime{Time: time.Now().UTC(), Valid: true}
	decrementDeletedStatusAccountCountForResponse(status)
	return c.JSON(http.StatusOK, statusWithSourceAndFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))
}

func decrementDeletedStatusAccountCountForResponse(status *models.Status) {
	if status == nil || !statusCountsTowardAccountStats(status.Visibility) || status.Account.AccountStat.StatusesCount <= 0 {
		return
	}
	status.Account.AccountStat.StatusesCount--
}

func (s *Server) refreshStatusAccount(status *models.Status) error {
	if s == nil || s.db == nil || status == nil || status.AccountID == 0 {
		return nil
	}
	return s.db.Preload("AccountStat").Preload("User.Role").Where("id = ?", status.AccountID).First(&status.Account).Error
}

func (s *Server) deleteStatusRecord(ctx context.Context, statusID int64, now time.Time) error {
	result, err := s.discardStatusRowsForRemoval(ctx, statusID, now)
	if err != nil {
		return err
	}
	s.applyDiscardedStatusRowSideEffects(ctx, result)
	return nil
}

type discardedStatusRowsResult struct {
	Statuses      []models.Status
	IndexedTagIDs []int64
}

type statusRemovalRow struct {
	ID             int64
	InReplyToID    sql.NullInt64
	ReblogOfID     sql.NullInt64
	AccountID      int64
	Visibility     int
	ConversationID sql.NullInt64
}

func (s *Server) discardStatusRowsForRemoval(ctx context.Context, statusID int64, now time.Time) (discardedStatusRowsResult, error) {
	var deletedFeedStatuses []models.Status
	var indexedTagIDs []int64
	var removedStatusPins []models.StatusPin
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var counter statusRemovalRow
		if err := tx.Model(&models.Status{}).
			Select("id, in_reply_to_id, reblog_of_id, account_id, visibility, conversation_id").
			Where("id = ? AND deleted_at IS NULL", statusID).
			Scan(&counter).Error; err != nil {
			return err
		}
		var reblogs []statusRemovalRow
		if err := tx.Model(&models.Status{}).
			Select("id, in_reply_to_id, reblog_of_id, account_id, visibility, conversation_id").
			Where("reblog_of_id = ? AND deleted_at IS NULL", statusID).
			Order("id ASC").
			Find(&reblogs).Error; err != nil {
			return err
		}
		rows := make([]statusRemovalRow, 0, 1+len(reblogs))
		if counter.ID != 0 {
			rows = append(rows, counter)
		}
		rows = append(rows, reblogs...)
		candidates := make([]statusRemovalReportCandidate, 0, len(rows))
		for _, row := range rows {
			candidates = append(candidates, statusRemovalReportCandidate{StatusID: row.ID, AccountID: row.AccountID})
		}
		reportedIDs, err := unresolvedReportedStatusIDs(tx, candidates)
		if err != nil {
			return err
		}
		countsByAccount := make(map[int64]int64)
		for _, row := range rows {
			deletedFeedStatuses = append(deletedFeedStatuses, models.Status{ID: row.ID, AccountID: row.AccountID, ReblogOfID: row.ReblogOfID})
			if row.Visibility != 3 && !statusRemovalReported(reportedIDs, row.ID) {
				countsByAccount[row.AccountID]++
			}
		}
		accountIDs := make([]int64, 0, len(countsByAccount))
		for accountID := range countsByAccount {
			accountIDs = append(accountIDs, accountID)
		}
		sort.Slice(accountIDs, func(i int, j int) bool { return accountIDs[i] < accountIDs[j] })
		counterReported := statusRemovalReported(reportedIDs, counter.ID)
		var reblogCount int64
		for _, reblog := range reblogs {
			if !statusRemovalReported(reportedIDs, reblog.ID) {
				reblogCount++
			}
		}
		tagIDs, err := statusTagIDs(tx, statusID)
		if err != nil {
			return err
		}
		indexedTagIDs = append(indexedTagIDs, tagIDs...)
		if err := unlinkDirectStatusFromConversations(ctx, tx, models.Status{ID: counter.ID, Visibility: counter.Visibility, ConversationID: counter.ConversationID}, now); err != nil {
			return err
		}
		if err := tx.Model(&models.Status{}).Where("id = ?", statusID).Update("deleted_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Status{}).Where("reblog_of_id = ? AND deleted_at IS NULL", statusID).Update("deleted_at", now).Error; err != nil {
			return err
		}
		if err := tx.Select("account_id, status_id").Where("status_id = ?", statusID).Find(&removedStatusPins).Error; err != nil {
			return err
		}
		if err := tx.Where("status_id = ?", statusID).Delete(&models.StatusPin{}).Error; err != nil {
			return err
		}
		for _, accountID := range accountIDs {
			if err := decrementAccountStatCounter(tx, accountID, accountStatCounterStatuses, countsByAccount[accountID]); err != nil {
				return err
			}
		}
		if !counterReported && counter.InReplyToID.Valid && statusCountsTowardReplyStats(counter.Visibility) {
			if err := decrementStatusStatCounter(tx, counter.InReplyToID.Int64, statusStatCounterReplies, 1); err != nil {
				return err
			}
		}
		if !counterReported && counter.ReblogOfID.Valid {
			if err := decrementStatusStatCounter(tx, counter.ReblogOfID.Int64, statusStatCounterReblogs, 1); err != nil {
				return err
			}
		} else if reblogCount > 0 {
			if err := decrementStatusStatCounter(tx, statusID, statusStatCounterReblogs, reblogCount); err != nil {
				return err
			}
		}
		if err := refreshFeaturedTagStatsForStatusTags(tx, counter.AccountID, counter.Visibility, tagIDs, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return discardedStatusRowsResult{}, err
	}
	for _, pin := range removedStatusPins {
		s.runStatusPinDestroyedSideEffects(ctx, pin)
	}
	return discardedStatusRowsResult{Statuses: deletedFeedStatuses, IndexedTagIDs: indexedTagIDs}, nil
}

func statusUnresolvedReported(tx *gorm.DB, statusID int64, accountID int64) (bool, error) {
	if tx == nil || statusID == 0 || accountID == 0 {
		return false, nil
	}
	reported, err := unresolvedReportedStatusIDs(tx, []statusRemovalReportCandidate{{StatusID: statusID, AccountID: accountID}})
	if err != nil {
		return false, err
	}
	return statusRemovalReported(reported, statusID), nil
}

func (s *Server) applyDiscardedStatusRowSideEffects(ctx context.Context, result discardedStatusRowsResult) {
	for _, status := range result.Statuses {
		_ = s.removeStatusFromRailsFeeds(ctx, s.db, status)
		s.meiliDeleteStatusBestEffort(ctx, status.ID)
		s.deleteStatusQuoteBestEffort(ctx, status.ID)
	}
	if len(result.IndexedTagIDs) > 0 {
		s.meiliIndexTagsBestEffort(ctx, result.IndexedTagIDs)
	}
}

func (s *Server) favouriteStatus(c *echo.Context) error {
	return s.toggleStatusJoin(c, "favourites", true, "write", "write:favourites")
}

func (s *Server) unfavouriteStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:favourites")
	if err != nil {
		return err
	}
	status, favourite, err := s.statusForUnfavourite(account, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if favourite != nil {
		if !s.enqueueUnfavouriteTask(account.ID, status.ID) {
			if err := s.runUnfavouriteWorkerEffects(c.Request().Context(), account.ID, status.ID); err != nil {
				return err
			}
		}
		if err := s.hydrateStatusRelationship(status, account); err != nil {
			return err
		}
		status.FavouritedByCurrent = false
		if status.StatusStat.FavouritesCount > 0 {
			status.StatusStat.FavouritesCount--
		}
	} else if err := s.hydrateStatusRelationship(status, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))
}

func (s *Server) bookmarkStatus(c *echo.Context) error {
	return s.toggleStatusJoin(c, "bookmarks", true, "write", "write:bookmarks")
}

func (s *Server) unbookmarkStatus(c *echo.Context) error {
	return s.toggleStatusJoin(c, "bookmarks", false, "write", "write:bookmarks")
}

func (s *Server) reblogStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	setStatusFamilyRateLimitHeaders(c, railsStatusFamilyLimit-1)
	payload, err := parseReblogPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if !validStatusVisibility(payload.Visibility) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Visibility is invalid")
	}
	if !validReblogVisibility(payload.Visibility) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Visibility is reserved")
	}
	target, err := s.findVisibleStatusForAccount(account, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	target = properReblogTarget(target)
	if target.Visibility == 3 || target.Visibility == 4 || (target.Visibility == 2 && target.AccountID != account.ID) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	acquired, releaseReblogLock, err := s.acquireActivityPubRedisLock(c.Request().Context(), "reblog:"+strconv.FormatInt(account.ID, 10)+":"+strconv.FormatInt(target.ID, 10), 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseReblogLock()
	now := time.Now().UTC()
	reblog := models.Status{
		Text:       "",
		CreatedAt:  now,
		UpdatedAt:  now,
		AccountID:  account.ID,
		Local:      sql.NullBool{Bool: true, Valid: true},
		Visibility: s.reblogVisibility(*account, *target, payload.Visibility),
		ReblogOfID: sql.NullInt64{Int64: target.ID, Valid: true},
	}
	reblogCreated := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Preload("Account.AccountStat").
			Preload("Account.User.Role").
			Preload("Reblog.Account.AccountStat").
			Preload("Reblog.Account.User.Role").
			Where("account_id = ? AND reblog_of_id = ? AND deleted_at IS NULL", account.ID, target.ID).
			First(&reblog).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		reblog.CreatedAt = now
		reblog.UpdatedAt = now
		if err := s.ensureStatusConversation(tx, &reblog, now); err != nil {
			return err
		}
		if err := safeInsertReblogStatus(tx, &reblog); err != nil {
			return err
		}
		if err := tx.Create(&models.StatusStat{StatusID: reblog.ID}).Error; err != nil {
			return err
		}
		reblogCreated = true
		return nil
	})
	if err != nil {
		if errors.Is(err, errSafeReblogTargetUnavailable) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	createdStatus := &reblog
	if reblogCreated {
		if err := s.runLocalStatusAfterCreateCommitEffects(s.db, createdStatus, *account, target, nil, func() {
			if statusCountsTowardLocalActivity(createdStatus.Visibility) {
				s.activityTrackerIncrementBasic(c.Request().Context(), "activity:statuses:local", createdStatus.CreatedAt, 1)
			}
		}); err != nil {
			return err
		}
	}
	createdStatus, err = s.findStatus(strconv.FormatInt(reblog.ID, 10))
	if err != nil {
		return err
	}
	if err := s.hydrateStatusRelationship(createdStatus, account); err != nil {
		return err
	}
	if reblogCreated {
		s.activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", createdStatus.CreatedAt, 1)
		s.recordStatusTrendUse(c.Request().Context(), target.ID, createdStatus.CreatedAt)
		s.recordPreviewCardTrendUseForStatus(c.Request().Context(), account.ID, target.ID, createdStatus.Visibility, createdStatus.CreatedAt)
		s.recordPotentialFriendship(c.Request().Context(), account.ID, target.AccountID, "reblog")
		s.meiliIndexStatusBestEffort(c.Request().Context(), target.ID)
		_ = s.fanOutStatusToLocalRecipientsSkipNotifications(c.Request().Context(), s.db, *createdStatus)
		_ = s.enqueueOrDeliverActivityPubDistribution(*createdStatus)
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *createdStatus, account, s.accountFilters(account), "public"))
}

func (s *Server) unreblogStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	target, err := s.findVisibleStatusForAccount(account, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var reblog models.Status
	if err := s.db.Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Preload("Reblog.Account.AccountStat").
		Preload("Reblog.Account.User.Role").
		Where("account_id = ? AND reblog_of_id = ? AND deleted_at IS NULL", account.ID, target.ID).
		First(&reblog).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := s.hydrateStatusRelationship(target, account); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *target, account, s.accountFilters(account), "public"))
	}
	acquired, releaseDistributionLock, err := s.acquireStatusDistributionRedisLock(c.Request().Context(), reblog.ID)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseDistributionLock()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Preload("Account.AccountStat").
			Preload("Account.User.Role").
			Preload("Reblog.Account.AccountStat").
			Preload("Reblog.Account.User.Role").
			Where("account_id = ? AND reblog_of_id = ? AND deleted_at IS NULL", account.ID, target.ID).
			First(&reblog).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Model(&models.Status{}).Where("id = ?", reblog.ID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		if err := decrementStatusStatCounter(tx, target.ID, statusStatCounterReblogs, 1); err != nil {
			return err
		}
		return decrementAccountStatForStatus(tx, account.ID, reblog.Visibility)
	})
	if err != nil {
		return err
	}
	if reblog.ID != 0 {
		s.meiliIndexStatusBestEffort(c.Request().Context(), target.ID)
		if !s.enqueueRemovalTask(asynqRemovalPayload{StatusID: reblog.ID}) {
			s.applyDeletedStatusRemovalSideEffects(c.Request().Context(), reblog, asynqRemovalPayload{StatusID: reblog.ID})
		}
	}
	if err := s.hydrateStatusRelationship(target, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *target, account, s.accountFilters(account), "public"))
}

func properReblogTarget(status *models.Status) *models.Status {
	if status != nil && status.ReblogOfID.Valid && status.Reblog != nil && status.Reblog.ID != 0 {
		return status.Reblog
	}
	return status
}

func (s *Server) toggleStatusJoin(c *echo.Context, table string, create bool, scopes ...string) error {
	account, _, err := s.requireAccountScope(c, scopes...)
	if err != nil {
		return err
	}
	var status *models.Status
	if create {
		status, err = s.findVisibleStatusForAccount(account, c.Param("id"))
		if err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
	} else {
		status, err = s.findStatusForJoinRemoval(account, c.Param("id"), table)
		if err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
	}
	joinStatus := statusJoinTarget(status)
	now := time.Now().UTC()
	var favourite *models.Favourite
	var bookmark *models.Bookmark
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	changed := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if create {
			if table == "favourites" {
				row := models.Favourite{AccountID: account.ID, StatusID: joinStatus.ID, CreatedAt: now, UpdatedAt: now}
				res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected > 0 {
					favourite = &row
				}
			} else {
				row := models.Bookmark{AccountID: account.ID, StatusID: joinStatus.ID, CreatedAt: now, UpdatedAt: now}
				res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected > 0 {
					bookmark = &row
				}
			}
			if table == "favourites" {
				if favourite == nil {
					return nil
				}
				if joinStatus.Account.Local() && joinStatus.AccountID != account.ID {
					notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: joinStatus.AccountID, FromAccountID: account.ID, ActivityID: favourite.ID, ActivityType: "Favourite", Type: "favourite"})
				}
				return incrementStatusStatCounter(tx, joinStatus.ID, statusStatCounterFavourites, 1)
			}
			return nil
		}

		if table == "favourites" {
			var row models.Favourite
			err := tx.Where("account_id = ? AND status_id = ?", account.ID, status.ID).First(&row).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			favourite = &row
			if err := tx.Where("activity_type = ? AND activity_id = ?", "Favourite", row.ID).Delete(&models.Notification{}).Error; err != nil {
				return err
			}
		}
		res := tx.Exec("DELETE FROM "+table+" WHERE account_id = ? AND status_id = ?", account.ID, status.ID)
		if res.Error != nil {
			return res.Error
		}
		changed = res.RowsAffected > 0
		if table == "favourites" && res.RowsAffected > 0 {
			return decrementStatusStatCounter(tx, status.ID, statusStatCounterFavourites, 1)
		}
		return nil
	})
	if err != nil {
		return err
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(c.Request().Context(), notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	if table == "bookmarks" {
		if create && bookmark != nil {
			s.addBookmarkToFeedCache(c.Request().Context(), *bookmark)
		} else if !create && changed {
			s.removeBookmarkFromFeedCache(c.Request().Context(), account.ID, status.ID)
		}
	}
	if !create && changed && status.AccountID == account.ID && account.Local() {
		if table == "favourites" {
			s.invalidateStatusesCleanupLastInspected(c.Request().Context(), account.ID, status.ID, "unfav")
		} else if table == "bookmarks" {
			s.invalidateStatusesCleanupLastInspected(c.Request().Context(), account.ID, status.ID, "unbookmark")
		}
	}
	s.publishNotificationIDs(notificationIDs)
	if table == "favourites" && favourite != nil {
		if create {
			s.activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", favourite.CreatedAt, 1)
			s.recordStatusTrendUse(c.Request().Context(), status.ID, favourite.CreatedAt)
			s.recordPotentialFriendship(c.Request().Context(), account.ID, joinStatus.AccountID, "favourite")
			_ = s.deliverActivityPubActivityToStatusAuthor(*account, *joinStatus, activityPubLike(s, *account, *joinStatus, favourite.ID))
		} else {
			_ = s.deliverActivityPubActivityToStatusAuthor(*account, *status, activityPubUndoLike(s, *account, *status, favourite.ID))
		}
	}
	if create {
		if (table == "favourites" && favourite != nil) || (table == "bookmarks" && bookmark != nil) {
			s.meiliIndexStatusBestEffort(c.Request().Context(), joinStatus.ID)
		}
	} else if changed {
		s.meiliIndexStatusBestEffort(c.Request().Context(), status.ID)
	}

	refreshed := status
	if create {
		refreshed, err = s.findVisibleStatusForAccount(account, c.Param("id"))
		if err != nil {
			return err
		}
	} else if changed {
		refreshed, err = s.findStatus(strconv.FormatInt(status.ID, 10))
		if err != nil {
			return err
		}
	}
	if err := s.hydrateStatusRelationship(refreshed, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *refreshed, account, s.accountFilters(account), "public"))
}

func statusJoinTarget(status *models.Status) *models.Status {
	if status == nil || !status.ReblogOfID.Valid || status.Reblog == nil || status.Reblog.ID == 0 {
		return status
	}
	return status.Reblog
}

func (s *Server) findStatusForJoinRemoval(account *models.Account, id string, table string) (*models.Status, error) {
	if s.db == nil || account == nil || account.ID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if table == "favourites" {
		var row models.Favourite
		err := s.db.Where("account_id = ? AND status_id = ?", account.ID, id).First(&row).Error
		if err == nil {
			return s.findStatus(strconv.FormatInt(row.StatusID, 10))
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	} else if table == "bookmarks" {
		var row models.Bookmark
		err := s.db.Where("account_id = ? AND status_id = ?", account.ID, id).First(&row).Error
		if err == nil {
			return s.findStatus(strconv.FormatInt(row.StatusID, 10))
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return s.findVisibleStatusForAccount(account, id)
}

func (s *Server) statusForUnfavourite(account *models.Account, id string) (*models.Status, *models.Favourite, error) {
	if s.db == nil || account == nil || account.ID == 0 {
		return nil, nil, gorm.ErrRecordNotFound
	}
	var favourite models.Favourite
	err := s.db.Where("account_id = ? AND status_id = ?", account.ID, id).First(&favourite).Error
	if err == nil {
		status, err := s.findStatus(strconv.FormatInt(favourite.StatusID, 10))
		if err != nil {
			return nil, nil, err
		}
		return status, &favourite, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, err
	}
	status, err := s.findVisibleStatusForAccount(account, id)
	return status, nil, err
}

func (s *Server) runUnfavouriteWorkerEffects(ctx context.Context, accountID int64, statusID int64) error {
	if s == nil || s.db == nil || accountID == 0 || statusID == 0 {
		return nil
	}
	status, err := s.findStatus(strconv.FormatInt(statusID, 10))
	if err != nil || status == nil {
		return nil
	}
	var favouriteID int64
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var favourite models.Favourite
		if err := tx.Where("account_id = ? AND status_id = ?", accountID, statusID).First(&favourite).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		favouriteID = favourite.ID
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Favourite", favourite.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&favourite).Error; err != nil {
			return err
		}
		return decrementStatusStatCounter(tx, statusID, statusStatCounterFavourites, 1)
	})
	if err != nil || favouriteID == 0 {
		return err
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", accountID).First(&account).Error; err != nil {
		return nil
	}
	_ = s.deliverActivityPubActivityToStatusAuthor(account, *status, activityPubUndoLike(s, account, *status, favouriteID))
	if status.AccountID == accountID {
		s.invalidateStatusesCleanupLastInspected(ctx, accountID, statusID, "unfav")
	}
	s.meiliIndexStatusBestEffort(ctx, statusID)
	return nil
}

func (s *Server) search(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:search"); err != nil {
		return err
	}
	if strings.TrimSpace(c.QueryParam("q")) == "" {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: q")
	}
	account, _, _ := s.currentAccount(c)
	if account == nil && queryParamValuePresent(c, "offset") {
		return apiError(c, http.StatusUnauthorized, "Search queries pagination is not supported without authentication")
	}
	resolve := truthy(c.QueryParam("resolve"))
	following := truthy(c.QueryParam("following"))
	excludeUnreviewed := truthy(c.QueryParam("exclude_unreviewed"))
	if account == nil && resolve {
		return apiError(c, http.StatusUnauthorized, "Search queries that resolve remote resources are not supported without authentication")
	}

	q := normalizeSearchQuery(c.QueryParam("q"))
	if q == "" || s.db == nil {
		return c.JSON(http.StatusOK, emptySearchResult())
	}
	limitValue := limit(c, 20, 40)
	searchType := c.QueryParam("type")
	offsetValue := searchOffsetValue(searchType, c.QueryParam("offset"))
	if limitValue < 1 {
		return c.JSON(http.StatusOK, emptySearchResult())
	}

	if resolve {
		resolvedAccounts, resolvedStatuses, handled, err := s.resolveSearchURL(q, searchType, offsetValue, account)
		if err != nil {
			return err
		}
		if handled {
			if err := s.hydrateStatusRelationships(resolvedStatuses, account); err != nil {
				return err
			}
			return c.JSON(http.StatusOK, serializer.Search{
				Accounts: serializeAccounts(s.cfg, resolvedAccounts),
				Statuses: serializeStatusesWithFilterContext(s.cfg, resolvedStatuses, account, s.accountFilters(account), statusListFilterContext(c)),
				Hashtags: []serializer.TagDetail{},
			})
		}
	}

	var accounts []models.Account
	if searchIncludesType(searchType, "accounts") {
		var exactAccountID int64
		exactAccount, err := s.resolveAccountSearchExact(q, account, resolve, following, offsetValue)
		if err != nil {
			return err
		}
		if exactAccount != nil {
			accounts = append(accounts, *exactAccount)
			exactAccountID = exactAccount.ID
		}
		nonExactLimit := accountSearchNonExactLimit(q, account, limitValue, exactAccount)
		if nonExactLimit > 0 {
			meiliAccounts, usedMeili, err := s.accountSearchMeiliResults(c.Request().Context(), q, account, following, nonExactLimit, offsetValue, exactAccountID)
			if err != nil {
				return err
			}
			if usedMeili {
				accounts = append(accounts, meiliAccounts...)
			} else {
				searchResults, err := s.accountSearchDatabaseResults(q, account, following, nonExactLimit, offsetValue, exactAccountID)
				if err != nil {
					return err
				}
				accounts = append(accounts, searchResults...)
			}
		}
	}

	var statuses []models.Status
	if account != nil && searchIncludesType(searchType, "statuses") {
		meiliIDs, meiliErr := s.searchMeiliStatusIDs(c.Request().Context(), q, account, c.QueryParam("account_id"), c.QueryParam("min_id"), c.QueryParam("max_id"), limitValue, offsetValue)
		if meiliErr == nil {
			if len(meiliIDs) > 0 {
				statusQuery := s.visibleSearchStatusQuery(account).Where("statuses.id IN ?", meiliIDs)
				if accountID := c.QueryParam("account_id"); accountID != "" {
					statusQuery = statusQuery.Where("statuses.account_id = ?", accountID)
				}
				if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id") {
					statusQuery = statusQuery.Where("statuses.id > ?", minID)
				}
				if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
					statusQuery = statusQuery.Where("statuses.id < ?", maxID)
				}
				if err := statusQuery.Find(&statuses).Error; err != nil {
					return err
				}
				statuses = orderStatusesByIDs(statuses, meiliIDs)
			}
		}
		if err := s.hydrateStatusRelationships(statuses, account); err != nil {
			return err
		}
	}

	var tags []models.Tag
	if searchIncludesType(searchType, "hashtags") {
		meiliIDs, meiliErr := s.searchMeiliTagIDs(c.Request().Context(), q, excludeUnreviewed, limitValue, offsetValue)
		if meiliErr == nil {
			if len(meiliIDs) > 0 {
				tagQuery := s.db.Where("tags.id IN ?", meiliIDs)
				if excludeUnreviewed {
					tagQuery = tagQuery.Where("tags.reviewed_at IS NOT NULL")
				}
				if err := tagQuery.Find(&tags).Error; err != nil {
					return err
				}
				tags = orderTagsByIDs(tags, meiliIDs)
			}
		} else {
			tagQuery := s.tagSearchDatabaseQuery(q, excludeUnreviewed)
			if tagQuery == nil {
				tagQuery = s.db.Where("1 = 0")
			}
			if err := tagQuery.Order("length(tags.name) ASC").Order("tags.name ASC").Offset(offsetValue).Limit(limitValue).Find(&tags).Error; err != nil {
				return err
			}
		}
		var exactErr error
		tags, exactErr = s.ensureExactTagMatch(tags, q, offsetValue)
		if exactErr != nil {
			return exactErr
		}
	}

	tagFollowing, err := s.searchTagFollowingMap(account, tags)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.Search{
		Accounts: serializeAccounts(s.cfg, accounts),
		Statuses: serializeStatusesWithFilterContext(s.cfg, statuses, account, s.accountFilters(account), statusListFilterContext(c)),
		Hashtags: searchHashtagResults(s.cfg, tags, tagFollowing, account != nil),
	})
}

func (s *Server) tagSearchDatabaseQuery(query string, excludeUnreviewed bool) *gorm.DB {
	if s.db == nil {
		return nil
	}
	normalized := normalizedSearchTagName(query)
	if normalized == "" {
		return s.db.Where("1 = 0")
	}
	tagQuery := s.db.Where("lower(tags.name) LIKE ? ESCAPE '\\'", strings.ToLower(escapeLikePattern(normalized))+"%").
		Where("(tags.listable IS NULL OR tags.listable = ?)", true)
	if excludeUnreviewed {
		tagQuery = tagQuery.Where("(lower(tags.name) = ? OR tags.reviewed_at IS NOT NULL)", strings.ToLower(normalized))
	}
	return tagQuery
}

func (s *Server) ensureExactTagMatch(tags []models.Tag, query string, offsetValue int) ([]models.Tag, error) {
	if offsetValue > 0 {
		return tags, nil
	}
	normalized := normalizedSearchTagName(query)
	if normalized == "" {
		return tags, nil
	}
	for i, tag := range tags {
		if strings.EqualFold(tag.Name, normalized) {
			if i == 0 {
				return tags, nil
			}
			out := make([]models.Tag, 0, len(tags))
			out = append(out, tag)
			out = append(out, tags[:i]...)
			out = append(out, tags[i+1:]...)
			return out, nil
		}
	}
	if s.db == nil {
		return tags, nil
	}
	var exact models.Tag
	err := s.db.Where("lower(tags.name) = ?", strings.ToLower(normalized)).First(&exact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tags, nil
	}
	if err != nil {
		return nil, err
	}
	return append([]models.Tag{exact}, tags...), nil
}

func normalizedSearchTagName(query string) string {
	raw := searchTagQuery(query)
	normalized := railsNormalizeHashtagName(raw)
	var out strings.Builder
	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '·' || r == '・' || r == '\u200c' {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func emptySearchResult() serializer.Search {
	return serializer.Search{
		Accounts: []serializer.Account{},
		Statuses: []serializer.Status{},
		Hashtags: []serializer.TagDetail{},
	}
}

func (s *Server) visibleSearchStatusQuery(account *models.Account) *gorm.DB {
	return s.visibleStatusQuery(account)
}

func searchOffsetValue(searchType string, rawOffset string) int {
	if strings.TrimSpace(searchType) == "" {
		return 0
	}
	return intParam(rawOffset, 0)
}

func (s *Server) findVisibleStatusForRequest(c *echo.Context, id string) (*models.Status, *models.Account, error) {
	if s.db == nil {
		return nil, nil, gorm.ErrRecordNotFound
	}
	account, _, _ := s.currentAccount(c)
	status, err := s.findVisibleStatusForAccount(account, id)
	return status, account, err
}

func (s *Server) findVisibleStatusForAccount(account *models.Account, id string) (*models.Status, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var status models.Status
	err := s.visibleStatusQuery(account).Where("statuses.id = ?", id).First(&status).Error
	if err == nil {
		err = s.hydrateStatusCustomEmojis(&status)
	}
	return &status, err
}

func (s *Server) visibleStatusQuery(account *models.Account) *gorm.DB {
	query := s.statusQuery().
		Where("statuses.deleted_at IS NULL").
		Where(`EXISTS (
			SELECT 1 FROM accounts visible_status_accounts
			WHERE visible_status_accounts.id = statuses.account_id
			  AND visible_status_accounts.suspended_at IS NULL
		)`)
	if account == nil || account.ID == 0 {
		return query.Where("statuses.visibility IN ?", []int{0, 1})
	}
	return query.Where(`(
		(
			statuses.visibility IN ?
			AND NOT EXISTS (
				SELECT 1 FROM blocks visible_status_author_blocks
				WHERE visible_status_author_blocks.account_id = statuses.account_id
				  AND visible_status_author_blocks.target_account_id = ?
			)
			AND NOT EXISTS (
				SELECT 1
				FROM account_domain_blocks visible_status_author_domain_blocks
				JOIN accounts visible_status_current_account
				  ON visible_status_current_account.id = ?
				WHERE visible_status_author_domain_blocks.account_id = statuses.account_id
				  AND visible_status_current_account.domain IS NOT NULL
				  AND lower(visible_status_author_domain_blocks.domain) = lower(visible_status_current_account.domain)
			)
		)
		OR statuses.account_id = ?
		OR (
			statuses.visibility = ?
			AND EXISTS (
				SELECT 1 FROM follows search_status_follows
				WHERE search_status_follows.account_id = ?
				AND search_status_follows.target_account_id = statuses.account_id
			)
		)
		OR (
			statuses.visibility IN ?
			AND EXISTS (
				SELECT 1 FROM mentions search_status_mentions
				WHERE search_status_mentions.account_id = ?
				AND search_status_mentions.status_id = statuses.id
			)
		)
	)`, []int{0, 1}, account.ID, account.ID, account.ID, 2, account.ID, []int{3, 4}, account.ID)
}

func applyStatusContextFilterQuery(query *gorm.DB, account *models.Account) *gorm.DB {
	if account == nil || account.ID == 0 {
		return query.Where(`NOT EXISTS (
			SELECT 1 FROM accounts context_status_accounts
			WHERE context_status_accounts.id = statuses.account_id
			  AND context_status_accounts.silenced_at IS NOT NULL
		)`)
	}
	return query.
		Where(`(
			statuses.account_id = ?
			OR NOT EXISTS (
				SELECT 1 FROM blocks context_status_blocks
				WHERE context_status_blocks.account_id = ?
				  AND context_status_blocks.target_account_id = statuses.account_id
			)
		)`, account.ID, account.ID).
		Where(`(
			statuses.account_id = ?
			OR NOT EXISTS (
				SELECT 1 FROM mutes context_status_mutes
				WHERE context_status_mutes.account_id = ?
				  AND context_status_mutes.target_account_id = statuses.account_id
			)
		)`, account.ID, account.ID).
		Where(`(
			statuses.account_id = ?
			OR NOT EXISTS (
				SELECT 1
				FROM account_domain_blocks context_status_domain_blocks
				JOIN accounts context_status_accounts
				  ON context_status_accounts.id = statuses.account_id
				WHERE context_status_domain_blocks.account_id = ?
				  AND context_status_accounts.domain IS NOT NULL
				  AND lower(context_status_domain_blocks.domain) = lower(context_status_accounts.domain)
			)
		)`, account.ID, account.ID).
		Where(`(
			statuses.account_id = ?
			OR NOT EXISTS (
				SELECT 1 FROM accounts context_status_silenced_accounts
				WHERE context_status_silenced_accounts.id = statuses.account_id
				  AND context_status_silenced_accounts.silenced_at IS NOT NULL
				  AND NOT EXISTS (
					SELECT 1 FROM follows context_status_silenced_follows
					WHERE context_status_silenced_follows.account_id = ?
					  AND context_status_silenced_follows.target_account_id = statuses.account_id
				  )
			)
		)`, account.ID, account.ID)
}

func normalizeSearchQuery(query string) string {
	query = strings.TrimSpace(query)
	replacer := strings.NewReplacer("“", `"`, "”", `"`, "„", `"`, "«", `"`, "»", `"`, "「", `"`, "」", `"`, "『", `"`, "』", `"`, "《", `"`, "》", `"`)
	return replacer.Replace(query)
}

func searchTagQuery(query string) string {
	return strings.TrimPrefix(strings.TrimSpace(query), "#")
}

func searchIncludesType(requested string, kind string) bool {
	return requested == "" || requested == kind
}

func (s *Server) createApp(c *echo.Context) error {
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	app, err := appRegistrationFromRequest(c)
	if err != nil {
		if _, ok := err.(applicationInputError); ok {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: "+err.Error())
		}
		return err
	}
	uid := randomHex(32)
	secret := randomHex(32)

	app.UID = uid
	app.Secret = secret
	app.CreatedAt = time.Now().UTC()
	app.UpdatedAt = app.CreatedAt
	app.Confidential = true
	if err := s.db.Create(&app).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, restApplicationResponse(app, s.cfg.VapidPublicKey))
}

func appRegistrationFromRequest(c *echo.Context) (oauthApplication, error) {
	payload := map[string]any{}
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
			return oauthApplication{}, err
		}
	}
	value := func(keys ...string) string {
		for _, key := range keys {
			if raw, ok := payload[key]; ok {
				if item := rawScalarStringFromJSONValue(raw); strings.TrimSpace(item) != "" {
					return item
				}
			}
			if item := c.FormValue(key); strings.TrimSpace(item) != "" {
				return item
			}
		}
		return ""
	}
	optionalValue := func(keys ...string) string {
		for _, key := range keys {
			if raw, ok := payload[key]; ok {
				return rawScalarStringFromJSONValue(raw)
			}
			if c.Request().Form == nil {
				_ = c.Request().ParseForm()
			}
			if values, ok := c.Request().Form[key]; ok && len(values) > 0 {
				return values[0]
			}
		}
		return ""
	}
	name := value("client_name")
	redirectURI := value("redirect_uris")
	if strings.TrimSpace(name) == "" {
		return oauthApplication{}, errApplicationNameRequired
	}
	if err := validateOAuthApplicationName(name); err != nil {
		return oauthApplication{}, err
	}
	if strings.TrimSpace(redirectURI) == "" {
		return oauthApplication{}, errApplicationRedirectURIRequired
	}
	if err := validateOAuthRedirectURI(redirectURI); err != nil {
		return oauthApplication{}, err
	}
	scopes := normalizeApplicationScopes(nil, firstNonEmpty(value("scopes"), "read"))
	if err := validateOAuthApplicationScopes(scopes); err != nil {
		return oauthApplication{}, err
	}
	website := optionalValue("website")
	if err := validateOAuthApplicationWebsite(website); err != nil {
		return oauthApplication{}, err
	}
	return oauthApplication{
		Name:        name,
		RedirectURI: redirectURI,
		Scopes:      scopes,
		Website:     models.NullSafeString(website),
	}, nil
}

func rawScalarStringFromJSONValue(raw any) string {
	value, _ := raw.(string)
	return value
}

func rawStringFromJSONValue(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				continue
			}
			text = strings.TrimSpace(text)
			if text != "" {
				items = append(items, text)
			}
		}
		return strings.Join(items, " ")
	default:
		return ""
	}
}

func stringFromJSONValue(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				continue
			}
			text = strings.TrimSpace(text)
			if text != "" {
				items = append(items, text)
			}
		}
		return strings.Join(items, " ")
	default:
		return ""
	}
}

func (s *Server) manifest(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "max-age=180, public")
	title := s.settingStringValue("site_title", s.cfg.Title)
	return c.JSON(http.StatusOK, map[string]any{
		"id":               "/home",
		"name":             title,
		"short_name":       title,
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#191b22",
		"theme_color":      "#191b22",
		"icons":            s.manifestIcons(),
		"share_target": map[string]any{
			"url_template": "share?title={title}&text={text}&url={url}",
			"action":       "share",
			"method":       "GET",
			"enctype":      "application/x-www-form-urlencoded",
			"params": map[string]string{
				"title": "title",
				"text":  "text",
				"url":   "url",
			},
		},
		"shortcuts": []map[string]string{
			{"name": "Compose new post", "url": "/publish"},
			{"name": "Notifications", "url": "/notifications"},
		},
	})
}

func (s *Server) manifestIcons() []map[string]string {
	sizes := []string{"36", "48", "72", "96", "144", "192", "256", "384", "512"}
	icons := make([]map[string]string, 0, len(sizes))
	for _, size := range sizes {
		dimensions := size + "x" + size
		icons = append(icons, map[string]string{
			"src":     s.packAssetURL("media/icons/android-chrome-" + dimensions + ".png"),
			"sizes":   dimensions,
			"type":    "image/png",
			"purpose": "any maskable",
		})
	}
	return icons
}

func (s *Server) statusList(c *echo.Context, query *gorm.DB) error {
	return s.statusListWithStatus(c, query, http.StatusOK)
}

func (s *Server) statusListWithStatus(c *echo.Context, query *gorm.DB, statusCode int) error {
	if s.db == nil {
		return c.JSON(statusCode, []any{})
	}
	minID := c.QueryParam("min_id")
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where("statuses.id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where("statuses.id > ?", sinceID)
	}
	if queryParamValuePresent(c, "min_id") {
		query = query.Where("statuses.id > ?", minID)
	}
	if queryParamValuePresent(c, "min_id") {
		query = query.Order("statuses.id ASC")
	} else {
		query = query.Order("statuses.id DESC")
	}
	limitValue := limit(c, 20, 40)
	query = query.Limit(limitValue)

	var statuses []models.Status
	if err := query.Find(&statuses).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseStatuses(statuses)
	}
	account, _, _ := s.currentAccount(c)
	if err := s.hydrateStatusRelationships(statuses, account); err != nil {
		return err
	}

	if len(statuses) > 0 && statusListIncludesPagination(c) {
		last := statuses[len(statuses)-1].ID
		first := statuses[0].ID
		c.Response().Header().Set("Link", statusListPaginationLink(c, first, last, statusListIncludesNext(c, len(statuses), limitValue)))
	}

	filterContext := statusListFilterContext(c)
	return c.JSON(statusCode, serializeStatusesWithFilterContext(s.cfg, statuses, account, s.accountFilters(account), filterContext))
}

func statusListIncludesPagination(c *echo.Context) bool {
	path := c.Request().URL.Path
	if truthy(c.QueryParam("pinned")) && strings.Contains(path, "/api/v1/accounts/") && strings.HasSuffix(path, "/statuses") {
		return false
	}
	return true
}

func statusListIncludesNext(c *echo.Context, count int, limitValue int) bool {
	if strings.Contains(c.Request().URL.Path, "/api/v1/timelines/") {
		return true
	}
	return count == limitValue
}

func statusListPaginationLink(c *echo.Context, first int64, last int64, includeNext bool) string {
	return paginationLinkWithAllowedParams(c, first, last, "min_id", includeNext, true, statusListPaginationParamKeys(c))
}

func statusListPaginationParamKeys(c *echo.Context) []string {
	path := c.Request().URL.Path
	switch {
	case strings.Contains(path, "/api/v1/timelines/public"):
		return []string{"local", "remote", "limit", "only_media"}
	case strings.Contains(path, "/api/v1/timelines/home"):
		return []string{"local", "limit"}
	case strings.Contains(path, "/api/v1/timelines/tag/"):
		return []string{"local", "limit", "only_media"}
	case strings.Contains(path, "/api/v1/timelines/list/"):
		return []string{"limit"}
	case strings.Contains(path, "/api/v1/accounts/") && strings.HasSuffix(path, "/statuses"):
		return []string{"limit", "pinned", "tagged", "only_media", "exclude_replies", "exclude_reblogs"}
	default:
		return nil
	}
}

func reverseStatuses(statuses []models.Status) {
	for i, j := 0, len(statuses)-1; i < j; i, j = i+1, j-1 {
		statuses[i], statuses[j] = statuses[j], statuses[i]
	}
}

type statusJoinRow struct {
	ID       int64
	StatusID int64
}

func (s *Server) statusJoinList(c *echo.Context, account *models.Account, table string) error {
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	limitValue := limit(c, 20, 40)
	rows, err := s.statusJoinRows(c, account.ID, table, limitValue)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return c.JSON(http.StatusOK, []serializer.Status{})
	}

	statusIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		statusIDs = append(statusIDs, row.StatusID)
	}
	var statuses []models.Status
	if err := s.statusQuery().Where("statuses.id IN ?", statusIDs).Find(&statuses).Error; err != nil {
		return err
	}
	ordered := orderStatusesByJoinRows(rows, statuses)
	if err := s.hydrateStatusRelationships(ordered, account); err != nil {
		return err
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", statusJoinPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, len(rows) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeStatusesWithFilterContext(s.cfg, ordered, account, s.accountFilters(account), statusListFilterContext(c)))
}

func statusJoinPaginationLink(c *echo.Context, first int64, last int64, includeNext bool) string {
	return paginationLinkWithAllowedParams(c, first, last, "min_id", includeNext, true, []string{"limit"})
}

func (s *Server) statusJoinRows(c *echo.Context, accountID int64, table string, limitValue int) ([]statusJoinRow, error) {
	minID := c.QueryParam("min_id")
	query := s.db.Table(table).
		Select(table+".id, "+table+".status_id").
		Joins("JOIN statuses ON statuses.id = "+table+".status_id").
		Where(table+".account_id = ?", accountID).
		Where("statuses.deleted_at IS NULL")
	if queryParamValuePresent(c, "min_id") {
		query = query.Where(table+".id > ?", minID)
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where(table+".id < ?", maxID)
		}
		query = query.Order(table + ".id ASC")
	} else {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where(table+".id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where(table+".id > ?", sinceID)
		}
		query = query.Order(table + ".id DESC")
	}
	var rows []statusJoinRow
	err := query.Limit(limitValue).Scan(&rows).Error
	if queryParamValuePresent(c, "min_id") {
		reverseStatusJoinRows(rows)
	}
	return rows, err
}

func reverseStatusJoinRows(rows []statusJoinRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func orderStatusesByJoinRows(rows []statusJoinRow, statuses []models.Status) []models.Status {
	byID := make(map[int64]models.Status, len(statuses))
	for _, status := range statuses {
		byID[status.ID] = status
	}
	ordered := make([]models.Status, 0, len(rows))
	for _, row := range rows {
		status, ok := byID[row.StatusID]
		if ok {
			ordered = append(ordered, status)
		}
	}
	return ordered
}

func (s *Server) statusQuery() *gorm.DB {
	if s.db == nil {
		return nil
	}
	return s.db.Model(&models.Status{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Preload("StatusStat").
		Preload("Application").
		Preload("MediaAttachments").
		Preload("Mentions.Account.AccountStat").
		Preload("Tags").
		Preload("PreviewCards").
		Preload("Poll.Votes").
		Preload("Reblog.Account.AccountStat").
		Preload("Reblog.Account.User.Role").
		Preload("Reblog.StatusStat").
		Preload("Reblog.MediaAttachments").
		Preload("Reblog.Mentions.Account.AccountStat").
		Preload("Reblog.Tags").
		Preload("Reblog.PreviewCards").
		Preload("Reblog.Application").
		Preload("Reblog.Poll.Votes")
}

func (s *Server) findStatus(id string) (*models.Status, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var status models.Status
	err := s.statusQuery().Where("statuses.id = ? AND statuses.deleted_at IS NULL", id).First(&status).Error
	if err == nil {
		err = s.hydrateStatusCustomEmojis(&status)
	}
	if err == nil {
		s.hydrateStatusQuote(&status)
	}
	return &status, err
}

func (s *Server) findAccountByID(id string) (*models.Account, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var account models.Account
	err := accountSerializerPreloads(s.db).Where("id = ?", id).First(&account).Error
	if err == nil {
		err = s.hydrateAccountCustomEmojis(&account)
	}
	return &account, err
}

func (s *Server) findAccountByAcct(acct string) (*models.Account, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.findAccountByAcctTx(s.db, acct)
}

func (s *Server) findAccountByAcctTx(tx *gorm.DB, acct string) (*models.Account, error) {
	username, domain, _ := strings.Cut(normalizeAcctInput(acct), "@")
	return s.findAccountByUsernameDomainTx(tx, username, domain)
}

func (s *Server) findAccountByLookupAcct(acct string) (*models.Account, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	username, domain := railsLookupAcctParts(acct)
	return s.findAccountByUsernameDomainTx(s.db, username, domain)
}

func railsLookupAcctParts(acct string) (string, string) {
	raw := strings.TrimPrefix(strings.TrimSpace(acct), "@")
	parts := strings.Split(raw, "@")
	if len(parts) < 2 {
		return raw, ""
	}
	return parts[0], parts[1]
}

func (s *Server) findAccountByUsernameDomainTx(tx *gorm.DB, username string, domain string) (*models.Account, error) {
	var account models.Account
	query := accountSerializerPreloads(tx).Where("lower(username) = ?", strings.ToLower(username))
	if domain == "" || strings.EqualFold(domain, s.cfg.LocalDomain) {
		query = query.Where("domain IS NULL")
	} else {
		query = query.Where("lower(domain) = ?", strings.ToLower(domain))
	}
	err := query.First(&account).Error
	if err == nil {
		err = s.hydrateAccountCustomEmojis(&account)
	}
	return &account, err
}

func (s *Server) currentAccount(c *echo.Context) (*models.Account, string, error) {
	user, token, err := s.currentUser(c)
	if err != nil {
		return nil, "", err
	}
	var account models.Account
	if err := accountSerializerPreloads(s.db).Where("id = ?", user.AccountID).First(&account).Error; err != nil {
		return nil, "", err
	}
	if err := s.hydrateAccountCustomEmojis(&account); err != nil {
		return nil, "", err
	}
	return &account, token, nil
}

func (s *Server) currentAccountForWeb(c *echo.Context) (*models.Account, string, *models.User, error) {
	user, token, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return nil, "", nil, err
	}
	if err := s.updateUserSignInIfNeeded(c, user); err != nil {
		return nil, "", nil, err
	}
	var account models.Account
	if err := accountSerializerPreloads(s.db).Where("id = ?", user.AccountID).First(&account).Error; err != nil {
		return nil, "", nil, err
	}
	if err := s.hydrateAccountCustomEmojis(&account); err != nil {
		return nil, "", nil, err
	}
	return &account, token, user, nil
}

func (s *Server) requireFunctionalAccountForWeb(c *echo.Context) (*models.Account, string, *models.User, error) {
	account, token, user, err := s.currentAccountForWeb(c)
	if err != nil {
		_ = redirectToSignIn(c)
		return nil, "", nil, errWebAuthResponseHandled
	}
	user.Account = account
	if !webUserFunctional(*user, false) {
		_ = c.Redirect(http.StatusFound, "/auth/edit")
		return nil, "", nil, errWebAuthResponseHandled
	}
	return account, token, user, nil
}

func (s *Server) requireFunctionalOrMovedAccountForWeb(c *echo.Context) (*models.Account, string, *models.User, error) {
	account, token, user, err := s.currentAccountForWeb(c)
	if err != nil {
		_ = redirectToSignIn(c)
		return nil, "", nil, errWebAuthResponseHandled
	}
	user.Account = account
	if !webUserFunctional(*user, true) {
		_ = c.Redirect(http.StatusFound, "/auth/edit")
		return nil, "", nil, errWebAuthResponseHandled
	}
	return account, token, user, nil
}

var errWebAuthResponseHandled = errors.New("web authentication response handled")

func webAuthResponseError(err error) error {
	if errors.Is(err, errWebAuthResponseHandled) {
		return nil
	}
	return err
}

func (s *Server) requireFunctionalWebUser(c *echo.Context) (*models.User, string, bool, error) {
	_, token, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return nil, "", true, webAuthResponseError(err)
	}
	return user, token, false, nil
}

func webUserFunctional(user models.User, allowMoved bool) bool {
	if !user.ConfirmedAt.Valid || !user.Approved || user.Disabled {
		return false
	}
	account := user.Account
	if account == nil {
		return true
	}
	if account.SuspendedAt.Valid || account.Memorial {
		return false
	}
	if account.MovedToAccountID.Valid && !allowMoved {
		return false
	}
	return true
}

func (s *Server) currentUser(c *echo.Context) (*models.User, string, error) {
	return s.currentUserByToken(c, false)
}

func (s *Server) currentUserIncludingDisabled(c *echo.Context) (*models.User, string, error) {
	return s.currentUserByToken(c, true)
}

func (s *Server) currentUserByToken(c *echo.Context, includeDisabled bool) (*models.User, string, error) {
	token := requestToken(c)
	if token == "" || s.db == nil {
		if user, railsToken, err := s.currentUserByRailsSession(c, includeDisabled); err == nil {
			if err := s.setSessionCookie(c, railsToken); err != nil {
				return nil, "", err
			}
			return user, railsToken, nil
		}
		return nil, "", errors.New("missing token")
	}
	var accessToken models.OAuthAccessToken
	err := s.db.Where("token = ? AND revoked_at IS NULL", token).First(&accessToken).Error
	if err != nil {
		return nil, "", errors.New("invalid token")
	}
	if !accessToken.ResourceOwnerID.Valid {
		return nil, "", errApplicationTokenRequiresUser
	}
	var user models.User
	query := s.db.Where("id = ?", accessToken.ResourceOwnerID.Int64)
	if !includeDisabled {
		query = query.Where("disabled = false")
	}
	if err := query.First(&user).Error; err != nil {
		return nil, "", err
	}
	_ = s.trackAccessTokenUse(c, &accessToken)
	if paonSessionCookieMatches(c, token) {
		if refreshed, _ := c.Get("paon.session_cookie_refreshed").(bool); !refreshed {
			s.writeSessionCookie(c, token)
			c.Set("paon.session_cookie_refreshed", true)
		}
	}
	return &user, token, nil
}

func paonSessionCookieMatches(c *echo.Context, token string) bool {
	if c == nil || strings.TrimSpace(token) == "" || bearerToken(c) != "" {
		return false
	}
	cookie, err := c.Cookie(sessionCookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) == 1
}

func (s *Server) requireAccount(c *echo.Context) (*models.Account, string, error) {
	c.Response().Header().Set("Vary", "Authorization")
	user, token, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		if errors.Is(err, errApplicationTokenRequiresUser) {
			return nil, "", apiError(c, http.StatusUnprocessableEntity, "This method requires an authenticated user")
		}
		return nil, "", apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if err := s.requireFunctionalUser(c, *user); err != nil {
		return nil, "", err
	}
	if err := s.updateUserSignInIfNeeded(c, user); err != nil {
		return nil, "", err
	}
	var account models.Account
	if err := accountSerializerPreloads(s.db).Where("id = ?", user.AccountID).First(&account).Error; err != nil {
		return nil, "", apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if err := s.hydrateAccountCustomEmojis(&account); err != nil {
		return nil, "", err
	}
	return &account, token, nil
}

func (s *Server) requireUser(c *echo.Context) (*models.User, string, error) {
	user, token, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		if errors.Is(err, errApplicationTokenRequiresUser) {
			return nil, "", apiError(c, http.StatusUnprocessableEntity, "This method requires an authenticated user")
		}
		return nil, "", apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if err := s.requireFunctionalUser(c, *user); err != nil {
		return nil, "", err
	}
	if err := s.updateUserSignInIfNeeded(c, user); err != nil {
		return nil, "", err
	}
	return user, token, nil
}

func userCanUseAuthenticatedAPI(user models.User) bool {
	return !user.Disabled && user.Approved && user.ConfirmedAt.Valid
}

const userSignInUpdateFrequency = 24 * time.Hour

func (s *Server) updateUserSignInIfNeeded(c *echo.Context, user *models.User) error {
	now := time.Now().UTC()
	if s == nil || s.db == nil || user == nil || user.ID == 0 || !userNeedsSignInUpdate(*user, now) {
		return nil
	}
	updates := userSignInUpdates(*user, now)
	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		return err
	}
	user.LastSignInAt = updates["last_sign_in_at"].(sql.NullTime)
	user.CurrentSignInAt = updates["current_sign_in_at"].(sql.NullTime)
	return nil
}

func userNeedsSignInUpdate(user models.User, now time.Time) bool {
	return !user.CurrentSignInAt.Valid || user.CurrentSignInAt.Time.Before(now.Add(-userSignInUpdateFrequency))
}

func userSignInUpdates(user models.User, now time.Time) map[string]any {
	current := sql.NullTime{Time: now, Valid: true}
	last := user.CurrentSignInAt
	if !last.Valid {
		last = current
	}
	return map[string]any{
		"last_sign_in_at":    last,
		"current_sign_in_at": current,
		"updated_at":         now,
	}
}

func (s *Server) userCanUseAPI(user models.User) bool {
	return userCanUseAuthenticatedAPI(user) && !s.userAccountUnavailable(user)
}

func (s *Server) requireFunctionalUser(c *echo.Context, user models.User) error {
	if !user.ConfirmedAt.Valid {
		return apiError(c, http.StatusForbidden, "Your login is missing a confirmed e-mail address")
	}
	if !user.Approved {
		return apiError(c, http.StatusForbidden, "Your login is currently pending approval")
	}
	if user.Disabled || s.userAccountUnavailable(user) {
		return apiError(c, http.StatusForbidden, "Your login is currently disabled")
	}
	return nil
}

func (s *Server) userAccountUnavailable(user models.User) bool {
	account := user.Account
	if account != nil && account.ID != 0 {
		return account.SuspendedAt.Valid || account.Memorial || account.MovedToAccountID.Valid
	}
	if s == nil || s.db == nil || user.AccountID == 0 {
		return false
	}
	var loaded models.Account
	if err := s.db.Select("id", "suspended_at", "memorial", "moved_to_account_id").Where("id = ?", user.AccountID).First(&loaded).Error; err != nil {
		return false
	}
	return loaded.SuspendedAt.Valid || loaded.Memorial || loaded.MovedToAccountID.Valid
}

func serializeStatuses(cfg config.Config, statuses []models.Status, current *models.Account) []serializer.Status {
	out := make([]serializer.Status, 0, len(statuses))
	for _, status := range statuses {
		status = statusWithoutHashtagPreviewCards(status)
		out = append(out, serializer.StatusFromModel(cfg, status, current))
	}
	return out
}

func (s *Server) hydrateStatusRelationship(status *models.Status, current *models.Account) error {
	if status == nil || status.ID == 0 {
		return nil
	}
	statuses := []models.Status{*status}
	if err := s.hydrateStatusRelationships(statuses, current); err != nil {
		return err
	}
	*status = statuses[0]
	return nil
}

func (s *Server) hydrateStatusRelationships(statuses []models.Status, current *models.Account) error {
	if s.db == nil || len(statuses) == 0 {
		return nil
	}
	if err := s.hydrateStatusesCustomEmojis(statuses); err != nil {
		return err
	}
	s.hydrateStatusesQuote(statuses)
	if current == nil {
		return nil
	}
	statusIDs, conversationIDs := relationshipStatusIDs(statuses)
	if len(statusIDs) == 0 {
		return nil
	}

	favourites, err := s.relationshipIDSet("favourites", "status_id", current.ID, statusIDs)
	if err != nil {
		return err
	}
	bookmarks, err := s.relationshipIDSet("bookmarks", "status_id", current.ID, statusIDs)
	if err != nil {
		return err
	}
	pins, err := s.relationshipIDSet("status_pins", "status_id", current.ID, statusIDs)
	if err != nil {
		return err
	}
	reblogs, err := s.rebloggedStatusIDSet(current.ID, statusIDs)
	if err != nil {
		return err
	}
	mutes := map[int64]struct{}{}
	if len(conversationIDs) > 0 {
		mutes, err = s.relationshipIDSet("conversation_mutes", "conversation_id", current.ID, conversationIDs)
		if err != nil {
			return err
		}
	}

	for i := range statuses {
		applyStatusRelationshipFlags(&statuses[i], favourites, bookmarks, reblogs, mutes, pins)
	}
	return nil
}

func relationshipStatusIDs(statuses []models.Status) ([]int64, []int64) {
	statusSeen := map[int64]struct{}{}
	conversationSeen := map[int64]struct{}{}
	for i := range statuses {
		collectRelationshipIDs(statuses[i], statusSeen, conversationSeen)
	}
	return mapKeys(statusSeen), mapKeys(conversationSeen)
}

func collectRelationshipIDs(status models.Status, statusSeen map[int64]struct{}, conversationSeen map[int64]struct{}) {
	if status.ID != 0 {
		statusSeen[status.ID] = struct{}{}
	}
	if status.ConversationID.Valid {
		conversationSeen[status.ConversationID.Int64] = struct{}{}
	}
	if status.Reblog != nil {
		collectRelationshipIDs(*status.Reblog, statusSeen, conversationSeen)
	}
}

func (s *Server) relationshipIDSet(table string, idColumn string, accountID int64, ids []int64) (map[int64]struct{}, error) {
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	err := s.db.Table(table).Select(idColumn+" AS id").Where("account_id = ? AND "+idColumn+" IN ?", accountID, ids).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		out[row.ID] = struct{}{}
	}
	return out, nil
}

func (s *Server) rebloggedStatusIDSet(accountID int64, statusIDs []int64) (map[int64]struct{}, error) {
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	err := s.db.Model(&models.Status{}).
		Select("reblog_of_id AS id").
		Where("account_id = ? AND reblog_of_id IN ? AND deleted_at IS NULL", accountID, statusIDs).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		out[row.ID] = struct{}{}
	}
	return out, nil
}

func applyStatusRelationshipFlags(status *models.Status, favourites map[int64]struct{}, bookmarks map[int64]struct{}, reblogs map[int64]struct{}, mutes map[int64]struct{}, pins map[int64]struct{}) {
	if status == nil {
		return
	}
	_, status.FavouritedByCurrent = favourites[status.ID]
	_, status.BookmarkedByCurrent = bookmarks[status.ID]
	_, status.RebloggedByCurrent = reblogs[status.ID]
	_, status.PinnedByCurrent = pins[status.ID]
	if status.ConversationID.Valid {
		_, status.MutedByCurrent = mutes[status.ConversationID.Int64]
	}
	if status.Reblog != nil {
		applyStatusRelationshipFlags(status.Reblog, favourites, bookmarks, reblogs, mutes, pins)
	}
}

func mapKeys(values map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(values))
	for id := range values {
		out = append(out, id)
	}
	return out
}

func serializeAccounts(cfg config.Config, accounts []models.Account) []serializer.Account {
	out := make([]serializer.Account, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, serializer.AccountFromModel(cfg, account))
	}
	return out
}

func serializerTags(cfg config.Config, tags []models.Tag) []serializer.Tag {
	out := make([]serializer.Tag, 0, len(tags))
	for _, tag := range tags {
		out = append(out, serializer.Tag{Name: tag.DisplayNameValue(), URL: cfg.BaseURL() + "/tags/" + url.PathEscape(tag.Name)})
	}
	return out
}

func searchHashtagResults(cfg config.Config, tags []models.Tag, following map[int64]bool, includeFollowing bool) []serializer.TagDetail {
	out := make([]serializer.TagDetail, 0, len(tags))
	for _, tag := range tags {
		var follows *bool
		if includeFollowing {
			value := following[tag.ID]
			follows = &value
		}
		out = append(out, serializer.TagDetailFromModel(cfg, tag, follows))
	}
	return out
}

func (s *Server) searchTagFollowingMap(account *models.Account, tags []models.Tag) (map[int64]bool, error) {
	out := map[int64]bool{}
	if account == nil || s.db == nil || len(tags) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(tags))
	for _, tag := range tags {
		if tag.ID > 0 {
			ids = append(ids, tag.ID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	var follows []models.TagFollow
	if err := s.db.Model(&models.TagFollow{}).
		Select("tag_id").
		Where("account_id = ? AND tag_id IN ?", account.ID, ids).
		Find(&follows).Error; err != nil {
		return nil, err
	}
	for _, follow := range follows {
		out[follow.TagID] = true
	}
	return out, nil
}

type apiHTTPError struct {
	status  int
	message string
	headers http.Header
	cause   error
}

func (e apiHTTPError) Error() string {
	return e.message
}

func (e apiHTTPError) Unwrap() error { return e.cause }

type noContentHTTPError struct {
	status int
}

func (e noContentHTTPError) Error() string {
	return http.StatusText(e.status)
}

func noContentError(status int) error {
	return noContentHTTPError{status: status}
}

func apiError(c *echo.Context, status int, message string) error {
	_ = c
	return apiHTTPError{status: status, message: message}
}

func publicAPIError(status int, message string, cause error, headers http.Header) error {
	return apiHTTPError{status: status, message: message, cause: cause, headers: headers}
}

func renderEmpty(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{})
}

func handleAPIError(c *echo.Context, err error) {
	handleAPIErrorWithHTML(c, err, func(_ *echo.Context, status int, message string) string {
		return htmlErrorPage(status, message)
	})
}

func (s *Server) handleHTTPError(c *echo.Context, err error) {
	handleAPIErrorWithHTML(c, err, s.serverErrorPage)
}

type errorHTMLRenderer func(c *echo.Context, status int, publicMessage string) string

func handleAPIErrorWithHTML(c *echo.Context, err error, renderHTML errorHTMLRenderer) {
	var oauthErr oauthTokenError
	if errors.As(err, &oauthErr) {
		if oauthErr.code == "invalid_client" {
			c.Response().Header().Set("WWW-Authenticate", `Basic realm="Doorkeeper"`)
		} else if oauthErr.code == "invalid_token" {
			c.Response().Header().Set("WWW-Authenticate", `Bearer realm="Doorkeeper"`)
		}
		_ = c.JSON(oauthErr.status, map[string]string{
			"error":             oauthErr.code,
			"error_description": oauthErr.description,
		})
		return
	}

	var noContentErr noContentHTTPError
	if errors.As(err, &noContentErr) {
		_ = c.NoContent(noContentErr.status)
		return
	}

	var apiErr apiHTTPError
	if errors.As(err, &apiErr) {
		if apiErr.cause != nil && apiErr.status >= http.StatusInternalServerError {
			logUnexpectedHTTPError(c, apiErr.cause)
		}
		copyErrorHeaders(c.Response().Header(), apiErr.headers)
		if errorRequestWantsHTML(c) {
			_ = c.HTML(apiErr.status, renderHTML(c, apiErr.status, apiErr.message))
			return
		}
		_ = c.JSON(apiErr.status, map[string]string{"error": apiErr.message})
		return
	}

	var echoErr *echo.HTTPError
	if errors.As(err, &echoErr) {
		if echoErr.Code >= http.StatusInternalServerError {
			logUnexpectedHTTPError(c, err)
			writeGenericHTTPError(c, echoErr.Code, renderHTML)
			return
		}
		message := echoErr.Message
		if message == "" {
			message = http.StatusText(echoErr.Code)
		}
		if errorRequestWantsHTML(c) {
			_ = c.HTML(echoErr.Code, renderHTML(c, echoErr.Code, fmt.Sprint(message)))
			return
		}
		_ = c.JSON(echoErr.Code, map[string]string{"error": message})
		return
	}

	if status := echo.StatusCode(err); status != 0 {
		if status >= http.StatusInternalServerError {
			logUnexpectedHTTPError(c, err)
			writeGenericHTTPError(c, status, renderHTML)
			return
		}
		message := http.StatusText(status)
		if message == "" {
			message = "Request failed"
		}
		if errorRequestWantsHTML(c) {
			_ = c.HTML(status, renderHTML(c, status, message))
			return
		}
		_ = c.JSON(status, map[string]string{"error": message})
		return
	}

	logUnexpectedHTTPError(c, err)
	writeGenericHTTPError(c, http.StatusInternalServerError, renderHTML)
}

func copyErrorHeaders(destination http.Header, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func writeGenericHTTPError(c *echo.Context, status int, renderHTML errorHTMLRenderer) {
	message := http.StatusText(status)
	if message == "" {
		status = http.StatusInternalServerError
		message = http.StatusText(status)
	}
	if errorRequestWantsHTML(c) {
		_ = c.HTML(status, renderHTML(c, status, message))
		return
	}
	_ = c.JSON(status, map[string]string{"error": message})
}

func logUnexpectedHTTPError(c *echo.Context, err error) {
	method := ""
	path := ""
	requestID := ""
	if c != nil {
		requestID = c.Response().Header().Get(echo.HeaderXRequestID)
		if requestID == "" {
			requestID, _ = c.Get("request_id").(string)
		}
		if request := c.Request(); request != nil {
			method = request.Method
			if request.URL != nil {
				path = request.URL.Path
			}
		}
	}
	log.Printf("level=ERROR event=http_request_failed request_id=%q method=%q path=%q error_type=%q error=%q",
		requestID,
		method,
		path,
		fmt.Sprintf("%T", err),
		activityPubErrorLogValue(err),
	)
}

func errorRequestWantsHTML(c *echo.Context) bool {
	if c == nil || c.Request() == nil || c.Request().URL == nil {
		return false
	}
	path := c.Request().URL.Path
	if path == "/api" || strings.HasPrefix(path, "/api/") || path == "/oauth/token" {
		return false
	}
	accept := c.Request().Header.Get("Accept")
	if !strings.Contains(accept, "text/html") {
		return false
	}
	if acceptsJSON(accept) {
		return false
	}
	return true
}

func htmlErrorPage(status int, message string) string {
	return errorPageHTML(status, "en", "", "default", message)
}

func (s *Server) serverErrorPage(c *echo.Context, status int, publicMessage string) string {
	locale := s.webLocale(c, nil)
	siteTitle := firstNonEmpty(s.settingStringValue("site_title", s.cfg.Title), s.cfg.Title)
	theme := adminThemeSetting(s.settingStringValue("theme", "default"))
	return errorPageHTML(status, locale, siteTitle, theme, publicMessage)
}

func limit(c *echo.Context, fallback int, max int) int {
	values, ok := c.Request().URL.Query()["limit"]
	if !ok {
		return fallback
	}
	raw := ""
	if len(values) > 0 {
		raw = values[0]
	}
	value := rubyToI(raw)
	if value < 0 {
		value = -value
	}
	if value > max {
		return max
	}
	return value
}

func rubyToI(raw string) int {
	text := strings.TrimLeft(raw, " \t\r\n")
	if text == "" {
		return 0
	}
	sign := 1
	switch text[0] {
	case '+':
		text = text[1:]
	case '-':
		sign = -1
		text = text[1:]
	}
	value := 0
	found := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch < '0' || ch > '9' {
			break
		}
		found = true
		value = value*10 + int(ch-'0')
	}
	if !found {
		return 0
	}
	return sign * value
}

func bearerToken(c *echo.Context) string {
	value := c.Request().Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func requestToken(c *echo.Context) string {
	if token := bearerToken(c); token != "" {
		return token
	}
	if token := requestRawParamValue(c, "access_token"); token != "" {
		return token
	}
	if token := requestRawParamValue(c, "bearer_token"); token != "" {
		return token
	}
	if token := webSocketProtocolToken(c.Request().Header.Get("Sec-WebSocket-Protocol")); token != "" {
		return token
	}
	return sessionToken(c)
}

func requestRawParamValue(c *echo.Context, key string) string {
	req := c.Request()
	_ = req.ParseForm()
	if values, ok := req.PostForm[key]; ok {
		return lastValue(values)
	}
	if value, ok := requestRawJSONParamValue(c, key); ok {
		return value
	}
	if values, ok := req.URL.Query()[key]; ok {
		return lastValue(values)
	}
	return ""
}

const (
	requestJSONParamsContextKey = "paon.request_json_params"
	maxRequestJSONParamsBytes   = 1 << 20
)

type requestJSONParams struct {
	values map[string]json.RawMessage
}

func requestRawJSONParamValue(c *echo.Context, key string) (string, bool) {
	params := cachedRequestJSONParams(c)
	raw, ok := params[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func cachedRequestJSONParams(c *echo.Context) map[string]json.RawMessage {
	if cached, ok := c.Get(requestJSONParamsContextKey).(requestJSONParams); ok {
		return cached.values
	}
	values := readRequestJSONParams(c.Request())
	c.Set(requestJSONParamsContextKey, requestJSONParams{values: values})
	return values
}

func readRequestJSONParams(req *http.Request) map[string]json.RawMessage {
	if req == nil || req.Body == nil || !requestContentTypeIsJSON(req.Header.Get(echo.HeaderContentType)) {
		return nil
	}
	if req.ContentLength > maxRequestJSONParamsBytes {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxRequestJSONParamsBytes+1))
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), req.Body))
	if err != nil || len(body) > maxRequestJSONParamsBytes {
		return nil
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(body, &values) != nil {
		return nil
	}
	return values
}

func requestContentTypeIsJSON(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return mediaType == echo.MIMEApplicationJSON || strings.HasSuffix(mediaType, "+json")
}

func firstRawRequestParamValue(c *echo.Context, keys ...string) string {
	for _, key := range keys {
		if value := requestRawParamValue(c, key); value != "" {
			return value
		}
	}
	return ""
}

func webSocketProtocolToken(value string) string {
	for _, candidate := range strings.Split(value, ",") {
		if token := strings.TrimSpace(candidate); token != "" {
			return token
		}
	}
	return ""
}

func visibilityValue(value string) int {
	switch value {
	case "limited":
		return 4
	case "unlisted":
		return 1
	case "private":
		return 2
	case "direct":
		return 3
	default:
		return 0
	}
}

func validStatusVisibility(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "public", "unlisted", "private", "direct", "limited":
		return true
	default:
		return false
	}
}

func validReblogVisibility(value string) bool {
	switch strings.TrimSpace(value) {
	case "direct", "limited":
		return false
	default:
		return validStatusVisibility(value)
	}
}

func (s *Server) reblogVisibility(account models.Account, target models.Status, requested string) int {
	if target.Visibility > 1 {
		return target.Visibility
	}
	return s.statusVisibility(account, requested)
}

func (s *Server) statusVisibility(account models.Account, requested string) int {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if account.SilencedAt.Valid && requested == "public" {
			return visibilityValue("unlisted")
		}
		return visibilityValue(requested)
	}
	defaultVisibility := serializer.UserDefaultPrivacy(userSettingsForAccount(account), account)
	if account.SilencedAt.Valid && defaultVisibility == "public" {
		return visibilityValue("unlisted")
	}
	return visibilityValue(defaultVisibility)
}

func (s *Server) statusLanguageForAccount(requested string, current sql.NullString, account models.Account) sql.NullString {
	settings := userSettingsForAccount(account)
	candidates := []string{requested}
	if current.Valid {
		candidates = append(candidates, current.String)
	}
	candidates = append(candidates, stringSettingValue(settings, "default_language"))
	if account.User.Locale.Valid {
		candidates = append(candidates, account.User.Locale.String)
	}
	candidates = append(candidates, s.cfg.Locale())
	if language := validStatusLocaleCascade(candidates...); language != "" {
		return sql.NullString{String: language, Valid: true}
	}
	return sql.NullString{}
}

func validStatusLocaleCascade(values ...string) string {
	for _, value := range values {
		if locale := validStatusLocaleOrNil(value); locale != "" {
			return locale
		}
	}
	return ""
}

func validStatusLocaleOrNil(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if supportedStatusLocale(value) {
		return value
	}
	for _, sep := range []string{"_", "-"} {
		if idx := strings.Index(value, sep); idx > 0 {
			code := value[:idx]
			if supportedStatusLocale(code) {
				return code
			}
		}
	}
	return ""
}

func supportedStatusLocale(locale string) bool {
	for _, supported := range serializer.SupportedLanguageCodes() {
		if supported == locale {
			return true
		}
	}
	return false
}

func stringSettingValue(settings map[string]any, key string) string {
	if value, ok := settings[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func statusNullStringEqual(left sql.NullString, right sql.NullString) bool {
	if left.Valid != right.Valid {
		return false
	}
	if !left.Valid {
		return true
	}
	return left.String == right.String
}

func userSettingsForAccount(account models.Account) map[string]any {
	settings := map[string]any{}
	if account.User.Settings.Valid && strings.TrimSpace(account.User.Settings.String) != "" {
		_ = json.Unmarshal([]byte(account.User.Settings.String), &settings)
	}
	return settings
}

func (s *Server) maxStatusChars() int {
	if s != nil && (s.cfg.StatusMaxCharsSet || s.cfg.StatusMaxChars > 0) {
		return s.cfg.StatusMaxChars
	}
	return 5000
}

func statusLengthTooLong(text string, spoilerText string, max int) bool {
	return statusCountableLength(text, spoilerText) > max
}

func statusCountableLength(text string, spoilerText string) int {
	return graphemeLength(spoilerText) + graphemeLength(statusCountableText(text))
}

func statusCountableText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	replaced := statusLengthURLPattern.ReplaceAllStringFunc(text, func(match string) string {
		prefix := ""
		urlText := match
		if !statusLengthTokenStartsURL(match) {
			prefix = match[:1]
			urlText = match[1:]
		}
		if len([]rune(urlText)) > 4096 {
			return match
		}
		return prefix + strings.Repeat("x", 23)
	})
	return statusLengthRemoteMention.ReplaceAllStringFunc(replaced, func(match string) string {
		indices := statusLengthRemoteMention.FindStringSubmatchIndex(match)
		if len(indices) < 8 {
			return match
		}
		prefix := match[indices[2]:indices[3]]
		username := match[indices[4]:indices[5]]
		domain := match[indices[6]:indices[7]]
		if !statusLengthMentionDomainCountable(domain) {
			return match
		}
		return prefix + "@" + username
	})
}

func statusLengthTokenStartsURL(token string) bool {
	return strings.HasPrefix(token, "http://") ||
		strings.HasPrefix(token, "https://") ||
		strings.HasPrefix(token, "dat://") ||
		strings.HasPrefix(token, "dweb://") ||
		strings.HasPrefix(token, "ipfs://") ||
		strings.HasPrefix(token, "ipns://") ||
		strings.HasPrefix(token, "ssb://") ||
		strings.HasPrefix(token, "gopher://") ||
		strings.HasPrefix(token, "gemini://") ||
		strings.HasPrefix(token, "xmpp:") ||
		strings.HasPrefix(token, "magnet:?")
}

func statusLengthMentionDomainCountable(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
	}
	return true
}

func (s *Server) maxMediaAttachments() int {
	if s != nil && (s.cfg.MaxMediaSet || s.cfg.MaxMedia > 0) {
		return s.cfg.MaxMedia
	}
	return 4
}

func statusSensitiveValue(payload statusUpdatePayload) bool {
	spoilerTextPresent := strings.TrimSpace(payload.SpoilerText) != ""
	if payload.HasSensitive {
		return payload.Sensitive || spoilerTextPresent
	}
	return spoilerTextPresent
}

func statusSensitiveForCreate(payload statusUpdatePayload, account models.Account) bool {
	if payload.HasSensitive {
		return statusSensitiveValue(payload)
	}
	return boolSettingValueFromMap(userSettingsForAccount(account), "default_sensitive") || strings.TrimSpace(payload.SpoilerText) != ""
}

func boolSettingValueFromMap(settings map[string]any, key string) bool {
	value, ok := settings[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) == "true" || strings.TrimSpace(typed) == "1"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return false
	}
}

func applyCreateSpoilerTextFallback(payload *statusUpdatePayload) {
	if payload == nil {
		return
	}
	if strings.TrimSpace(payload.Status) != "" || strings.TrimSpace(payload.SpoilerText) == "" {
		return
	}
	payload.Status = payload.SpoilerText
	payload.SpoilerText = ""
	payload.HasStatus = true
	payload.HasSpoilerText = false
}

func applyUpdateSpoilerTextFallback(payload *statusUpdatePayload) {
	if payload == nil || !payload.HasStatus {
		return
	}
	applyCreateSpoilerTextFallback(payload)
}

func statusUpdatePayloadSubmitted(payload statusUpdatePayload) bool {
	return payload.HasStatus ||
		payload.HasMediaIDs ||
		len(payload.MediaAttributes) > 0 ||
		payload.HasSensitive ||
		payload.HasSpoilerText ||
		payload.HasLanguage ||
		payload.HasPoll
}

func statusUpdateHasSignificantChanges(status models.Status, payload statusUpdatePayload, nextText string, nextSpoilerText string, nextLanguage sql.NullString) bool {
	if !statusNullStringEqual(status.Language, nextLanguage) {
		return true
	}
	if payload.HasStatus && status.Text != nextText {
		return true
	}
	if payload.HasSpoilerText && status.SpoilerText != nextSpoilerText {
		return true
	}
	if (payload.HasSensitive || payload.HasSpoilerText) && status.Sensitive != statusSensitiveValue(payload) {
		return true
	}
	if payload.HasMediaIDs && !sameInt64Array(status.OrderedMediaAttachmentIDs, mediaIDsToInt64Array(payload.MediaIDs)) {
		return true
	}
	if len(payload.MediaAttributes) > 0 {
		return true
	}
	if payload.HasPoll {
		if payload.Poll == nil || len(payload.Poll.Options) == 0 {
			return status.PollID.Valid
		}
		return true
	}
	return false
}

func cleanIdempotencyKey(key string) string {
	return strings.TrimSpace(key)
}

func statusIdempotencyRedisKey(cfg config.Config, accountID int64, key string) string {
	return redisConfig(cfg).prefix + "idempotency:status:" + strconv.FormatInt(accountID, 10) + ":" + key
}

func (s *Server) statusIdempotencyDuplicate(ctx context.Context, accountID int64, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	value, err := s.redisCommand(ctx, "GET", statusIdempotencyRedisKey(s.cfg, accountID, key))
	if err != nil {
		return "", false
	}
	id, ok := value.(string)
	return id, ok && strings.TrimSpace(id) != ""
}

func (s *Server) rememberStatusIdempotency(ctx context.Context, accountID int64, key string, statusID int64) {
	if key == "" || statusID <= 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, _ = s.redisCommand(ctx, "SETEX", statusIdempotencyRedisKey(s.cfg, accountID, key), "3600", strconv.FormatInt(statusID, 10))
}

func scheduledStatusParamsFromPayload(payload statusCreatePayload, mediaIDs []string) (models.JSONValue, error) {
	params := map[string]any{
		"status":       payload.Status,
		"sensitive":    statusSensitiveValue(payload.statusUpdatePayload),
		"spoiler_text": payload.SpoilerText,
		"visibility":   payload.Visibility,
		"scheduled_at": nil,
	}
	if payload.ApplicationID.Valid {
		params["application_id"] = payload.ApplicationID.Int64
	}
	if payload.Language != "" {
		params["language"] = payload.Language
	}
	if payload.InReplyToID != "" {
		params["in_reply_to_id"] = payload.InReplyToID
	}
	if payload.QuoteID != "" {
		params["quote_id"] = payload.QuoteID
	}
	if payload.HasAllowedMentions {
		params["allowed_mentions"] = payload.AllowedMentions
	}
	if len(mediaIDs) > 0 {
		params["media_ids"] = mediaIDs
	}
	if payload.HasPoll && payload.Poll != nil {
		params["poll"] = map[string]any{
			"options":     payload.Poll.Options,
			"multiple":    payload.Poll.Multiple,
			"hide_totals": payload.Poll.HideTotals,
			"expires_in":  payload.Poll.ExpiresIn,
		}
	}
	raw, err := json.Marshal(params)
	return models.JSONValue(raw), err
}

func attachMediaToScheduledStatus(tx *gorm.DB, accountID int64, scheduledStatusID int64, mediaIDs []string, now time.Time) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	media, err := loadScheduledStatusMediaAttachments(tx, accountID, mediaIDs)
	if err != nil {
		return err
	}
	if len(media) != len(mediaIDs) {
		return errInvalidMediaAttachment
	}
	if err := validateStatusMediaAttachments(media); err != nil {
		return err
	}
	res := tx.Model(&models.MediaAttachment{}).
		Where("account_id = ? AND status_id IS NULL AND id IN ?", accountID, mediaIDs).
		Updates(map[string]any{"scheduled_status_id": scheduledStatusID, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != int64(len(mediaIDs)) {
		return errInvalidMediaAttachment
	}
	return nil
}

func parseReblogPayload(c *echo.Context) (reblogPayload, error) {
	var payload reblogPayload
	if requestIsJSON(c) {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["visibility"]; ok && string(value) != "null" {
			_ = json.Unmarshal(value, &payload.Visibility)
		}
		return payload, nil
	}
	if value, ok := formField(c, "visibility"); ok {
		payload.Visibility = value
	}
	return payload, nil
}

func parseStatusCreatePayload(c *echo.Context) (statusCreatePayload, error) {
	var payload statusCreatePayload
	contentType := strings.ToLower(c.Request().Header.Get("Content-Type"))
	if strings.Contains(contentType, "json") {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["status"]; ok {
			payload.HasStatus = true
			_ = json.Unmarshal(value, &payload.Status)
		}
		if value, ok := raw["media_ids"]; ok {
			payload.HasMediaIDs = true
			payload.MediaIDs = stringSliceFromRaw(value)
		}
		if value, ok := raw["media_attributes"]; ok {
			payload.MediaAttributes = mediaAttributesFromRaw(value)
		}
		if value, ok := raw["sensitive"]; ok {
			payload.HasSensitive = true
			_ = json.Unmarshal(value, &payload.Sensitive)
		}
		if value, ok := raw["spoiler_text"]; ok {
			payload.HasSpoilerText = true
			_ = json.Unmarshal(value, &payload.SpoilerText)
		}
		if value, ok := raw["language"]; ok {
			payload.HasLanguage = true
			_ = json.Unmarshal(value, &payload.Language)
		}
		if value, ok := raw["poll"]; ok {
			payload.HasPoll = true
			if string(value) != "null" {
				var poll pollUpdatePayload
				if err := json.Unmarshal(value, &poll); err != nil {
					return payload, err
				}
				payload.Poll = &poll
			}
		}
		if value, ok := raw["visibility"]; ok {
			_ = json.Unmarshal(value, &payload.Visibility)
		}
		if value, ok := raw["in_reply_to_id"]; ok && string(value) != "null" {
			payload.InReplyToID = stringValueFromRaw(value)
		}
		if value, ok := raw["scheduled_at"]; ok && string(value) != "null" {
			payload.ScheduledAt = stringValueFromRaw(value)
		}
		if value, ok := raw["quote_id"]; ok && string(value) != "null" {
			payload.QuoteID = stringValueFromRaw(value)
		}
		if value, ok := raw["allowed_mentions"]; ok {
			payload.HasAllowedMentions = true
			payload.AllowedMentions = stringSliceFromRaw(value)
		}
		return payload, nil
	}

	update, err := parseStatusUpdatePayload(c)
	if err != nil {
		return payload, err
	}
	payload.statusUpdatePayload = update
	if value, ok := formField(c, "visibility"); ok {
		payload.Visibility = value
	}
	if value, ok := formField(c, "in_reply_to_id"); ok {
		payload.InReplyToID = value
	}
	if value, ok := formField(c, "scheduled_at"); ok {
		payload.ScheduledAt = value
	}
	if value, ok := formField(c, "quote_id"); ok {
		payload.QuoteID = value
	}
	values, _ := c.FormValues()
	if allowed, ok := statusAllowedMentionsFromForm(values); ok {
		payload.AllowedMentions = allowed
		payload.HasAllowedMentions = true
	}
	return payload, nil
}

func parseStatusUpdatePayload(c *echo.Context) (statusUpdatePayload, error) {
	var payload statusUpdatePayload
	contentType := strings.ToLower(c.Request().Header.Get("Content-Type"))
	if strings.Contains(contentType, "json") {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["status"]; ok {
			payload.HasStatus = true
			_ = json.Unmarshal(value, &payload.Status)
		}
		if value, ok := raw["media_ids"]; ok {
			payload.HasMediaIDs = true
			payload.MediaIDs = stringSliceFromRaw(value)
		}
		if value, ok := raw["media_attributes"]; ok {
			payload.MediaAttributes = mediaAttributesFromRaw(value)
		}
		if value, ok := raw["sensitive"]; ok {
			payload.HasSensitive = true
			_ = json.Unmarshal(value, &payload.Sensitive)
		}
		if value, ok := raw["spoiler_text"]; ok {
			payload.HasSpoilerText = true
			_ = json.Unmarshal(value, &payload.SpoilerText)
		}
		if value, ok := raw["language"]; ok {
			payload.HasLanguage = true
			_ = json.Unmarshal(value, &payload.Language)
		}
		if value, ok := raw["poll"]; ok {
			payload.HasPoll = true
			if string(value) != "null" {
				var poll pollUpdatePayload
				if err := json.Unmarshal(value, &poll); err != nil {
					return payload, err
				}
				payload.Poll = &poll
			}
		}
		return payload, nil
	}

	if value, ok := formField(c, "status"); ok {
		payload.Status = value
		payload.HasStatus = true
	}
	values, _ := c.FormValues()
	if _, ok := values["media_ids[]"]; ok {
		payload.HasMediaIDs = true
		payload.MediaIDs = mediaIDsFromForm(c)
	}
	payload.MediaAttributes = mediaAttributesFromForm(values)
	if value, ok := formField(c, "sensitive"); ok {
		payload.HasSensitive = true
		payload.Sensitive = formBoolValue(value)
	}
	if value, ok := formField(c, "spoiler_text"); ok {
		payload.SpoilerText = value
		payload.HasSpoilerText = true
	}
	if value, ok := formField(c, "language"); ok {
		payload.Language = value
		payload.HasLanguage = true
	}
	if poll, ok := pollPayloadFromFormValues(values); ok {
		payload.Poll = poll
		payload.HasPoll = true
	}
	return payload, nil
}

func pollPayloadFromFormValues(values map[string][]string) (*pollUpdatePayload, bool) {
	if len(values) == 0 {
		return nil, false
	}
	hasPoll := false
	poll := &pollUpdatePayload{}
	if _, ok := values["poll[options][]"]; ok {
		hasPoll = true
		for _, value := range values["poll[options][]"] {
			if option := strings.TrimSpace(value); option != "" {
				poll.Options = append(poll.Options, option)
			}
		}
	}
	if value, ok := firstFormValue(values, "poll[multiple]"); ok {
		poll.Multiple = formBoolValue(value)
		hasPoll = true
	}
	if value, ok := firstFormValue(values, "poll[hide_totals]"); ok {
		poll.HideTotals = formBoolValue(value)
		hasPoll = true
	}
	if value, ok := firstFormValue(values, "poll[expires_in]"); ok {
		if expiresIn, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			poll.ExpiresIn = expiresIn
		}
		hasPoll = true
	}
	if !hasPoll {
		return nil, false
	}
	return poll, true
}

func statusAllowedMentionsFromForm(values map[string][]string) ([]string, bool) {
	if len(values) == 0 {
		return nil, false
	}
	out := []string{}
	found := false
	if _, ok := values["allowed_mentions[]"]; ok {
		found = true
		for _, value := range values["allowed_mentions[]"] {
			if item := strings.TrimSpace(value); item != "" {
				out = append(out, item)
			}
		}
	}
	return out, found
}

func firstFormValue(values map[string][]string, key string) (string, bool) {
	items, ok := values[key]
	if !ok || len(items) == 0 {
		return "", false
	}
	return items[0], true
}

func formBoolValue(value string) bool {
	return truthy(value)
}

func stringSliceFromRaw(raw json.RawMessage) []string {
	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		return values
	}
	var generic []any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil
	}
	out := make([]string, 0, len(generic))
	for _, value := range generic {
		out = append(out, strings.TrimSuffix(strings.TrimSuffix(fmt.Sprint(value), ".0"), "."))
	}
	return out
}

func stringValueFromRaw(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprint(generic), ".0"), ".")
}

func mediaAttributesFromRaw(raw json.RawMessage) []mediaAttributePayload {
	var generic []map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil
	}
	out := make([]mediaAttributePayload, 0, len(generic))
	for _, item := range generic {
		attr := mediaAttributePayload{ID: strings.TrimSuffix(strings.TrimSuffix(fmt.Sprint(item["id"]), ".0"), ".")}
		if value, ok := item["description"].(string); ok {
			attr.Description = &value
		}
		if value, ok := item["focus"].(string); ok {
			attr.Focus = &value
		}
		out = append(out, attr)
	}
	return out
}

func mediaAttributesFromForm(values map[string][]string) []mediaAttributePayload {
	byIndex := map[string]*mediaAttributePayload{}
	for key := range values {
		index, field, ok := mediaAttributeFormPath(key)
		if !ok {
			continue
		}
		attr := byIndex[index]
		if attr == nil {
			attr = &mediaAttributePayload{}
			byIndex[index] = attr
		}
		value := lastFormValue(values, key)
		switch field {
		case "id":
			attr.ID = value
		case "description":
			attr.Description = &value
		case "focus":
			attr.Focus = &value
		}
	}
	indexes := make([]string, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		left, leftErr := strconv.Atoi(indexes[i])
		right, rightErr := strconv.Atoi(indexes[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return indexes[i] < indexes[j]
	})
	out := make([]mediaAttributePayload, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, *byIndex[index])
	}
	return out
}

func mediaAttributeFormPath(key string) (string, string, bool) {
	const prefix = "media_attributes["
	if !strings.HasPrefix(key, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, prefix)
	end := strings.Index(rest, "]")
	if end < 0 {
		return "", "", false
	}
	index := rest[:end]
	rest = rest[end+1:]
	if index == "" || !strings.HasPrefix(rest, "[") || !strings.HasSuffix(rest, "]") {
		return "", "", false
	}
	field := strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
	switch field {
	case "id", "description", "focus":
		return index, field, true
	default:
		return "", "", false
	}
}

func updateStatusMedia(tx *gorm.DB, accountID int64, statusID int64, mediaIDs []string, mediaIntIDs models.Int64Array, attributes []mediaAttributePayload, requireAll bool) (models.Int64Array, error) {
	acceptedMediaIDs := mediaIntIDs
	if len(mediaIDs) > 0 {
		media, err := loadStatusMediaAttachments(tx, accountID, statusID, mediaIDs)
		if err != nil {
			return nil, err
		}
		if requireAll && len(media) != len(mediaIDs) {
			return nil, errInvalidMediaAttachment
		}
		if err := validateStatusMediaAttachments(media); err != nil {
			return nil, err
		}
		acceptedMediaIDs = orderedExistingMediaIDs(mediaIntIDs, media)
		if len(acceptedMediaIDs) == 0 {
			return acceptedMediaIDs, nil
		}
		if err := assignMediaAttachmentsToStatus(tx, accountID, statusID, []int64(acceptedMediaIDs), time.Now().UTC()).Error; err != nil {
			return nil, err
		}
	}
	allowed := map[int64]struct{}{}
	for _, id := range acceptedMediaIDs {
		allowed[id] = struct{}{}
	}
	for _, attr := range attributes {
		if attr.ID == "" || (attr.Description == nil && attr.Focus == nil) {
			continue
		}
		mediaID := railsToInt64(attr.ID)
		if _, ok := allowed[mediaID]; !ok {
			continue
		}
		updates := map[string]any{"updated_at": time.Now().UTC()}
		if attr.Description != nil {
			if len([]rune(*attr.Description)) > maxMediaDescriptionLength {
				return nil, errInvalidMediaAttachment
			}
			updates["description"] = sql.NullString{String: *attr.Description, Valid: *attr.Description != ""}
		}
		if attr.Focus != nil && strings.TrimSpace(*attr.Focus) != "" {
			var attachment models.MediaAttachment
			if err := tx.Select("file_meta").
				Where("account_id = ? AND status_id = ? AND id = ?", accountID, statusID, mediaID).
				First(&attachment).Error; err != nil {
				return nil, err
			}
			meta, ok := mediaMetaWithFocus(attachment.FileMeta, *attr.Focus)
			if !ok {
				return nil, errInvalidMediaAttachment
			}
			updates["file_meta"] = meta
		}
		if err := tx.Model(&models.MediaAttachment{}).
			Where("account_id = ? AND status_id = ? AND id = ?", accountID, statusID, mediaID).
			Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return acceptedMediaIDs, nil
}

func assignMediaAttachmentsToStatus(tx *gorm.DB, accountID int64, statusID int64, mediaIDs []int64, now time.Time) *gorm.DB {
	return tx.Model(&models.MediaAttachment{}).
		Where("account_id = ? AND id IN ?", accountID, mediaIDs).
		Updates(map[string]any{"status_id": statusID, "scheduled_status_id": sql.NullInt64{}, "updated_at": now})
}

func orderedExistingMediaIDs(requested models.Int64Array, media []models.MediaAttachment) models.Int64Array {
	found := make(map[int64]struct{}, len(media))
	for _, attachment := range media {
		found[attachment.ID] = struct{}{}
	}
	out := make(models.Int64Array, 0, len(requested))
	for _, id := range requested {
		if _, ok := found[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

func loadStatusMediaAttachments(tx *gorm.DB, accountID int64, statusID int64, mediaIDs []string) ([]models.MediaAttachment, error) {
	var media []models.MediaAttachment
	err := tx.Select("id", "type", "processing").
		Where("account_id = ? AND id IN ? AND (status_id IS NULL OR status_id = ?)", accountID, mediaIDs, statusID).
		Find(&media).Error
	return media, err
}

func loadScheduledStatusMediaAttachments(tx *gorm.DB, accountID int64, mediaIDs []string) ([]models.MediaAttachment, error) {
	var media []models.MediaAttachment
	err := tx.Select("id", "type", "processing").
		Where("account_id = ? AND status_id IS NULL AND id IN ?", accountID, mediaIDs).
		Find(&media).Error
	return media, err
}

func validateStatusMediaAttachments(media []models.MediaAttachment) error {
	hasAudioOrVideo := false
	for _, attachment := range media {
		if mediaAttachmentNotProcessed(attachment) {
			return errMediaAttachmentNotReady
		}
		if attachment.Type == 2 || attachment.Type == 4 {
			hasAudioOrVideo = true
		}
	}
	if len(media) > 1 && hasAudioOrVideo {
		return errMediaAttachmentsMixed
	}
	return nil
}

func mediaAttachmentValidationAPIError(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, errMediaAttachmentsMixed):
		return apiError(c, http.StatusUnprocessableEntity, "Cannot attach a video to a post that already contains images")
	case errors.Is(err, errMediaAttachmentNotReady):
		return apiError(c, http.StatusUnprocessableEntity, "Cannot attach files that have not finished processing. Try again in a moment!")
	case errors.Is(err, errInvalidMediaAttachment):
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Media attachment is invalid")
	default:
		return nil
	}
}

func mediaAttachmentValidationError(err error) bool {
	return errors.Is(err, errInvalidMediaAttachment) ||
		errors.Is(err, errMediaAttachmentsMixed) ||
		errors.Is(err, errMediaAttachmentNotReady)
}

func updateStatusPoll(tx *gorm.DB, accountID int64, statusID int64, payload *pollUpdatePayload, now time.Time) (sql.NullInt64, models.StringArray, error) {
	if payload != nil {
		if err := validatePollPayload(payload, now); err != nil {
			return sql.NullInt64{}, nil, err
		}
	}
	if payload == nil || len(payload.Options) == 0 {
		var polls []models.Poll
		if err := tx.Where("status_id = ?", statusID).Find(&polls).Error; err != nil {
			return sql.NullInt64{}, nil, err
		}
		for _, poll := range polls {
			if err := tx.Where("poll_id = ?", poll.ID).Delete(&models.PollVote{}).Error; err != nil {
				return sql.NullInt64{}, nil, err
			}
		}
		if err := tx.Where("status_id = ?", statusID).Delete(&models.Poll{}).Error; err != nil {
			return sql.NullInt64{}, nil, err
		}
		return sql.NullInt64{}, nil, nil
	}

	options := models.StringArray(payload.Options)
	tallies := make(models.Int64Array, len(options))
	expiresAt := sql.NullTime{}
	if payload.ExpiresIn > 0 {
		expiresAt = sql.NullTime{Time: now.Add(time.Duration(payload.ExpiresIn) * time.Second), Valid: true}
	}
	var poll models.Poll
	err := tx.Where("status_id = ?", statusID).First(&poll).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		poll = models.Poll{
			AccountID:     models.PollAccountID(accountID),
			StatusID:      sql.NullInt64{Int64: statusID, Valid: true},
			Options:       options,
			CachedTallies: tallies,
			Multiple:      payload.Multiple,
			HideTotals:    payload.HideTotals,
			VotesCount:    0,
			VotersCount:   sql.NullInt64{Int64: 0, Valid: true},
			ExpiresAt:     expiresAt,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := tx.Create(&poll).Error; err != nil {
			return sql.NullInt64{}, nil, err
		}
		return sql.NullInt64{Int64: poll.ID, Valid: true}, options, nil
	}
	if err != nil {
		return sql.NullInt64{}, nil, err
	}
	if !sameStringArray(poll.Options, options) || poll.Multiple != payload.Multiple {
		if err := tx.Where("poll_id = ?", poll.ID).Delete(&models.PollVote{}).Error; err != nil {
			return sql.NullInt64{}, nil, err
		}
		poll.VotesCount = 0
		poll.VotersCount = sql.NullInt64{Int64: 0, Valid: true}
		poll.CachedTallies = tallies
	}
	poll.Options = options
	poll.Multiple = payload.Multiple
	poll.HideTotals = payload.HideTotals
	poll.ExpiresAt = expiresAt
	poll.UpdatedAt = now
	if err := tx.Save(&poll).Error; err != nil {
		return sql.NullInt64{}, nil, err
	}
	return sql.NullInt64{Int64: poll.ID, Valid: true}, options, nil
}

func validatePollPayload(payload *pollUpdatePayload, now time.Time) error {
	if payload == nil {
		return nil
	}
	payload.Options = normalizePollOptions(payload.Options)
	if len(payload.Options) < 2 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Options must have more than one item"}
	}
	if len(payload.Options) > 4 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Options can't contain more than 4 items"}
	}
	seen := map[string]struct{}{}
	for _, option := range payload.Options {
		if graphemeLength(option) > 50 {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Options are over the character limit"}
		}
		if _, ok := seen[option]; ok {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Options contain duplicates"}
		}
		seen[option] = struct{}{}
	}
	if payload.ExpiresIn <= 0 || payload.ExpiresIn > pollMaxExpirationSeconds {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Expires at is too far into the future"}
	}
	if payload.ExpiresIn < pollMinExpirationSeconds {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Expires at is too soon"}
	}
	return nil
}

func graphemeLength(value string) int {
	graphemes := uniseg.NewGraphemes(value)
	count := 0
	for graphemes.Next() {
		count++
	}
	return count
}

func normalizePollOptions(options []string) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		out = append(out, option)
	}
	return out
}

func validateStatusDisallowedHashtags(ctx context.Context, db *gorm.DB, text string) error {
	tags, err := disallowedStatusHashtags(ctx, db, text)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return nil
	}
	return apiHTTPError{status: http.StatusUnprocessableEntity, message: disallowedStatusHashtagsValidationMessage(tags)}
}

func disallowedStatusHashtags(ctx context.Context, db *gorm.DB, text string) ([]string, error) {
	if db == nil {
		return nil, nil
	}
	refs := statusHashtagRefs(text)
	if len(refs) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, strings.ToLower(ref.Normalized))
	}
	var rows []models.Tag
	if err := db.WithContext(ctx).
		Select("name").
		Where("lower(name) IN ? AND usable = FALSE", names).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	byName := make(map[string]string, len(rows))
	for _, row := range rows {
		byName[strings.ToLower(row.Name)] = row.Name
	}
	out := make([]string, 0, len(byName))
	for _, ref := range refs {
		if name, ok := byName[strings.ToLower(ref.Normalized)]; ok {
			out = append(out, name)
		}
	}
	return out, nil
}

func disallowedStatusHashtagsValidationMessage(tags []string) string {
	if len(tags) == 1 {
		return "Validation failed: Text contained a disallowed hashtag: " + tags[0]
	}
	return "Validation failed: Text contained the disallowed hashtags: " + strings.Join(tags, ", ")
}

var statusMentionPattern = regexp.MustCompile(`(^|[^\pL\pN_])@([A-Za-z0-9_]+(?:[.-]+[A-Za-z0-9_]+)*)(?:@([A-Za-z0-9.-]+))?`)

type statusMentionSaveResult struct {
	NotificationIDs      []int64
	NotificationPayloads []asynqLocalNotificationPayload
	Accounts             []models.Account
}

func (s *Server) saveStatusMentionsFromText(tx *gorm.DB, statusID int64, actorID int64, text string, now time.Time) error {
	_, err := s.saveStatusMentionsFromTextAndCollect(tx, statusID, actorID, text, now)
	return err
}

func (s *Server) saveStatusMentionsFromTextAndCollect(tx *gorm.DB, statusID int64, actorID int64, text string, now time.Time) ([]int64, error) {
	result, err := s.saveStatusMentionsFromTextAndCollectAccounts(tx, statusID, actorID, text, now)
	return result.NotificationIDs, err
}

func (s *Server) saveStatusMentionsFromTextAndCollectAccounts(tx *gorm.DB, statusID int64, actorID int64, text string, now time.Time) (statusMentionSaveResult, error) {
	refs := statusMentionRefs(text)
	seen := map[int64]struct{}{}
	result := statusMentionSaveResult{NotificationIDs: make([]int64, 0), NotificationPayloads: make([]asynqLocalNotificationPayload, 0), Accounts: make([]models.Account, 0)}
	for _, ref := range refs {
		account, err := s.accountFromMentionRef(tx, ref)
		if err != nil {
			return result, err
		}
		if account == nil || account.ID == actorID {
			continue
		}
		if !statusMentionAccountMentionable(account) {
			continue
		}
		blocked, err := statusMentionBlockedByActor(tx, actorID, *account)
		if err != nil {
			return result, err
		}
		if blocked {
			continue
		}
		if _, ok := seen[account.ID]; ok {
			continue
		}
		seen[account.ID] = struct{}{}
		result.Accounts = append(result.Accounts, *account)
		mention := models.Mention{StatusID: models.MentionStatusID(statusID), CreatedAt: now, UpdatedAt: now, AccountID: models.MentionAccountID(account.ID)}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mention)
		if res.Error != nil {
			return result, res.Error
		}
		if res.RowsAffected > 0 && account.Local() {
			result.NotificationPayloads = append(result.NotificationPayloads, asynqLocalNotificationPayload{
				ReceiverAccountID: account.ID,
				FromAccountID:     actorID,
				ActivityID:        mention.ID,
				ActivityType:      "Mention",
				Type:              "mention",
			})
		}
	}
	return result, nil
}

func (s *Server) updateStatusMentionsFromTextAndCollectAccounts(tx *gorm.DB, statusID int64, actorID int64, text string, now time.Time) (statusMentionSaveResult, error) {
	var previous []models.Mention
	if err := tx.Where("status_id = ? AND silent = ?", statusID, false).Find(&previous).Error; err != nil {
		return statusMentionSaveResult{}, err
	}
	previousByAccount := make(map[int64]models.Mention, len(previous))
	for _, mention := range previous {
		if mention.AccountID.Valid {
			previousByAccount[mention.AccountID.Int64] = mention
		}
	}

	refs := statusMentionRefs(text)
	seen := map[int64]struct{}{}
	currentAccountIDs := map[int64]struct{}{}
	blockedAccountIDs := map[int64]struct{}{}
	result := statusMentionSaveResult{NotificationIDs: make([]int64, 0), NotificationPayloads: make([]asynqLocalNotificationPayload, 0), Accounts: make([]models.Account, 0)}
	for _, ref := range refs {
		account, err := s.accountFromMentionRef(tx, ref)
		if err != nil {
			return result, err
		}
		if account == nil || account.ID == actorID {
			continue
		}
		if !statusMentionAccountMentionable(account) {
			continue
		}
		blocked, err := statusMentionBlockedByActor(tx, actorID, *account)
		if err != nil {
			return result, err
		}
		if blocked {
			blockedAccountIDs[account.ID] = struct{}{}
			continue
		}
		if _, ok := seen[account.ID]; ok {
			continue
		}
		seen[account.ID] = struct{}{}
		currentAccountIDs[account.ID] = struct{}{}
		result.Accounts = append(result.Accounts, *account)
		if _, ok := previousByAccount[account.ID]; ok {
			continue
		}
		mention := models.Mention{StatusID: models.MentionStatusID(statusID), CreatedAt: now, UpdatedAt: now, AccountID: models.MentionAccountID(account.ID)}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mention)
		if res.Error != nil {
			return result, res.Error
		}
		if res.RowsAffected > 0 && account.Local() {
			result.NotificationPayloads = append(result.NotificationPayloads, asynqLocalNotificationPayload{
				ReceiverAccountID: account.ID,
				FromAccountID:     actorID,
				ActivityID:        mention.ID,
				ActivityType:      "Mention",
				Type:              "mention",
			})
		}
	}

	droppedIDs, removedIDs := statusMentionChangeIDs(previous, currentAccountIDs, blockedAccountIDs)
	if err := applyStatusMentionChanges(tx, droppedIDs, removedIDs); err != nil {
		return result, err
	}
	return result, nil
}

func statusMentionChangeIDs(previous []models.Mention, currentAccountIDs map[int64]struct{}, blockedAccountIDs map[int64]struct{}) ([]int64, []int64) {
	droppedIDs := make([]int64, 0)
	removedIDs := make([]int64, 0)
	for _, mention := range previous {
		if !mention.AccountID.Valid {
			continue
		}
		accountID := mention.AccountID.Int64
		if _, blocked := blockedAccountIDs[accountID]; blocked {
			droppedIDs = append(droppedIDs, mention.ID)
			continue
		}
		if _, current := currentAccountIDs[accountID]; !current {
			removedIDs = append(removedIDs, mention.ID)
		}
	}
	return droppedIDs, removedIDs
}

func applyStatusMentionChanges(tx *gorm.DB, droppedIDs []int64, removedIDs []int64) error {
	if len(droppedIDs) > 0 {
		var locked []models.Mention
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id IN ?", droppedIDs).Find(&locked).Error; err != nil {
			return err
		}
		if err := tx.Where("activity_type = ? AND activity_id IN ?", "Mention", droppedIDs).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", droppedIDs).Delete(&models.Mention{}).Error; err != nil {
			return err
		}
	}
	if len(removedIDs) > 0 {
		if err := tx.Model(&models.Mention{}).Where("id IN ?", removedIDs).Update("silent", true).Error; err != nil {
			return err
		}
	}
	return nil
}

func statusMentionAccountMentionable(account *models.Account) bool {
	if account == nil || account.SuspendedAt.Valid {
		return false
	}
	if !account.Local() {
		return true
	}
	return account.User.ID != 0 && account.User.Approved && account.User.ConfirmedAt.Valid
}

func statusMentionBlockedByActor(tx *gorm.DB, actorID int64, account models.Account) (bool, error) {
	if actorID == 0 || account.ID == 0 {
		return false, nil
	}
	var count int64
	if err := tx.Model(&models.Block{}).Where("account_id = ? AND target_account_id = ?", actorID, account.ID).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if strings.TrimSpace(account.Domain.String) == "" {
		return false, nil
	}
	if err := tx.Model(&models.AccountDomainBlock{}).
		Where("account_id = ? AND lower(domain) = lower(?)", actorID, account.Domain.String).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func unexpectedMentionAccounts(accounts []models.Account, allowed []string, enforce bool) []models.Account {
	if !enforce {
		return nil
	}
	allowedIDs := map[int64]struct{}{}
	for _, value := range allowed {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil {
			allowedIDs[id] = struct{}{}
		}
	}
	out := make([]models.Account, 0)
	for _, account := range accounts {
		if _, ok := allowedIDs[account.ID]; !ok {
			out = append(out, account)
		}
	}
	return out
}

func deleteStatusMentionMetadata(tx *gorm.DB, statusID int64) error {
	var mentions []models.Mention
	if err := tx.Where("status_id = ?", statusID).Find(&mentions).Error; err != nil {
		return err
	}
	for _, mention := range mentions {
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Mention", mention.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
	}
	return tx.Where("status_id = ?", statusID).Delete(&models.Mention{}).Error
}

var statusHashtagPattern = regexp.MustCompile(`(^|[^\pL\pN_])#([\pL\pN_·・‌]+)`)

func replaceStatusTagsFromText(tx *gorm.DB, statusID int64, text string, now time.Time) ([]int64, error) {
	oldTagIDs, err := statusTagIDs(tx, statusID)
	if err != nil {
		return nil, err
	}
	if err := tx.Exec("DELETE FROM statuses_tags WHERE status_id = ?", statusID).Error; err != nil {
		return nil, err
	}
	newTagIDs, err := saveStatusTagsFromText(tx, statusID, text, now)
	if err != nil {
		return nil, err
	}
	return uniqueInt64s(append(oldTagIDs, newTagIDs...)), nil
}

func deleteStatusPreviewCardLinks(tx *gorm.DB, statusID int64) error {
	return tx.Exec("DELETE FROM preview_cards_statuses WHERE status_id = ?", statusID).Error
}

func saveStatusTagsFromText(tx *gorm.DB, statusID int64, text string, now time.Time) ([]int64, error) {
	refs := statusHashtagRefs(text)
	tagIDs := make([]int64, 0, len(refs))
	for _, ref := range refs {
		tag, err := findOrCreateStatusTag(tx, ref.Normalized, ref.Display, now)
		if err != nil {
			return nil, err
		}
		if err := tx.Exec("INSERT INTO statuses_tags (status_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", statusID, tag.ID).Error; err != nil {
			return nil, err
		}
		tagIDs = append(tagIDs, tag.ID)
	}
	return uniqueInt64s(tagIDs), nil
}

func statusTagIDs(tx *gorm.DB, statusID int64) ([]int64, error) {
	var tagIDs []int64
	err := tx.Table("statuses_tags").Where("status_id = ?", statusID).Pluck("tag_id", &tagIDs).Error
	return uniqueInt64s(tagIDs), err
}

func refreshFeaturedTagStatsForStatusTags(tx *gorm.DB, accountID int64, visibility int, tagIDs []int64, now time.Time) error {
	if accountID == 0 || visibility > 1 || len(tagIDs) == 0 {
		return nil
	}
	for _, tagID := range uniqueInt64s(tagIDs) {
		stats, err := featuredStats(tx, accountID, tagID)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.FeaturedTag{}).
			Where("account_id = ? AND tag_id = ?", accountID, tagID).
			Updates(map[string]any{
				"statuses_count": stats.StatusesCount,
				"last_status_at": stats.LastStatusAt,
				"updated_at":     now,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

type statusHashtagRef struct {
	Normalized string
	Display    string
}

func statusHashtagRefs(text string) []statusHashtagRef {
	text = norm.NFC.String(text)
	matches := statusHashtagPattern.FindAllStringSubmatch(text, -1)
	refs := make([]statusHashtagRef, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		normalized, display, ok := normalizeTagName(match[2])
		if !ok {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		refs = append(refs, statusHashtagRef{Normalized: normalized, Display: display})
	}
	return refs
}

func findOrCreateStatusTag(tx *gorm.DB, normalized string, display string, now time.Time) (*models.Tag, error) {
	var tag models.Tag
	err := tx.Where("lower(name) = ?", strings.ToLower(normalized)).First(&tag).Error
	if err == nil {
		return &tag, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	tag = models.Tag{
		Name:        normalized,
		CreatedAt:   now,
		UpdatedAt:   now,
		DisplayName: sql.NullString{String: display, Valid: display != "" && display != normalized},
	}
	if err := tx.Create(&tag).Error; err != nil {
		if err := tx.Where("lower(name) = ?", strings.ToLower(normalized)).First(&tag).Error; err != nil {
			return nil, err
		}
	}
	return &tag, nil
}

type statusMentionRef struct {
	Username string
	Domain   string
}

func statusMentionRefs(text string) []statusMentionRef {
	matches := statusMentionPattern.FindAllStringSubmatch(text, -1)
	refs := make([]statusMentionRef, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		username := strings.ToLower(strings.TrimSpace(match[2]))
		domain := ""
		if len(match) > 3 {
			domain = strings.ToLower(strings.TrimSpace(match[3]))
		}
		if username == "" {
			continue
		}
		key := username + "@" + domain
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, statusMentionRef{Username: username, Domain: domain})
	}
	return refs
}

func (s *Server) accountFromMentionRef(tx *gorm.DB, ref statusMentionRef) (*models.Account, error) {
	var account models.Account
	query := tx.Preload("AccountStat").Preload("User.Role").Where("lower(username) = ? AND suspended_at IS NULL", ref.Username)
	if s.localMentionRef(ref) {
		query = query.Where("domain IS NULL OR domain = ''")
	} else {
		query = query.Where("lower(domain) = ?", ref.Domain)
	}
	err := query.First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.fetchRemoteMentionAccount(tx, ref)
	}
	return &account, err
}

func (s *Server) localMentionRef(ref statusMentionRef) bool {
	return ref.Domain == "" || strings.EqualFold(ref.Domain, s.cfg.LocalDomain) || strings.EqualFold(ref.Domain, s.cfg.WebDomain)
}

func (s *Server) fetchRemoteMentionAccount(tx *gorm.DB, ref statusMentionRef) (*models.Account, error) {
	acct, ok := s.remoteMentionAcct(ref)
	if !ok {
		return nil, nil
	}
	actorURL, err := fetchActivityActorURLFromWebFinger(acct)
	if err != nil {
		return nil, nil
	}
	actor, err := s.fetchActivityActor(actorURL)
	if err != nil || actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
		return nil, nil
	}
	account, err := s.upsertRemoteActivityActorDB(tx, actor)
	if err != nil {
		return nil, err
	}
	if !accountMatchesMentionRef(account, ref) {
		return nil, nil
	}
	return account, nil
}

func (s *Server) remoteMentionAcct(ref statusMentionRef) (string, bool) {
	if ref.Username == "" || s.localMentionRef(ref) {
		return "", false
	}
	return ref.Username + "@" + ref.Domain, true
}

func accountMatchesMentionRef(account *models.Account, ref statusMentionRef) bool {
	if account == nil || account.Local() {
		return false
	}
	return strings.EqualFold(account.Username, ref.Username) && strings.EqualFold(account.Domain.String, ref.Domain)
}

func loadStatusForSnapshot(tx *gorm.DB, id int64) (*models.Status, error) {
	var status models.Status
	err := tx.Model(&models.Status{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Preload("MediaAttachments").
		Preload("Poll").
		Where("statuses.id = ? AND statuses.deleted_at IS NULL", id).
		First(&status).Error
	return &status, err
}

func mediaIDsToInt64Array(ids []string) models.Int64Array {
	out := make(models.Int64Array, 0, len(ids))
	for _, id := range ids {
		value, err := strconv.ParseInt(id, 10, 64)
		if err == nil {
			out = append(out, value)
		}
	}
	return out
}

func sameStringArray(left models.StringArray, right models.StringArray) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameInt64Array(left models.Int64Array, right models.Int64Array) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func statusCountsTowardAccountStats(visibility int) bool {
	return visibility != 3
}

func statusCountsTowardReplyStats(visibility int) bool {
	return visibility <= 1
}

func statusCountsTowardLocalActivity(visibility int) bool {
	return visibility <= 1
}

func upsertAccountStatForStatus(tx *gorm.DB, accountID int64, visibility int, at time.Time) error {
	if !statusCountsTowardAccountStats(visibility) {
		return nil
	}
	return upsertAccountStat(tx, accountID, at)
}

func decrementAccountStatForStatus(tx *gorm.DB, accountID int64, visibility int) error {
	if !statusCountsTowardAccountStats(visibility) {
		return nil
	}
	return decrementAccountStatCounter(tx, accountID, accountStatCounterStatuses, 1)
}

func upsertAccountStat(tx *gorm.DB, accountID int64, at time.Time) error {
	return tx.Exec(`
		INSERT INTO account_stats (account_id, statuses_count, following_count, followers_count, created_at, updated_at, last_status_at)
		VALUES (?, 1, 0, 0, now(), now(), ?)
		ON CONFLICT (account_id)
		DO UPDATE SET statuses_count = account_stats.statuses_count + 1, updated_at = now(), last_status_at = excluded.last_status_at
	`, accountID, at).Error
}

func paginationLink(c *echo.Context, first int64, last int64) string {
	return paginationLinkWithPrevParam(c, first, last, "since_id")
}

func paginationLinkWithPrevParam(c *echo.Context, first int64, last int64, prevParam string) string {
	return paginationLinkWithOptions(c, first, last, prevParam, true, true)
}

func paginationLinkWithOptions(c *echo.Context, first int64, last int64, prevParam string, includeNext bool, includePrev bool) string {
	return paginationLinkWithAllowedParams(c, first, last, prevParam, includeNext, includePrev, nil)
}

func setPaginationLinkHeader(c *echo.Context, link string) {
	if link != "" {
		c.Response().Header().Set("Link", link)
	}
}

func paginationLinkWithAllowedParams(c *echo.Context, first int64, last int64, prevParam string, includeNext bool, includePrev bool, allowedParams []string) string {
	req := c.Request()
	base := "http://" + req.Host + req.URL.Path
	if req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https" {
		base = "https://" + req.Host + req.URL.Path
	}
	links := []string{}
	if includeNext {
		next := clonePaginationQueryWithAllowedParams(req.URL.Query(), allowedParams)
		next.Set("max_id", strconv.FormatInt(last, 10))
		links = append(links, fmt.Sprintf("<%s?%s>; rel=\"next\"", base, next.Encode()))
	}
	if includePrev {
		prev := clonePaginationQueryWithAllowedParams(req.URL.Query(), allowedParams)
		prev.Set(prevParam, strconv.FormatInt(first, 10))
		links = append(links, fmt.Sprintf("<%s?%s>; rel=\"prev\"", base, prev.Encode()))
	}
	return strings.Join(links, ", ")
}

func limitOnlyPaginationLink(c *echo.Context, first int64, last int64, prevParam string, includeNext bool) string {
	return paginationLinkWithAllowedParams(c, first, last, prevParam, includeNext, true, []string{"limit"})
}

func queryParamValuePresent(c *echo.Context, key string) bool {
	return strings.TrimSpace(c.QueryParam(key)) != ""
}

func offsetPaginationLink(c *echo.Context, offsetValue int, limitValue int, count int) string {
	return offsetPaginationLinkWithAllowedParams(c, offsetValue, limitValue, count, nil)
}

func offsetPaginationLinkWithAllowedParams(c *echo.Context, offsetValue int, limitValue int, count int, allowedParams []string) string {
	return offsetPaginationLinkWithPathAndAllowedParams(c, "", offsetValue, limitValue, count, allowedParams)
}

func offsetPaginationLinkWithPathAndAllowedParams(c *echo.Context, path string, offsetValue int, limitValue int, count int, allowedParams []string) string {
	req := c.Request()
	if path == "" {
		path = req.URL.Path
	}
	base := "http://" + req.Host + path
	if req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https" {
		base = "https://" + req.Host + path
	}
	links := []string{}
	if count == limitValue {
		next := clonePaginationQueryWithAllowedParams(req.URL.Query(), allowedParams)
		next.Set("offset", strconv.Itoa(offsetValue+limitValue))
		links = append(links, fmt.Sprintf("<%s?%s>; rel=\"next\"", base, next.Encode()))
	}
	if offsetValue > limitValue {
		prev := clonePaginationQueryWithAllowedParams(req.URL.Query(), allowedParams)
		prev.Set("offset", strconv.Itoa(offsetValue-limitValue))
		links = append(links, fmt.Sprintf("<%s?%s>; rel=\"prev\"", base, prev.Encode()))
	}
	return strings.Join(links, ", ")
}

func clonePaginationQuery(values url.Values) url.Values {
	return clonePaginationQueryWithAllowedParams(values, nil)
}

func clonePaginationQueryWithAllowedParams(values url.Values, allowedParams []string) url.Values {
	cloned := cloneQuery(values)
	if allowedParams != nil {
		allowed := map[string]struct{}{}
		for _, key := range allowedParams {
			allowed[key] = struct{}{}
		}
		for key := range cloned {
			if _, ok := allowed[key]; !ok {
				cloned.Del(key)
			}
		}
	}
	cloned.Del("max_id")
	cloned.Del("since_id")
	cloned.Del("min_id")
	cloned.Del("offset")
	return cloned
}

func cloneQuery(values url.Values) url.Values {
	cloned := url.Values{}
	for key, value := range values {
		for _, item := range value {
			cloned.Add(key, item)
		}
	}
	return cloned
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringContext(c *echo.Context, key string) string {
	value, _ := c.Get(key).(string)
	return value
}

func boolContext(c *echo.Context, key string) bool {
	value, _ := c.Get(key).(bool)
	return value
}

func emptyNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
