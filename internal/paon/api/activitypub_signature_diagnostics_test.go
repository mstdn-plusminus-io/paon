package api

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
)

func TestActivityPubSignatureDiagnosticsPreserveVerificationEvidence(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"@context": []string{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext},
		"id":       "https://origin.example/activities/1",
		"type":     "Create",
		"actor":    "https://origin.example/users/alice",
		"object": map[string]any{
			"id": "https://origin.example/notes/1", "type": "Note", "content": "original confidential text",
		},
		"signature": map[string]any{
			"type": "RsaSignature2017", "creator": "https://origin.example/users/alice#main-key", "created": "2026-09-20T00:00:00Z",
		},
	}
	_, originalVerification, err := activityPubLinkedDataSignatureVerificationStringWithPlaceholder(document)
	if err != nil {
		t.Fatal(err)
	}
	originalDigest := sha256.Sum256([]byte(originalVerification))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, originalDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	document["signature"].(map[string]any)["signatureValue"] = base64.StdEncoding.EncodeToString(signature)
	originalBody, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(originalBody, &key.PublicKey); err != nil {
		t.Fatalf("valid document: %v", err)
	}
	document["object"].(map[string]any)["content"] = "tampered confidential text"
	tamperedBody, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	verificationErr := verifyActivityPubLinkedDataSignatureWithError(tamperedBody, &key.PublicKey)
	var diagnostic *activityPubSignatureVerificationError
	if !errors.As(verificationErr, &diagnostic) || !errors.Is(verificationErr, rsa.ErrVerification) {
		t.Fatalf("tampered document error does not preserve typed diagnostics and RSA cause: %v", verificationErr)
	}
	enrichActivityPubSignatureDiagnostics(nil, verificationErr, originalBody, nil)
	details := diagnostic.Diagnostics
	if details.RecoveredDigestStatus != "sha256_digest_info" || details.RecoveredSHA256 != hex.EncodeToString(originalDigest[:]) {
		t.Fatalf("recovered digest = %q (%s), want original signed hash", details.RecoveredSHA256, details.RecoveredDigestStatus)
	}
	if details.Verification.Verified || details.Verification.VerificationSHA256 == details.RecoveredSHA256 {
		t.Fatalf("tampered document should fail with different verifier hash: %#v", details.Verification)
	}
	if details.Original == nil || !details.Original.Verified || details.Original.VerificationSHA256 != details.RecoveredSHA256 {
		t.Fatalf("original document evidence = %#v", details.Original)
	}
	if details.Original.OptionsSHA256 != details.Verification.OptionsSHA256 || details.Original.DocumentSHA256 == details.Verification.DocumentSHA256 {
		t.Fatalf("content change did not isolate document hash: original=%#v verification=%#v", details.Original, details.Verification)
	}
	if details.Verification.BodyBytes != len(tamperedBody) || details.Verification.BodySHA256 != activityPubDiagnosticSHA256(tamperedBody) {
		t.Fatalf("verification body evidence = %#v", details.Verification)
	}
	der, err := base64.StdEncoding.DecodeString(details.PublicKeySPKI)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	snapshotKey, ok := snapshot.(*rsa.PublicKey)
	if !ok || !snapshotKey.Equal(&key.PublicKey) || details.PublicKeySHA256 != activityPubDiagnosticSHA256(der) || details.KeySnapshotOmitted {
		t.Fatal("diagnostic public key cannot reproduce the key used for verification")
	}
	if details.SignatureBytes != len(signature) || details.SignatureSHA256 != activityPubDiagnosticSHA256(signature) {
		t.Fatal("diagnostic signature hash differs from decoded signature bytes")
	}
	_, encoded, ok := strings.Cut(verificationErr.Error(), "; signature_diagnostics=")
	if !ok {
		t.Fatal("returned error does not include diagnostic JSON")
	}
	var fromTask activityPubSignatureDiagnostics
	if err := json.Unmarshal([]byte(encoded), &fromTask); err != nil {
		t.Fatalf("task diagnostic JSON cannot be decoded: %v", err)
	}
	if fromTask.PublicKeySPKI != details.PublicKeySPKI || fromTask.RecoveredSHA256 != details.RecoveredSHA256 || fromTask.Creator != "https://origin.example/users/alice#main-key" {
		t.Fatal("returned error lost key, digest, or creator evidence")
	}
	for _, secret := range []string{"original confidential text", "tampered confidential text", "PRIVATE KEY", base64.StdEncoding.EncodeToString(signature)} {
		if strings.Contains(verificationErr.Error(), secret) {
			t.Fatalf("diagnostic error includes content or unnecessary signature value: %q", secret)
		}
	}
}

func TestActivityPubRecoverSignatureSHA256RejectsMalformedEncodedMessages(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("known signed digest"))
	prefix, err := hex.DecodeString("3031300d060960864801650304020105000420")
	if err != nil {
		t.Fatal(err)
	}
	encoded := make([]byte, key.Size())
	encoded[1] = 1
	separator := len(encoded) - len(prefix) - sha256.Size - 1
	for i := 2; i < separator; i++ {
		encoded[i] = 0xff
	}
	copy(encoded[separator+1:], prefix)
	copy(encoded[len(encoded)-sha256.Size:], digest[:])
	tests := []struct {
		name string
		edit func([]byte)
		want string
	}{
		{name: "valid SHA256 DigestInfo", want: "sha256_digest_info"},
		{name: "incorrect block type", edit: func(block []byte) { block[1] = 2 }, want: "invalid_padding"},
		{name: "seven padding bytes", edit: func(block []byte) { block[9] = 0 }, want: "invalid_padding"},
		{name: "non FF padding byte", edit: func(block []byte) { block[12] = 0x7f }, want: "invalid_padding"},
		{name: "missing separator", edit: func(block []byte) {
			for i := 2; i < len(block); i++ {
				block[i] = 0xff
			}
		}, want: "invalid_padding"},
		{name: "wrong digest algorithm", edit: func(block []byte) { block[separator+15] = 2 }, want: "not_sha256_digest_info"},
		{name: "extra DigestInfo byte", edit: func(block []byte) { block[separator-1] = 0 }, want: "not_sha256_digest_info"},
		{name: "short DigestInfo", edit: func(block []byte) {
			for i := 2; i < len(block)-5; i++ {
				block[i] = 0xff
			}
			block[len(block)-5] = 0
		}, want: "not_sha256_digest_info"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := bytes.Clone(encoded)
			if tt.edit != nil {
				tt.edit(block)
			}
			// Sign the exact test EMSA block so the test reaches padding checks
			// after RSA recovery, including blocks rsa.SignPKCS1v15 cannot emit.
			signature := new(big.Int).Exp(new(big.Int).SetBytes(block), key.D, key.N).FillBytes(make([]byte, key.Size()))
			got, status := activityPubRecoverSignatureSHA256(&key.PublicKey, signature)
			if status != tt.want {
				t.Fatalf("status = %q, want %q", status, tt.want)
			}
			if tt.want == "sha256_digest_info" {
				if got != hex.EncodeToString(digest[:]) {
					t.Fatalf("digest = %q, want exact signed SHA256", got)
				}
			} else if got != "" {
				t.Fatalf("invalid block reported signer digest %q", got)
			}
		})
	}
}

func TestActivityPubRecoverSignatureSHA256InputGuards(t *testing.T) {
	modulus := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 1024), big.NewInt(1))
	key := &rsa.PublicKey{N: modulus, E: 65537}
	tests := []struct {
		name      string
		key       *rsa.PublicKey
		signature []byte
		want      string
	}{
		{name: "nil key", want: "unsupported_key"},
		{name: "nil modulus", key: &rsa.PublicKey{E: 65537}, want: "unsupported_key"},
		{name: "zero modulus", key: &rsa.PublicKey{N: new(big.Int), E: 65537}, want: "unsupported_key"},
		{name: "negative modulus", key: &rsa.PublicKey{N: big.NewInt(-1), E: 65537}, want: "unsupported_key"},
		{name: "oversized modulus", key: &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), 8192), E: 65537}, want: "unsupported_key"},
		{name: "identity exponent", key: &rsa.PublicKey{N: modulus, E: 1}, want: "unsupported_key"},
		{name: "oversized exponent", key: &rsa.PublicKey{N: modulus, E: 1 << 31}, want: "unsupported_key"},
		{name: "short signature", key: key, signature: make([]byte, key.Size()-1), want: "invalid_signature_length"},
		{name: "long signature", key: key, signature: make([]byte, key.Size()+1), want: "invalid_signature_length"},
		{name: "signature equals modulus", key: key, signature: modulus.Bytes(), want: "signature_out_of_range"},
		{name: "zero signature", key: key, signature: make([]byte, key.Size()), want: "invalid_padding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if digest, status := activityPubRecoverSignatureSHA256(tt.key, tt.signature); digest != "" || status != tt.want {
				t.Fatalf("recovery = %q, %q; want no digest, %q", digest, status, tt.want)
			}
		})
	}
	oversizedKey := &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), 8192), E: 65537}
	err := newActivityPubSignatureVerificationError(rsa.ErrVerification, []byte(`{}`), nil, oversizedKey, nil, "")
	var diagnostic *activityPubSignatureVerificationError
	if !errors.As(err, &diagnostic) || !diagnostic.Diagnostics.KeySnapshotOmitted || diagnostic.Diagnostics.PublicKeySPKI != "" || diagnostic.Diagnostics.RecoveredDigestStatus != "unsupported_key" {
		t.Fatalf("oversized key diagnostic was not safely omitted: %v", err)
	}
}

func TestActivityPubSignatureDiagnosticLoaderUsesOnlyBuiltinsAndCache(t *testing.T) {
	previousClient := activityHTTPClient
	requests := 0
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("diagnostic attempted HTTP to %s", request.URL)
	})}
	t.Cleanup(func() { activityHTTPClient = previousClient })
	const cachedURI = "https://signature-diagnostics.example/cached-context"
	const missingURI = "https://signature-diagnostics.example/missing-context"
	activityPubJSONLDContextCache.Lock()
	previousCached, hadCached := activityPubJSONLDContextCache.entries[cachedURI]
	previousMissing, hadMissing := activityPubJSONLDContextCache.entries[missingURI]
	delete(activityPubJSONLDContextCache.entries, cachedURI)
	delete(activityPubJSONLDContextCache.entries, missingURI)
	activityPubJSONLDContextCache.Unlock()
	t.Cleanup(func() {
		activityPubJSONLDContextCache.Lock()
		defer activityPubJSONLDContextCache.Unlock()
		for uri, previous := range map[string]activityPubJSONLDContextCacheEntry{cachedURI: previousCached, missingURI: previousMissing} {
			if (uri == cachedURI && hadCached) || (uri == missingURI && hadMissing) {
				activityPubJSONLDContextCache.entries[uri] = previous
			} else {
				delete(activityPubJSONLDContextCache.entries, uri)
			}
		}
	})
	activityPubJSONLDContextCacheStore(cachedURI, cachedURI, []byte(`{"@context":{"example":"https://signature-diagnostics.example/vocab#"}}`))
	loader := newActivityPubJSONLDDocumentLoader(true)
	for _, uri := range []string{activityPubIdentityContext, activityPubSecurityContext, "https://www.w3.org/ns/activitystreams", cachedURI} {
		if document, err := loader.LoadDocument(uri); err != nil || document == nil {
			t.Fatalf("offline context %q = %v, %v", uri, document, err)
		}
	}
	if _, err := loader.LoadDocument(missingURI); err == nil || !strings.Contains(err.Error(), "not cached") {
		t.Fatalf("uncached diagnostic context error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("offline diagnostic loader made %d HTTP requests", requests)
	}
}
