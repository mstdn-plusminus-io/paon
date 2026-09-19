//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRelayedUnsignedAnnounceAuthenticatesCanonicalActivity(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	redisURL := os.Getenv("PAON_TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("PAON_TEST_REDIS_URL is required for integration tests")
	}
	cfg := config.Config{DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2, RedisURL: redisURL, RedisNamespace: "relay-fetch-integration", Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example", SecretKeyBase: "integration-secret"}
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
	_, publicKey, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		accepted       bool
		signature      bool
		blocked        bool
		tamperedRelay  bool
		wrongCanonical bool
		wantRequests   int
		wantError      string
	}{
		{name: "accepted relay retrieves origin activity", accepted: true, wantRequests: 1},
		{name: "tampered relay recipients and embedded note are ignored", accepted: true, tamperedRelay: true, wantRequests: 1},
		{name: "unaccepted sender is not eligible", wantError: "not an accepted relay"},
		{name: "invalid supplied signature is not bypassed", accepted: true, signature: true, wantError: "linked-data"},
		{name: "blocked origin is not fetched", accepted: true, blocked: true, wantError: "domain is not allowed"},
		{name: "canonical actor mismatch is rejected", accepted: true, wrongCanonical: true, wantRequests: 1, wantError: "canonical Announce identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := database.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { tx.Rollback() })
			now := time.Now().UTC()
			origin := models.Account{Username: "likewolfpenguin", Domain: sql.NullString{String: "mstdn.kemono-friends.info", Valid: true}, URI: claimed.Actor, PublicKey: publicKey, Protocol: 1, CreatedAt: now, UpdatedAt: now}
			author := models.Account{Username: "oaug", Domain: origin.Domain, URI: "https://mstdn.kemono-friends.info/users/oaug", Protocol: 1, CreatedAt: now, UpdatedAt: now}
			relay := models.Account{Username: "relay", Domain: sql.NullString{String: "relay.example", Valid: true}, URI: "https://relay.example/actor", InboxURL: "https://relay.example/inbox", Protocol: 1, CreatedAt: now, UpdatedAt: now}
			for _, account := range []*models.Account{&origin, &author, &relay} {
				if err := tx.Create(account).Error; err != nil {
					t.Fatal(err)
				}
			}
			if test.accepted {
				if err := tx.Create(&models.Relay{InboxURL: relay.InboxURL, State: relayStateAccepted, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if test.blocked {
				if err := tx.Create(&models.DomainBlock{Domain: origin.Domain.String, Severity: models.DomainBlockSeverity(1), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
					t.Fatal(err)
				}
			}
			target := models.Status{URI: sql.NullString{String: claimed.Object.ID, Valid: true}, AccountID: author.ID, Text: "authentic target", Local: sql.NullBool{Bool: false, Valid: true}, Visibility: 0, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&target).Error; err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal([]byte(unsignedRelayAnnounceBody), &document); err != nil {
				t.Fatal(err)
			}
			if test.signature {
				document["signature"] = map[string]any{"type": "RsaSignature2017", "creator": origin.URI + "#main-key", "created": "2026-09-19T02:49:54Z", "signatureValue": "AQ=="}
			}
			if test.tamperedRelay {
				document["to"] = []string{"https://attacker.example/actor"}
				document["published"] = "2099-01-01T00:00:00Z"
				document["object"] = map[string]any{"id": claimed.Object.ID, "type": "Note", "content": "forged content", "attributedTo": origin.URI}
			}
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			canonical := unsignedRelayAnnounceBody
			if test.wrongCanonical {
				canonical = strings.Replace(canonical, `"actor":"`+origin.URI+`"`, `"actor":"`+author.URI+`"`, 1)
			}
			requests := 0
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.String() != claimed.ID {
					t.Fatalf("unexpected canonical Announce request: %s", request.URL)
				}
				return textResponse(http.StatusOK, "application/activity+json", canonical), nil
			})}
			server := &Server{cfg: cfg, db: tx}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, &relay, nil, 0)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("inbox error = %v, want %q", err, test.wantError)
			}
			if requests != test.wantRequests {
				t.Fatalf("HTTP requests = %d, want %d", requests, test.wantRequests)
			}
			var reblogs int64
			if err := tx.Model(&models.Status{}).Where("reblog_of_id = ?", target.ID).Count(&reblogs).Error; err != nil {
				t.Fatal(err)
			}
			if reblogs != 0 {
				t.Fatalf("relayed Announce unexpectedly created %d boosts", reblogs)
			}
			var preserved models.Status
			if err := tx.First(&preserved, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if preserved.Text != "authentic target" || preserved.AccountID != author.ID {
				t.Fatalf("relay modified canonical target: %#v", preserved)
			}
		})
	}
}
