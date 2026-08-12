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
	if len(manifest.Cases) < 20 {
		t.Fatalf("authenticated differential cases = %d, want at least 20", len(manifest.Cases))
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
	}
	wantGoOnlyPointers := map[string]string{
		"AUTH-STATUS-PUBLIC-001":               "/quote_id,/quote_original_url",
		"AUTH-STATUS-PRIVATE-OWNER-001":        "/quote_id,/quote_original_url",
		"AUTH-STATUS-BLOCKED-001":              "/quote_id,/quote_original_url",
		"AUTH-BATCH-STATUSES-001":              "/*/quote_id,/*/quote_original_url",
		"AUTH-ACCOUNT-PINNED-PAGINATION-001":   "/*/quote_id,/*/quote_original_url",
		"AUTH-NOTIFICATIONS-V2-PAGINATION-001": "/statuses/*/quote_id,/statuses/*/quote_original_url",
	}
	for _, item := range manifest.Cases {
		if _, exists := wantContracts[item.Contract]; exists {
			wantContracts[item.Contract] = true
		}
		wantPointers, allowed := wantGoOnlyPointers[item.ID]
		if len(item.GoOnlyJSONPointers) > 0 && !allowed {
			t.Errorf("authenticated case %s has an undeclared Go-only JSON exception", item.ID)
		}
		if allowed && strings.Join(item.GoOnlyJSONPointers, ",") != wantPointers {
			t.Errorf("authenticated case %s Go-only JSON pointers = %#v, want %q", item.ID, item.GoOnlyJSONPointers, wantPointers)
		}
	}
	for contract, found := range wantContracts {
		if !found {
			t.Errorf("authenticated differential manifest is missing contract %s", contract)
		}
	}
}
