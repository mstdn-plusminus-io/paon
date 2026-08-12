package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMastodon43LocalInputLengthBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		got  bool
		want bool
	}{
		{name: "local emoji 128", got: validLocalCustomEmojiShortcode(strings.Repeat("a", 128)), want: true},
		{name: "local emoji 129", got: validLocalCustomEmojiShortcode(strings.Repeat("a", 129)), want: false},
		{name: "list title 256 runes", got: validListTitle(strings.Repeat("界", 256)), want: true},
		{name: "list title 257 runes", got: validListTitle(strings.Repeat("界", 257)), want: false},
		{name: "filter title 256 runes", got: validFilterTitle(strings.Repeat("界", 256)), want: true},
		{name: "filter title 257 runes", got: validFilterTitle(strings.Repeat("界", 257)), want: false},
		{name: "filter keyword 512 runes", got: validFilterKeyword(strings.Repeat("界", 512)), want: true},
		{name: "filter keyword 513 runes", got: validFilterKeyword(strings.Repeat("界", 513)), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("valid = %v, want %v", test.got, test.want)
			}
		})
	}

	if got := activityTagEmojiShortcode(":" + strings.Repeat("a", 2048) + ":"); len(got) != 2048 {
		t.Fatalf("remote shortcode at limit was rejected: len = %d", len(got))
	}
	if got := activityTagEmojiShortcode(":" + strings.Repeat("a", 2049) + ":"); got != "" {
		t.Fatalf("remote shortcode over limit was accepted: len = %d", len(got))
	}
}

func TestMastodon43SignatureRejectsDuplicateParameters(t *testing.T) {
	for _, raw := range []string{
		`keyId="https://remote.example/key",keyId="https://attacker.example/key",signature="abc"`,
		`keyId="https://remote.example/key",signature="abc",signature="def"`,
		`keyId="https://remote.example/key",headers="(request-target)",headers="date",signature="abc"`,
	} {
		if _, err := parseActivitySignature(raw); err == nil {
			t.Fatalf("duplicate signature parameter was accepted: %s", raw)
		}
	}
}

func TestMastodon43InboxRejectsDeclaredAndActualBodiesOverOneMiB(t *testing.T) {
	server := &Server{}
	for _, test := range []struct {
		name    string
		chunked bool
	}{
		{name: "declared content length"},
		{name: "chunked actual body", chunked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/inbox", strings.NewReader(strings.Repeat("x", activityPubInboxMaxJSONBytes+1)))
			if test.chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			ctx := echo.NewContext(req, rec, echo.New())
			if err := server.activityPubInbox(ctx); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", rec.Code)
			}
		})
	}
}

func TestMastodon43SignedJSONLDGraphFeaturesAreRejectedRecursively(t *testing.T) {
	for _, value := range []any{
		map[string]any{"@graph": []any{}},
		map[string]any{"object": map[string]any{"nested": []any{map[string]any{"@included": []any{}}}}},
		[]any{map[string]any{"object": map[string]any{"@reverse": map[string]any{}}}},
	} {
		if !activityPubHasUnsupportedSignedJSONLDFeature(value) {
			t.Fatalf("graph-restructuring feature was not detected: %#v", value)
		}
	}
	if activityPubHasUnsupportedSignedJSONLDFeature(map[string]any{"object": map[string]any{"type": "Note", "content": "ok"}}) {
		t.Fatal("ordinary signed JSON-LD was incorrectly rejected")
	}
}

func TestMastodon43MaliciousLinkedDataGraphFixturesLoseActorAuthority(t *testing.T) {
	signingServer, signer, publicKey := mastodon43LinkedDataTestSigner(t)
	victimURI := activityPubActorID(signingServer, signer)
	relay := &models.Account{
		ID:     2,
		URI:    "https://relay.example/users/relay",
		Domain: sql.NullString{String: "relay.example", Valid: true},
	}
	processingServer := &Server{cfg: signingServer.cfg, db: &gorm.DB{}}

	type fixture struct {
		name       string
		payload    map[string]any
		mutate     func(map[string]any)
		wantType   string
		keyRefresh bool
	}
	deleteNode := func() map[string]any {
		return map[string]any{
			"id":     victimURI + "/activities/delete",
			"type":   "Delete",
			"actor":  victimURI,
			"object": victimURI + "/statuses/deleted",
		}
	}
	undoNode := func(object string) map[string]any {
		return map[string]any{
			"id":    victimURI + "/activities/undo-announce",
			"type":  "Undo",
			"actor": victimURI,
			"object": map[string]any{
				"id":     victimURI + "/activities/announce",
				"type":   "Announce",
				"actor":  victimURI,
				"object": object,
			},
		}
	}
	fixtures := []fixture{
		{
			name: "Delete duplicate graph entry removal",
			payload: map[string]any{
				"@context": "https://www.w3.org/ns/activitystreams",
				"@graph":   []any{deleteNode(), deleteNode()},
			},
			mutate: func(signed map[string]any) {
				graph := signed["@graph"].([]any)
				signed["@graph"] = graph[:1]
			},
			wantType: "Delete",
		},
		{
			name: "Undo Announce named graph reorder",
			payload: map[string]any{
				"@context": "https://www.w3.org/ns/activitystreams",
				"id":       victimURI + "/graphs/undo-announce",
				"@graph": []any{
					undoNode(victimURI + "/statuses/one"),
					undoNode(victimURI + "/statuses/two"),
				},
			},
			mutate: func(signed map[string]any) {
				graph := signed["@graph"].([]any)
				signed["@graph"] = []any{graph[1], graph[0]}
			},
			wantType: "Undo",
		},
		{
			name: "Announce multiple graph nodes",
			payload: map[string]any{
				"@context": "https://www.w3.org/ns/activitystreams",
				"@graph": []any{
					map[string]any{"id": victimURI, "type": "Person"},
					map[string]any{
						"id":     victimURI + "/activities/announce-one",
						"type":   "Announce",
						"actor":  victimURI,
						"object": victimURI + "/statuses/one",
					},
					map[string]any{
						"id":     victimURI + "/activities/announce-two",
						"type":   "Announce",
						"actor":  victimURI,
						"object": victimURI + "/statuses/two",
					},
				},
			},
			wantType: "Announce",
		},
		{
			name: "Update nested included",
			payload: map[string]any{
				"@context": "https://www.w3.org/ns/activitystreams",
				"id":       victimURI + "/activities/update",
				"type":     "Update",
				"actor":    victimURI,
				"object": map[string]any{
					"id":   victimURI,
					"type": "Person",
					"@included": []any{
						map[string]any{"id": victimURI + "/statuses/included", "type": "Note"},
					},
				},
			},
			wantType: "Update",
		},
		{
			name: "actor key refresh nested reverse",
			payload: map[string]any{
				"@context": "https://www.w3.org/ns/activitystreams",
				"id":       victimURI + "/activities/key-update",
				"type":     "Update",
				"actor":    victimURI,
				"object": map[string]any{
					"id":   victimURI,
					"type": "Person",
					"@reverse": map[string]any{
						"https://www.w3.org/ns/activitystreams#attributedTo": map[string]any{"id": victimURI + "/keys/rotated"},
					},
				},
			},
			wantType:   "Update",
			keyRefresh: true,
		},
	}

	for _, test := range fixtures {
		t.Run(test.name, func(t *testing.T) {
			signed, err := signingServer.signActivityPubLinkedDataSignaturePayload(signer, test.payload)
			if err != nil {
				t.Fatalf("sign cryptographically valid malicious fixture: %v", err)
			}
			if test.mutate != nil {
				test.mutate(signed)
			}
			body, err := json.Marshal(signed)
			if err != nil {
				t.Fatal(err)
			}
			if !verifyActivityPubLinkedDataSignature(body, publicKey) {
				t.Fatal("fixture is not a valid Linked Data Signature after its graph mutation")
			}
			if !activityPubHasUnsupportedSignedJSONLDFeature(signed) {
				t.Fatal("malicious graph feature was not detected")
			}

			processedBody := activityPubProcessCollectionBody(body)
			var processed map[string]any
			if err := json.Unmarshal(processedBody, &processed); err != nil {
				t.Fatal(err)
			}
			if _, ok := processed["signature"]; ok {
				t.Fatal("Linked Data Signature authority survived graph fallback")
			}
			expected := make(map[string]any, len(signed)-1)
			for key, value := range signed {
				if key != "signature" {
					expected[key] = value
				}
			}
			if !reflect.DeepEqual(processed, expected) {
				t.Fatalf("fallback changed the original malicious document:\n got: %#v\nwant: %#v", processed, expected)
			}

			payload, err := parseActivityPayload(processedBody)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Type != test.wantType || payload.Actor != victimURI {
				t.Fatalf("fallback activity = type %q actor %q, want %q / %q", payload.Type, payload.Actor, test.wantType, victimURI)
			}
			if payload.Signature.Present {
				t.Fatal("fallback payload still exposes a Linked Data Signature")
			}
			if test.keyRefresh && processingServer.activityPubLinkedDataSignatureActor(processedBody, payload) != nil {
				t.Fatal("graph fallback was allowed to authorize or refresh the embedded creator key")
			}

			err = processingServer.processActivityPubInbox(body, relay)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "actor does not match verified HTTP signature actor") {
				t.Fatalf("malicious LD-signed graph crossed HTTP actor authority: %v", err)
			}
		})
	}
}

func TestMastodon43JSONLDGraphHTTPAuthorityFallbackBoundary(t *testing.T) {
	signingServer, signer, _ := mastodon43LinkedDataTestSigner(t)
	victimURI := activityPubActorID(signingServer, signer)
	payload := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"@graph": []any{
			map[string]any{"id": victimURI, "type": "Person"},
			map[string]any{
				"id":     victimURI + "/activities/announce",
				"type":   "Announce",
				"actor":  victimURI,
				"object": victimURI + "/statuses/one",
			},
		},
	}
	signed, err := signingServer.signActivityPubLinkedDataSignaturePayload(signer, payload)
	if err != nil {
		t.Fatal(err)
	}
	signedBody, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := make(map[string]any, len(signed)-1)
	for key, value := range signed {
		if key != "signature" {
			unsigned[key] = value
		}
	}
	unsignedBody, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}

	processingServer := &Server{cfg: signingServer.cfg, db: &gorm.DB{}}
	httpSigner := &models.Account{
		ID:          10,
		URI:         victimURI,
		Domain:      sql.NullString{String: "victim.example", Valid: true},
		SuspendedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}
	if err := processingServer.processActivityPubInbox(unsignedBody, httpSigner); err != nil {
		t.Fatalf("ordinary unsigned @graph did not retain HTTP Signature compatibility: %v", err)
	}
	if err := processingServer.processActivityPubInbox(signedBody, httpSigner); err != nil {
		t.Fatalf("signed graph did not fall back to its matching HTTP Signature actor: %v", err)
	}

	wrongHTTPSigner := &models.Account{
		ID:          11,
		URI:         "https://relay.example/users/relay",
		Domain:      sql.NullString{String: "relay.example", Valid: true},
		SuspendedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}
	err = processingServer.processActivityPubInbox(signedBody, wrongHTTPSigner)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "actor does not match verified HTTP signature actor") {
		t.Fatalf("graph fallback accepted a non-matching HTTP Signature actor: %v", err)
	}
}

func TestMastodon43MaliciousJSONLDGraphCannotTriggerActorKeyRefresh(t *testing.T) {
	signingServer, signer, _ := mastodon43LinkedDataTestSigner(t)
	victimURI := activityPubActorID(signingServer, signer)
	knownActor := models.Account{
		ID:        21,
		Username:  "alice",
		Domain:    sql.NullString{String: "victim.example", Valid: true},
		URI:       victimURI,
		PublicKey: "",
	}
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: false,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		switch destination := tx.Statement.Dest.(type) {
		case *models.Account:
			*destination = knownActor
		case *models.DomainBlock:
			tx.AddError(gorm.ErrRecordNotFound)
		}
	}); err != nil {
		t.Fatal(err)
	}

	oldClient := activityHTTPClient
	oldProxy := activityHTTPProxyConfigured
	t.Cleanup(func() {
		activityHTTPClient = oldClient
		activityHTTPProxyConfigured = oldProxy
	})
	activityHTTPProxyConfigured = true
	refreshRequests := 0
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshRequests++
		return nil, errors.New("key refresh request observed")
	})}
	processingServer := &Server{
		cfg: config.Config{Scheme: "https", WebDomain: "paon.example", LocalDomain: "paon.example"},
		db:  database,
	}

	ordinary := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       victimURI + "/activities/ordinary-key-update",
		"type":     "Update",
		"actor":    victimURI,
		"object":   map[string]any{"id": victimURI, "type": "Person"},
	}
	ordinaryBody := mastodon43SignedLinkedDataBody(t, signingServer, signer, ordinary)
	ordinaryPayload, err := parseActivityPayload(ordinaryBody)
	if err != nil {
		t.Fatal(err)
	}
	if actor := processingServer.activityPubLinkedDataSignatureActor(ordinaryBody, ordinaryPayload); actor != nil {
		t.Fatalf("failed refresh unexpectedly verified actor %#v", actor)
	}
	if refreshRequests == 0 {
		t.Fatal("test setup did not exercise the ordinary missing-key refresh path")
	}

	refreshRequests = 0
	malicious := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       victimURI + "/activities/malicious-key-update",
		"type":     "Update",
		"actor":    victimURI,
		"object": map[string]any{
			"id":   victimURI,
			"type": "Person",
			"@reverse": map[string]any{
				"https://www.w3.org/ns/activitystreams#attributedTo": map[string]any{"id": victimURI + "/keys/rotated"},
			},
		},
	}
	maliciousBody := mastodon43SignedLinkedDataBody(t, signingServer, signer, malicious)
	maliciousPayload, err := parseActivityPayload(maliciousBody)
	if err != nil {
		t.Fatal(err)
	}
	if actor := processingServer.activityPubLinkedDataSignatureActor(maliciousBody, maliciousPayload); actor != nil {
		t.Fatalf("malicious graph unexpectedly verified actor %#v", actor)
	}
	if refreshRequests != 0 {
		t.Fatalf("malicious graph triggered %d actor key refresh requests", refreshRequests)
	}
}

func mastodon43LinkedDataTestSigner(t *testing.T) (*Server, models.Account, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "victim.example", LocalDomain: "victim.example"}}
	signer := models.Account{
		ID:         1,
		Username:   "alice",
		PrivateKey: sql.NullString{String: privatePEM, Valid: true},
	}
	return server, signer, &key.PublicKey
}

func mastodon43SignedLinkedDataBody(t *testing.T, server *Server, signer models.Account, payload map[string]any) []byte {
	t.Helper()
	signed, err := server.signActivityPubLinkedDataSignaturePayload(signer, payload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestMastodon43RemoteFederationLimits(t *testing.T) {
	options := make([]any, 501)
	for index := range options {
		options[index] = map[string]any{"name": "option"}
	}
	if got := activityPollOptionList(options); len(got) != 500 {
		t.Fatalf("poll options = %d, want 500", len(got))
	}

	fields := make([]any, 51)
	for index := range fields {
		fields[index] = map[string]any{"type": "PropertyValue", "name": "name", "value": "value"}
	}
	if got := activityProfileFields(fields); len(got) != 50 {
		t.Fatalf("profile fields = %d, want 50", len(got))
	}

	if got := activityAttachmentDescription(strings.Repeat("a", 10_001), ""); len([]rune(got)) != 10_000 {
		t.Fatalf("remote media description length = %d, want 10000", len([]rune(got)))
	}

	aliases := make([]any, 257)
	for index := range aliases {
		aliases[index] = "https://remote.example/users/alias"
	}
	if got := activityLimitedValueOrIDList(aliases, 256); len(got) != 256 {
		t.Fatalf("limited actor list = %d, want 256", len(got))
	}
}

func TestMastodon43AttributionDomainSettingsNormalizationAndLimit(t *testing.T) {
	got, err := localAttributionDomains("HTTPS://EXAMPLE.COM\n*.Sub.Example\texample.net")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"example.com", "sub.example", "example.net"}) {
		t.Fatalf("domains = %#v", got)
	}
	if _, err := localAttributionDomains(strings.TrimSpace(strings.Repeat("example.org ", 101))); err == nil {
		t.Fatal("101 attribution domains were accepted")
	}
	if _, err := localAttributionDomains("example.org/path"); err == nil {
		t.Fatal("domain containing a path was accepted")
	}
}

func TestMastodon43EmailValidationBoundaries(t *testing.T) {
	for _, valid := range []string{"alice@example.com", strings.Repeat("a", 308) + "@example.com"} {
		if !railsEmailAddressValid(valid) {
			t.Fatalf("valid email was rejected: len=%d", len([]rune(valid)))
		}
	}
	for _, invalid := range []string{
		"alice%example@example.com",
		"alice,example@example.com",
		`"alice"@example.com`,
		strings.Repeat("a", 309) + "@example.com",
	} {
		if railsEmailAddressValid(invalid) {
			t.Fatalf("invalid email was accepted: len=%d value=%q", len([]rune(invalid)), invalid)
		}
	}
}

func TestMastodon43HighEntropyVaryUsesCommaSeparatedTokens(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/activity", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	ctx := echo.NewContext(req, rec, echo.New())
	rec.Header().Set("Cache-Control", "max-age=180, public")
	rec.Header().Set("Vary", "Accept, Authorization, Accept-Language")

	enforceRailsHighEntropyVaryCacheControl(ctx)
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestMastodon43FederatedURLsRequireUnambiguousHTTPOrHTTPS(t *testing.T) {
	for _, valid := range []string{"http://remote.example/object", "https://remote.example/object"} {
		if !activityPubHTTPURL(valid) {
			t.Fatalf("valid federated URL was rejected: %q", valid)
		}
	}
	for _, invalid := range []string{
		"javascript:alert(1)",
		"data:text/html,hello",
		"//remote.example/object",
		"https:///missing-host",
		"https://remote.example/line\nbreak",
	} {
		if activityPubHTTPURL(invalid) {
			t.Fatalf("unsafe federated URL was accepted: %q", invalid)
		}
	}
}

func TestMastodon43ConfirmationThrottleNormalizesEmailAndBindsAPIUser(t *testing.T) {
	form := url.Values{"user[email]": {" Alice@Example.COM "}}
	webRequest := httptest.NewRequest(http.MethodPost, "/auth/confirmation", strings.NewReader(form.Encode()))
	webRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	web := rackAttackHTMLThrottleCandidates(webRequest, "192.0.2.1", "", 0)
	if !containsThrottleIdentity(web, "throttle_email_confirmations/email", "alice@example.com") {
		t.Fatalf("web confirmation throttle candidates = %#v", web)
	}

	apiRequest := httptest.NewRequest(http.MethodPost, "/api/v1/emails/confirmations", nil)
	api := rackAttackHTMLThrottleCandidates(apiRequest, "192.0.2.1", "user:42", 0)
	if !containsThrottleIdentity(api, "throttle_email_confirmations/email", "user:42") {
		t.Fatalf("API confirmation throttle candidates = %#v", api)
	}
}

func containsThrottleIdentity(candidates []rackAttackThrottleCandidate, name string, identity string) bool {
	for _, candidate := range candidates {
		if candidate.name == name && candidate.identity == identity && candidate.limit == 5 && candidate.period == railsPasswordResetEmailPeriod {
			return true
		}
	}
	return false
}
