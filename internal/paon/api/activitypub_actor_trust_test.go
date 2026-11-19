package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type actorTrustRequests struct {
	mu   sync.Mutex
	urls []string
}

func (r *actorTrustRequests) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

func (r *actorTrustRequests) contains(target string) bool {
	for _, requested := range r.list() {
		if requested == target {
			return true
		}
	}
	return false
}

// actorTrustMockRemote serves the given bodies by exact URL (404 otherwise) and
// records every outgoing ActivityPub and WebFinger request.
func actorTrustMockRemote(t *testing.T, responses map[string]string) *actorTrustRequests {
	t.Helper()
	requests := &actorTrustRequests{}
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.mu.Lock()
		requests.urls = append(requests.urls, r.URL.String())
		requests.mu.Unlock()
		body, ok := responses[r.URL.String()]
		if !ok {
			return textResponse(http.StatusNotFound, "text/plain", "not found"), nil
		}
		contentType := "application/activity+json"
		if strings.Contains(r.URL.Path, "/.well-known/webfinger") {
			contentType = "application/jrd+json"
		}
		return textResponse(http.StatusOK, contentType, body), nil
	})}
	return requests
}

func actorTrustActorObject(id, username, inbox, publicKeyPem string) string {
	return `{
		"id":"` + id + `",
		"type":"Person",
		"preferredUsername":"` + username + `",
		"inbox":"` + inbox + `",
		"publicKey":{"id":"` + id + `#main-key","owner":"` + id + `","publicKeyPem":"` + publicKeyPem + `"}
	}`
}

func actorTrustActor(id, username, inbox, publicKeyPem string) string {
	return activityTestJSON(actorTrustActorObject(id, username, inbox, publicKeyPem))
}

func actorTrustWebFingerURL(acct string) string {
	_, domain, _ := strings.Cut(acct, "@")
	return activityWebFingerURL(domain, "acct:"+acct)
}

func actorTrustWebFinger(subject, actorURL string) string {
	return `{"subject":"acct:` + subject + `","links":[{"rel":"self","type":"application/activity+json","href":"` + actorURL + `"}]}`
}

// actorTrustForbiddenDB is a dry-run database that fails the test on any
// statement. Handing it to a resolver as the transaction proves the resolver
// returned before upsertRemoteActivityActorDB touched the accounts table.
func actorTrustForbiddenDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=paon dbname=paon sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	forbid := func(db *gorm.DB) {
		t.Errorf("unexpected database statement: %s", db.Statement.SQL.String())
		_ = db.AddError(errors.New("database access is forbidden in this test"))
	}
	callbacks := database.Callback()
	for _, register := range []func(string, func(*gorm.DB)) error{
		callbacks.Create().Register,
		callbacks.Query().Register,
		callbacks.Update().Register,
		callbacks.Delete().Register,
		callbacks.Row().Register,
		callbacks.Raw().Register,
	} {
		if err := register("paon_test:forbid", forbid); err != nil {
			t.Fatal(err)
		}
	}
	return database
}

func actorTrustDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=paon dbname=paon sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func actorTrustPublicKeyDocumentFetchers() map[string]func(string) (remoteActivityActor, error) {
	return map[string]func(string) (remoteActivityActor, error){
		"server":  (&Server{}).fetchActivityActorForPublicKeyDocument,
		"package": fetchActivityActorForPublicKeyDocument,
	}
}

func TestFetchActivityActorForPublicKeyDocumentRequiresCanonicalIDLikeRails(t *testing.T) {
	const keyID = "https://evil.example/k"
	const canonicalURI = "https://evil.example/a2"
	const victimURI = "https://victim.example/users/alice"
	cases := []struct {
		name      string
		responses map[string]string
	}{
		{
			name: "refetched document claims another host's actor id",
			responses: map[string]string{
				keyID:        activityTestJSON(`{"id":"` + canonicalURI + `"}`),
				canonicalURI: actorTrustActor(victimURI, "alice", "https://evil.example/inbox", "ATTACKER"),
			},
		},
		{
			name: "refetched document hides another host's actor in @graph",
			responses: map[string]string{
				keyID:        activityTestJSON(`{"id":"` + canonicalURI + `"}`),
				canonicalURI: activityTestJSON(`{"id":"` + canonicalURI + `","@graph":[` + actorTrustActorObject(victimURI, "alice", "https://evil.example/inbox", "ATTACKER") + `]}`),
			},
		},
		{
			name: "refetched document hides another host's key in @graph",
			responses: map[string]string{
				keyID:        activityTestJSON(`{"id":"` + canonicalURI + `"}`),
				canonicalURI: activityTestJSON(`{"id":"` + canonicalURI + `","@graph":[{"id":"` + victimURI + `#main-key","owner":"` + victimURI + `","publicKeyPem":"ATTACKER"}]}`),
				// The owner only names its key, so the attacker's PEM would fill the gap.
				victimURI: activityTestJSON(`{"id":"` + victimURI + `","type":"Person","preferredUsername":"alice","inbox":"` + victimURI + `/inbox","publicKey":"` + victimURI + `#main-key"}`),
			},
		},
	}
	for name, fetch := range actorTrustPublicKeyDocumentFetchers() {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				actorTrustMockRemote(t, tc.responses)
				actor, err := fetch(keyID)
				if err == nil {
					t.Fatalf("key document resolved to actor id=%q key=%q, want rejection", actor.ID, actor.PublicKey.PublicKeyPem)
				}
				if !strings.Contains(err.Error(), "public key not found") {
					t.Fatalf("error = %v, want public key not found", err)
				}
			})
		}
	}
}

func TestFetchActivityActorForPublicKeyDocumentAcceptsCanonicalDocuments(t *testing.T) {
	const gtsKeyID = "https://gts.example/users/alice/main-key"
	const gtsActorURI = "https://gts.example/users/alice"
	gtsActor := activityTestJSON(`{
		"id":"` + gtsActorURI + `",
		"type":"Person",
		"preferredUsername":"alice",
		"inbox":"` + gtsActorURI + `/inbox",
		"publicKey":{"id":"` + gtsKeyID + `","owner":"` + gtsActorURI + `","publicKeyPem":"PEM-GTS"}
	}`)
	const keyID = "https://remote.example/keys/bob"
	const ownerURI = "https://remote.example/users/bob"
	cases := []struct {
		name      string
		keyID     string
		responses map[string]string
		wantID    string
		wantPEM   string
	}{
		{
			name:  "key URL serving its actor stub",
			keyID: gtsKeyID,
			responses: map[string]string{
				gtsKeyID:    gtsActor,
				gtsActorURI: gtsActor,
			},
			wantID:  gtsActorURI,
			wantPEM: "PEM-GTS",
		},
		{
			name:  "standalone key document confirmed by its owner",
			keyID: keyID,
			responses: map[string]string{
				keyID: activityTestJSON(`{"id":"` + keyID + `","owner":"` + ownerURI + `","publicKeyPem":"PEM-BOB"}`),
				ownerURI: activityTestJSON(`{
					"id":"` + ownerURI + `",
					"type":"Person",
					"preferredUsername":"bob",
					"inbox":"` + ownerURI + `/inbox",
					"publicKey":{"id":"` + keyID + `","owner":"` + ownerURI + `","publicKeyPem":"PEM-BOB"}
				}`),
			},
			wantID:  ownerURI,
			wantPEM: "PEM-BOB",
		},
	}
	for name, fetch := range actorTrustPublicKeyDocumentFetchers() {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				actorTrustMockRemote(t, tc.responses)
				actor, err := fetch(tc.keyID)
				if err != nil {
					t.Fatal(err)
				}
				if actor.ID != tc.wantID || actor.PublicKey.PublicKeyPem != tc.wantPEM {
					t.Fatalf("actor id=%q key=%q, want id=%q key=%q", actor.ID, actor.PublicKey.PublicKeyPem, tc.wantID, tc.wantPEM)
				}
			})
		}
	}
}

func TestFetchActivityPubMentionAccountByHrefRequiresWebFingerLoopBack(t *testing.T) {
	// A same-host URL (for example a boost URL redirecting to the attacker)
	// serves an actor claiming that URL as its id and alice as its username.
	const gadget = "https://victim.example/users/mallory/statuses/1"
	webfingerURL := actorTrustWebFingerURL("alice@victim.example")
	server := &Server{cfg: config.Config{LocalDomain: "paon.example"}}

	t.Run("actor whose WebFinger does not loop back", func(t *testing.T) {
		requests := actorTrustMockRemote(t, map[string]string{
			gadget:       actorTrustActor(gadget, "alice", "https://evil.example/inbox", "ATTACKER"),
			webfingerURL: actorTrustWebFinger("alice@victim.example", "https://victim.example/users/alice"),
		})
		account, err := server.fetchActivityPubMentionAccountByHref(actorTrustForbiddenDB(t), gadget)
		if err != nil || account != nil {
			t.Fatalf("mention account = %v, %v, want skipped mention", account, err)
		}
		if !requests.contains(webfingerURL) {
			t.Fatalf("requests = %v, want the WebFinger loop-back check", requests.list())
		}
	})

	t.Run("actor without a public key", func(t *testing.T) {
		requests := actorTrustMockRemote(t, map[string]string{
			gadget: activityTestJSON(`{"id":"` + gadget + `","type":"Person","preferredUsername":"alice","inbox":"https://evil.example/inbox"}`),
		})
		account, err := server.fetchActivityPubMentionAccountByHref(actorTrustForbiddenDB(t), gadget)
		if err != nil || account != nil {
			t.Fatalf("mention account = %v, %v, want skipped mention", account, err)
		}
		if requests.contains(webfingerURL) {
			t.Fatalf("requests = %v, keyless actor must be rejected before WebFinger", requests.list())
		}
	})
}

func TestFetchRemoteMentionAccountChecksActorBeforeUpsert(t *testing.T) {
	ref := statusMentionRef{Username: "bob", Domain: "evil.example"}
	server := &Server{cfg: config.Config{LocalDomain: "paon.example"}}
	cases := []struct {
		name     string
		actorURL string
		username string
	}{
		{name: "self link to another host's URL with another username", actorURL: "https://victim.example/users/mallory/statuses/1", username: "alice"},
		{name: "self link to the same username on another host", actorURL: "https://victim.example/users/bob", username: "bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := actorTrustMockRemote(t, map[string]string{
				actorTrustWebFingerURL("bob@evil.example"): actorTrustWebFinger("bob@evil.example", tc.actorURL),
				tc.actorURL: actorTrustActor(tc.actorURL, tc.username, "https://evil.example/inbox", "ATTACKER"),
			})
			account, err := server.fetchRemoteMentionAccount(actorTrustForbiddenDB(t), ref)
			if err != nil || account != nil {
				t.Fatalf("mention account = %v, %v, want unresolved mention", account, err)
			}
			if !requests.contains(tc.actorURL) {
				t.Fatalf("requests = %v, want the self link fetched", requests.list())
			}
		})
	}
}

func TestFetchActivityPubQuoteActorRequiresAllowedDomainAndWebFinger(t *testing.T) {
	const gadget = "https://victim.example/users/mallory/statuses/1"

	t.Run("actor whose WebFinger does not loop back", func(t *testing.T) {
		webfingerURL := actorTrustWebFingerURL("alice@victim.example")
		requests := actorTrustMockRemote(t, map[string]string{
			gadget:       actorTrustActor(gadget, "alice", "https://evil.example/inbox", "ATTACKER"),
			webfingerURL: actorTrustWebFinger("alice@victim.example", "https://victim.example/users/alice"),
		})
		server := &Server{cfg: config.Config{LocalDomain: "paon.example"}, db: actorTrustDryRunDB(t)}
		if server.fetchActivityPubQuoteActorForRequest(gadget, "") {
			t.Fatal("quote actor accepted without a WebFinger loop back")
		}
		if got := requests.list(); len(got) != 2 || got[0] != gadget || got[1] != webfingerURL {
			t.Fatalf("requests = %v, want the actor and its WebFinger only", got)
		}
	})

	t.Run("actor on a domain outside the allowlist", func(t *testing.T) {
		requests := actorTrustMockRemote(t, map[string]string{
			gadget: actorTrustActor(gadget, "alice", "https://evil.example/inbox", "ATTACKER"),
		})
		// The dry-run database has no domain_allows rows, so every domain is
		// outside the LIMITED_FEDERATION_MODE allowlist.
		server := &Server{cfg: config.Config{LocalDomain: "paon.example", LimitedFederationMode: true}, db: actorTrustDryRunDB(t)}
		if server.fetchActivityPubQuoteActorForRequest(gadget, "") {
			t.Fatal("quote actor accepted on a disallowed domain")
		}
		if got := requests.list(); len(got) != 0 {
			t.Fatalf("requests = %v, disallowed domains must not be fetched", got)
		}
	})
}
