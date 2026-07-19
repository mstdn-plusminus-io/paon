package api

import (
	"context"
	"os"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestMaterializeDomainControlIsSafeWithoutDatabase(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.materializeDomainControl(context.Background(), "remote.example.com"); err != nil {
		t.Fatalf("materializeDomainControl returned error without db: %v", err)
	}
}

func TestDomainControlMutationHelpersInvalidateRailsUnavailableDomainsCache(t *testing.T) {
	src, err := os.ReadFile("domain_materialization.go")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]string{
		"materializeDomainControlMutation": {
			`s.invalidateUnavailableDomainsCache(ctx)`,
			`return s.materializeDomainControl(ctx, domain)`,
		},
		"refreshDomainControlMutation": {
			`s.invalidateUnavailableDomainsCache(ctx)`,
			`if err := s.refreshInstancesMaterializedView(); err != nil {`,
			`s.meiliIndexInstanceBestEffort(ctx, domain)`,
		},
	}
	for fn, wants := range tests {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s does not contain %q", fn, want)
			}
		}
	}
}
