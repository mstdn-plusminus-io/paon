//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestActivityPubMovedToResolutionIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "web.paon.example",
		AlternateDomains: []string{"alt.paon.example"},
		SecretKeyBase:    "activitypub-moved-to-integration-secret",
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

	t.Run("local movedTo URIs resolve to the local account without fetching", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		requests := settingsResolveMockRemote(t, nil)
		for _, uri := range []string{
			activityPubActorURL(server, alice),
			"https://paon.example/users/alice",
			"https://web.paon.example/@alice",
			"https://web.paon.example/users/ALICE#main-key",
		} {
			movedToID, movedToSet := server.remoteActorMovedToAccountID(uri, "")
			if !movedToSet || !movedToID.Valid || movedToID.Int64 != alice.ID {
				t.Fatalf("%s: moved_to = %v, %v, want local alice %d", uri, movedToID, movedToSet, alice.ID)
			}
		}
		movedToID, movedToSet := server.remoteActorMovedToAccountID("https://web.paon.example/users/nobody", "")
		if !movedToSet || movedToID.Valid {
			t.Fatalf("unknown local movedTo = %v, %v, want NULL", movedToID, movedToSet)
		}
		if requests.count() != 0 {
			t.Fatalf("local movedTo URIs must not be fetched, requests = %d", requests.count())
		}
	})

	t.Run("local movedTo wins over a duplicate remote row with the local URI", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		localURI := activityPubActorURL(server, alice)
		duplicate := settingsResolveRemoteAccount(t, server.db, "alice", "web.paon.example", localURI, time.Now().UTC())
		settingsResolveMockRemote(t, nil)
		movedToID, _ := server.remoteActorMovedToAccountID(localURI, "")
		if movedToID.Int64 != alice.ID || movedToID.Int64 == duplicate.ID {
			t.Fatalf("moved_to = %v, want local alice %d (not duplicate %d)", movedToID, alice.ID, duplicate.ID)
		}
	})

	t.Run("actor upsert and Update(Person) keep movedTo on the local account", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		requests := settingsResolveMockRemote(t, nil)
		localURI := activityPubActorURL(server, alice)
		body := `{"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1"],"id":"https://old.example/users/alice","type":"Person","preferredUsername":"alice","inbox":"https://old.example/users/alice/inbox","movedTo":"` + localURI + `","publicKey":{"id":"https://old.example/users/alice#main-key","owner":"https://old.example/users/alice","publicKeyPem":"PEM"}}`
		actor, err := parseRemoteActivityActor([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		old, err := server.upsertRemoteActivityActorDB(server.db, actor)
		if err != nil || old == nil {
			t.Fatalf("upsert = %v, %v", old, err)
		}
		movedToAccountID := func() sql.NullInt64 {
			var reloaded models.Account
			if err := server.db.First(&reloaded, old.ID).Error; err != nil {
				t.Fatal(err)
			}
			return reloaded.MovedToAccountID
		}
		if got := movedToAccountID(); !got.Valid || got.Int64 != alice.ID {
			t.Fatalf("created moved_to_account_id = %v, want %d", got, alice.ID)
		}
		// Refetching the known actor goes through the existing-account update branch.
		if _, err := server.upsertRemoteActivityActorDB(server.db, actor); err != nil {
			t.Fatal(err)
		}
		if got := movedToAccountID(); !got.Valid || got.Int64 != alice.ID {
			t.Fatalf("refreshed moved_to_account_id = %v, want %d", got, alice.ID)
		}
		update, err := parseActivityPayload([]byte(`{"type":"Update","actor":"https://old.example/users/alice","object":` + body + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := server.db.Model(&models.Account{}).Where("id = ?", old.ID).Update("moved_to_account_id", nil).Error; err != nil {
			t.Fatal(err)
		}
		var current models.Account
		if err := server.db.First(&current, old.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := server.updateActivityPubActor(&current, update.Object, ""); err != nil {
			t.Fatal(err)
		}
		if got := movedToAccountID(); !got.Valid || got.Int64 != alice.ID {
			t.Fatalf("Update(Person) moved_to_account_id = %v, want %d", got, alice.ID)
		}
		var duplicates int64
		if err := server.db.Model(&models.Account{}).Where("lower(domain) IN ?", []string{"paon.example", "web.paon.example"}).Count(&duplicates).Error; err != nil {
			t.Fatal(err)
		}
		if duplicates != 0 || requests.count() != 0 {
			t.Fatalf("local movedTo created %d remote duplicates with %d requests", duplicates, requests.count())
		}
	})

	t.Run("remote movedTo requires a WebFinger round-trip", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		settingsResolveMockRemote(t, map[string]string{
			"https://new.example/users/alice":                               settingsResolveActor("https://new.example/users/alice", "alice", nil),
			settingsResolveWebFingerURL("new.example", "alice@new.example"): settingsResolveWebFinger("alice@new.example", "https://new.example/users/someone-else"),
		})
		movedToID, movedToSet := server.remoteActorMovedToAccountID("https://new.example/users/alice", "")
		if !movedToSet || movedToID.Valid {
			t.Fatalf("moved_to = %v, %v, want NULL when WebFinger does not loop back", movedToID, movedToSet)
		}
		var count int64
		if err := server.db.Model(&models.Account{}).Where("uri = ?", "https://new.example/users/alice").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("unverified movedTo actor must not be stored")
		}

		settingsResolveMockRemote(t, map[string]string{
			"https://new.example/users/alice":                               settingsResolveActor("https://new.example/users/alice", "alice", nil),
			settingsResolveWebFingerURL("new.example", "alice@new.example"): settingsResolveWebFinger("alice@new.example", "https://new.example/users/alice"),
		})
		movedToID, _ = server.remoteActorMovedToAccountID("https://new.example/users/alice", "")
		var target models.Account
		if err := server.db.Where("uri = ?", "https://new.example/users/alice").First(&target).Error; err != nil {
			t.Fatal(err)
		}
		if !movedToID.Valid || movedToID.Int64 != target.ID {
			t.Fatalf("moved_to = %v, want verified target %d", movedToID, target.ID)
		}
	})

	t.Run("remote movedTo on disallowed domains is not fetched", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		now := time.Now().UTC()
		if err := server.db.Create(&models.DomainBlock{Domain: "blocked.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		requests := settingsResolveMockRemote(t, map[string]string{
			"https://blocked.example/users/eve":                                   settingsResolveActor("https://blocked.example/users/eve", "eve", nil),
			settingsResolveWebFingerURL("blocked.example", "eve@blocked.example"): settingsResolveWebFinger("eve@blocked.example", "https://blocked.example/users/eve"),
			"https://other.example/users/frank":                                   settingsResolveActor("https://other.example/users/frank", "frank", nil),
			settingsResolveWebFingerURL("other.example", "frank@other.example"):   settingsResolveWebFinger("frank@other.example", "https://other.example/users/frank"),
		})
		if movedToID, _ := server.remoteActorMovedToAccountID("https://blocked.example/users/eve", ""); movedToID.Valid {
			t.Fatalf("suspended domain moved_to = %v, want NULL", movedToID)
		}
		server.cfg.LimitedFederationMode = true
		if movedToID, _ := server.remoteActorMovedToAccountID("https://other.example/users/frank", ""); movedToID.Valid {
			t.Fatalf("limited federation moved_to = %v, want NULL", movedToID)
		}
		if requests.count() != 0 {
			t.Fatalf("disallowed movedTo domains must not be fetched, requests = %d", requests.count())
		}
		// TagManager#uri_to_resource still finds an already known account.
		known := settingsResolveRemoteAccount(t, server.db, "frank", "other.example", "https://other.example/users/frank", now)
		if movedToID, _ := server.remoteActorMovedToAccountID("https://other.example/users/frank", ""); movedToID.Int64 != known.ID {
			t.Fatalf("known moved_to = %v, want %d", movedToID, known.ID)
		}
	})

	t.Run("remote actor upsert refuses actors hosted on this instance", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		settingsResolveMockRemote(t, nil)
		for _, id := range []string{
			"https://paon.example/users/mallory",
			"https://web.paon.example/users/mallory",
			"https://WEB.paon.example:443/users/mallory",
			"https://alt.paon.example/users/mallory",
		} {
			actor, err := parseRemoteActivityActor([]byte(settingsResolveActor(id, "mallory", nil)))
			if err != nil {
				t.Fatal(err)
			}
			if account, err := server.upsertRemoteActivityActorDB(server.db, actor); !errors.Is(err, errRemoteActivityActorIsLocal) || account != nil {
				t.Fatalf("%s: upsert = %v, %v, want errRemoteActivityActorIsLocal", id, account, err)
			}
		}
		var count int64
		if err := server.db.Model(&models.Account{}).Where("username = ?", "mallory").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("local-host actors created %d remote accounts", count)
		}
	})

	t.Run("inbound mention of an actor hosted on this instance is skipped", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		settingsResolveMockRemote(t, map[string]string{
			"https://alt.paon.example/users/mallory": settingsResolveActor("https://alt.paon.example/users/mallory", "mallory", nil),
		})
		account, err := server.accountFromActivityPubMention(server.db, activityTag{Type: "Mention", Href: "https://alt.paon.example/users/mallory"})
		if err != nil || account != nil {
			t.Fatalf("mention account = %v, %v, want nil without error", account, err)
		}
	})

	t.Run("remote mention whose WebFinger points at this instance is skipped", func(t *testing.T) {
		server, _ := settingsResolveFixture(t, database, cfg)
		settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("evil.example", "bob@evil.example"): settingsResolveWebFinger("bob@evil.example", "https://web.paon.example/users/mallory"),
			"https://web.paon.example/users/mallory":                        settingsResolveActor("https://web.paon.example/users/mallory", "mallory", nil),
		})
		account, err := server.fetchRemoteMentionAccount(server.db, statusMentionRef{Username: "bob", Domain: "evil.example"})
		if err != nil || account != nil {
			t.Fatalf("mention account = %v, %v, want nil without error", account, err)
		}
	})
}
