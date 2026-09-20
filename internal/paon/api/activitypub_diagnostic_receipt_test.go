package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestActivityPubInboxReceiptMatchesAsynqPayload(t *testing.T) {
	for name, body := range map[string]string{
		"compact":             `{"type":"Create","object":{"content":"hello"}}`,
		"whitespace and HTML": " {\n\t\"type\": \"Create\", \"object\": {\"content\": \"<p>日本語 & text</p>\"}\n } ",
		"literal precision":   `{"type":"Create","id":9007199254740993,"other":1.2300e+09,"content":"\u65e5本語"}`,
		"unicode separators":  "{\"type\":\"Create\",\"content\":\"before\u2028middle\u2029after\"}",
	} {
		t.Run(name, func(t *testing.T) {
			receipt := newActivityPubInboxReceipt(nil, []byte(body))
			if receipt == nil {
				t.Fatal("valid body has no receipt")
			}
			job := activityPubInboxProcessingJob{
				ActorID: 42,
				Body:    json.RawMessage(body),
				Receipt: receipt,
			}
			payload, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			var processed activityPubInboxProcessingJob
			if err := json.Unmarshal(payload, &processed); err != nil {
				t.Fatal(err)
			}
			if processed.Receipt == nil {
				t.Fatal("Asynq payload lost receipt")
			}
			receivedHash := sha256.Sum256([]byte(body))
			queuedHash := sha256.Sum256(processed.Body)
			if got, want := processed.Receipt.BodySHA256, hex.EncodeToString(receivedHash[:]); got != want {
				t.Fatalf("received hash = %q, want %q", got, want)
			}
			if got, want := processed.Receipt.QueuedBodySHA256, hex.EncodeToString(queuedHash[:]); got != want {
				t.Fatalf("queued hash = %q, want %q", got, want)
			}
			if name == "whitespace and HTML" || name == "unicode separators" {
				if processed.Receipt.BodySHA256 == processed.Receipt.QueuedBodySHA256 {
					t.Fatal("receipt did not distinguish normal JSON marshaling changes")
				}
			}
			if name == "literal precision" {
				for _, literal := range []string{"9007199254740993", "1.2300e+09", `\u65e5本語`} {
					if !strings.Contains(string(processed.Body), literal) {
						t.Fatalf("queued body lost original literal %q: %s", literal, processed.Body)
					}
				}
			}
		})
	}
}

func TestActivityPubInboxReceiptIsBoundedAndExcludesBody(t *testing.T) {
	privateText := "private text that must not be duplicated"
	body, err := json.Marshal(map[string]string{
		"content": strings.Repeat(privateText, 10000),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := newActivityPubInboxReceipt(nil, body)
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 1024 || strings.Contains(string(encoded), privateText) {
		t.Fatalf("receipt contains payload data or grew with the body: %d bytes", len(encoded))
	}
}

func TestActivityPubInboxReceiptRetainsLegacyAndInvalidPayloadBehavior(t *testing.T) {
	t.Run("legacy payload", func(t *testing.T) {
		var job activityPubInboxProcessingJob
		if err := json.Unmarshal([]byte(`{"actor_id":42,"body":{"type":"Create"}}`), &job); err != nil {
			t.Fatal(err)
		}
		if job.Receipt != nil || job.ActorID != 42 {
			t.Fatalf("legacy task = %#v", job)
		}
		encoded, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"receipt"`) {
			t.Fatal("legacy task was given receipt metadata it never had")
		}
	})
	t.Run("invalid payload", func(t *testing.T) {
		body := []byte(`{"type":`)
		job := activityPubInboxProcessingJob{
			ActorID: 42,
			Body:    body,
			Receipt: newActivityPubInboxReceipt(nil, body),
		}
		if job.Receipt != nil {
			t.Fatal("invalid JSON should leave error handling to enqueue")
		}
		called := false
		err := enqueueActivityPubInboxProcessingJobWithAsynq(job, func(job activityPubInboxProcessingJob) bool {
			called = true
			_, err := json.Marshal(job)
			return err == nil
		})
		if !called || err == nil || err.Error() != "activitypub inbox processing Asynq enqueue failed" {
			t.Fatalf("invalid JSON enqueue behavior changed: called=%v err=%v", called, err)
		}
	})
}

func TestActivityPubRuntimeInfoUsesConfiguredAndBuildVersions(t *testing.T) {
	for name, server := range map[string]*Server{
		"nil server": nil,
		"default":    {},
		"configured": {cfg: config.Config{Version: "test-version"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := activityPubRuntimeInfo(server)
			want := config.DefaultVersion
			if name == "configured" {
				want = "test-version"
			}
			if got.Version != want || got.BuildVersion != config.DefaultVersion || got.GoVersion != runtime.Version() {
				t.Fatalf("runtime diagnostics = %#v", got)
			}
		})
	}
}

func TestActivityPubRuntimeInfoSelectsAvailableBuildMetadata(t *testing.T) {
	for _, modified := range []string{"true", "false"} {
		t.Run(modified, func(t *testing.T) {
			got := activityPubRuntimeInfoFromBuild(&debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc123"},
				{Key: "vcs.modified", Value: modified},
				{Key: "-ldflags", Value: "unrelated-sensitive-build-flag"},
			}})
			if got.VCSRevision != "abc123" || got.VCSModified == nil || *got.VCSModified != (modified == "true") {
				t.Fatalf("VCS metadata = %#v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "unrelated-sensitive-build-flag") {
				t.Fatal("runtime diagnostics leaked unrelated build settings")
			}
		})
	}
	for name, build := range map[string]*debug.BuildInfo{
		"unavailable": nil,
		"absent":      {},
		"invalid":     {Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "unknown"}}},
	} {
		t.Run(name, func(t *testing.T) {
			got := activityPubRuntimeInfoFromBuild(build)
			if got.VCSRevision != "" || got.VCSModified != nil {
				t.Fatalf("unavailable metadata was invented: %#v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "vcs_") {
				t.Fatalf("unavailable VCS metadata was not omitted: %s", encoded)
			}
		})
	}
}

func TestActivityPubRuntimeInfoSelectsJSONLDDependencyVersion(t *testing.T) {
	for name, test := range map[string]struct {
		module *debug.Module
		want   string
	}{
		"dependency": {module: &debug.Module{Path: "github.com/piprate/json-gold", Version: "v0.5.0"}, want: "v0.5.0"},
		"versioned replacement": {
			module: &debug.Module{Path: "github.com/piprate/json-gold", Version: "v0.5.0", Replace: &debug.Module{Path: "example.com/fork", Version: "v0.6.0"}},
			want:   "v0.6.0",
		},
		"local replacement": {
			module: &debug.Module{Path: "github.com/piprate/json-gold", Version: "v0.5.0", Replace: &debug.Module{Path: "../private-local-checkout"}},
			want:   "(devel)",
		},
		"absent": {module: &debug.Module{Path: "example.com/unrelated", Version: "v9.0.0"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := activityPubRuntimeInfoFromBuild(&debug.BuildInfo{Deps: []*debug.Module{test.module}})
			if got.JSONLDVersion != test.want {
				t.Fatalf("JSON-LD version = %q, want %q", got.JSONLDVersion, test.want)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private-local-checkout") || strings.Contains(string(encoded), "example.com") {
				t.Fatal("runtime diagnostics leaked dependency paths")
			}
			if test.want == "" && strings.Contains(string(encoded), "jsonld_version") {
				t.Fatalf("unavailable dependency version was not omitted: %s", encoded)
			}
		})
	}
}
