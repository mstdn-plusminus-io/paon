package api

import (
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestXPosterStatusRequestMatchesRailsMalformedMultipartFallback(t *testing.T) {
	e := echo.New()
	e.Pre(railsFormContentTypeMiddleware)
	e.POST("/api/v1/statuses", func(c *echo.Context) error {
		if token := requestToken(c); token != "xposter-token" {
			t.Fatalf("requestToken() = %q, want xposter-token", token)
		}
		payload, err := parseStatusCreatePayload(c)
		if err != nil {
			return err
		}
		if payload.Status != "Posted from WordPress" || payload.Visibility != "public" {
			t.Fatalf("payload = %#v", payload)
		}
		if len(payload.MediaIDs) != 1 || payload.MediaIDs[0] != "12345" {
			t.Fatalf("media IDs = %#v", payload.MediaIDs)
		}
		return c.NoContent(http.StatusOK)
	})

	body := url.Values{}
	body.Set("status", "Posted from WordPress")
	body.Set("visibility", "public")
	body.Set("media_ids[]", "12345")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/statuses", strings.NewReader(body.Encode()))
	req.Header.Set(echo.HeaderAuthorization, "Bearer xposter-token")
	// XPoster 5.0.9 sends this header without a boundary while WordPress encodes
	// the array body as application/x-www-form-urlencoded.
	req.Header.Set(echo.HeaderContentType, echo.MIMEMultipartForm)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestRailsFormContentTypeMiddlewareKeepsValidMultipart(t *testing.T) {
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("status", "valid multipart"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/statuses", strings.NewReader(body.String()))
	req.Header.Set(echo.HeaderContentType, writer.FormDataContentType())
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	handler := railsFormContentTypeMiddleware(func(c *echo.Context) error {
		if got := c.Request().Header.Get(echo.HeaderContentType); got != writer.FormDataContentType() {
			t.Fatalf("Content-Type = %q, want %q", got, writer.FormDataContentType())
		}
		payload, err := parseStatusCreatePayload(c)
		if err != nil {
			return err
		}
		if payload.Status != "valid multipart" {
			t.Fatalf("payload = %#v", payload)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatal(err)
	}
}
