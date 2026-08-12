package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const mastodon44HTTPMessageSignatureFixturePublicKey = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAqIAYvNFGbZ5g4iiK6feS
dXD4bDStFM58A7tHycYXaYtzZQpIeHXAmaXuZzXIwtrP4N0gIk8JNwZvXj2UPS+S
07t0V9wNK94he01LV5EMz/GN4eNnFmDL64HIEuKLvV8TvgjbUPRD6Y5X0UpKi2ZI
FLSb96Q5w0Z/k7ntpVKV52y8kz5Fjr/O/0JuHryZe0yItzJh8kzFfeMf0EXzfSna
KvT7P9jhgC6uTre+jXyvVZjiHDrnqvvucdI3I7DRfXo1OqARBrLjy+TdseUAjNYJ
+OuPRI1URIWQI01DCHqcohVu9+Ar+BiCjFp3ua+XMuJvrvbD61d1Fvig/9nbBRR+
8QIDAQAB
-----END PUBLIC KEY-----`

func TestMastodon44HTTPMessageSignatureVerifiesUpstreamFixture(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://www.example.com/activitypub/success", nil)
	input, err := parseActivityHTTPMessageSignatureInput(`sig1=("@method" "@target-uri");created=1703066400;keyid="https://remote.domain/users/bob#main-key"`)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := parseActivityHTTPMessageSignature(`sig1=:WfM6q/qBqhUyqPUDt9metjadJGtLLpmMTBzk/t+R3byKe4/TGAXC6vBB/M6NsD5qv8GCmQGtisCMQxJQO0IGODGzi+Jv+eqDJ50agMVXNV6nUOzY44c4/XTPoI98qyx1oEMa4Hefy3vSYKq96iDVAc+RDLCMTeGP3wn9wizjD1SNmU0RZI1bTB+eCkywMP9mM5zXzUOYF+Qkuf+WdEpPR1XUGPlnqfdvPalcKVfaI/VThBjI91D/lmUGoa69x4EBEHM+aJmW6086e7/dVh+FndKkdGfXslZXFZKi2flTGQZgEWLn948SqAaJQROkJg8B14Sb1NONS1qZBhK3Mum8Pg==:`, input.Label)
	if err != nil {
		t.Fatal(err)
	}
	base, err := buildActivityHTTPMessageSignatureBase(request, input)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := activityPublicKey(mastodon44HTTPMessageSignatureFixturePublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyActivityHTTPMessageSignature(publicKey, signature, base); err != nil {
		t.Fatalf("Mastodon v4.4.22 RFC 9421 fixture failed verification: %v\nbase=%s", err, base)
	}
}

func TestMastodon44HTTPMessageSignatureGETFixture(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_703_066_400, 0).UTC()
	request := httptest.NewRequest(http.MethodGet, "https://www.example.com/activitypub/success?foo=42", nil)
	signatureInput := `sig1=("@method" "@target-uri");created=1703066400;keyid="https://remote.domain/users/bob#main-key"`
	input, err := parseActivityHTTPMessageSignatureInput(signatureInput)
	if err != nil {
		t.Fatal(err)
	}
	if input.Label != "sig1" || input.KeyID != "https://remote.domain/users/bob#main-key" || len(input.Components) != 2 {
		t.Fatalf("input = %#v", input)
	}
	if err := validateActivityHTTPMessageSignatureTime(input, now); err != nil {
		t.Fatal(err)
	}
	if err := validateActivityHTTPMessageSignatureStrength(request, input); err != nil {
		t.Fatal(err)
	}
	base, err := buildActivityHTTPMessageSignatureBase(request, input)
	if err != nil {
		t.Fatal(err)
	}
	wantBase := strings.Join([]string{
		`"@method": GET`,
		`"@target-uri": https://www.example.com/activitypub/success?foo=42`,
		`"@signature-params": ("@method" "@target-uri");created=1703066400;keyid="https://remote.domain/users/bob#main-key"`,
	}, "\n")
	if base != wantBase {
		t.Fatalf("signature base = %q, want %q", base, wantBase)
	}
	signature := signActivityHTTPMessageSignatureForTest(t, privateKey, base)
	request.Header.Set("Signature-Input", signatureInput)
	request.Header.Set("Signature", "sig1=:"+base64.StdEncoding.EncodeToString(signature)+":")
	parsedSignature, err := parseActivityHTTPMessageSignature(request.Header.Get("Signature"), input.Label)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyActivityHTTPMessageSignature(&privateKey.PublicKey, parsedSignature, base); err != nil {
		t.Fatalf("valid signature failed: %v", err)
	}

	tampered := request.Clone(request.Context())
	tampered.URL.RawQuery = "foo=43"
	tamperedBase, err := buildActivityHTTPMessageSignatureBase(tampered, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyActivityHTTPMessageSignature(&privateKey.PublicKey, parsedSignature, tamperedBase); err == nil {
		t.Fatal("signature with a tampered query string verified")
	}
}

func TestMastodon44HTTPMessageSignaturePOSTContentDigest(t *testing.T) {
	body := []byte("Hello world")
	digest := sha256.Sum256(body)
	request := httptest.NewRequest(http.MethodPost, "https://www.example.com/inbox", strings.NewReader(string(body)))
	request.Header.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
	input, err := parseActivityHTTPMessageSignatureInput(`sig1=("@method" "@target-uri" "content-digest");created=1703066400;keyid="https://remote.domain/users/bob#main-key"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActivityHTTPMessageSignatureStrength(request, input); err != nil {
		t.Fatal(err)
	}
	if err := validateActivityContentDigest(request, input, body); err != nil {
		t.Fatal(err)
	}
	if err := validateActivityContentDigest(request, input, []byte("Hello world!")); err == nil {
		t.Fatal("tampered body digest was accepted")
	}

	request.Header.Set("Content-Digest", "SHA-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
	err = validateActivityContentDigest(request, input, body)
	if !errors.Is(err, errActivitySignatureMalformed) || activityPubSignatureErrorStatus(err) != http.StatusBadRequest {
		t.Fatalf("malformed Content-Digest error = %v, status = %d", err, activityPubSignatureErrorStatus(err))
	}

	withoutDigest, err := parseActivityHTTPMessageSignatureInput(`sig1=("@method" "@target-uri");created=1703066400;keyid="https://remote.domain/users/bob#main-key"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActivityHTTPMessageSignatureStrength(request, withoutDigest); err == nil || !strings.Contains(err.Error(), "Content-Digest") {
		t.Fatalf("unsigned POST Content-Digest error = %v", err)
	}
}

func TestMastodon44HTTPMessageSignatureTimeAndNonceContract(t *testing.T) {
	now := time.Unix(1_703_066_400, 0).UTC()
	input, err := parseActivityHTTPMessageSignatureInput(`sig1=("@method" "@target-uri");created=1703066400;expires=1703066700;keyid="https://remote.domain/users/bob#main-key";alg="rsa-v1_5-sha256"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActivityHTTPMessageSignatureTime(input, now); err != nil {
		t.Fatalf("nonce-free signature was rejected: %v", err)
	}
	if strings.Contains(input.Parameters, "nonce") {
		t.Fatalf("unexpected nonce in parameters: %s", input.Parameters)
	}
	if err := validateActivityHTTPMessageSignatureTime(input, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired signature was accepted")
	}
}

func TestMastodon44HTTPMessageSignatureParsersAreRequestLocalAndRejectDuplicates(t *testing.T) {
	headers := []string{
		`sig1=("@method" "@target-uri");created=1703066400;keyid="https://one.example/key"`,
		`sig2=("@method" "@target-uri" "content-type");created=1703066401;keyid="https://two.example/key"`,
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 64)
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				header := headers[(index+iteration)%len(headers)]
				input, err := parseActivityHTTPMessageSignatureInput(header)
				if err != nil {
					errorsFound <- err
					return
				}
				wantLabel := "sig1"
				wantKey := "https://one.example/key"
				if header == headers[1] {
					wantLabel, wantKey = "sig2", "https://two.example/key"
				}
				if input.Label != wantLabel || input.KeyID != wantKey {
					errorsFound <- fmt.Errorf("cross-request parser state: %#v", input)
					return
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}

	for _, raw := range []string{
		`sig1=("@method" "@method" "@target-uri");created=1703066400;keyid="https://one.example/key"`,
		`sig1=("@method" "@target-uri");created=1703066400;created=1703066401;keyid="https://one.example/key"`,
		`sig1=("@method" "@target-uri");created=1703066400;keyid="https://one.example/key", sig2=("@method" "@target-uri");created=1703066400;keyid="https://two.example/key"`,
	} {
		if _, err := parseActivityHTTPMessageSignatureInput(raw); err == nil {
			t.Fatalf("ambiguous Signature-Input was accepted: %s", raw)
		}
	}
}

func TestMastodon44HTTPMessageSignatureFeatureAndTemporaryFailureStatuses(t *testing.T) {
	if (&Server{cfg: config.Config{}}).activityHTTPMessageSignaturesEnabled() {
		t.Fatal("HTTP message signatures unexpectedly enabled")
	}
	if !(&Server{cfg: config.Config{ExperimentalFeatures: []string{"HTTP_MESSAGE_SIGNATURES"}}}).activityHTTPMessageSignaturesEnabled() {
		t.Fatal("HTTP message signatures feature was not enabled")
	}
	temporary := activityPubSignatureActorResolutionError(activityFetchHTTPError{StatusCode: http.StatusServiceUnavailable, URL: "https://remote.example/key"})
	if activityPubSignatureErrorStatus(temporary) != http.StatusServiceUnavailable {
		t.Fatalf("temporary fetch status = %d, error = %v", activityPubSignatureErrorStatus(temporary), temporary)
	}
	permanent := activityPubSignatureActorResolutionError(activityFetchHTTPError{StatusCode: http.StatusNotFound, URL: "https://remote.example/key"})
	if activityPubSignatureErrorStatus(permanent) != http.StatusUnauthorized {
		t.Fatalf("permanent fetch status = %d, error = %v", activityPubSignatureErrorStatus(permanent), permanent)
	}
}

func TestMastodon44RedirectedLegacySignatureDoesNotCoverAccept(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateDER := x509.MarshalPKCS1PrivateKey(privateKey)
	account := models.Account{
		ID:         1,
		Username:   "alice",
		PrivateKey: sql.NullString{String: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privateDER})), Valid: true},
	}
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "local.example", LocalDomain: "local.example"}}
	request := httptest.NewRequest(http.MethodGet, "https://remote.example/users/bob", nil)
	request.Header.Set("Accept", "application/activity+json")
	if err := server.signActivityPubFetchRequestWithAccept(request, account, false); err != nil {
		t.Fatal(err)
	}
	params, err := parseActivitySignature(request.Header.Get("Signature"))
	if err != nil {
		t.Fatal(err)
	}
	if stringSliceContains(activitySignedHeaders(params, activitySignatureAlgorithm(params)), "accept") {
		t.Fatalf("redirect signature covers Accept: %s", request.Header.Get("Signature"))
	}
}

func signActivityHTTPMessageSignatureForTest(t *testing.T, key *rsa.PrivateKey, base string) []byte {
	t.Helper()
	digest := sha256.Sum256([]byte(base))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signature
}
