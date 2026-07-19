package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestActivityPubActorHTMLRouteRedirectsToPublicProfile(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/alice?foo=bar", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Location"), "/@alice?foo=bar"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Vary"), "Origin, Accept"; got != want {
		t.Fatalf("Vary = %q, want %q", got, want)
	}
}

func TestActivityPubFollowerHTMLRoutesRedirectToPublicCollections(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/users/alice/followers", want: "/@alice/followers"},
		{path: "/users/alice/following", want: "/@alice/following"},
	} {
		s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusMovedPermanently {
			t.Fatalf("%s status = %d body = %s", tc.path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != tc.want {
			t.Fatalf("%s Location = %q, want %q", tc.path, got, tc.want)
		}
		if got, want := rec.Header().Get("Vary"), "Origin, Accept"; got != want {
			t.Fatalf("%s Vary = %q, want %q", tc.path, got, want)
		}
	}
}

func TestActivityPubStatusHTMLRouteRedirectsToPublicStatus(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/alice/statuses/123", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Location"), "/@alice/123"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Vary"), "Origin, Accept"; got != want {
		t.Fatalf("Vary = %q, want %q", got, want)
	}
}

func TestActivityPubDirectHTMLHandlersUseRailsRedirectVary(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		pathValues echo.PathValues
		call       func(*echo.Context) error
		want       string
	}{
		{
			name:       "actor",
			pathValues: echo.PathValues{{Name: "username", Value: "alice"}},
			call:       s.activityPubActor,
			want:       "/@alice",
		},
		{
			name:       "followers",
			pathValues: echo.PathValues{{Name: "username", Value: "alice"}},
			call:       s.activityPubFollowers,
			want:       "/@alice/followers",
		},
		{
			name:       "following",
			pathValues: echo.PathValues{{Name: "username", Value: "alice"}},
			call:       s.activityPubFollowing,
			want:       "/@alice/following",
		},
		{
			name:       "status",
			pathValues: echo.PathValues{{Name: "username", Value: "alice"}, {Name: "id", Value: "123"}},
			call:       s.activityPubStatus,
			want:       "/@alice/123",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/unused", nil)
			req.Header.Set("Accept", "text/html")
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, s.echo)
			c.SetPathValues(tc.pathValues)

			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusMovedPermanently {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
			if got, want := rec.Header().Get("Vary"), "Origin, Accept"; got != want {
				t.Fatalf("Vary = %q, want %q", got, want)
			}
		})
	}
}

func TestActivityPubActorAcceptStillUsesJSONRoute(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/alice", nil)
	req.Header.Set("Accept", `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") {
		t.Fatalf("content-type = %q", got)
	}
}

func TestActivityPubJSONFormatRoutesBypassHTMLRedirectLikeRails(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/users/alice.json",
		"/users/alice/followers.json",
		"/users/alice/following.json",
		"/users/alice/statuses/123.json",
		"/users/alice/statuses/123/activity.json",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Accept", "text/html")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code == http.StatusMovedPermanently || rec.Code == http.StatusFound {
				t.Fatalf("status = %d Location = %q, want ActivityPub handler", rec.Code, rec.Header().Get("Location"))
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Fatalf("Location = %q", got)
			}
			if got := rec.Header().Get("Vary"); got != "Accept, Accept-Language, Cookie" {
				t.Fatalf("Vary = %q, want Accept, Accept-Language, Cookie", got)
			}
		})
	}
}

func TestActivityPubAccountJSONRoutesMatchRailsPublicVary(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/users/alice",
		"/users/alice.json",
		"/users/alice/followers",
		"/users/alice/followers.json",
		"/users/alice/following",
		"/users/alice/following.json",
		"/users/alice/statuses/123",
		"/users/alice/statuses/123.json",
		"/users/alice/statuses/123/activity",
		"/users/alice/statuses/123/activity.json",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Accept", "application/activity+json")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if got := rec.Header().Get("Vary"); got != "Accept, Accept-Language, Cookie" {
				t.Fatalf("Vary = %q, want Accept, Accept-Language, Cookie", got)
			}
		})
	}
}

func TestBareJSONLDAcceptUsesActivityPubRouteInLimitedFederationMode(t *testing.T) {
	s, err := NewServer(config.Config{
		Title:                 "Paon",
		LocalDomain:           "example.com",
		WebDomain:             "example.com",
		Scheme:                "https",
		LimitedFederationMode: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/alice", nil)
	req.Header.Set("Accept", "application/ld+json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request not signed") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Accept, Accept-Language, Cookie, Signature" {
		t.Fatalf("Vary = %q", got)
	}
}

func TestActivityPubJSONRoutesRequireSignatureInLimitedFederationMode(t *testing.T) {
	s, err := NewServer(config.Config{
		Title:                 "Paon",
		LocalDomain:           "example.com",
		WebDomain:             "example.com",
		Scheme:                "https",
		LimitedFederationMode: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		vary string
	}{
		{path: "/actor/outbox", vary: "Signature"},
		{path: "/actor/outbox.json", vary: "Signature"},
		{path: "/users/alice", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice.json", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/outbox", vary: "Signature"},
		{path: "/users/alice/outbox.json", vary: "Signature"},
		{path: "/users/alice/followers", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/followers.json", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/following", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/following.json", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/collections/featured", vary: "Signature"},
		{path: "/users/alice/collections/featured.json", vary: "Signature"},
		{path: "/users/alice/statuses/123", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/statuses/123.json", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/statuses/123/activity", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/statuses/123/activity.json", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/users/alice/statuses/123/replies", vary: "Signature"},
		{path: "/users/alice/statuses/123/replies.json", vary: "Signature"},
		{path: "/@alice", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/@alice/with_replies", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/@alice/media", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/@alice/tagged/go", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/@alice/followers", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/@alice/following", vary: "Accept, Accept-Language, Cookie, Signature"},
		{path: "/tags/golang", vary: "Accept, Accept-Language, Cookie, Signature"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Accept", "application/activity+json")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "request not signed") {
				t.Fatalf("body = %s", rec.Body.String())
			}
			if got := rec.Header().Get("Vary"); got != tc.vary {
				t.Fatalf("Vary = %q, want %q", got, tc.vary)
			}
		})
	}
}

func TestActivityPubHTMLRoutesDoNotRequireSignatureInLimitedFederationMode(t *testing.T) {
	s, err := NewServer(config.Config{
		Title:                 "Paon",
		LocalDomain:           "example.com",
		WebDomain:             "example.com",
		Scheme:                "https",
		LimitedFederationMode: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/users/alice", want: "/@alice"},
		{path: "/users/alice/followers", want: "/@alice/followers"},
		{path: "/users/alice/following", want: "/@alice/following"},
		{path: "/users/alice/statuses/123", want: "/@alice/123"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Accept", "text/html")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusMovedPermanently {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}
