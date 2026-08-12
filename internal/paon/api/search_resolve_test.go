package api

import (
	"os"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestSearchURLQueryRequiresHTTPURL(t *testing.T) {
	for _, raw := range []string{"https://remote.example/users/alice", "http://remote.example/@alice"} {
		if !searchURLQuery(raw) {
			t.Fatalf("expected %q to be treated as URL query", raw)
		}
	}
	for _, raw := range []string{"alice", "acct:alice@remote.example", " ftp://remote.example/users/alice"} {
		if searchURLQuery(raw) {
			t.Fatalf("expected %q to not be treated as URL query", raw)
		}
	}
}

func TestResolveSearchURLSkipsOffsetURLLikeRails(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "example.com", WebDomain: "example.com"}}
	accounts, statuses, handled, err := s.resolveSearchURL("https://remote.example/users/alice", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected URL query to be handled")
	}
	if len(accounts) != 0 || len(statuses) != 0 {
		t.Fatalf("accounts = %#v statuses = %#v", accounts, statuses)
	}
}

func TestLocalAccountUsernameFromURL(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "example.com", WebDomain: "example.com"}}
	for raw, want := range map[string]string{
		"https://example.com/@alice":             "alice",
		"https://example.com/users/bob":          "bob",
		"https://example.com/@carol/following":   "carol",
		"https://example.com/@dan/123":           "",
		"https://remote.example/@alice":          "",
		"https://example.com/users/alice/status": "",
	} {
		if got := s.localAccountUsernameFromURL(raw); got != want {
			t.Fatalf("localAccountUsernameFromURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLocalRemoteAccountAcctFromURLMatchesRailsHomeRoute(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "example.com", WebDomain: "example.com"}}
	for raw, want := range map[string]string{
		"https://example.com/@alice@remote.example":           "alice@remote.example",
		"https://example.com/@alice@remote.example/following": "",
		"https://example.com/@alice":                          "",
		"https://remote.example/@alice@example.com":           "",
	} {
		if got := s.localRemoteAccountAcctFromURL(raw); got != want {
			t.Fatalf("localRemoteAccountAcctFromURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLocalActivityStatusID(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "example.com", WebDomain: "example.com"}}
	for raw, want := range map[string]string{
		"https://example.com/users/alice/statuses/123":  "123",
		"https://example.com/users/alice/statuses/not":  "",
		"https://example.com/users/alice/123":           "",
		"https://remote.example/users/alice/statuses/1": "",
	} {
		if got := localActivityStatusID(s, raw); got != want {
			t.Fatalf("localActivityStatusID(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestResolveSearchAccountURLChecksLocalRemoteHomeRouteBeforeLocalUsername(t *testing.T) {
	src, err := os.ReadFile("search_resolve.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "resolveSearchAccountURL", `if acct := s.localRemoteAccountAcctFromURL(raw); acct != ""`) {
		t.Fatal("resolveSearchAccountURL must handle Rails local /@user@domain URLs before local username lookup")
	}
	if !functionBodyContains(t, src, "resolveSearchAccountURL", `s.findAccountByAcct(acct)`) {
		t.Fatal("resolveSearchAccountURL must resolve local remote account URLs through account acct lookup")
	}
}

func TestRailsRemoteStatusURIFromWebURL(t *testing.T) {
	if got := railsRemoteStatusURIFromWebURL("https://remote.example/@alice/123"); got != "https://remote.example/users/alice/statuses/123" {
		t.Fatalf("canonical URI = %q", got)
	}
	for _, raw := range []string{
		"https://remote.example/@alice/following",
		"https://remote.example/users/alice/statuses/123",
		"not a url",
	} {
		if got := railsRemoteStatusURIFromWebURL(raw); got != "" {
			t.Fatalf("railsRemoteStatusURIFromWebURL(%q) = %q, want empty", raw, got)
		}
	}
}

func TestKnownPrivateStatusURLMatchesGoToSocialShape(t *testing.T) {
	for _, raw := range []string{
		"https://gts.example/@alice/01JABCDEF123",
		"https://gts.example/@alice/statuses/01JABCDEF123",
	} {
		if !knownPrivateStatusURL(raw) {
			t.Fatalf("knownPrivateStatusURL(%q) = false", raw)
		}
	}
	for _, raw := range []string{
		"https://gts.example/@alice/statuses/abc-def",
		"https://gts.example/@alice/statuses/ABC?token=secret",
		"https://gts.example/users/alice/statuses/ABC",
		"javascript://gts.example/@alice/ABC",
	} {
		if knownPrivateStatusURL(raw) {
			t.Fatalf("knownPrivateStatusURL(%q) = true", raw)
		}
	}
}
