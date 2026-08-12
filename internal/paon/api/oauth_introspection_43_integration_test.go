//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestOAuthIntrospectionAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		LocalDomain:          "example.test",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	application := models.OAuthApplication{
		Name:         "Introspection fixture",
		UID:          "introspection-client",
		Secret:       "introspection-secret",
		RedirectURI:  nativeOAuthRedirectURI,
		Scopes:       "read",
		CreatedAt:    sql.NullTime{Time: now, Valid: true},
		UpdatedAt:    sql.NullTime{Time: now, Valid: true},
		Confidential: true,
	}
	if err := database.Create(&application).Error; err != nil {
		t.Fatal(err)
	}
	target := models.OAuthAccessToken{
		Token:         "introspection-target",
		CreatedAt:     now,
		Scopes:        models.NullSafeString("read"),
		ApplicationID: sql.NullInt64{Int64: application.ID, Valid: true},
	}
	if err := database.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	authorization := models.OAuthAccessToken{
		Token:         "introspection-authorization",
		CreatedAt:     now.Add(time.Second),
		Scopes:        models.NullSafeString("read"),
		ApplicationID: sql.NullInt64{Int64: application.ID, Valid: true},
	}
	if err := database.Create(&authorization).Error; err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	request := func(token string, clientID string, clientSecret string, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"token": {token}}
		req := httptest.NewRequest(http.MethodPost, "/oauth/introspect", strings.NewReader(form.Encode()))
		req.Host = cfg.LocalDomain
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if clientID != "" || clientSecret != "" {
			req.SetBasicAuth(clientID, clientSecret)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		server.echo.ServeHTTP(rec, req)
		return rec
	}

	active := request(target.Token, application.UID, application.Secret, "")
	if active.Code != http.StatusOK || active.Header().Get("Cache-Control") != "max-age=0, private, must-revalidate" {
		t.Fatalf("active response = %d headers=%v body=%s", active.Code, active.Header(), active.Body.String())
	}
	var activeBody map[string]any
	if err := json.Unmarshal(active.Body.Bytes(), &activeBody); err != nil {
		t.Fatal(err)
	}
	if activeBody["active"] != true || activeBody["scope"] != "read" || activeBody["client_id"] != application.UID || activeBody["token_type"] != "Bearer" || activeBody["exp"] != float64(0) || activeBody["iat"] != float64(now.Unix()) {
		t.Fatalf("active body = %#v", activeBody)
	}

	inactive := request("missing-token", application.UID, application.Secret, "")
	if inactive.Code != http.StatusOK || inactive.Body.String() != "{\"active\":false}\n" {
		t.Fatalf("inactive response = %d body=%s", inactive.Code, inactive.Body.String())
	}

	invalidClient := request(target.Token, application.UID, "wrong-secret", "")
	if invalidClient.Code != http.StatusUnauthorized || invalidClient.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid client response = %d headers=%v body=%s", invalidClient.Code, invalidClient.Header(), invalidClient.Body.String())
	}
	if got := invalidClient.Header().Get("WWW-Authenticate"); got != `Bearer realm="Doorkeeper", error="invalid_client", error_description="`+oauthIntrospectionInvalidClientDescription+`"` {
		t.Fatalf("invalid client WWW-Authenticate = %q", got)
	}

	bearer := request(target.Token, "", "", authorization.Token)
	if bearer.Code != http.StatusOK {
		t.Fatalf("bearer response = %d body=%s", bearer.Code, bearer.Body.String())
	}
	self := request(target.Token, "", "", target.Token)
	if self.Code != http.StatusUnauthorized {
		t.Fatalf("self response = %d body=%s", self.Code, self.Body.String())
	}
	var selfBody map[string]string
	if err := json.Unmarshal(self.Body.Bytes(), &selfBody); err != nil {
		t.Fatal(err)
	}
	if selfBody["error"] != "invalid_token" || selfBody["state"] != "unauthorized" {
		t.Fatalf("self body = %#v", selfBody)
	}
}
