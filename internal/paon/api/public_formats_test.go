package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestPublicPathWithoutFormatDetectsJSONSuffix(t *testing.T) {
	value, ok := publicPathWithoutFormat("alice.JSON", "json")
	if value != "alice" || !ok {
		t.Fatalf("value = %q ok = %v", value, ok)
	}

	value, ok = publicPathWithoutFormat("alice@example.com", "json")
	if value != "alice@example.com" || ok {
		t.Fatalf("value = %q ok = %v", value, ok)
	}

	value, ok = publicPathWithoutFormat("alice..json", "json")
	if value != "alice." || !ok {
		t.Fatalf("value = %q ok = %v, want trailing dot preserved like Rails route constraints", value, ok)
	}
}

func TestPublicPathFormatSplitsRailsOptionalFormatSuffix(t *testing.T) {
	base, format, ok := publicPathFormat("alice.xml")
	if base != "alice" || format != "xml" || !ok {
		t.Fatalf("base=%q format=%q ok=%v", base, format, ok)
	}
	base, format, ok = publicPathFormat("alice.JSON")
	if base != "alice" || format != "json" || !ok {
		t.Fatalf("base=%q format=%q ok=%v", base, format, ok)
	}
	base, format, ok = publicPathFormat("alice.")
	if base != "alice." || format != "" || ok {
		t.Fatalf("base=%q format=%q ok=%v, want no route format", base, format, ok)
	}
	if got := publicPathWithoutAnyFormat("123.html"); got != "123" {
		t.Fatalf("without any format = %q", got)
	}
	if got := publicPathWithoutAnyFormat("123"); got != "123" {
		t.Fatalf("without any format plain = %q", got)
	}
}

func TestWithPathParamRestoresEchoPathValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/@alice/123.json", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "123.json"}})

	err := withPathParam(c, "id", "123", func() error {
		if got := c.Param("id"); got != "123" {
			t.Fatalf("inner id = %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Param("id"); got != "123.json" {
		t.Fatalf("restored id = %q", got)
	}
}

func TestWithPathParamTemporarilyAddsMissingPathValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/@alice/123.xml", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	c.SetPathValues(echo.PathValues{{Name: "id.:format", Value: "123.xml"}})

	err := withPathParam(c, "id", "123", func() error {
		if got := c.Param("id"); got != "123" {
			t.Fatalf("inner id = %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Param("id"); got != "" {
		t.Fatalf("restored synthetic id = %q, want empty", got)
	}
	if got := c.Param("id.:format"); got != "123.xml" {
		t.Fatalf("restored composite id = %q", got)
	}
}

func TestPublicShortAccountParamExtractsCompositeFormatParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/@alice/123.xml", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	c.SetPathValues(echo.PathValues{{Name: "id.:format", Value: "123.xml"}})
	if got := publicShortAccountParam(c, "id"); got != "123" {
		t.Fatalf("id = %q", got)
	}
	c.SetPathValues(echo.PathValues{{Name: "username.:format", Value: "alice@example.com.json"}})
	if got := publicShortAccountParam(c, "username"); got != "alice@example.com" {
		t.Fatalf("username = %q", got)
	}
	c.SetPathValues(echo.PathValues{{Name: "invite_code.:format", Value: "abc123.xml"}})
	if got := publicShortAccountParam(c, "invite_code"); got != "abc123" {
		t.Fatalf("invite_code = %q", got)
	}
}

func TestOptionalFormatPathParamNormalizesCompositeID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/settings/applications/42.html", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	c.SetPathValues(echo.PathValues{{Name: "id.:format", Value: "42.html"}})

	called := false
	handler := optionalFormatPathParam("id", func(c *echo.Context) error {
		called = true
		if got := c.Param("id"); got != "42" {
			t.Fatalf("inner id = %q", got)
		}
		if got := c.Param("id.:format"); got != "42.html" {
			t.Fatalf("inner composite id = %q", got)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
	if got := c.Param("id"); got != "" {
		t.Fatalf("restored synthetic id = %q, want empty", got)
	}
	if got := c.Param("id.:format"); got != "42.html" {
		t.Fatalf("restored composite id = %q", got)
	}
}

func TestPublicJSONFormatRoutesStayRegistered(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		`e.GET("/@:username/with_replies.json", s.publicAccountWithReplies)`,
		`e.GET("/@:username/with_replies.:format", s.publicAccountWithReplies)`,
		`e.GET("/@:username/media.json", s.publicAccountMedia)`,
		`e.GET("/@:username/media.:format", s.publicAccountMedia)`,
		`e.GET("/@:username/followers.json", s.publicAccountFollowersJSON)`,
		`e.GET("/@:username/followers.:format", s.publicAccountFollowers)`,
		`e.GET("/@:username/following.json", s.publicAccountFollowingJSON)`,
		`e.GET("/@:username/following.:format", s.publicAccountFollowing)`,
		`e.GET("/users/:username.rss", s.publicAccount)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("server.go missing public format route %q", want)
		}
	}
}

func TestPublicJSONFormatRoutesDoNotServeHTMLShell(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/@alice.json",
		"/@alice/with_replies.json",
		"/@alice/media.json",
		"/@alice/tagged/golang.json",
		"/@alice/followers.json",
		"/@alice/following.json",
		"/@alice/123.json",
		"/tags/golang.json",
		"/emojis/42.json",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK && strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s served HTML shell: %s", path, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") {
			t.Fatalf("%s content-type = %q", path, got)
		}
	}
}
