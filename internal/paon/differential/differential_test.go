package differential

import (
	"context"
	"net/http"
	"net/http/httptest"
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
