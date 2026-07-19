package api

import (
	"os"
	"testing"
)

func TestOptionalTokenAccountReadEndpointsCheckScopes(t *testing.T) {
	type check struct {
		fn   string
		want string
	}
	checks := map[string][]check{
		"server.go": {
			{"getAccount", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil`},
			{"lookupAccount", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil`},
		},
		"account_follows.go": {
			{"accountFollowers", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil`},
			{"accountFollowing", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil`},
		},
	}
	for file, fileChecks := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range fileChecks {
			if !functionBodyContains(t, src, check.fn, check.want) {
				t.Fatalf("%s:%s does not contain %q", file, check.fn, check.want)
			}
		}
	}
}
