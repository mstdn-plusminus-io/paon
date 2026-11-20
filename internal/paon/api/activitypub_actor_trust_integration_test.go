//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestActivityPubActorTrustIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "web.paon.example",
		SecretKeyBase: "activitypub-actor-trust-integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	const victimURI = "https://victim.example/users/alice"
	const gadget = "https://victim.example/users/mallory/statuses/1"

	t.Run("keyId document naming another host's actor is rejected", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		victim := settingsResolveRemoteAccount(t, server.db, "alice", "victim.example", victimURI, time.Now().UTC())
		const unknownVictimURI = "https://victim.example/users/bob"
		settingsResolveMockRemote(t, map[string]string{
			"https://evil.example/k":                       activityTestJSON(`{"id":"https://evil.example/a2"}`),
			"https://evil.example/a2":                      actorTrustActor(victimURI, "alice", "https://evil.example/inbox", "ATTACKER"),
			"https://evil.example/kb":                      activityTestJSON(`{"id":"https://evil.example/b2"}`),
			"https://evil.example/b2":                      activityTestJSON(`{"id":"https://evil.example/b2","@graph":[` + actorTrustActorObject(unknownVictimURI, "bob", "https://evil.example/inbox", "ATTACKER") + `]}`),
			actorTrustWebFingerURL("alice@victim.example"): actorTrustWebFinger("alice@victim.example", victimURI),
			actorTrustWebFingerURL("bob@victim.example"):   actorTrustWebFinger("bob@victim.example", unknownVictimURI),
		})
		if account, err := server.fetchAndStoreActivityActorForKeyID("https://evil.example/k"); err == nil {
			t.Fatalf("forged keyId resolved to account %#v", account)
		}
		actorTrustAssertAccountUntouched(t, server.db, victim)
		if account, err := server.fetchAndStoreActivityActorForKeyID("https://evil.example/kb"); err == nil {
			t.Fatalf("forged @graph keyId resolved to account %#v", account)
		}
		actorTrustAssertNoAccountWithURI(t, server.db, unknownVictimURI)
	})

	t.Run("keyId path returns a known actor without overwriting its key and inboxes", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		const actorURI = "https://remote.example/users/carol"
		const keyID = actorURI + "/main-key"
		existing := settingsResolveRemoteAccount(t, server.db, "carol", "remote.example", actorURI, time.Now().UTC())
		rotated := activityTestJSON(`{
			"id":"` + actorURI + `",
			"type":"Person",
			"preferredUsername":"carol",
			"inbox":"https://remote.example/users/carol/new-inbox",
			"endpoints":{"sharedInbox":"https://remote.example/new-shared-inbox"},
			"publicKey":{"id":"` + keyID + `","owner":"` + actorURI + `","publicKeyPem":"NEW-PEM"}
		}`)
		settingsResolveMockRemote(t, map[string]string{keyID: rotated, actorURI: rotated})
		account, err := server.fetchAndStoreActivityActorForKeyID(keyID)
		if err != nil {
			t.Fatal(err)
		}
		if account == nil || account.ID != existing.ID || account.PublicKey != existing.PublicKey {
			t.Fatalf("keyId account = %#v, want existing account %d as stored", account, existing.ID)
		}
		actorTrustAssertAccountUntouched(t, server.db, existing)
	})

	t.Run("signature key rotation still refreshes the known actor", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		const actorURI = "https://remote.example/users/dave"
		existing := settingsResolveRemoteAccount(t, server.db, "dave", "remote.example", actorURI, time.Now().UTC())
		settingsResolveMockRemote(t, map[string]string{
			actorURI: actorTrustActor(actorURI, "dave", actorURI+"/inbox", "NEW-PEM"),
		})
		refreshed, err := server.refreshActivityPubActorKey(actorURI+"#main-key", &existing)
		if err != nil {
			t.Fatal(err)
		}
		if refreshed == nil || refreshed.ID != existing.ID {
			t.Fatalf("refreshed account = %#v, want account %d", refreshed, existing.ID)
		}
		actorTrustAssertKeypair(t, server.db, existing.ID, actorURI+"#main-key", "NEW-PEM")
	})

	t.Run("keyId path reprocesses a possibly stale known actor from its own document", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		const actorURI = "https://remote.example/users/oscar"
		existing := settingsResolveRemoteAccount(t, server.db, "oscar", "remote.example", actorURI, time.Now().UTC().Add(-48*time.Hour))
		settingsResolveMockRemote(t, map[string]string{
			actorURI: actorTrustActor(actorURI, "oscar", actorURI+"/new-inbox", "NEW-PEM"),
			actorTrustWebFingerURL("oscar@remote.example"): actorTrustWebFinger("oscar@remote.example", actorURI),
		})
		account, err := server.fetchAndStoreActivityActorForKeyID(actorURI + "#main-key")
		if err != nil {
			t.Fatal(err)
		}
		if account == nil || account.ID != existing.ID || account.InboxURL != actorURI+"/new-inbox" {
			t.Fatalf("keyId account = %#v, want stale account %d reprocessed", account, existing.ID)
		}
		actorTrustAssertKeypair(t, server.db, existing.ID, actorURI+"#main-key", "NEW-PEM")
	})

	t.Run("unknown Mastodon actor is stored through its keyId", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		const actorURI = "https://remote.example/users/erin"
		settingsResolveMockRemote(t, map[string]string{
			actorURI: actorTrustActor(actorURI, "erin", actorURI+"/inbox", "PEM-ERIN"),
			actorTrustWebFingerURL("erin@remote.example"): actorTrustWebFinger("erin@remote.example", actorURI),
		})
		account, err := server.fetchAndStoreActivityActorForKeyID(actorURI + "#main-key")
		if err != nil {
			t.Fatal(err)
		}
		if account == nil || account.URI != actorURI {
			t.Fatalf("keyId account = %#v, want stored actor", account)
		}
		actorTrustAssertKeypair(t, server.db, account.ID, actorURI+"#main-key", "PEM-ERIN")
	})

	t.Run("keyId actor on a disallowed domain is not stored", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		const actorURI = "https://blocked.example/users/frank"
		now := time.Now().UTC()
		if err := server.db.Create(&models.DomainBlock{Domain: "blocked.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		const keyID = "https://keys.example/k"
		// The key document is served by an allowed host and confirmed by its
		// owner on the blocked domain (FetchRemoteKeyService#confirmed_owner?),
		// so the lookup reaches the domain check of FetchRemoteActorService on
		// the actor URI rather than on the keyId host.
		settingsResolveMockRemote(t, map[string]string{
			keyID: activityTestJSON(`{"id":"` + keyID + `","owner":"` + actorURI + `","publicKeyPem":"PEM-FRANK"}`),
			actorURI: activityTestJSON(`{
				"id":"` + actorURI + `",
				"type":"Person",
				"preferredUsername":"frank",
				"inbox":"` + actorURI + `/inbox",
				"publicKey":"` + keyID + `"
			}`),
			actorTrustWebFingerURL("frank@blocked.example"): actorTrustWebFinger("frank@blocked.example", actorURI),
		})
		if _, err := server.fetchAndStoreActivityActorForKeyID(keyID); !errors.Is(err, errActivitySignatureDomainNotAllowed) {
			t.Fatalf("error = %v, want errActivitySignatureDomainNotAllowed", err)
		}
		actorTrustAssertNoAccountWithURI(t, server.db, actorURI)
	})

	t.Run("inbound mention href must loop back through WebFinger", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		victim := settingsResolveRemoteAccount(t, server.db, "alice", "victim.example", victimURI, time.Now().UTC())
		const keylessURI = "https://remote.example/users/nokey"
		const actorURI = "https://remote.example/users/grace"
		settingsResolveMockRemote(t, map[string]string{
			gadget: actorTrustActor(gadget, "alice", "https://evil.example/inbox", "ATTACKER"),
			actorTrustWebFingerURL("alice@victim.example"): actorTrustWebFinger("alice@victim.example", victimURI),
			keylessURI: activityTestJSON(`{"id":"` + keylessURI + `","type":"Person","preferredUsername":"nokey","inbox":"` + keylessURI + `/inbox"}`),
			actorTrustWebFingerURL("nokey@remote.example"): actorTrustWebFinger("nokey@remote.example", keylessURI),
			actorURI: actorTrustActor(actorURI, "grace", actorURI+"/inbox", "PEM-GRACE"),
			actorTrustWebFingerURL("grace@remote.example"): actorTrustWebFinger("grace@remote.example", actorURI),
		})
		for _, href := range []string{gadget, keylessURI} {
			account, err := server.accountFromActivityPubMention(server.db, activityTag{Type: "Mention", Href: href})
			if err != nil || account != nil {
				t.Fatalf("%s: mention account = %v, %v, want skipped mention", href, account, err)
			}
			actorTrustAssertNoAccountWithURI(t, server.db, href)
		}
		actorTrustAssertAccountUntouched(t, server.db, victim)
		account, err := server.accountFromActivityPubMention(server.db, activityTag{Type: "Mention", Href: actorURI})
		if err != nil || account == nil || account.URI != actorURI {
			t.Fatalf("mention account = %v, %v, want stored actor", account, err)
		}
	})

	t.Run("local mention resolution stores only an actor matching the acct", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		victim := settingsResolveRemoteAccount(t, server.db, "alice", "victim.example", victimURI, time.Now().UTC())
		const actorURI = "https://remote.example/users/heidi"
		settingsResolveMockRemote(t, map[string]string{
			actorTrustWebFingerURL("x@evil.example"): actorTrustWebFinger("x@evil.example", gadget),
			gadget:                                   actorTrustActor(gadget, "alice", "https://evil.example/inbox", "ATTACKER"),
			actorTrustWebFingerURL("heidi@remote.example"): actorTrustWebFinger("heidi@remote.example", actorURI),
			actorURI: actorTrustActor(actorURI, "heidi", actorURI+"/inbox", "PEM-HEIDI"),
		})
		account, err := server.fetchRemoteMentionAccount(server.db, statusMentionRef{Username: "x", Domain: "evil.example"})
		if err != nil || account != nil {
			t.Fatalf("mention account = %v, %v, want unresolved mention", account, err)
		}
		actorTrustAssertAccountUntouched(t, server.db, victim)
		actorTrustAssertNoAccountWithURI(t, server.db, gadget)
		account, err = server.fetchRemoteMentionAccount(server.db, statusMentionRef{Username: "heidi", Domain: "remote.example"})
		if err != nil || account == nil || account.URI != actorURI {
			t.Fatalf("mention account = %v, %v, want stored actor", account, err)
		}
	})

	t.Run("quote attributedTo must pass the domain and WebFinger checks", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		victim := settingsResolveRemoteAccount(t, server.db, "alice", "victim.example", victimURI, time.Now().UTC())
		now := time.Now().UTC()
		if err := server.db.Create(&models.DomainBlock{Domain: "blocked.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		const blockedURI = "https://blocked.example/users/ivan"
		const actorURI = "https://remote.example/users/judy"
		requests := settingsResolveMockRemote(t, map[string]string{
			gadget: actorTrustActor(gadget, "alice", "https://evil.example/inbox", "ATTACKER"),
			actorTrustWebFingerURL("alice@victim.example"): actorTrustWebFinger("alice@victim.example", victimURI),
			actorURI: actorTrustActor(actorURI, "judy", actorURI+"/inbox", "PEM-JUDY"),
			actorTrustWebFingerURL("judy@remote.example"): actorTrustWebFinger("judy@remote.example", actorURI),
		})
		if server.fetchActivityPubQuoteActorForRequest(gadget, "") {
			t.Fatal("quote actor accepted without a WebFinger loop back")
		}
		actorTrustAssertAccountUntouched(t, server.db, victim)
		actorTrustAssertNoAccountWithURI(t, server.db, gadget)
		before := requests.count()
		if server.fetchActivityPubQuoteActorForRequest(blockedURI, "") {
			t.Fatal("quote actor accepted on a suspended domain")
		}
		if requests.count() != before {
			t.Fatal("quote actor on a suspended domain must not be fetched")
		}
		if !server.fetchActivityPubQuoteActorForRequest(actorURI, "") {
			t.Fatal("verified quote actor was rejected")
		}
	})
}

func actorTrustAssertAccountUntouched(t *testing.T, tx *gorm.DB, want models.Account) {
	t.Helper()
	var got models.Account
	if err := tx.First(&got, want.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.URI != want.URI || got.PublicKey != want.PublicKey || got.InboxURL != want.InboxURL || got.SharedInboxURL != want.SharedInboxURL || !strings.EqualFold(got.Username, want.Username) {
		t.Fatalf("account %d uri=%q public_key=%q inbox=%q shared_inbox=%q username=%q, want it untouched", want.ID, got.URI, got.PublicKey, got.InboxURL, got.SharedInboxURL, got.Username)
	}
}

// actorTrustAssertKeypair checks the key stored in keypairs; like Mastodon 4.6,
// accounts.public_key is cleared once a remote actor's keys are synced.
func actorTrustAssertKeypair(t *testing.T, tx *gorm.DB, accountID int64, uri, publicKey string) {
	t.Helper()
	var keypair models.Keypair
	if err := tx.Where("account_id = ? AND uri = ?", accountID, uri).First(&keypair).Error; err != nil {
		t.Fatalf("keypair %q of account %d: %v", uri, accountID, err)
	}
	if keypair.PublicKey != publicKey {
		t.Fatalf("keypair %q public_key = %q, want %q", uri, keypair.PublicKey, publicKey)
	}
}

func actorTrustAssertNoAccountWithURI(t *testing.T, tx *gorm.DB, uri string) {
	t.Helper()
	var count int64
	if err := tx.Model(&models.Account{}).Where("uri = ?", uri).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d accounts stored with uri %q, want none", count, uri)
	}
}
