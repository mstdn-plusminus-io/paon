//go:build integration

package api

import (
	"bytes"
	"context"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestUnverifiedActivityRefetchWorkerIntegration(t *testing.T) {
	databaseURL, redisURL := os.Getenv("PAON_TEST_DATABASE_URL"), os.Getenv("PAON_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL and PAON_TEST_REDIS_URL are required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		RedisURL: redisURL, Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example",
		SecretKeyBase: "unverified-refetch-integration-secret",
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
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Create authentication and source selection", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			enabled  bool
			accepted bool
			valid    bool
			spoofed  bool
		}{
			{name: "default rejects invalid signature without fetching", accepted: true},
			{name: "opt in imports the origin copy", enabled: true, accepted: true},
			{name: "unaccepted relay cannot request recovery", enabled: true},
			{name: "accepted inbox claimed by another origin is rejected", enabled: true, accepted: true, spoofed: true},
			{name: "valid signature retains the normal path", enabled: true, accepted: true, valid: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				server, origin, relay, _ := unverifiedRefetchIntegrationFixture(t, database, cfg, publicKey, test.enabled, test.accepted)
				if test.spoofed {
					relay.URI = "https://impostor.example/actor"
					if err := server.db.Model(&models.Account{}).Where("id = ?", relay.ID).Update("uri", relay.URI).Error; err != nil {
						t.Fatal(err)
					}
				}
				now := time.Now().UTC().Truncate(time.Second)
				objectURI := origin.URI + "/statuses/created"
				otherURI := origin.URI + "/statuses/must-not-overwrite"
				// AtomURI participates in same-account deduplication. If the relay
				// alias leaks through recovery, this row would hide the new object.
				unverifiedRefetchCreateStatus(t, server.db, origin, otherURI, "existing unrelated content", now.Add(-time.Hour))
				object := map[string]any{
					"id": objectURI, "type": "Note", "attributedTo": origin.URI,
					"content": "<p>original signed content</p>", "published": now.Format(time.RFC3339),
					"to": []string{activityPubPublicIRI},
				}
				activity := unverifiedRefetchActivity(origin.URI, "Create", objectURI+"/activity", object)
				body := unverifiedRefetchSignedBody(t, origin, privateKey, activity, func(signed map[string]any) {
					if test.valid {
						return
					}
					signed["to"] = []string{"https://attacker.example/users/private-recipient"}
					relayObject := signed["object"].(map[string]any)
					relayObject["content"] = "<p>forged relay content</p>"
					relayObject["to"] = []string{"https://attacker.example/users/private-recipient"}
					relayObject["atomUri"] = otherURI
				})
				fetchedObject := map[string]any{
					"@context": "https://www.w3.org/ns/activitystreams", "id": objectURI, "type": "Note",
					"attributedTo": origin.URI, "content": "<p>content from origin</p>",
					"published": now.Format(time.RFC3339), "to": []string{activityPubPublicIRI},
				}
				requests := unverifiedRefetchMockOrigin(t, objectURI, http.StatusOK, fetchedObject)
				var logs bytes.Buffer
				previousWriter := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(previousWriter) })
				task, err := unverifiedRefetchRunWorker(t, server, relay, body)
				expectSuccess := test.valid || test.enabled && test.accepted && !test.spoofed
				if !expectSuccess {
					if !errors.Is(err, errActivityPubEventNotApplied) || !errors.Is(err, rsa.ErrVerification) {
						t.Fatalf("worker error = %v, want original signature rejection", err)
					}
					var diagnostic *activityPubSignatureVerificationError
					if !errors.As(err, &diagnostic) || diagnostic.Diagnostics.Receipt == nil {
						t.Fatalf("worker lost signature diagnostics or receipt: %v", err)
					}
					assertSignatureDiagnosticTaskLog(t, task, err, "forged relay content", privateKey)
					if *requests != 0 {
						t.Fatalf("ineligible job issued %d origin requests", *requests)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					wantContent, wantRequests := "<p>content from origin</p>", 1
					if test.valid {
						wantContent, wantRequests = "<p>original signed content</p>", 0
					}
					var saved models.Status
					if err := server.db.Where("uri = ?", objectURI).First(&saved).Error; err != nil {
						t.Fatal(err)
					}
					if saved.Text != wantContent || saved.AccountID != origin.ID || saved.Visibility != 0 || *requests != wantRequests {
						t.Fatalf("saved status content=%q owner=%d visibility=%d; requests=%d", saved.Text, saved.AccountID, saved.Visibility, *requests)
					}
					if !test.valid && !strings.Contains(logs.String(), "event=activitypub_unverified_activity_refetched") {
						t.Fatalf("worker did not report successful origin recovery: %s", logs.String())
					}
				}
				var preserved models.Status
				if err := server.db.Where("uri = ?", otherURI).First(&preserved).Error; err != nil {
					t.Fatal(err)
				}
				if preserved.Text != "existing unrelated content" || preserved.DeletedAt.Valid {
					t.Fatalf("relay atomUri modified unrelated status: %+v", preserved)
				}
			})
		}
	})

	t.Run("Update status applies only fetched content", func(t *testing.T) {
		server, origin, relay, _ := unverifiedRefetchIntegrationFixture(t, database, cfg, publicKey, true, true)
		now := time.Now().UTC().Truncate(time.Second)
		objectURI := origin.URI + "/statuses/updated"
		status := unverifiedRefetchCreateStatus(t, server.db, origin, objectURI, "old content", now.Add(-time.Hour))
		object := map[string]any{
			"id": objectURI, "type": "Note", "attributedTo": origin.URI,
			"content": "<p>signed update</p>", "published": now.Add(-time.Hour).Format(time.RFC3339),
			"updated": now.Format(time.RFC3339), "to": []string{activityPubPublicIRI},
		}
		body := unverifiedRefetchSignedBody(t, origin, privateKey, unverifiedRefetchActivity(origin.URI, "Update", objectURI+"#updates/1", object), func(signed map[string]any) {
			signed["object"].(map[string]any)["content"] = "<p>forged update</p>"
		})
		object["@context"] = "https://www.w3.org/ns/activitystreams"
		object["content"] = "<p>fresh origin update</p>"
		requests := unverifiedRefetchMockOrigin(t, objectURI, http.StatusOK, object)
		if _, err := unverifiedRefetchRunWorker(t, server, relay, body); err != nil {
			t.Fatal(err)
		}
		var saved models.Status
		if err := server.db.First(&saved, status.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.Text != "<p>fresh origin update</p>" || !saved.EditedAt.Valid || *requests != 1 {
			t.Fatalf("updated status text=%q edited=%v requests=%d", saved.Text, saved.EditedAt, *requests)
		}
	})

	t.Run("Update actor fragment fetches the actor document", func(t *testing.T) {
		server, origin, relay, _ := unverifiedRefetchIntegrationFixture(t, database, cfg, publicKey, true, true)
		object := map[string]any{
			"id": origin.URI, "type": "Person", "preferredUsername": origin.Username,
			"name": "signed profile", "inbox": origin.URI + "/inbox",
			"publicKey": map[string]any{"id": origin.URI + "#main-key", "owner": origin.URI, "publicKeyPem": publicKey},
		}
		body := unverifiedRefetchSignedBody(t, origin, privateKey, unverifiedRefetchActivity(origin.URI, "Update", origin.URI+"#updates/123", object), func(signed map[string]any) {
			signed["object"].(map[string]any)["name"] = "forged relay profile"
		})
		object["@context"] = []string{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext}
		object["name"] = "current origin profile"
		requests := unverifiedRefetchMockOrigin(t, origin.URI, http.StatusOK, object)
		if _, err := unverifiedRefetchRunWorker(t, server, relay, body); err != nil {
			t.Fatal(err)
		}
		var saved models.Account
		if err := server.db.First(&saved, origin.ID).Error; err != nil {
			t.Fatal(err)
		}
		var savedKeypair models.Keypair
		if err := server.db.Where("uri = ? AND account_id = ?", origin.URI+"#main-key", origin.ID).First(&savedKeypair).Error; err != nil {
			t.Fatal(err)
		}
		if saved.DisplayName != "current origin profile" || savedKeypair.PublicKey != publicKey || *requests != 1 {
			t.Fatalf("updated actor name=%q key preserved=%v requests=%d", saved.DisplayName, savedKeypair.PublicKey == publicKey, *requests)
		}
	})

	t.Run("Delete requires origin evidence and persisted ownership", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			status        int
			known         bool
			otherOwner    bool
			tombstone     bool
			wantSuccess   bool
			wantDeleted   bool
			wantTombstone bool
		}{
			{name: "known object is gone", status: http.StatusGone, known: true, wantSuccess: true, wantDeleted: true, wantTombstone: true},
			{name: "known object returns a tombstone", status: http.StatusOK, known: true, tombstone: true, wantSuccess: true, wantDeleted: true, wantTombstone: true},
			{name: "not found does not mean deleted", status: http.StatusNotFound, known: true},
			{name: "gone object belonging to another actor is rejected", status: http.StatusGone, known: true, otherOwner: true},
			{name: "unknown gone object creates no tombstone", status: http.StatusGone, wantSuccess: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				server, origin, relay, other := unverifiedRefetchIntegrationFixture(t, database, cfg, publicKey, true, true)
				now := time.Now().UTC().Truncate(time.Second)
				objectURI := origin.URI + "/statuses/deleted"
				var status models.Status
				if test.known {
					owner := origin
					if test.otherOwner {
						owner = other
					}
					status = unverifiedRefetchCreateStatus(t, server.db, owner, objectURI, "existing content", now.Add(-time.Hour))
				}
				object := map[string]any{"id": objectURI, "type": "Tombstone"}
				body := unverifiedRefetchSignedBody(t, origin, privateKey, unverifiedRefetchActivity(origin.URI, "Delete", "https://origin.example/deletion-event", object), func(signed map[string]any) {
					signed["published"] = now.Add(time.Second).Format(time.RFC3339)
					signed["object"].(map[string]any)["atomUri"] = other.URI + "/statuses/other"
				})
				var response any
				if test.tombstone {
					response = map[string]any{"@context": "https://www.w3.org/ns/activitystreams", "id": objectURI, "type": "Tombstone"}
				}
				unverifiedRefetchMockOrigin(t, objectURI, test.status, response)
				_, err := unverifiedRefetchRunWorker(t, server, relay, body)
				if test.wantSuccess {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !errors.Is(err, errActivityPubEventNotApplied) {
					t.Fatalf("worker error = %v, want failed authentication/recovery", err)
				}
				if test.known {
					var saved models.Status
					if err := server.db.First(&saved, status.ID).Error; err != nil {
						t.Fatal(err)
					}
					if saved.DeletedAt.Valid != test.wantDeleted {
						t.Fatalf("status deleted = %v, want %v", saved.DeletedAt.Valid, test.wantDeleted)
					}
				}
				var tombstones int64
				if err := server.db.Model(&models.Tombstone{}).Where("uri = ?", objectURI).Count(&tombstones).Error; err != nil {
					t.Fatal(err)
				}
				if (tombstones != 0) != test.wantTombstone {
					t.Fatalf("tombstones = %d, want presence %v", tombstones, test.wantTombstone)
				}
			})
		}
	})

	t.Run("Delete actor requires confirmed disappearance at its own URI", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			enabled bool
			status  int
			deleted bool
		}{
			{name: "default cannot delete an actor", status: http.StatusGone},
			{name: "actor not found preserves the account", enabled: true, status: http.StatusNotFound},
			{name: "actor gone deletes only its account", enabled: true, status: http.StatusGone, deleted: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				server, origin, relay, other := unverifiedRefetchIntegrationFixture(t, database, cfg, publicKey, test.enabled, true)
				now := time.Now().UTC().Truncate(time.Second)
				unverifiedRefetchCreateStatus(t, server.db, origin, origin.URI+"/statuses/owned", "owned content", now.Add(-time.Hour))
				preserved := unverifiedRefetchCreateStatus(t, server.db, other, other.URI+"/statuses/preserved", "unrelated content", now.Add(-time.Hour))
				body := unverifiedRefetchSignedBody(t, origin, privateKey, unverifiedRefetchActivity(origin.URI, "Delete", origin.URI+"#delete", origin.URI), func(signed map[string]any) {
					signed["published"] = now.Format(time.RFC3339)
				})
				requests := unverifiedRefetchMockOrigin(t, origin.URI, test.status, nil)
				_, err := unverifiedRefetchRunWorker(t, server, relay, body)
				if test.deleted {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !errors.Is(err, errActivityPubEventNotApplied) {
					t.Fatalf("actor Delete error = %v, want rejection", err)
				}
				var account models.Account
				err = server.db.First(&account, origin.ID).Error
				if test.deleted {
					if !errors.Is(err, gorm.ErrRecordNotFound) {
						t.Fatalf("confirmed remote actor was not purged: %v, %+v", err, account)
					}
				} else if err != nil || account.SuspendedAt.Valid {
					t.Fatalf("unconfirmed actor Delete changed the account: %v, %+v", err, account)
				}
				if !test.enabled && *requests != 0 {
					t.Fatalf("disabled actor Delete issued %d requests", *requests)
				}
				var otherStatus models.Status
				if err := server.db.First(&otherStatus, preserved.ID).Error; err != nil || otherStatus.DeletedAt.Valid {
					t.Fatalf("actor Delete affected another account's status: %v, %+v", err, otherStatus)
				}
			})
		}
	})
}

func unverifiedRefetchIntegrationFixture(t *testing.T, database *gorm.DB, cfg config.Config, publicKey string, enabled, accepted bool) (*Server, models.Account, models.Account, models.Account) {
	t.Helper()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	cfg.AllowUnverifiedActivityRefetch = enabled
	cfg.RedisNamespace = "unverified-refetch-integration-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	now := time.Now().UTC()
	origin := models.Account{Username: "alice", Domain: sql.NullString{String: "origin.example", Valid: true}, URI: "https://origin.example/users/alice", PublicKey: publicKey, Protocol: 1, CreatedAt: now, UpdatedAt: now}
	relay := models.Account{Username: "relay", Domain: sql.NullString{String: "relay.example", Valid: true}, URI: "https://relay.example/actor", InboxURL: "https://relay.example/inbox", Protocol: 1, CreatedAt: now, UpdatedAt: now}
	other := models.Account{Username: "bob", Domain: origin.Domain, URI: "https://origin.example/users/bob", Protocol: 1, CreatedAt: now, UpdatedAt: now}
	for _, account := range []*models.Account{&origin, &relay, &other} {
		if err := tx.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	if accepted {
		if err := tx.Create(&models.Relay{InboxURL: relay.InboxURL, State: relayStateAccepted, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &Server{cfg: cfg, db: tx}, origin, relay, other
}

func unverifiedRefetchActivity(actorURI, typ, activityID string, object any) map[string]any {
	return map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams", "id": activityID,
		"type": typ, "actor": actorURI, "to": []string{activityPubPublicIRI}, "object": object,
	}
}

func unverifiedRefetchSignedBody(t *testing.T, actor models.Account, privateKey string, document map[string]any, mutate func(map[string]any)) []byte {
	t.Helper()
	actor.PrivateKey = sql.NullString{String: privateKey, Valid: true}
	signer := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "origin.example", WebDomain: "origin.example"}}
	signed, err := signer.signActivityPubLinkedDataSignaturePayload(actor, document)
	if err != nil {
		t.Fatal(err)
	}
	mutate(signed)
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func unverifiedRefetchMockOrigin(t *testing.T, expectedURI string, status int, document any) *int {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	requests := 0
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != expectedURI {
			t.Fatalf("unexpected recovery request: %s, want exact object %s", request.URL, expectedURI)
		}
		return textResponse(status, "application/activity+json", string(body)), nil
	})}
	return &requests
}

func unverifiedRefetchRunWorker(t *testing.T, server *Server, relay models.Account, body []byte) (*asynq.Task, error) {
	t.Helper()
	job := activityPubInboxProcessingJob{ActorID: relay.ID, Body: body, Receipt: newActivityPubInboxReceipt(server, body)}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	task := asynq.NewTask(asynqTaskActivityPubProcessing, payload)
	return task, server.handleAsynqActivityPubProcessing(t.Context(), task)
}

func unverifiedRefetchCreateStatus(t *testing.T, database *gorm.DB, owner models.Account, uri, content string, createdAt time.Time) models.Status {
	t.Helper()
	status := models.Status{URI: sql.NullString{String: uri, Valid: true}, AccountID: owner.ID, Text: content, Local: sql.NullBool{Bool: false, Valid: true}, Visibility: 0, CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := database.Create(&status).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.StatusStat{StatusID: status.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return status
}
