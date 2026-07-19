package api

import (
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestRuntimeRouteManifestAssignsEveryEchoRouteToAContract(t *testing.T) {
	server, err := NewServer(config.Config{LocalDomain: "example.test", Title: "Paon"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := server.RouteManifest()
	if len(manifest) < 500 {
		t.Fatalf("route manifest has only %d routes", len(manifest))
	}
	seen := map[string]bool{}
	owners := map[string]int{}
	for _, route := range manifest {
		key := route.Method + " " + route.Path
		if seen[key] {
			t.Fatalf("duplicate route %s", key)
		}
		seen[key] = true
		if route.Contract == "" || route.Handler == "" {
			t.Fatalf("unowned route = %#v", route)
		}
		owners[route.Contract]++
	}
	for _, owner := range []string{"REST-API", "ACTIVITYPUB", "ADMIN-WEB", "SETTINGS-WEB", "AUTH-WEB", "PUBLIC-WEB", "STREAMING-HTTP", "RUNTIME-HEALTH"} {
		if owners[owner] == 0 {
			t.Fatalf("contract owner %s has no routes: %#v", owner, owners)
		}
	}
}

func TestAsynqRoutesBelongToAdminWebContract(t *testing.T) {
	for _, path := range []string{"/asynq", "/asynq/stats", "/asynq/active", "/sidekiq/retries"} {
		if got := routeContractOwner(path); got != "ADMIN-WEB" {
			t.Fatalf("routeContractOwner(%q) = %q, want ADMIN-WEB", path, got)
		}
	}
}
