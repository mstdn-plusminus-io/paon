package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// webSessionFakeStore answers the handful of lookups the authentication
// helpers make against a DryRun GORM connection and records every query.
type webSessionFakeStore struct {
	tokens  map[string]webSessionFakeToken
	users   map[int64]models.User
	roles   map[int64]models.UserRole
	apps    map[string]oauthApplication
	queries []webSessionFakeQuery
}

type webSessionFakeToken struct {
	id      int64
	ownerID int64
	scopes  string
	// session marks a token bound to a session activation.
	session bool
}

type webSessionFakeQuery struct {
	sql  string
	vars []any
}

func newWebSessionFakeDB(t *testing.T, store *webSessionFakeStore) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: false,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Callback().Query().After("gorm:query").Register("paon:test_web_session_fake", store.query); err != nil {
		t.Fatal(err)
	}
	return database
}

func (f *webSessionFakeStore) query(db *gorm.DB) {
	if db.Error != nil {
		return
	}
	statement := db.Statement
	query := statement.SQL.String()
	vars := append([]any(nil), statement.Vars...)
	f.queries = append(f.queries, webSessionFakeQuery{sql: query, vars: vars})
	var first any
	if len(vars) > 0 {
		first = vars[0]
	}
	switch dest := statement.Dest.(type) {
	case *models.OAuthAccessToken:
		value, _ := first.(string)
		row, ok := f.tokens[value]
		if !ok || (strings.Contains(query, "session_activations") && !row.session) {
			db.AddError(gorm.ErrRecordNotFound)
			return
		}
		now := time.Now().UTC()
		*dest = models.OAuthAccessToken{
			ID:              row.id,
			Token:           value,
			Scopes:          models.NullSafeString(row.scopes),
			ResourceOwnerID: sql.NullInt64{Int64: row.ownerID, Valid: row.ownerID != 0},
			CreatedAt:       now,
			LastUsedAt:      sql.NullTime{Time: now, Valid: true},
		}
	case *models.User:
		id, _ := first.(int64)
		user, ok := f.users[id]
		if !ok {
			db.AddError(gorm.ErrRecordNotFound)
			return
		}
		*dest = user
	case *models.UserRole:
		id, _ := first.(int64)
		role, ok := f.roles[id]
		if !ok {
			db.AddError(gorm.ErrRecordNotFound)
			return
		}
		*dest = role
	case *oauthApplication:
		uid, _ := first.(string)
		app, ok := f.apps[uid]
		if !ok {
			db.AddError(gorm.ErrRecordNotFound)
			return
		}
		*dest = app
	}
}

// queriedWith reports whether any recorded query bound value as a parameter.
func (f *webSessionFakeStore) queriedWith(value string) bool {
	for _, query := range f.queries {
		for _, v := range query.vars {
			if s, ok := v.(string); ok && s == value {
				return true
			}
		}
	}
	return false
}

func (f *webSessionFakeStore) tokenQueryFor(value string) (webSessionFakeQuery, bool) {
	for _, query := range f.queries {
		if !strings.Contains(query.sql, "oauth_access_tokens") || len(query.vars) == 0 {
			continue
		}
		if s, ok := query.vars[0].(string); ok && s == value {
			return query, true
		}
	}
	return webSessionFakeQuery{}, false
}

const (
	webSessionTestAppToken     = "third-party-read-token"
	webSessionTestAdminToken   = "third-party-admin-read-token"
	webSessionTestSessionToken = "browser-session-token"
	webSessionTestClientID     = "attacker-client"
	webSessionTestRedirectURI  = "https://attacker.example/callback"
)

func newWebSessionTestServer(t *testing.T) (*Server, *webSessionFakeStore) {
	t.Helper()
	now := time.Now().UTC()
	store := &webSessionFakeStore{
		tokens: map[string]webSessionFakeToken{
			webSessionTestAppToken:     {id: 11, ownerID: 7, scopes: "read"},
			webSessionTestAdminToken:   {id: 12, ownerID: 7, scopes: "admin:read"},
			webSessionTestSessionToken: {id: 13, ownerID: 7, scopes: "read write follow push", session: true},
		},
		users: map[int64]models.User{
			7: {ID: 7, AccountID: 70, Email: "admin@example.com", Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}, RoleID: sql.NullInt64{Int64: 3, Valid: true}},
		},
		roles: map[int64]models.UserRole{
			3: {ID: 3, Name: "Owner", Position: 1000, Permissions: rolePermissionAdministrator},
		},
		apps: map[string]oauthApplication{
			webSessionTestClientID: {ID: 21, Name: "Attacker", UID: webSessionTestClientID, RedirectURI: webSessionTestRedirectURI, Scopes: "read write follow push admin:read admin:write"},
		},
	}
	return &Server{db: newWebSessionFakeDB(t, store)}, store
}

// serveWebSessionRoute dispatches req through an Echo router holding only the
// given route, so handlers see the matched route path like in production.
func serveWebSessionRoute(method, route string, handler echo.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	e := echo.New()
	e.HTTPErrorHandler = handleAPIError
	e.Add(method, route, handler)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestAPIAuthenticationRouteUsesMatchedRoutePath(t *testing.T) {
	tests := []struct {
		name      string
		routePath string
		target    string
		want      bool
	}{
		{name: "api route", routePath: "/api/v1/accounts/verify_credentials", target: "/api/v1/accounts/verify_credentials", want: true},
		{name: "api root", routePath: "/api", target: "/api", want: true},
		{name: "api web route", routePath: "/api/web/settings", target: "/api/web/settings", want: true},
		{name: "settings route", routePath: "/settings/applications", target: "/settings/applications", want: false},
		{name: "oauth authorize", routePath: "/oauth/authorize", target: "/oauth/authorize", want: false},
		{name: "admin route", routePath: "/admin/accounts/:id", target: "/admin/accounts/1", want: false},
		{name: "api prefix lookalike", routePath: "/apidocs", target: "/apidocs", want: false},
		{name: "route path wins over request path", routePath: "/*", target: "/api/v1/accounts/verify_credentials", want: false},
		{name: "unmatched api request", target: "/api/v1/unknown", want: true},
		{name: "unmatched web request", target: "/settings/unknown", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := echo.NewContext(httptest.NewRequest(http.MethodGet, test.target, nil), httptest.NewRecorder(), echo.New())
			c.SetPath(test.routePath)
			if got := apiAuthenticationRoute(c); got != test.want {
				t.Fatalf("apiAuthenticationRoute(route=%q, path=%q) = %v, want %v", test.routePath, test.target, got, test.want)
			}
		})
	}
}

func TestAdminBrowserSessionRequestRequiresNoOAuthToken(t *testing.T) {
	sessionCookie := &http.Cookie{Name: sessionCookieName, Value: webSessionTestSessionToken}
	tests := []struct {
		name        string
		method      string
		target      string
		contentType string
		body        string
		authorize   string
		wsProtocol  string
		cookie      *http.Cookie
		want        bool
	}{
		{name: "session cookie only", method: http.MethodGet, target: "/api/v1/admin/accounts", cookie: sessionCookie, want: true},
		{name: "rails session cookie only", method: http.MethodGet, target: "/api/v1/admin/accounts", cookie: &http.Cookie{Name: railsSessionIDCookieName, Value: "signed"}, want: true},
		{name: "no cookie", method: http.MethodGet, target: "/api/v1/admin/accounts", want: false},
		{name: "bearer header and cookie", method: http.MethodGet, target: "/api/v1/admin/accounts", authorize: "Bearer " + webSessionTestAppToken, cookie: sessionCookie, want: false},
		{name: "access_token query and cookie", method: http.MethodGet, target: "/api/v1/admin/accounts?access_token=" + webSessionTestAppToken, cookie: sessionCookie, want: false},
		{name: "bearer_token query and cookie", method: http.MethodGet, target: "/api/v1/admin/accounts?bearer_token=" + webSessionTestAppToken, cookie: sessionCookie, want: false},
		{name: "access_token form and cookie", method: http.MethodPost, target: "/api/v1/admin/measures", contentType: "application/x-www-form-urlencoded", body: "access_token=" + webSessionTestAppToken, cookie: sessionCookie, want: false},
		{name: "bearer_token json and cookie", method: http.MethodPost, target: "/api/v1/admin/measures", contentType: "application/json", body: `{"bearer_token":"` + webSessionTestAppToken + `"}`, cookie: sessionCookie, want: false},
		// requestToken prefers a Sec-WebSocket-Protocol token over the session
		// cookie, so the /api suspension gate would check a different credential.
		{name: "websocket protocol token and cookie", method: http.MethodGet, target: "/api/v1/admin/accounts", wsProtocol: "unrelated-token", cookie: sessionCookie, want: false},
		{name: "blank websocket protocol and cookie", method: http.MethodGet, target: "/api/v1/admin/accounts", wsProtocol: " , ", cookie: sessionCookie, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			if test.contentType != "" {
				req.Header.Set(echo.HeaderContentType, test.contentType)
			}
			if test.authorize != "" {
				req.Header.Set(echo.HeaderAuthorization, test.authorize)
			}
			if test.wsProtocol != "" {
				req.Header.Set("Sec-WebSocket-Protocol", test.wsProtocol)
			}
			if test.cookie != nil {
				req.AddCookie(test.cookie)
			}
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			if got := adminBrowserSessionRequest(c); got != test.want {
				t.Fatalf("adminBrowserSessionRequest() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWebRoutesIgnoreOAuthTokensAndRequireSessionActivation(t *testing.T) {
	type result struct {
		user  *models.User
		token string
		err   error
	}
	resolve := func(t *testing.T, s *Server, route string, configure func(*http.Request)) result {
		t.Helper()
		var got result
		req := httptest.NewRequest(http.MethodGet, strings.Replace(route, ":id", "1", 1), nil)
		configure(req)
		serveWebSessionRoute(http.MethodGet, route, func(c *echo.Context) error {
			got.user, got.token, got.err = s.currentUserIncludingDisabled(c)
			return nil
		}, req)
		return got
	}

	tokenSources := map[string]func(*http.Request){
		"bearer header": func(req *http.Request) {
			req.Header.Set(echo.HeaderAuthorization, "Bearer "+webSessionTestAppToken)
		},
		"access_token query": func(req *http.Request) {
			req.URL.RawQuery = url.Values{"access_token": {webSessionTestAppToken}}.Encode()
		},
		"bearer_token query": func(req *http.Request) {
			req.URL.RawQuery = url.Values{"bearer_token": {webSessionTestAppToken}}.Encode()
		},
		"websocket protocol": func(req *http.Request) {
			req.Header.Set("Sec-WebSocket-Protocol", webSessionTestAppToken)
		},
	}
	for _, route := range []string{"/settings/applications", "/oauth/authorize", "/admin/accounts/:id", "/backups/:id/download"} {
		for name, configure := range tokenSources {
			t.Run(route+" "+name, func(t *testing.T) {
				s, store := newWebSessionTestServer(t)
				got := resolve(t, s, route, configure)
				if got.err == nil || got.user != nil {
					t.Fatalf("OAuth token via %s authenticated web route %s as user %+v", name, route, got.user)
				}
				if store.queriedWith(webSessionTestAppToken) {
					t.Fatalf("web route %s looked up the OAuth token sent via %s: %+v", route, name, store.queries)
				}
			})
		}

		t.Run(route+" application token in session cookie", func(t *testing.T) {
			s, store := newWebSessionTestServer(t)
			got := resolve(t, s, route, func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestAppToken})
			})
			if got.err == nil || got.user != nil {
				t.Fatalf("application token in the session cookie authenticated web route %s as user %+v", route, got.user)
			}
			query, ok := store.tokenQueryFor(webSessionTestAppToken)
			if !ok || !strings.Contains(query.sql, webSessionActivationCondition) {
				t.Fatalf("session cookie lookup must require a session activation: %+v", store.queries)
			}
		})

		t.Run(route+" session-bound cookie", func(t *testing.T) {
			s, store := newWebSessionTestServer(t)
			got := resolve(t, s, route, func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestSessionToken})
				// A bearer token next to the session cookie is ignored.
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+webSessionTestAppToken)
			})
			if got.err != nil || got.user == nil || got.user.ID != 7 || got.token != webSessionTestSessionToken {
				t.Fatalf("session-bound cookie on %s = user %+v token %q err %v", route, got.user, got.token, got.err)
			}
			if store.queriedWith(webSessionTestAppToken) {
				t.Fatalf("web route %s looked up the bearer token beside the session cookie", route)
			}
		})
	}
}

func TestAPIRoutesKeepAcceptingOAuthTokens(t *testing.T) {
	sources := map[string]func(*http.Request){
		"bearer header": func(req *http.Request) {
			req.Header.Set(echo.HeaderAuthorization, "Bearer "+webSessionTestAppToken)
		},
		"access_token query": func(req *http.Request) {
			req.URL.RawQuery = url.Values{"access_token": {webSessionTestAppToken}}.Encode()
		},
		"websocket protocol": func(req *http.Request) {
			req.Header.Set("Sec-WebSocket-Protocol", webSessionTestAppToken)
		},
		"session cookie": func(req *http.Request) {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestAppToken})
		},
	}
	for name, configure := range sources {
		t.Run(name, func(t *testing.T) {
			s, store := newWebSessionTestServer(t)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/verify_credentials", nil)
			configure(req)
			var user *models.User
			var err error
			serveWebSessionRoute(http.MethodGet, "/api/v1/accounts/verify_credentials", func(c *echo.Context) error {
				user, _, err = s.currentUser(c)
				return nil
			}, req)
			if err != nil || user == nil || user.ID != 7 {
				t.Fatalf("API token via %s = user %+v err %v, want user 7", name, user, err)
			}
			query, ok := store.tokenQueryFor(webSessionTestAppToken)
			if !ok || strings.Contains(query.sql, "session_activations") {
				t.Fatalf("API token lookup must not require a session activation: %+v", store.queries)
			}
		})
	}
}

func TestWebRoutesRedirectOAuthTokenRequestsToSignIn(t *testing.T) {
	authorizeForm := url.Values{
		"response_type": {"code"},
		"client_id":     {webSessionTestClientID},
		"redirect_uri":  {webSessionTestRedirectURI},
		"scope":         {"read write follow push admin:read admin:write"},
	}
	tests := []struct {
		name      string
		method    string
		route     string
		target    string
		handler   func(*Server) echo.HandlerFunc
		form      url.Values
		configure func(*http.Request)
	}{
		{
			name: "authorize decision with bearer", method: http.MethodPost, route: "/oauth/authorize", target: "/oauth/authorize",
			handler: func(s *Server) echo.HandlerFunc { return s.oauthAuthorizeDecision }, form: authorizeForm,
			configure: func(req *http.Request) {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+webSessionTestAppToken)
			},
		},
		{
			name: "authorize decision with access_token form", method: http.MethodPost, route: "/oauth/authorize", target: "/oauth/authorize",
			handler: func(s *Server) echo.HandlerFunc { return s.oauthAuthorizeDecision },
			form: func() url.Values {
				form := url.Values{"access_token": {webSessionTestAppToken}}
				for key, values := range authorizeForm {
					form[key] = values
				}
				return form
			}(),
		},
		{
			name: "authorize decision with application token cookie", method: http.MethodPost, route: "/oauth/authorize", target: "/oauth/authorize",
			handler: func(s *Server) echo.HandlerFunc { return s.oauthAuthorizeDecision }, form: authorizeForm,
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestAppToken})
			},
		},
		{
			name: "settings applications with bearer", method: http.MethodGet, route: "/settings/applications", target: "/settings/applications",
			handler: func(s *Server) echo.HandlerFunc { return s.settingsApplicationsPage },
			configure: func(req *http.Request) {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+webSessionTestAppToken)
			},
		},
		{
			name: "settings applications with access_token query", method: http.MethodGet, route: "/settings/applications", target: "/settings/applications?access_token=" + webSessionTestAppToken,
			handler: func(s *Server) echo.HandlerFunc { return s.settingsApplicationsPage },
		},
		{
			name: "create settings application with bearer_token form", method: http.MethodPost, route: "/settings/applications", target: "/settings/applications",
			handler: func(s *Server) echo.HandlerFunc { return s.createSettingsApplication },
			form:    url.Values{"bearer_token": {webSessionTestAppToken}, "doorkeeper_application[name]": {"x"}, "doorkeeper_application[redirect_uri]": {nativeOAuthRedirectURI}, "doorkeeper_application[scopes][]": {"admin:write"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newWebSessionTestServer(t)
			var body *strings.Reader
			if test.form != nil {
				body = strings.NewReader(test.form.Encode())
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(test.method, test.target, body)
			if test.form != nil {
				req.Header.Set(echo.HeaderContentType, "application/x-www-form-urlencoded")
			}
			if test.configure != nil {
				test.configure(req)
			}
			rec := serveWebSessionRoute(test.method, test.route, test.handler(s), req)
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/sign_in") {
				t.Fatalf("status = %d location = %q body = %s, want a redirect to sign in", rec.Code, rec.Header().Get("Location"), rec.Body.String())
			}
		})
	}
}

func TestAdminReadAPIBrowserBranchCannotSkipScopeChecks(t *testing.T) {
	tests := []struct {
		name      string
		route     string
		target    string
		handler   func(*Server) echo.HandlerFunc
		configure func(*http.Request)
		status    int
		message   string
	}{
		{
			name: "session cookie with access_token query", route: "/api/v1/admin/domain_blocks", target: "/api/v1/admin/domain_blocks?access_token=" + webSessionTestAppToken,
			handler: func(s *Server) echo.HandlerFunc { return s.adminDomainBlocks },
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestSessionToken})
			},
			status: http.StatusForbidden, message: "This action is outside the authorized scopes",
		},
		{
			name: "dummy cookie with access_token query", route: "/api/v1/admin/domain_blocks", target: "/api/v1/admin/domain_blocks?access_token=" + webSessionTestAppToken,
			handler: func(s *Server) echo.HandlerFunc { return s.adminDomainBlocks },
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: railsSessionIDCookieName, Value: "dummy"})
			},
			status: http.StatusForbidden, message: "This action is outside the authorized scopes",
		},
		{
			name: "application token in session cookie", route: "/api/v1/admin/domain_blocks", target: "/api/v1/admin/domain_blocks",
			handler: func(s *Server) echo.HandlerFunc { return s.adminDomainBlocks },
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestAppToken})
			},
			status: http.StatusUnauthorized, message: "The access token is invalid",
		},
		{
			name: "session cookie with bearer header", route: "/api/v1/admin/domain_blocks", target: "/api/v1/admin/domain_blocks",
			handler: func(s *Server) echo.HandlerFunc { return s.adminDomainBlocks },
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestSessionToken})
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+webSessionTestAppToken)
			},
			status: http.StatusForbidden, message: "This action is outside the authorized scopes",
		},
		{
			name: "trend tags with session cookie and access_token query", route: "/api/v1/admin/trends/tags", target: "/api/v1/admin/trends/tags?access_token=" + webSessionTestAppToken,
			handler: func(s *Server) echo.HandlerFunc { return s.adminTrendTags },
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestSessionToken})
			},
			status: http.StatusForbidden, message: "This action is outside the authorized scopes",
		},
		{
			name: "trend tags with application token in session cookie", route: "/api/v1/admin/trends/tags", target: "/api/v1/admin/trends/tags",
			handler: func(s *Server) echo.HandlerFunc { return s.adminTrendTags },
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestAppToken})
			},
			status: http.StatusUnauthorized, message: "The access token is invalid",
		},
		{
			name: "session-bound cookie", route: "/api/v1/admin/domain_blocks", target: "/api/v1/admin/domain_blocks",
			handler: func(s *Server) echo.HandlerFunc { return s.adminDomainBlocks },
			configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webSessionTestSessionToken})
			},
			status: http.StatusOK,
		},
		{
			name: "admin:read bearer token", route: "/api/v1/admin/domain_blocks", target: "/api/v1/admin/domain_blocks",
			handler: func(s *Server) echo.HandlerFunc { return s.adminDomainBlocks },
			configure: func(req *http.Request) {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+webSessionTestAdminToken)
			},
			status: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newWebSessionTestServer(t)
			req := httptest.NewRequest(http.MethodGet, test.target, nil)
			test.configure(req)
			rec := serveWebSessionRoute(http.MethodGet, test.route, test.handler(s), req)
			if rec.Code != test.status {
				t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), test.status)
			}
			if test.message != "" && !strings.Contains(rec.Body.String(), test.message) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), test.message)
			}
		})
	}
}
