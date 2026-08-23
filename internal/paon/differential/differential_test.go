package differential

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownAndDuplicateCases(t *testing.T) {
	_, err := Load(strings.NewReader(`{"cases":[{"id":"same","contract":"REST-API","method":"get","path":"/api/v1/instance"},{"id":"same","contract":"REST-API","method":"GET","path":"/api/v1/instance"}]}`))
	if err == nil || !strings.Contains(err.Error(), "duplicate case") {
		t.Fatalf("Load duplicate error = %v", err)
	}
	_, err = Load(strings.NewReader(`{"cases":[],"silent":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load unknown field error = %v", err)
	}
}

func TestRunnerComparesStatusHeadersJSONAndDeclaredVolatility(t *testing.T) {
	server := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Location", "http://"+request.Host+"/accounts/1")
			w.Header().Set("X-Request-Id", id)
			_, _ = w.Write([]byte(`{"account":{"id":"` + id + `","username":"alice"}}`))
		}))
	}
	rails := server("rails-generated")
	defer rails.Close()
	goServer := server("go-generated")
	defer goServer.Close()

	manifest := Manifest{Cases: []Case{{
		ID: "REST-INSTANCE-001", Contract: "REST-API", Method: http.MethodGet, Path: "/api/v1/instance",
		CompareHeaders:   []string{"Content-Type", "Location", "X-Request-Id"},
		VolatileHeaders:  []string{"X-Request-Id"},
		JSONReplacements: map[string]any{"/account/id": "{{ID}}"},
	}}}
	results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL}).Run(context.Background(), manifest)
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("Run results = %#v", results)
	}
}

func TestRunnerReportsUndeclaredDifference(t *testing.T) {
	rails := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true}`))
	}))
	defer rails.Close()
	goServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":false}`))
	}))
	defer goServer.Close()

	results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL}).Run(context.Background(), Manifest{Cases: []Case{{
		ID: "REST-DIFF-001", Contract: "REST-API", Method: http.MethodGet, Path: "/api/v1/instance",
	}}})
	if len(results) != 1 || !strings.Contains(results[0].Error, "JSON body differs") {
		t.Fatalf("Run results = %#v", results)
	}
}

func TestRunnerAllowsOnlyDeclaredGoJSONExtensions(t *testing.T) {
	server := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	}

	rails := server(`{"statuses":[{"id":"1"},{"id":"2"}]}`)
	defer rails.Close()
	goServer := server(`{"statuses":[{"id":"1","quote_id":null},{"id":"2","quote_id":"9"}]}`)
	defer goServer.Close()

	item := Case{
		ID: "GO-EXTENSION-001", Contract: "STATUS", Method: http.MethodGet, Path: "/api/v1/statuses",
		GoOnlyJSONPointers: []string{"/statuses/*/quote_id"},
	}
	results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL}).Run(context.Background(), Manifest{Cases: []Case{item}})
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("Run results = %#v", results)
	}
}

func TestRunnerRejectsStaleOrOverbroadGoJSONExtensionDeclarations(t *testing.T) {
	server := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	}

	t.Run("Rails gains the declared field", func(t *testing.T) {
		rails := server(`{"quote_id":null}`)
		defer rails.Close()
		goServer := server(`{"quote_id":null}`)
		defer goServer.Close()
		results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL}).Run(context.Background(), Manifest{Cases: []Case{{
			ID: "GO-EXTENSION-RAILS-001", Contract: "STATUS", Method: http.MethodGet, Path: "/status",
			GoOnlyJSONPointers: []string{"/quote_id"},
		}}})
		if len(results) != 1 || !strings.Contains(results[0].Error, "unexpectedly exists in Rails") {
			t.Fatalf("Run results = %#v", results)
		}
	})

	t.Run("Go drops the declared field", func(t *testing.T) {
		rails := server(`{"id":"1"}`)
		defer rails.Close()
		goServer := server(`{"id":"1"}`)
		defer goServer.Close()
		results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL}).Run(context.Background(), Manifest{Cases: []Case{{
			ID: "GO-EXTENSION-MISSING-001", Contract: "STATUS", Method: http.MethodGet, Path: "/status",
			GoOnlyJSONPointers: []string{"/quote_id"},
		}}})
		if len(results) != 1 || !strings.Contains(results[0].Error, "absent from Go response") {
			t.Fatalf("Run results = %#v", results)
		}
	})
}

func TestRunnerCanExplicitlyCompareOnlyStatusAndHeadersForPaonOwnedUI(t *testing.T) {
	server := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "private, no-store")
			_, _ = w.Write([]byte(body))
		}))
	}
	rails := server("<html>Rails UI</html>")
	defer rails.Close()
	goServer := server("<html>Paon UI</html>")
	defer goServer.Close()

	compareBody := false
	results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL}).Run(context.Background(), Manifest{Cases: []Case{{
		ID:             "AUTH-SIGNIN-001",
		Contract:       "AUTH-WEB",
		Method:         http.MethodGet,
		Path:           "/auth/sign_in",
		CompareHeaders: []string{"Content-Type", "Cache-Control"},
		CompareBody:    &compareBody,
	}}})
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("Run results = %#v", results)
	}
}

func TestRunnerComparesCORSHeadersByDefault(t *testing.T) {
	server := func(corsOrigin string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if corsOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", corsOrigin)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	}
	rails := server("*")
	defer rails.Close()
	goServer := server("")
	defer goServer.Close()

	results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL}).Run(context.Background(), Manifest{Cases: []Case{{
		ID: "CORS-DIFF-001", Contract: "CORS", Method: http.MethodGet, Path: "/nodeinfo/2.0",
	}}})
	if len(results) != 1 || !strings.Contains(results[0].Error, "Access-Control-Allow-Origin") {
		t.Fatalf("Run results = %#v", results)
	}
}

func TestCoreManifestCoversMastodon43PublicAndAuthContracts(t *testing.T) {
	file, err := os.Open("../../../testdata/differential/core.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"DISC-HOSTMETA-JRD-001":           false,
		"DISC-OAUTH-METADATA-001":         false,
		"DISC-NODEINFO-20-001":            false,
		"REST-INSTANCE-V2-001":            false,
		"REST-BATCH-ACCOUNTS-EMPTY-001":   false,
		"REST-BATCH-STATUSES-EMPTY-001":   false,
		"AUTH-NOTIFICATION-V2-001":        false,
		"AUTH-NOTIFICATION-POLICY-V2-001": false,
		"AUTH-ANNUAL-REPORTS-001":         false,
		"OAUTH-REVOKE-CORS-001":           false,
	}
	for _, item := range manifest.Cases {
		if _, exists := want[item.ID]; exists {
			want[item.ID] = true
		}
		if item.CompareBody != nil && !*item.CompareBody && item.ID != "AUTH-SIGNIN-001" {
			t.Errorf("only the Paon-owned sign-in UI may skip Rails body equality, got %s", item.ID)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("core differential manifest is missing %s", id)
		}
	}
}

func TestRunnerMaterializesAuthenticatedFixtureFromEnvironment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer fixture-token" || request.URL.Path != "/api/v1/accounts/42" {
			http.Error(w, "fixture mismatch", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42"}`))
	}))
	defer server.Close()

	environment := map[string]string{
		"PAON_DIFF_USER_AUTHORIZATION": "Bearer fixture-token",
		"PAON_DIFF_LOCAL_ACCOUNT_ID":   "42",
	}
	lookup := func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	}
	manifest := Manifest{Cases: []Case{{
		ID:             "AUTH-FIXTURE-001",
		Contract:       "AUTHENTICATED",
		Method:         http.MethodGet,
		Path:           "/api/v1/accounts/${PAON_DIFF_LOCAL_ACCOUNT_ID}",
		HeadersFromEnv: map[string]string{"Authorization": "PAON_DIFF_USER_AUTHORIZATION"},
	}}}
	results := (Runner{RailsBaseURL: server.URL, GoBaseURL: server.URL, Env: lookup}).Run(context.Background(), manifest)
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("Run results = %#v", results)
	}

	delete(environment, "PAON_DIFF_USER_AUTHORIZATION")
	results = (Runner{RailsBaseURL: server.URL, GoBaseURL: server.URL, Env: lookup}).Run(context.Background(), manifest)
	if len(results) != 1 || !strings.Contains(results[0].Error, "PAON_DIFF_USER_AUTHORIZATION") {
		t.Fatalf("missing environment result = %#v", results)
	}
}

func TestRunnerKeepsTargetSpecificSessionAndCSRFHeadersOutOfManifestResults(t *testing.T) {
	server := func(cookie, csrf string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Cookie") != cookie || request.Header.Get("X-CSRF-Token") != csrf {
				http.Error(w, "target authentication mismatch", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	}
	rails := server("_mastodon_session=rails", "rails-csrf")
	defer rails.Close()
	goServer := server("paon_session=go", "go-csrf")
	defer goServer.Close()

	environment := map[string]string{
		"PAON_DIFF_RAILS_USER_COOKIE":     "_mastodon_session=rails",
		"PAON_DIFF_RAILS_USER_CSRF_TOKEN": "rails-csrf",
		"PAON_DIFF_GO_USER_COOKIE":        "paon_session=go",
		"PAON_DIFF_GO_USER_CSRF_TOKEN":    "go-csrf",
	}
	lookup := func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	}
	manifest := Manifest{Cases: []Case{{
		ID:       "SETTINGS-WEB-CSRF-001",
		Contract: "SETTINGS-WEB",
		Method:   http.MethodPatch,
		Path:     "/settings/preferences/posting_defaults",
		RailsHeadersFromEnv: map[string]string{
			"Cookie":       "PAON_DIFF_RAILS_USER_COOKIE",
			"X-CSRF-Token": "PAON_DIFF_RAILS_USER_CSRF_TOKEN",
		},
		GoHeadersFromEnv: map[string]string{
			"Cookie":       "PAON_DIFF_GO_USER_COOKIE",
			"X-CSRF-Token": "PAON_DIFF_GO_USER_CSRF_TOKEN",
		},
	}}}
	results := (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL, Env: lookup}).Run(context.Background(), manifest)
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("Run results = %#v", results)
	}

	delete(environment, "PAON_DIFF_GO_USER_CSRF_TOKEN")
	results = (Runner{RailsBaseURL: rails.URL, GoBaseURL: goServer.URL, Env: lookup}).Run(context.Background(), manifest)
	if len(results) != 1 || !strings.Contains(results[0].Error, "PAON_DIFF_GO_USER_CSRF_TOKEN") || strings.Contains(results[0].Error, "rails-csrf") {
		t.Fatalf("missing target environment result = %#v", results)
	}
}

func TestAuthenticatedManifestCoversReleaseMatrix(t *testing.T) {
	file, err := os.Open("../../../testdata/differential/authenticated.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) < 40 {
		t.Fatalf("authenticated differential cases = %d, want at least 40", len(manifest.Cases))
	}
	wantContracts := map[string]bool{
		"ACCOUNT-LOCAL":         false,
		"ACCOUNT-REMOTE":        false,
		"ACCOUNT-SUSPENSION":    false,
		"STATUS-VISIBILITY":     false,
		"STATUS-NONDISCLOSURE":  false,
		"STATUS-BLOCK":          false,
		"OAUTH-SCOPE":           false,
		"ADMIN-PAGINATION":      false,
		"NOTIFICATIONS-GROUPED": false,
		"SEARCH":                false,
		"QUOTE-CREATE":          false,
		"QUOTE-LIST":            false,
		"QUOTE-REVOKE":          false,
		"QUOTE-POLICY":          false,
		"FEP-7888":              false,
		"ACTIVITYPUB-NUMERIC":   false,
		"FEED-ACCESS":           false,
		"ADMIN-DISCOVERY":       false,
		"ADMIN-USERNAME-BLOCKS": false,
		"POSTING-DEFAULTS":      false,
	}
	for _, item := range manifest.Cases {
		if _, exists := wantContracts[item.Contract]; exists {
			wantContracts[item.Contract] = true
		}
		if len(item.GoOnlyJSONPointers) > 0 {
			t.Errorf("authenticated case %s has an undeclared Go-only JSON exception", item.ID)
		}
		if item.CompareBody != nil && !*item.CompareBody {
			switch item.Contract {
			case "ADMIN-DISCOVERY", "ADMIN-USERNAME-BLOCKS", "POSTING-DEFAULTS":
			default:
				t.Errorf("authenticated case %s skips body comparison outside retained Paon web UI", item.ID)
			}
		}
	}
	for contract, found := range wantContracts {
		if !found {
			t.Errorf("authenticated differential manifest is missing contract %s", contract)
		}
	}
}

func TestMastodon45StatefulDifferentialSequenceIsDeterministic(t *testing.T) {
	file, err := os.Open("../../../testdata/differential/authenticated.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Case, len(manifest.Cases))
	positions := make(map[string]int, len(manifest.Cases))
	for index, item := range manifest.Cases {
		byID[item.ID] = item
		positions[item.ID] = index
	}
	ordered := [][]string{
		{"AP-QUOTE-AUTHORIZATION-001", "QUOTE-LIST-001", "QUOTE-REVOKE-001"},
		{"ADMIN-DISCOVERY-UPDATE-001", "FEED-LOCAL-ANONYMOUS-001", "FEED-LOCAL-AUTHORIZED-001"},
		{"POSTING-DEFAULTS-UPDATE-001", "POSTING-DEFAULTS-POST-001", "POSTING-DEFAULTS-SHOW-001"},
		{"USERNAME-BLOCK-CREATE-001", "USERNAME-BLOCK-INDEX-001"},
	}
	for _, sequence := range ordered {
		for index, id := range sequence {
			_, exists := byID[id]
			if !exists {
				t.Fatalf("authenticated differential manifest is missing stateful case %s", id)
			}
			if index > 0 && positions[sequence[index-1]] >= positions[id] {
				t.Fatalf("stateful cases are out of order: %s must precede %s", sequence[index-1], id)
			}
		}
	}
	for _, id := range []string{"QUOTE-CREATE-001", "POSTING-DEFAULTS-POST-001"} {
		item, exists := byID[id]
		if !exists || item.Headers["Idempotency-Key"] == "" {
			t.Fatalf("state-creating case %s must declare an idempotency key", id)
		}
	}
	for _, item := range manifest.Cases {
		for name := range item.RailsHeaders {
			if name == "Cookie" || name == "Authorization" || name == "X-CSRF-Token" {
				t.Errorf("case %s embeds a Rails credential in the manifest", item.ID)
			}
		}
		for name := range item.GoHeaders {
			if name == "Cookie" || name == "Authorization" || name == "X-CSRF-Token" {
				t.Errorf("case %s embeds a Go credential in the manifest", item.ID)
			}
		}
	}
}
