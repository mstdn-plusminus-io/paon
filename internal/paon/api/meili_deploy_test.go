package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestDeployMeiliIndexesRequiresEnabledMeili(t *testing.T) {
	server := &Server{cfg: config.Config{MeiliEnabled: false, MeiliHost: "http://meili.test"}}
	if _, err := server.DeployMeiliIndexes(t.Context(), MeiliDeployOptions{}); !errors.Is(err, errMeiliDisabled) {
		t.Fatalf("DeployMeiliIndexes error = %v", err)
	}
}

func TestDeployMeiliIndexesRequiresDatabase(t *testing.T) {
	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test"}}
	_, err := server.DeployMeiliIndexes(t.Context(), MeiliDeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is not set") {
		t.Fatalf("DeployMeiliIndexes error = %v", err)
	}
}

func TestMeiliDeployLogWritesCount(t *testing.T) {
	var out bytes.Buffer
	meiliDeployLog(&out, "statuses", 42)
	if got := out.String(); got != "statuses: 42\n" {
		t.Fatalf("log = %q", got)
	}
}

func TestMeiliDeployProgressLogPrintsResumeHint(t *testing.T) {
	var out bytes.Buffer
	meiliDeployProgressLog(&out, "tmp/meilisearch_deploy_progress.json", meiliDeployProgress{Model: meiliDeployModelStatus, LastProcessedID: 5500000})
	got := out.String()
	for _, want := range []string{
		"progress saved\n",
		"  model: Status\n",
		"  last processed ID: 5500000\n",
		"  progress file: tmp/meilisearch_deploy_progress.json\n",
		"  resume: RESUME=true paon-meili-deploy\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress log missing %q in %q", want, got)
		}
	}
}

func TestMeiliDeployProgressLogPrintsInstanceDomain(t *testing.T) {
	var out bytes.Buffer
	meiliDeployProgressLog(&out, "tmp/progress.json", meiliDeployProgress{Model: meiliDeployModelInstance, LastProcessedDomain: "remote.example"})
	got := out.String()
	if !strings.Contains(got, "  last processed domain: remote.example\n") {
		t.Fatalf("progress log = %q", got)
	}
	if strings.Contains(got, "last processed ID") {
		t.Fatalf("domain progress log should not print an ID: %q", got)
	}
}

func TestMeiliDeployProgressRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmp", "meilisearch_deploy_progress.json")
	progress := meiliDeployProgress{Model: meiliDeployModelStatus, LastProcessedID: 5500000}
	if err := writeMeiliDeployProgress(path, progress); err != nil {
		t.Fatalf("writeMeiliDeployProgress: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read progress file: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode progress JSON: %v", err)
	}
	if decoded["model"] != "Status" || decoded["last_processed_id"].(float64) != 5500000 {
		t.Fatalf("progress JSON = %#v", decoded)
	}
	got, ok, err := readMeiliDeployProgress(path)
	if err != nil || !ok {
		t.Fatalf("readMeiliDeployProgress ok=%v err=%v", ok, err)
	}
	if got != progress {
		t.Fatalf("progress = %#v", got)
	}
}

func TestMeiliDeployProgressReadsRailsResumeShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmp", "meilisearch_deploy_progress.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"current_model":"Status","current_model_index":1,"last_processed_id":5500000}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := readMeiliDeployProgress(path)
	if err != nil || !ok {
		t.Fatalf("readMeiliDeployProgress ok=%v err=%v", ok, err)
	}
	want := meiliDeployProgress{Model: meiliDeployModelStatus, LastProcessedID: 5500000}
	if got != want {
		t.Fatalf("progress = %#v, want %#v", got, want)
	}
}

func TestMeiliDeployResumeModelOrdering(t *testing.T) {
	if shouldRunMeiliDeployModel(meiliDeployModelStatus, meiliDeployModelAccount) {
		t.Fatal("resume from Status should skip Account")
	}
	for _, model := range []string{meiliDeployModelStatus, meiliDeployModelTag, meiliDeployModelInstance} {
		if !shouldRunMeiliDeployModel(meiliDeployModelStatus, model) {
			t.Fatalf("resume from Status should run %s", model)
		}
	}
	if got := meiliDeployStartID(meiliDeployProgress{Model: meiliDeployModelStatus, LastProcessedID: 123}, meiliDeployModelStatus); got != 123 {
		t.Fatalf("start id = %d", got)
	}
	if got := meiliDeployStartDomain(meiliDeployProgress{Model: meiliDeployModelInstance, LastProcessedDomain: "remote.example"}); got != "remote.example" {
		t.Fatalf("start domain = %q", got)
	}
}
