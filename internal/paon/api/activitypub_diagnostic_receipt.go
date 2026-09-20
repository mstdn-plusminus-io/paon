package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

// Only explicitly selected build metadata is retained. Build settings can contain
// flags and paths that are unrelated to diagnosing a signature failure.
type activityPubRuntimeDiagnostics struct {
	Version       string `json:"paon_version"`
	BuildVersion  string `json:"build_version"`
	GoVersion     string `json:"go_version"`
	JSONLDVersion string `json:"jsonld_version,omitempty"`
	VCSRevision   string `json:"vcs_revision,omitempty"`
	VCSModified   *bool  `json:"vcs_modified,omitempty"`
}

var activityPubProcessRuntimeInfo = sync.OnceValue(func() activityPubRuntimeDiagnostics {
	info, _ := debug.ReadBuildInfo()
	return activityPubRuntimeInfoFromBuild(info)
})

func activityPubRuntimeInfoFromBuild(info *debug.BuildInfo) activityPubRuntimeDiagnostics {
	diagnostics := activityPubRuntimeDiagnostics{
		Version:      config.DefaultVersion,
		BuildVersion: config.DefaultVersion,
		GoVersion:    runtime.Version(),
	}
	if info != nil {
		for _, dependency := range info.Deps {
			if dependency.Path != "github.com/piprate/json-gold" {
				continue
			}
			diagnostics.JSONLDVersion = dependency.Version
			if dependency.Replace != nil {
				diagnostics.JSONLDVersion = dependency.Replace.Version
				if diagnostics.JSONLDVersion == "" {
					diagnostics.JSONLDVersion = "(devel)"
				}
			}
			break
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				diagnostics.VCSRevision = setting.Value
			case "vcs.modified":
				if modified, err := strconv.ParseBool(setting.Value); err == nil {
					diagnostics.VCSModified = &modified
				}
			}
		}
	}
	return diagnostics.sanitized()
}

func activityPubRuntimeInfo(s *Server) activityPubRuntimeDiagnostics {
	diagnostics := activityPubProcessRuntimeInfo()
	if s != nil && s.cfg.Version != "" {
		diagnostics.Version = s.cfg.Version
	}
	return diagnostics.sanitized()
}

func (diagnostics activityPubRuntimeDiagnostics) sanitized() activityPubRuntimeDiagnostics {
	diagnostics.Version = activityPubSafeLogValue(diagnostics.Version, 128)
	diagnostics.BuildVersion = activityPubSafeLogValue(diagnostics.BuildVersion, 128)
	diagnostics.GoVersion = activityPubSafeLogValue(diagnostics.GoVersion, 128)
	diagnostics.JSONLDVersion = activityPubSafeLogValue(diagnostics.JSONLDVersion, 128)
	diagnostics.VCSRevision = activityPubSafeLogValue(diagnostics.VCSRevision, 128)
	return diagnostics
}

type activityPubInboxReceipt struct {
	BodySHA256       string                        `json:"body_sha256"`
	QueuedBodySHA256 string                        `json:"queued_body_sha256"`
	Runtime          activityPubRuntimeDiagnostics `json:"runtime"`
}

type activityPubInboxReceiptContextKey struct{}

func newActivityPubInboxReceipt(s *Server, body []byte) *activityPubInboxReceipt {
	// Asynq marshals the job with encoding/json. RawMessage preserves numeric
	// literals, but that marshal compacts whitespace and escapes HTML characters.
	// Hash those exact bytes as well so a normal queue round trip is distinguishable
	// from a later payload change without storing another copy of the body.
	queuedBody, err := json.Marshal(json.RawMessage(body))
	if err != nil {
		// Leave invalid-body handling to the existing job marshal/enqueue path.
		return nil
	}
	receivedHash := sha256.Sum256(body)
	queuedHash := sha256.Sum256(queuedBody)
	return &activityPubInboxReceipt{
		BodySHA256:       hex.EncodeToString(receivedHash[:]),
		QueuedBodySHA256: hex.EncodeToString(queuedHash[:]),
		Runtime:          activityPubRuntimeInfo(s),
	}
}
