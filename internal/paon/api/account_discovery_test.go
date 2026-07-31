package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestEndorsementsRequireAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/endorsements", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.endorsements(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "The access token is invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestFamiliarFollowersRequireAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/accounts/familiar_followers?id[]=1", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.familiarFollowers(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
}

func TestSearchAccountsRequireAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/accounts/search?q=alice&following=true", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.searchAccounts(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSearchAccountsUsesRailsDefaultAccountsLimit(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "searchAccounts", `limitValue := limit(c, 40, 80)`) {
		t.Fatal("searchAccounts must use Rails DEFAULT_ACCOUNTS_LIMIT default and 80 max")
	}
}

func TestAccountSearchCompleteAcctRequiresUsernameAndDomain(t *testing.T) {
	for _, query := range []string{"alice@example.com", "@alice@example.com", "acct:alice@example.com"} {
		if !accountSearchCompleteAcct(query) {
			t.Fatalf("expected %q to be a complete acct", query)
		}
	}
	for _, query := range []string{"alice", "@alice", "alice@", "@example.com", "alice@example.com@bad"} {
		if accountSearchCompleteAcct(query) {
			t.Fatalf("expected %q to be incomplete", query)
		}
	}
}

func TestNormalizeAcctInputTrimsAcctAndAtPrefixes(t *testing.T) {
	for raw, want := range map[string]string{
		"alice@example.com":       "alice@example.com",
		"@alice@example.com":      "alice@example.com",
		"acct:alice@example.com":  "alice@example.com",
		"Acct:@alice@example.com": "alice@example.com",
	} {
		if got := normalizeAcctInput(raw); got != want {
			t.Fatalf("normalizeAcctInput(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestAccountSearchDatabaseFallbackUsesRailsSearchShape(t *testing.T) {
	src, err := os.ReadFile("account_search_sql.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`to_tsquery('simple', ?) @@`,
		`accounts.moved_to_account_id IS NULL`,
		`accounts.domain IS NOT NULL OR (users.approved = TRUE AND users.confirmed_at IS NOT NULL)`,
		`account_search_first_degree`,
		`count(account_search_followers.id) DESC`,
		`rank DESC`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("account_search_sql.go missing Rails account search fragment %q", want)
		}
	}
}

func TestAccountSearchTermsForQueryMatchesRailsLocalDomainShape(t *testing.T) {
	s := &Server{cfg: config.Config{LocalDomain: "example.com", WebDomain: "web.example.com"}}
	for raw, want := range map[string]string{
		"alice":                 "alice",
		"@alice":                "alice",
		"alice@example.com":     "alice",
		"alice@web.example.com": "alice",
		"alice@remote.example":  "alice@remote.example",
	} {
		if got := s.accountSearchTermsForQuery(raw); got != want {
			t.Fatalf("accountSearchTermsForQuery(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestAccountSearchTSQuerySanitizesRailsDisallowedCharacters(t *testing.T) {
	if got := accountSearchTSQuery("al:ice?"); got != "' al ice  ':*" {
		t.Fatalf("accountSearchTSQuery sanitized value = %q", got)
	}
	if got := accountSearchTSQuery("?:"); got != "" {
		t.Fatalf("accountSearchTSQuery disallowed-only value = %q, want empty", got)
	}
}

func TestResolveAccountSearchExactWithoutDatabaseIsEmpty(t *testing.T) {
	s := &Server{}
	account, err := s.resolveAccountSearchExact("alice@remote.example", &models.Account{ID: 1}, true, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if account != nil {
		t.Fatalf("account = %#v, want nil", account)
	}
}
