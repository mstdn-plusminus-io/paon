package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAccessLogMiddlewareLogsCompletedRequestAtInfo(t *testing.T) {
	var output []string
	e := echo.New()
	e.Use(requestIDMiddleware)
	e.Use(accessLogMiddlewareWithLogger(config.Config{RailsLogLevel: "info"}, func(format string, args ...any) {
		output = append(output, fmt.Sprintf(format, args...))
	}))
	e.GET("/example", func(c *echo.Context) error {
		return c.String(http.StatusCreated, "created")
	})

	req := httptest.NewRequest(http.MethodGet, "/example?access_token=must-not-be-logged", nil)
	req.Header.Set("X-Request-Id", "access-log-request")
	req.Header.Set("User-Agent", "Paon access log test")
	req.RemoteAddr = "192.0.2.10:4321"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if len(output) != 2 {
		t.Fatalf("access log entries = %d, want request start and completion: %#v", len(output), output)
	}
	started, completed := output[0], output[1]
	for _, want := range []string{
		`level=INFO`,
		`event=http_request_started`,
		`request_id="access-log-request"`,
		`method="GET"`,
		`path="/example"`,
		`remote_ip="192.0.2.10"`,
		`user_agent="Paon access log test"`,
	} {
		if !strings.Contains(started, want) {
			t.Fatalf("request-start log missing %q: %s", want, started)
		}
	}
	for _, want := range []string{
		`level=INFO`,
		`event=http_access`,
		`request_id="access-log-request"`,
		`method="GET"`,
		`path="/example"`,
		`status=201`,
		`duration_ms=`,
		`bytes=7`,
		`remote_ip="192.0.2.10"`,
		`user_agent="Paon access log test"`,
	} {
		if !strings.Contains(completed, want) {
			t.Fatalf("access log missing %q: %s", want, completed)
		}
	}
	joined := strings.Join(output, "\n")
	if strings.Contains(joined, "must-not-be-logged") || strings.Contains(joined, "access_token") {
		t.Fatalf("access log exposed query parameters: %s", joined)
	}
}

func TestAccessLogMiddlewareResolvesUnhandledErrorStatus(t *testing.T) {
	var output []string
	e := echo.New()
	e.Use(requestIDMiddleware)
	e.Use(accessLogMiddlewareWithLogger(config.Config{RailsLogLevel: "info"}, func(format string, args ...any) {
		output = append(output, fmt.Sprintf(format, args...))
	}))
	e.GET("/broken", func(*echo.Context) error {
		return errors.New("broken")
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/broken", nil))
	completed := output[len(output)-1]
	if !strings.Contains(completed, `path="/broken"`) || !strings.Contains(completed, `status=500`) {
		t.Fatalf("access log did not resolve the unhandled error status: %s", completed)
	}
}

func TestAccessLogMiddlewareIsSuppressedAboveInfo(t *testing.T) {
	called := false
	e := echo.New()
	e.Use(accessLogMiddlewareWithLogger(config.Config{RailsLogLevel: "warn"}, func(string, ...any) {
		called = true
	}))
	e.GET("/health", func(c *echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if called {
		t.Fatal("INFO access log was emitted when RAILS_LOG_LEVEL=warn")
	}
}
