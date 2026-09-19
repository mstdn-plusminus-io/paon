package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/piprate/json-gold/ld"
)

// The fixture is the W3C context from https://www.w3.org/ns/activitystreams,
// fetched with Accept: application/ld+json on 2026-09-19.
func activityPubW3CContextFixture(t *testing.T) map[string]any {
	t.Helper()
	body, err := os.ReadFile("testdata/activitystreams-context.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestActivityPubActivityStreamsContextMatchesW3C(t *testing.T) {
	want := activityPubW3CContextFixture(t)["@context"]
	if got := activityPubActivityStreamsJSONLDContext(); !reflect.DeepEqual(got, want) {
		t.Fatal("embedded ActivityStreams context differs from the W3C context")
	}
}

func TestActivityPubEncryptedMessageContextRetainsDigest(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "origin.example", WebDomain: "origin.example"}}
	payload := activityPubEncryptedMessagePayload(server,
		models.Account{Username: "alice"}, models.Device{DeviceID: "alice-device"},
		models.Account{Username: "bob"}, cryptoDevicePayload{DeviceID: "bob-device", Type: 1, Body: "ciphertext", HMAC: "message-hmac"},
		"franking", time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC))
	normalized, err := activityPubJSONLDNormalize(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{
		"<https://www.w3.org/ns/activitystreams#digest> _:",
		"<http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <https://www.w3.org/ns/activitystreams#Digest>",
		`<https://w3id.org/security#digestValue> "message-hmac"`,
		`<https://w3id.org/security#digestAlgorithm> "http://www.w3.org/2000/09/xmldsig#hmac-sha256"`,
	} {
		if !strings.Contains(normalized, term) {
			t.Fatalf("encrypted message normalization lost %q:\n%s", term, normalized)
		}
	}
	compacted, err := activityPubJSONLDCompactToActivityStreams(payload)
	if err != nil {
		t.Fatal(err)
	}
	compactedNormalized, err := activityPubJSONLDNormalize(compacted)
	if err != nil {
		t.Fatal(err)
	}
	if compactedNormalized != normalized {
		t.Fatal("compaction changed encrypted message digest semantics")
	}
}

func TestActivityPubLinkedDataSignatureWithW3CActorContext(t *testing.T) {
	privateKeyPEM, publicKeyPEM, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := activityPrivateKey(privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := activityPublicKey(publicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		name := "undefined extension"
		if explicit {
			name = "explicit extension"
		}
		t.Run(name, func(t *testing.T) {
			context := []any{activityPubActivityStreamsContext(), activityPubSecurityContext}
			if explicit {
				context = append(context, map[string]any{
					"additionalPublicKeys": map[string]any{"@id": "https://example.org/ns#additionalPublicKeys", "@type": "@id"},
				})
			}
			actorURI := "https://origin.example/users/alice"
			actor := map[string]any{
				"id":                   actorURI,
				"type":                 "Person",
				"preferredUsername":    "alice",
				"additionalPublicKeys": actorURI + "#ed25519-key",
				"endpoints": map[string]any{
					"oauthAuthorizationEndpoint": "https://origin.example/oauth/authorize",
					"oauthTokenEndpoint":         "https://origin.example/oauth/token",
					"proxyUrl":                   "https://origin.example/proxy",
					"provideClientKey":           "https://origin.example/keys/provide",
					"signClientKey":              "https://origin.example/keys/sign",
					"uploadMedia":                "https://origin.example/media",
				},
				"streams": []any{"https://origin.example/streams/1"},
			}
			document := map[string]any{
				"@context": context,
				"id":       "https://origin.example/updates/1",
				"type":     "Update",
				"actor":    actorURI,
				"object":   actor,
			}
			signActivityPubDocumentWithW3CContext(t, document, privateKey)
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(body, publicKey); err != nil {
				t.Fatalf("original W3C-context signature: %v", err)
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(activityPubCompactCollectionBody(body), publicKey); err != nil {
				t.Fatalf("compacted W3C-context signature: %v", err)
			}
			actor["additionalPublicKeys"] = actorURI + "#tampered-key"
			tampered, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if got := verifyActivityPubLinkedDataSignature(tampered, publicKey); got == explicit {
				t.Fatalf("signature after changing extension = %t, explicit mapping = %t", got, explicit)
			}
			actor["preferredUsername"] = "tampered"
			tampered, err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if verifyActivityPubLinkedDataSignature(tampered, publicKey) {
				t.Fatal("signature accepted a changed standard actor property")
			}
		})
	}
}

func signActivityPubDocumentWithW3CContext(t *testing.T, document map[string]any, privateKey *rsa.PrivateKey) {
	t.Helper()
	loader := activityPubJSONLDDocumentLoader().(*ld.CachingDocumentLoader)
	loader.AddDocument(activityPubActivityStreamsContext(), activityPubW3CContextFixture(t))
	normalizeHash := func(value any) string {
		options := ld.NewJsonLdOptions("")
		options.Algorithm = ld.AlgorithmURDNA2015
		options.Format = "application/n-quads"
		options.DocumentLoader = loader
		normalized, err := ld.NewJsonLdProcessor().Normalize(value, options)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(normalized.(string)))
		return hex.EncodeToString(digest[:])
	}
	options := map[string]any{
		"@context": activityPubIdentityContext,
		"creator":  "https://origin.example/users/alice#main-key",
		"created":  "2026-09-19T00:00:00Z",
	}
	toSign := normalizeHash(options) + normalizeHash(document)
	digest := sha256.Sum256([]byte(toSign))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	document["signature"] = map[string]any{
		"type":           "RsaSignature2017",
		"creator":        options["creator"],
		"created":        options["created"],
		"signatureValue": base64.StdEncoding.EncodeToString(signature),
	}
}
