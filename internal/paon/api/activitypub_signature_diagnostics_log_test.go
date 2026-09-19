package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
)

func assertSignatureDiagnosticTaskLog(t *testing.T, task *asynq.Task, err error, excluded ...string) {
	t.Helper()
	var output bytes.Buffer
	previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	}()
	logAsynqTaskError(context.Background(), task, err)
	line := strings.TrimSuffix(output.String(), "\n")
	if strings.ContainsAny(line, "\n\r") || !strings.Contains(line, `event=asynq_task_failed`) || !strings.Contains(line, `task_type="activitypub:processing"`) {
		t.Fatalf("invalid task error log: %s", line)
	}
	_, quoted, ok := strings.Cut(line, " error=")
	if !ok {
		t.Fatalf("log is missing error: %s", line)
	}
	logged, unquoteErr := strconv.Unquote(quoted)
	if unquoteErr != nil {
		t.Fatal(unquoteErr)
	}
	if logged != err.Error() {
		t.Fatalf("Asynq log changed or truncated the worker error (logged %d, returned %d bytes)", len(logged), len(err.Error()))
	}
	_, raw, ok := strings.Cut(logged, "; signature_diagnostics=")
	if !ok || !json.Valid([]byte(raw)) {
		t.Fatalf("Asynq error lost complete diagnostic JSON: %s", logged)
	}
	var details activityPubSignatureDiagnostics
	if json.Unmarshal([]byte(raw), &details) != nil || details.Version != 1 {
		t.Fatalf("invalid diagnostic schema: %s", raw)
	}
	var diagnostic *activityPubSignatureVerificationError
	if !errors.As(err, &diagnostic) {
		t.Fatal("worker error lost typed verification cause")
	}
	expected, marshalErr := json.Marshal(diagnostic.Diagnostics)
	if marshalErr != nil || string(expected) != raw {
		t.Fatal("serialized diagnostic lost evidence while wrapping the error")
	}
	for _, value := range excluded {
		if strings.Contains(logged, value) || strings.Contains(line, value) {
			t.Fatal("diagnostic log contains excluded body or private-key data")
		}
	}
}

func TestAsynqSignatureDiagnosticLogPreservesJSON(t *testing.T) {
	// A large public-key snapshot and actor URIs exceed the ordinary 4 KiB error
	// limit. The worker log must retain parseable JSON, including its last field.
	diagnostic := &activityPubSignatureVerificationError{
		cause: errors.New("verify linked-data RSA signature: verification error"),
		Diagnostics: activityPubSignatureDiagnostics{
			Version: 1, Algorithm: "RsaSignature2017", Normalization: "URDNA2015",
			PublicKeySPKI: strings.Repeat("A", 1500),
			Creator:       strings.Repeat("c", 512), HTTPActorURI: strings.Repeat("r", 512), SignatureActorURI: strings.Repeat("a", 512),
			Verification: activityPubSignatureDocumentInfo{BodySHA256: strings.Repeat("a", 64), OptionsSHA256: strings.Repeat("b", 64), DocumentSHA256: strings.Repeat("c", 64), VerificationSHA256: strings.Repeat("d", 64)},
			Original:     &activityPubSignatureDocumentInfo{BodySHA256: strings.Repeat("a", 64), OptionsSHA256: strings.Repeat("b", 64), DocumentSHA256: strings.Repeat("c", 64), VerificationSHA256: strings.Repeat("d", 64)},
		},
	}
	body := []byte(`{"type":"Create","actor":"https://example.org/alice","object":{"content":"never log this body"}}`)
	err := activityPubProcessingError(body, 18251, 0, fmt.Errorf("%w: %w", errActivityPubEventNotApplied, diagnostic))
	if len(err.Error()) <= 4*1024 {
		t.Fatal("test error does not exercise the extended diagnostic log limit")
	}
	assertSignatureDiagnosticTaskLog(t, asynq.NewTask(asynqTaskActivityPubProcessing, body), err, "never log this body")
}

func TestActivityPubSignatureReceiptDetectsBodyChanges(t *testing.T) {
	body := []byte(`{"type":"Create"}`)
	receipt := newActivityPubInboxReceipt(nil, body)
	err := &activityPubSignatureVerificationError{Diagnostics: activityPubSignatureDiagnostics{
		Original: &activityPubSignatureDocumentInfo{BodySHA256: activityPubDiagnosticSHA256([]byte(`{"type":"Delete"}`))},
	}}
	attachActivityPubSignatureReceipt(err, receipt)
	if err.Diagnostics.ReceiptMatchesWorkerBody == nil || *err.Diagnostics.ReceiptMatchesWorkerBody {
		t.Fatal("receipt failed to detect a changed queue body")
	}
	if err.Diagnostics.Receipt == receipt {
		t.Fatal("diagnostic receipt must retain a snapshot")
	}
}

func TestAsynqSignatureDiagnosticLogPreservesMaximallyEscapedJSON(t *testing.T) {
	// JSON escaping expands each ampersand to six bytes. Exercise the field
	// bounds after escaping, with both runtime snapshots and the maximum allowed
	// public-key snapshot; plain ASCII length alone does not bound the log.
	runtime := activityPubRuntimeDiagnostics{
		Version: strings.Repeat("&", 128), BuildVersion: strings.Repeat("&", 128),
		GoVersion: strings.Repeat("&", 128), JSONLDVersion: strings.Repeat("&", 128), VCSRevision: strings.Repeat("&", 128),
	}.sanitized()
	diagnostic := &activityPubSignatureVerificationError{
		cause: errors.New("verify linked-data RSA signature: verification error"),
		Diagnostics: activityPubSignatureDiagnostics{
			Version: 1, Algorithm: "RsaSignature2017", Normalization: "URDNA2015",
			BuiltinContextsSHA256: strings.Repeat("a", 64), Runtime: runtime,
			HTTPActorID: 18251, HTTPActorURI: activityPubSafeLogValue(strings.Repeat("&", 512), 512),
			SignatureActorID: 18252, SignatureActorURI: activityPubSafeLogValue(strings.Repeat("&", 512), 512),
			Creator: activityPubSafeLogValue(strings.Repeat("&", 512), 512), Created: activityPubSafeLogValue(strings.Repeat("&", 128), 128),
			ActorUpdatedAt: "2026-09-20T00:00:00.123456789Z", KeyBits: 8192,
			PublicKeySHA256: strings.Repeat("b", 64), PublicKeySPKI: strings.Repeat("A", base64.StdEncoding.EncodedLen(2048)),
			SignatureBytes: 1024, SignatureSHA256: strings.Repeat("c", 64),
			RecoveredDigestStatus: "sha256_digest_info", RecoveredSHA256: strings.Repeat("d", 64),
			Verification: activityPubSignatureDocumentInfo{BodyBytes: 1 << 20, BodySHA256: strings.Repeat("a", 64), OptionsSHA256: strings.Repeat("b", 64), DocumentSHA256: strings.Repeat("c", 64), VerificationSHA256: strings.Repeat("d", 64)},
			Original: &activityPubSignatureDocumentInfo{
				BodyBytes: 1 << 20, BodySHA256: strings.Repeat("a", 64), OptionsSHA256: strings.Repeat("b", 64), DocumentSHA256: strings.Repeat("c", 64), VerificationSHA256: strings.Repeat("d", 64),
				Error: activityPubSafeLogValue(strings.Repeat("&", 256), 256),
			},
		},
	}
	attachActivityPubSignatureReceipt(diagnostic, &activityPubInboxReceipt{
		BodySHA256: strings.Repeat("&", 64), QueuedBodySHA256: strings.Repeat("&", 64), Runtime: runtime,
	})
	// NUL is retained by the log sanitizer and becomes four bytes under %q.
	// An actual accepted activity type is a short known value; all remaining
	// envelope fields can reach the 512-byte bound.
	body, err := json.Marshal(map[string]any{
		"type": "Create", "id": strings.Repeat("\x00", 512), "actor": strings.Repeat("\x00", 512),
		"object": map[string]any{"id": strings.Repeat("\x00", 512), "content": "excluded private body"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = activityPubProcessingError(body, 18251, 18252, fmt.Errorf("%w: %w", errActivityPubEventNotApplied, diagnostic))
	if len(err.Error()) <= 16*1024 {
		t.Fatal("test error does not exercise JSON escaping beyond the previous 16 KiB limit")
	}
	assertSignatureDiagnosticTaskLog(t, asynq.NewTask(asynqTaskActivityPubProcessing, body), err, "excluded private body")
}
