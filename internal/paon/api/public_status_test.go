package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestPublicStatusActivityPubRouteDoesNotServeHTMLShell(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/@alice/123", nil)
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") {
		t.Fatalf("content-type = %q", got)
	}
}

func TestPublicStatusLinkHeaderMatchesRailsAlternateLink(t *testing.T) {
	cfg := config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}
	status := models.Status{ID: 123, Account: models.Account{ID: 10, Username: "alice"}}

	got := publicStatusLinkHeader(cfg, status)
	want := `<https://example.com/users/alice/statuses/123>; rel="alternate"; type="application/activity+json"`
	if got != want {
		t.Fatalf("Link = %q, want %q", got, want)
	}
}

func TestPublicStatusPathStatusAllowedMatchesRailsSetStatusScope(t *testing.T) {
	account := models.Account{ID: 10, Username: "alice"}
	if !publicStatusPathStatusAllowed(models.Status{ID: 123, Account: account, Visibility: 0}, "alice") {
		t.Fatal("public local status should be allowed")
	}
	if !publicStatusPathStatusAllowed(models.Status{ID: 123, Account: account, Visibility: 1}, "alice") {
		t.Fatal("unlisted local status should be allowed")
	}
	for _, status := range []models.Status{
		{ID: 123, Account: account, Visibility: 2},
		{ID: 123, Account: account, Visibility: 3},
		{ID: 123, Account: models.Account{ID: 11, Username: "bob"}, Visibility: 0},
		{ID: 123, Account: models.Account{ID: 12, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}}, Visibility: 0},
	} {
		if publicStatusPathStatusAllowed(status, "alice") {
			t.Fatalf("status should not be allowed for Rails public HTML route: %#v", status)
		}
	}
}

func TestPublicStatusOriginalRedirectURLMatchesRailsReblogRedirect(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	status := models.Status{
		ID:         123,
		ReblogOfID: sql.NullInt64{Int64: 99, Valid: true},
		Reblog: &models.Status{
			ID:      99,
			Account: models.Account{ID: 20, Username: "bob"},
		},
	}
	if got := s.publicStatusOriginalRedirectURL(status); got != "https://example.com/@bob/99" {
		t.Fatalf("redirect URL = %q", got)
	}
	status.ReblogOfID = sql.NullInt64{}
	if got := s.publicStatusOriginalRedirectURL(status); got != "" {
		t.Fatalf("non-reblog redirect URL = %q", got)
	}
}
