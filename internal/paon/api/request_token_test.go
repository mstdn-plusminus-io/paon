package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestRequestTokenMatchesDoorkeeperTokenSources(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		contentType string
		body        string
		authorize   string
		want        string
	}{
		{name: "bearer header", authorize: "Bearer header-token", want: "header-token"},
		{name: "access token query", target: "?access_token=query-token", want: "query-token"},
		{name: "bearer token query", target: "?bearer_token=query-token", want: "query-token"},
		{name: "access token form", contentType: "application/x-www-form-urlencoded", body: "access_token=form-token&status=hello", want: "form-token"},
		{name: "bearer token form", contentType: "application/x-www-form-urlencoded", body: "bearer_token=form-token&status=hello", want: "form-token"},
		{name: "access token json", contentType: "application/json; charset=UTF-8", body: `{"access_token":"json-token","status":"hello"}`, want: "json-token"},
		{name: "bearer token json", contentType: "application/vnd.api+json", body: `{"bearer_token":"json-token","status":"hello"}`, want: "json-token"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/statuses"+test.target, strings.NewReader(test.body))
			req.Header.Set(echo.HeaderContentType, test.contentType)
			req.Header.Set(echo.HeaderAuthorization, test.authorize)
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

			if got := requestToken(c); got != test.want {
				t.Fatalf("requestToken() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRequestTokenPrefersBearerHeaderOverRequestParameters(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/statuses?access_token=query-token", strings.NewReader(`{"access_token":"json-token"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer header-token")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if got := requestToken(c); got != "header-token" {
		t.Fatalf("requestToken() = %q, want header-token", got)
	}
}

func TestRequestTokenPreservesJSONBodyForStatusParsing(t *testing.T) {
	body := `{"access_token":"json-token","status":"hello from WordPress","visibility":"public"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/statuses", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if got := requestToken(c); got != "json-token" {
		t.Fatalf("requestToken() = %q, want json-token", got)
	}
	payload, err := parseStatusCreatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Status != "hello from WordPress" || payload.Visibility != "public" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestRequestTokenDoesNotConsumeOversizedJSONBody(t *testing.T) {
	body := `{"access_token":"json-token","status":"` + strings.Repeat("x", maxRequestJSONParamsBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/statuses", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if got := requestToken(c); got != "" {
		t.Fatalf("requestToken() = %q, want empty", got)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != body {
		t.Fatalf("restored body length = %d, want %d", len(restored), len(body))
	}
}
