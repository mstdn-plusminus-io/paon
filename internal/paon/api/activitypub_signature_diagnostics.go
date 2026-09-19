package api

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sync"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// Include room for JSON escaping of the bounded metadata and the processing
// envelope; cutting the JSON at the ordinary 4 KiB error limit loses evidence.
const activityPubSignatureDiagnosticLogLimit = 32 * 1024

// Only verification evidence is retained here. The activity body stays in the
// Asynq payload, and private keys are never part of a diagnostic.
type activityPubSignatureDiagnostics struct {
	Version                  int                               `json:"version"`
	Algorithm                string                            `json:"algorithm"`
	Normalization            string                            `json:"normalization"`
	BuiltinContextsSHA256    string                            `json:"builtin_contexts_sha256"`
	Runtime                  activityPubRuntimeDiagnostics     `json:"runtime"`
	Receipt                  *activityPubInboxReceipt          `json:"receipt,omitempty"`
	ReceiptMatchesWorkerBody *bool                             `json:"receipt_matches_worker_body,omitempty"`
	HTTPActorID              int64                             `json:"http_actor_id,omitempty"`
	HTTPActorURI             string                            `json:"http_actor_uri,omitempty"`
	SignatureActorID         int64                             `json:"signature_actor_id,omitempty"`
	SignatureActorURI        string                            `json:"signature_actor_uri,omitempty"`
	ActorUpdatedAt           string                            `json:"actor_updated_at,omitempty"`
	Creator                  string                            `json:"creator"`
	Created                  string                            `json:"created,omitempty"`
	KeyBits                  int                               `json:"key_bits"`
	PublicKeySHA256          string                            `json:"public_key_sha256,omitempty"`
	PublicKeySPKI            string                            `json:"public_key_spki_base64,omitempty"`
	KeySnapshotOmitted       bool                              `json:"key_snapshot_omitted,omitempty"`
	SignatureBytes           int                               `json:"signature_bytes"`
	SignatureSHA256          string                            `json:"signature_sha256"`
	RecoveredDigestStatus    string                            `json:"recovered_digest_status"`
	RecoveredSHA256          string                            `json:"recovered_sha256,omitempty"`
	Verification             activityPubSignatureDocumentInfo  `json:"verification"`
	Original                 *activityPubSignatureDocumentInfo `json:"original,omitempty"`
}

type activityPubSignatureDocumentInfo struct {
	BodyBytes          int    `json:"body_bytes"`
	BodySHA256         string `json:"body_sha256"`
	OptionsSHA256      string `json:"options_sha256,omitempty"`
	DocumentSHA256     string `json:"document_sha256,omitempty"`
	VerificationSHA256 string `json:"verification_sha256,omitempty"`
	Verified           bool   `json:"verified"`
	Error              string `json:"error,omitempty"`
}

type activityPubSignatureVerificationError struct {
	cause       error
	key         *rsa.PublicKey
	Diagnostics activityPubSignatureDiagnostics
}

func (err *activityPubSignatureVerificationError) Error() string {
	encoded, _ := json.Marshal(err.Diagnostics)
	return err.cause.Error() + "; signature_diagnostics=" + string(encoded)
}

func (err *activityPubSignatureVerificationError) Unwrap() error { return err.cause }

func activityPubDiagnosticSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func activityPubSignatureDocumentDetails(body []byte, verificationString string) activityPubSignatureDocumentInfo {
	info := activityPubSignatureDocumentInfo{BodyBytes: len(body), BodySHA256: activityPubDiagnosticSHA256(body)}
	// The verification string is two hexadecimal SHA256 hashes, not document
	// text. Keep the hashes from the actual verification instead of normalizing
	// that document a second time just to report a failure.
	if len(verificationString) == 2*sha256.Size*2 {
		info.OptionsSHA256 = verificationString[:sha256.Size*2]
		info.DocumentSHA256 = verificationString[sha256.Size*2:]
		info.VerificationSHA256 = activityPubDiagnosticSHA256([]byte(verificationString))
	}
	return info
}

func newActivityPubSignatureVerificationError(cause error, body []byte, document map[string]any, key *rsa.PublicKey, signature []byte, verificationString string) error {
	diagnostic := activityPubSignatureDiagnostics{
		Version: 1, Algorithm: "RsaSignature2017", Normalization: "URDNA2015",
		BuiltinContextsSHA256: activityPubDiagnosticContextsSHA256(), Runtime: activityPubRuntimeInfo(nil),
		KeyBits: key.N.BitLen(), SignatureBytes: len(signature), SignatureSHA256: activityPubDiagnosticSHA256(signature),
		Verification: activityPubSignatureDocumentDetails(body, verificationString),
	}
	if options, ok := activityPubLinkedDataSignatureMap(document); ok {
		diagnostic.Creator = activityPubSafeLogValue(activityJSONLDString(options, "creator"), 512)
		diagnostic.Created = activityPubSafeLogValue(activityJSONLDString(options, "created"), 128)
	}
	// Cap optional diagnostic work and the public-key snapshot independently of
	// the verifier. Unusual keys remain rejected by the original verification.
	if key.N.BitLen() <= 8192 && key.E > 1 && key.E <= 1<<31-1 {
		if der, err := x509.MarshalPKIXPublicKey(key); err == nil && len(der) <= 2048 {
			diagnostic.PublicKeySHA256 = activityPubDiagnosticSHA256(der)
			diagnostic.PublicKeySPKI = base64.StdEncoding.EncodeToString(der)
		}
	}
	diagnostic.KeySnapshotOmitted = diagnostic.PublicKeySPKI == ""
	diagnostic.RecoveredSHA256, diagnostic.RecoveredDigestStatus = activityPubRecoverSignatureSHA256(key, signature)
	return &activityPubSignatureVerificationError{cause: cause, key: key, Diagnostics: diagnostic}
}

// This is diagnostic evidence, never an authentication decision. A digest is
// reported only for an exact EMSA-PKCS1-v1_5 SHA256 block under the key used by
// the failing verifier; padding errors must not be presented as a signer hash.
func activityPubRecoverSignatureSHA256(key *rsa.PublicKey, signature []byte) (string, string) {
	if key == nil || key.N == nil || key.N.Sign() <= 0 || key.N.BitLen() > 8192 || key.E <= 1 || key.E > 1<<31-1 {
		return "", "unsupported_key"
	}
	if len(signature) != key.Size() {
		return "", "invalid_signature_length"
	}
	value := new(big.Int).SetBytes(signature)
	if value.Cmp(key.N) >= 0 {
		return "", "signature_out_of_range"
	}
	encoded := make([]byte, key.Size())
	new(big.Int).Exp(value, big.NewInt(int64(key.E)), key.N).FillBytes(encoded)
	if len(encoded) < 11 || encoded[0] != 0 || encoded[1] != 1 {
		return "", "invalid_padding"
	}
	i := 2
	for i < len(encoded) && encoded[i] == 0xff {
		i++
	}
	if i < 10 || i >= len(encoded) || encoded[i] != 0 {
		return "", "invalid_padding"
	}
	prefix := []byte{0x30, 0x31, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01, 0x05, 0x00, 0x04, 0x20}
	info := encoded[i+1:]
	if len(info) != len(prefix)+sha256.Size || !bytes.Equal(info[:len(prefix)], prefix) {
		return "", "not_sha256_digest_info"
	}
	return hex.EncodeToString(info[len(prefix):]), "sha256_digest_info"
}

func enrichActivityPubSignatureDiagnostics(s *Server, err error, originalBody []byte, httpActor *models.Account) {
	var diagnostic *activityPubSignatureVerificationError
	if !errors.As(err, &diagnostic) {
		return
	}
	diagnostic.Diagnostics.Runtime = activityPubRuntimeInfo(s)
	if httpActor != nil {
		diagnostic.Diagnostics.HTTPActorID = httpActor.ID
		diagnostic.Diagnostics.HTTPActorURI = activityPubSafeLogValue(httpActor.URI, 512)
	}
	info := activityPubSignatureDocumentDetails(originalBody, "")
	diagnostic.Diagnostics.Original = &info
	// A second analysis must not make more HTTP requests or replace the original
	// error if a remote context has expired from the cache.
	var document map[string]any
	if err := json.Unmarshal(originalBody, &document); err != nil {
		info.Error = activityPubSafeLogValue(err.Error(), 256)
		return
	}
	loader := newActivityPubJSONLDDocumentLoader(true)
	hash := func(value any) (string, error) {
		normalized, err := activityPubJSONLDNormalizeWithLoader(value, loader)
		if err != nil {
			return "", err
		}
		return activityPubDiagnosticSHA256([]byte(normalized)), nil
	}
	signatureValue, verificationString, err := activityPubLinkedDataSignatureVerificationStringWithHash(document, hash)
	if err != nil {
		info.Error = activityPubSafeLogValue(err.Error(), 256)
		return
	}
	info = activityPubSignatureDocumentDetails(originalBody, verificationString)
	signature, err := decodeActivityPubLinkedDataSignatureValue(signatureValue)
	if err != nil {
		info.Error = activityPubSafeLogValue(err.Error(), 256)
		return
	}
	digest := sha256.Sum256([]byte(verificationString))
	info.Verified = rsa.VerifyPKCS1v15(diagnostic.key, crypto.SHA256, digest[:], signature) == nil
}

func attachActivityPubSignatureReceipt(err error, receipt *activityPubInboxReceipt) {
	var diagnostic *activityPubSignatureVerificationError
	if receipt == nil || !errors.As(err, &diagnostic) {
		return
	}
	snapshot := *receipt
	snapshot.BodySHA256 = activityPubSafeLogValue(snapshot.BodySHA256, sha256.Size*2)
	snapshot.QueuedBodySHA256 = activityPubSafeLogValue(snapshot.QueuedBodySHA256, sha256.Size*2)
	snapshot.Runtime = snapshot.Runtime.sanitized()
	diagnostic.Diagnostics.Receipt = &snapshot
	if original := diagnostic.Diagnostics.Original; original != nil {
		matches := receipt.QueuedBodySHA256 == original.BodySHA256
		diagnostic.Diagnostics.ReceiptMatchesWorkerBody = &matches
	}
}

var activityPubDiagnosticContextsSHA256 = sync.OnceValue(func() string {
	contexts := []any{
		activityPubActivityStreamsJSONLDContext(), activityPubSecurityJSONLDContext(), activityPubIdentityJSONLDContext(),
		activityPubFEP044FJSONLDContext(), activityPubTootJSONLDContext(), activityPubGTSJSONLDContext(),
		activityPubMisskeyJSONLDContext(), activityPubOStatusJSONLDContext(), activityPubSchemaJSONLDContext(),
		activityPubFullJSONLDContextExtensions(),
	}
	encoded, _ := json.Marshal(contexts)
	return activityPubDiagnosticSHA256(encoded)
})
