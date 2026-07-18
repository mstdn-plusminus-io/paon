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
	var output string
	e := echo.New()
	e.Use(requestIDMiddleware)
	e.Use(accessLogMiddlewareWithLogger(config.Config{RailsLogLevel: "info"}, func(format string, args ...any) {
		output = fmt.Sprintf(format, args...)
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
		if !strings.Contains(output, want) {
			t.Fatalf("access log missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "must-not-be-logged") || strings.Contains(output, "access_token") {
		t.Fatalf("access log exposed query parameters: %s", output)
	}
}

func TestAccessLogMiddlewareResolvesUnhandledErrorStatus(t *testing.T) {
	var output string
	e := echo.New()
	e.Use(requestIDMiddleware)
	e.Use(accessLogMiddlewareWithLogger(config.Config{RailsLogLevel: "info"}, func(format string, args ...any) {
		output = fmt.Sprintf(format, args...)
	}))
	e.GET("/broken", func(*echo.Context) error {
		return errors.New("broken")
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/broken", nil))
	if !strings.Contains(output, `path="/broken"`) || !strings.Contains(output, `status=500`) {
		t.Fatalf("access log did not resolve the unhandled error status: %s", output)
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
