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
)

func TestRelayedLinkedDataSignatureVerifiesOnlyCompactedDocuments(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		Scheme:               "https",
		LocalDomain:          "paon.example",
		WebDomain:            "paon.example",
		SecretKeyBase:        "integration-secret",
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
	now := time.Now().UTC()
	origin := models.Account{
		Username:  "alice",
		Domain:    sql.NullString{String: "origin.example", Valid: true},
		URI:       linkedDataForgeryVictimURI,
		InboxURL:  linkedDataForgeryVictimURI + "/inbox",
		Protocol:  1,
		PublicKey: publicKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
	relay := models.Account{
		Username:  "mallory",
		Domain:    sql.NullString{String: "attacker.example", Valid: true},
		URI:       "https://attacker.example/actor",
		Protocol:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := database.Create(&origin).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&relay).Error; err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: cfg, db: database}

	signingServer := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "origin.example", WebDomain: "origin.example"}}
	originSigner := origin
	originSigner.PrivateKey = sql.NullString{String: privateKey, Valid: true}
	signed, err := signingServer.signActivityPubLinkedDataSignaturePayload(originSigner, map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       origin.URI + "#updates/1",
		"type":     "Update",
		"actor":    origin.URI,
		"to":       []any{activityPubPublicIRI},
		"object":   map[string]any{"id": origin.URI + "/unsupported", "type": "Tombstone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	signedDocument := cloneLinkedDataTestDocument(t, signed)

	// The forged object would replace the victim's key and inbox if the
	// uncompacted document were authenticated by the victim's signature.
	_, attackerPublicKey, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	forgedActor := map[string]any{
		"id":                origin.URI,
		"type":              "Person",
		"preferredUsername": "alice",
		"inbox":             "https://attacker.example/inbox",
		"publicKey": map[string]any{
			"id":           origin.URI + "#main-key",
			"owner":        origin.URI,
			"publicKeyPem": attackerPublicKey,
		},
	}
	assertOriginUnchanged := func(t *testing.T) {
		t.Helper()
		var stored models.Account
		if err := database.Where("id = ?", origin.ID).First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored.PublicKey != publicKey || stored.InboxURL != origin.InboxURL || stored.SuspendedAt.Valid {
			t.Fatalf("victim account changed: inbox=%q suspended=%t key changed=%t", stored.InboxURL, stored.SuspendedAt.Valid, stored.PublicKey != publicKey)
		}
	}

	for _, forged := range []bool{false, true} {
		name := "literal signature"
		if forged {
			name += " with null-context forgery"
		}
		t.Run(name, func(t *testing.T) {
			document := cloneLinkedDataTestDocument(t, signedDocument)
			if forged {
				forgeLinkedDataObject(t, document, forgedActor)
			}
			err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), marshalLinkedDataTestDocument(t, document), &relay, nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), `Update object type "Tombstone" is unsupported`) {
				t.Fatalf("relayed activity error = %v, want the signed Tombstone to be processed", err)
			}
			assertOriginUnchanged(t)
		})
	}
	for _, test := range disguisedLinkedDataSignatures {
		t.Run(test.name+" with null-context forgery", func(t *testing.T) {
			document := cloneLinkedDataTestDocument(t, signedDocument)
			forgeLinkedDataObject(t, document, forgedActor)
			test.disguise(document)
			err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), marshalLinkedDataTestDocument(t, document), &relay, nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "not JSON-LD compacted") {
				t.Fatalf("relayed activity error = %v, want rejection before linked-data verification", err)
			}
			assertOriginUnchanged(t)
		})
	}

	// A key node with a relative @id is left out of the canonicalized RDF, so
	// the victim's signature of its own Update still verifies after it is
	// placed ahead of the signed key, which Paon would store.
	signedPersonUpdate, err := signingServer.signActivityPubLinkedDataSignaturePayload(originSigner, linkedDataPersonUpdatePayload(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("publicKey with relative id", func(t *testing.T) {
		document := cloneLinkedDataTestDocument(t, signedPersonUpdate)
		person := document["object"].(map[string]any)
		person["publicKey"] = []any{
			map[string]any{"id": "injected-key", "owner": origin.URI, "publicKeyPem": attackerPublicKey},
			person["publicKey"],
		}
		err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), marshalLinkedDataTestDocument(t, document), &relay, nil, 0)
		if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature does not cover the document") {
			t.Fatalf("relayed activity error = %v, want rejection of content outside the signed RDF", err)
		}
		assertOriginUnchanged(t)
	})
}
