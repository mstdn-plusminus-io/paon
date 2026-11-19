package api

import (
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func suspendedGateDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: false,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

// suspendedGateFakeDB returns a dry-run database whose suspended-owner
// lookups report a hit when a bound value is one of suspendedCredentials. It
// records every query so tests can count the lookups a request makes.
func suspendedGateFakeDB(t *testing.T, suspendedCredentials ...string) (*gorm.DB, *[]string) {
	t.Helper()
	database := suspendedGateDryRunDB(t)
	queries := []string{}
	err := database.Callback().Query().After("gorm:query").Register("test:suspended_gate", func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		queries = append(queries, query)
		ids, ok := tx.Statement.Dest.(*[]int64)
		if !ok || !strings.Contains(query, "suspended_at IS NOT NULL") {
			return
		}
		for _, value := range tx.Statement.Vars {
			if credential, ok := value.(string); ok && slices.Contains(suspendedCredentials, credential) {
				*ids = append(*ids, 1)
				return
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return database, &queries
}

func TestSuspendedOwnerQueriesUseOneIndexedLookup(t *testing.T) {
	database := suspendedGateDryRunDB(t)
	for _, tt := range []struct {
		name  string
		query *gorm.DB
		value string
		want  string
	}{
		{
			// oauth_access_tokens.token has a unique index; users and accounts are joined by primary key.
			name:  "token",
			query: suspendedTokenOwnerQuery(database, "token-value"),
			value: "token-value",
			want:  `SELECT "users"."id" FROM "oauth_access_tokens" JOIN users ON users.id = oauth_access_tokens.resource_owner_id JOIN accounts ON accounts.id = users.account_id WHERE (oauth_access_tokens.token = $1 AND oauth_access_tokens.revoked_at IS NULL) AND accounts.suspended_at IS NOT NULL LIMIT $2`,
		},
		{
			// session_activations.session_id has a unique index.
			name:  "rails session",
			query: suspendedSessionOwnerQuery(database, "session-value"),
			value: "session-value",
			want:  `SELECT "users"."id" FROM "session_activations" JOIN users ON users.id = session_activations.user_id JOIN accounts ON accounts.id = users.account_id WHERE session_activations.session_id = $1 AND accounts.suspended_at IS NOT NULL LIMIT $2`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ids []int64
			result := tt.query.Pluck("users.id", &ids)
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			if got := result.Statement.SQL.String(); got != tt.want {
				t.Fatalf("SQL = %s\nwant  %s", got, tt.want)
			}
			if len(result.Statement.Vars) != 2 || result.Statement.Vars[0] != tt.value || result.Statement.Vars[1] != 1 {
				t.Fatalf("vars = %#v", result.Statement.Vars)
			}
		})
	}

	fake, queries := suspendedGateFakeDB(t)
	if suspended, err := (&Server{db: fake}).userAccountSuspended(models.User{ID: 3, AccountID: 7}); err != nil || suspended {
		t.Fatalf("userAccountSuspended = %v, %v", suspended, err)
	}
	if want := []string{`SELECT "id" FROM "accounts" WHERE id = $1 AND suspended_at IS NOT NULL LIMIT $2`}; !slices.Equal(*queries, want) {
		t.Fatalf("account queries = %q, want %q", *queries, want)
	}
}

func TestAPIAuthenticationGateRejectsSuspendedUsersLikeRails(t *testing.T) {
	const secretKeyBase = "suspended-gate-secret"
	database, queries := suspendedGateFakeDB(t, "suspended-token", "suspended-session")
	e := echo.New()
	e.HTTPErrorHandler = handleAPIError
	s := &Server{echo: e, cfg: config.Config{SecretKeyBase: secretKeyBase}, db: database}
	e.Use(s.apiAuthenticationGateMiddleware)
	handlerCalls := 0
	handler := func(c *echo.Context) error {
		handlerCalls++
		return c.String(http.StatusOK, "handler")
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/statuses/:id"},
		{http.MethodGet, "/api/v1/accounts/verify_credentials"},
		{http.MethodGet, "/api/v1/accounts/:id/statuses"},
		{http.MethodGet, "/api/v1/admin/accounts"},
		{http.MethodPost, "/api/v1/admin/accounts/:id/unsuspend"},
		{http.MethodGet, "/api/oembed"},
		{http.MethodGet, "/api/v1/streaming"},
		{http.MethodGet, "/api/v1/streaming/*"},
		{http.MethodGet, "/api/v1/streaming/health"},
		{http.MethodGet, "/auth/edit"},
		{http.MethodGet, "/settings/export"},
	} {
		e.Add(route.method, route.path, handler)
	}
	railsSessionCookie := railsSignedCookieFixture(t, "suspended-session", secretKeyBase, railsSessionCookiePurpose, time.Now().Add(time.Hour), sha1.New)

	for _, tt := range []struct {
		name        string
		method      string
		target      string
		bearer      string
		cookieName  string
		cookieValue string
		contentType string
		body        string
		wantBlocked bool
		wantQueries int
	}{
		{name: "bearer token on status", method: http.MethodGet, target: "/api/v1/statuses/1", bearer: "suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "bearer token on verify_credentials", method: http.MethodGet, target: "/api/v1/accounts/verify_credentials", bearer: "suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "bearer token on account statuses", method: http.MethodGet, target: "/api/v1/accounts/2/statuses", bearer: "suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "access_token query parameter", method: http.MethodGet, target: "/api/v1/statuses/1?access_token=suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "access_token form parameter", method: http.MethodPost, target: "/api/v1/admin/accounts/3/unsuspend", contentType: "application/x-www-form-urlencoded", body: "access_token=suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "access_token JSON parameter", method: http.MethodPost, target: "/api/v1/admin/accounts/3/unsuspend", contentType: "application/json", body: `{"access_token":"suspended-token"}`, wantBlocked: true, wantQueries: 1},
		{name: "paon session cookie on admin read", method: http.MethodGet, target: "/api/v1/admin/accounts", cookieName: sessionCookieName, cookieValue: "suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "legacy Rails session cookie", method: http.MethodGet, target: "/api/v1/statuses/1", cookieName: railsSessionIDCookieName, cookieValue: railsSessionCookie, wantBlocked: true, wantQueries: 1},
		{name: "admin unsuspend", method: http.MethodPost, target: "/api/v1/admin/accounts/3/unsuspend", bearer: "suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "oembed runs require_not_suspended! in Rails", method: http.MethodGet, target: "/api/oembed?url=https://example.com/@a/1", bearer: "suspended-token", wantBlocked: true, wantQueries: 1},
		{name: "streaming keeps its own suspension check", method: http.MethodGet, target: "/api/v1/streaming/user", bearer: "suspended-token"},
		{name: "streaming root keeps its own suspension check", method: http.MethodGet, target: "/api/v1/streaming?stream=user", bearer: "suspended-token"},
		{name: "streaming health", method: http.MethodGet, target: "/api/v1/streaming/health", bearer: "suspended-token"},
		{name: "web edit page is not an API route", method: http.MethodGet, target: "/auth/edit", cookieName: sessionCookieName, cookieValue: "suspended-token"},
		{name: "web export page is not an API route", method: http.MethodGet, target: "/settings/export", cookieName: sessionCookieName, cookieValue: "suspended-token"},
		{name: "active user token", method: http.MethodGet, target: "/api/v1/statuses/1", bearer: "active-token", wantQueries: 1},
		{name: "active user admin unsuspend", method: http.MethodPost, target: "/api/v1/admin/accounts/3/unsuspend", bearer: "active-token", wantQueries: 1},
		{name: "active user cookie", method: http.MethodGet, target: "/api/v1/admin/accounts", cookieName: sessionCookieName, cookieValue: "active-token", wantQueries: 1},
		{name: "anonymous request", method: http.MethodGet, target: "/api/v1/statuses/1"},
		{name: "anonymous oembed", method: http.MethodGet, target: "/api/oembed?url=https://example.com/@a/1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			*queries = (*queries)[:0]
			handlerCalls = 0
			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tt.bearer)
			}
			if tt.cookieName != "" {
				req.AddCookie(&http.Cookie{Name: tt.cookieName, Value: tt.cookieValue})
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if len(*queries) != tt.wantQueries {
				t.Fatalf("queries = %d %q, want %d", len(*queries), *queries, tt.wantQueries)
			}
			if !tt.wantBlocked {
				if rec.Code != http.StatusOK || handlerCalls != 1 {
					t.Fatalf("status = %d, handler calls = %d, body = %s; want the handler to decide", rec.Code, handlerCalls, rec.Body.String())
				}
				return
			}
			if handlerCalls != 0 {
				t.Fatal("handler ran for a suspended user")
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body = %s; want 403", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body = %s: %v", rec.Body.String(), err)
			}
			if len(body) != 1 || body["error"] != "Your login is currently disabled" {
				t.Fatalf("body = %#v, want the Rails require_not_suspended! error", body)
			}
		})
	}
}

func TestAPIAuthenticationGateSkipsSuspensionLookupWithoutDatabase(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handleAPIError
	s := &Server{echo: e}
	e.Use(s.apiAuthenticationGateMiddleware)
	e.GET("/api/v1/statuses/:id", func(c *echo.Context) error { return c.String(http.StatusOK, "handler") })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/statuses/1", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestRequireAdminWriteRejectsSuspendedStaff(t *testing.T) {
	for _, suspended := range []bool{true, false} {
		database := suspendedGateDryRunDB(t)
		err := database.Callback().Query().After("gorm:query").Register("test:admin_write", func(tx *gorm.DB) {
			switch dest := tx.Statement.Dest.(type) {
			case *models.OAuthAccessToken:
				*dest = models.OAuthAccessToken{ID: 1, Token: "staff-token", Scopes: "admin:write", ResourceOwnerID: sql.NullInt64{Int64: 10, Valid: true}, LastUsedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}}
			case *models.User:
				*dest = models.User{ID: 10, AccountID: 20, RoleID: sql.NullInt64{Int64: 3, Valid: true}}
			case *models.UserRole:
				*dest = models.UserRole{ID: 3, Permissions: rolePermissionManageUsers}
			case *[]int64:
				if suspended && strings.Contains(tx.Statement.SQL.String(), "suspended_at IS NOT NULL") {
					*dest = append(*dest, 20)
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		// Call the gate directly, without apiAuthenticationGateMiddleware in front.
		s := &Server{db: database}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/20/unsuspend", nil)
		req.Header.Set("Authorization", "Bearer staff-token")
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

		user, err := s.requireAdminWrite(c, "admin:write:accounts")
		if suspended {
			var apiErr apiHTTPError
			if !errors.As(err, &apiErr) || apiErr.status != http.StatusForbidden || apiErr.message != "Your login is currently disabled" {
				t.Fatalf("suspended staff: user = %+v, err = %#v; want 403 Your login is currently disabled", user, err)
			}
			continue
		}
		if err != nil || user == nil || user.ID != 10 {
			t.Fatalf("active staff: user = %+v, err = %v; want the staff user", user, err)
		}
	}
}

func TestUserAccountSuspendedOnlyRejectsSuspension(t *testing.T) {
	s := &Server{}
	suspended, err := s.userAccountSuspended(models.User{AccountID: 1, Account: &models.Account{ID: 1, SuspendedAt: sql.NullTime{Time: time.Now(), Valid: true}}})
	if err != nil || !suspended {
		t.Fatalf("suspended account = %v, %v", suspended, err)
	}
	// Rails require_not_suspended! only checks suspension, so moved and
	// memorial staff accounts keep their admin API access.
	suspended, err = s.userAccountSuspended(models.User{AccountID: 1, Account: &models.Account{ID: 1, Memorial: true, MovedToAccountID: sql.NullInt64{Int64: 2, Valid: true}}})
	if err != nil || suspended {
		t.Fatalf("moved memorial account = %v, %v", suspended, err)
	}
}

func TestAPIGateKeepsMultipartFieldsReadableAfterParamLookup(t *testing.T) {
	newRequest := func() *http.Request {
		body := &strings.Builder{}
		writer := multipart.NewWriter(body)
		if err := writer.WriteField("client_name", "Example App"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(body.String()))
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req
	}

	// Without the pre-parse, the gate's ParseForm call sets Request.Form and
	// net/http FormValue stops parsing the multipart body for the handler.
	unparsed := newRequest()
	_ = requestRawParamValue(echo.New().NewContext(unparsed, httptest.NewRecorder()), "access_token")
	if got := unparsed.FormValue("client_name"); got != "" {
		t.Fatalf("precondition: FormValue after ParseForm = %q, want empty", got)
	}

	req := newRequest()
	ensureMultipartFormParsed(req)
	_ = requestRawParamValue(echo.New().NewContext(req, httptest.NewRecorder()), "access_token")
	if got := req.FormValue("client_name"); got != "Example App" {
		t.Fatalf("FormValue after gate parameter lookup = %q, want %q", got, "Example App")
	}
}
