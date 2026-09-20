//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

func TestRelayedMisskeySquareDeleteResolvesUnknownSignatureCreator(t *testing.T) {
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

	const webfingerURL = "https://misskey-square.net/.well-known/webfinger?resource=acct%3Apontyukidesu%40misskey-square.net"
	tests := []struct {
		name            string
		actorStatus     int
		webfingerActor  string
		webfingerXML    bool
		rejectActorSave bool
		wantRequests    []string
		wantError       []string
	}{
		{
			name:           "unknown creator is fetched and Delete applied",
			actorStatus:    http.StatusOK,
			webfingerActor: misskeySquareDeleteActorURI,
			wantRequests:   []string{misskeySquareDeleteActorURI, webfingerURL},
		},
		{
			name:           "unknown creator with XRD WebFinger is fetched and Delete applied",
			actorStatus:    http.StatusOK,
			webfingerActor: misskeySquareDeleteActorURI,
			webfingerXML:   true,
			wantRequests:   []string{misskeySquareDeleteActorURI, webfingerURL},
		},
		{
			name:         "actor fetch failure preserves cause",
			actorStatus:  http.StatusServiceUnavailable,
			wantRequests: []string{misskeySquareDeleteActorURI, misskeySquareDeleteActorURI + "#main-key"},
			wantError:    []string{"resolve linked-data signature creator", "503"},
		},
		{
			name:           "WebFinger rejection preserves cause",
			actorStatus:    http.StatusOK,
			webfingerActor: "https://misskey-square.net/users/someone-else",
			wantRequests:   []string{misskeySquareDeleteActorURI, webfingerURL},
			wantError:      []string{"resolve linked-data signature creator", "webfinger response does not loop back to actor"},
		},
		{
			name:           "XRD WebFinger cannot authenticate another actor",
			actorStatus:    http.StatusOK,
			webfingerActor: "https://misskey-square.net/users/someone-else",
			webfingerXML:   true,
			wantRequests:   []string{misskeySquareDeleteActorURI, webfingerURL},
			wantError:      []string{"resolve linked-data signature creator", "webfinger response does not loop back to actor"},
		},
		{
			name:            "actor save failure preserves database cause",
			actorStatus:     http.StatusOK,
			webfingerActor:  misskeySquareDeleteActorURI,
			rejectActorSave: true,
			wantRequests:    []string{misskeySquareDeleteActorURI, webfingerURL},
			wantError:       []string{"resolve linked-data signature creator", "reject_misskey_square_actor"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := database.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { tx.Rollback() })
			now := time.Now().UTC()
			relay := models.Account{
				Username:  "relay",
				Domain:    sql.NullString{String: "relay.example", Valid: true},
				URI:       "https://relay.example/actor",
				Protocol:  1,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := tx.Create(&relay).Error; err != nil {
				t.Fatal(err)
			}
			if tt.rejectActorSave {
				if err := tx.Exec(`ALTER TABLE accounts ADD CONSTRAINT reject_misskey_square_actor CHECK (uri <> 'https://misskey-square.net/users/apwc34xd5r')`).Error; err != nil {
					t.Fatal(err)
				}
			}
			server := &Server{cfg: cfg, db: tx}
			var requests []string
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests = append(requests, request.URL.String())
				switch request.URL.String() {
				case misskeySquareDeleteActorURI, misskeySquareDeleteActorURI + "#main-key":
					if tt.actorStatus != http.StatusOK {
						return textResponse(tt.actorStatus, "text/plain", "actor temporarily unavailable"), nil
					}
					body, err := json.Marshal(map[string]any{
						"@context":          []string{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
						"id":                misskeySquareDeleteActorURI,
						"type":              "Person",
						"preferredUsername": "pontyukidesu",
						"inbox":             misskeySquareDeleteActorURI + "/inbox",
						"publicKey": map[string]any{
							"id":           misskeySquareDeleteActorURI + "#main-key",
							"owner":        misskeySquareDeleteActorURI,
							"publicKeyPem": misskeySquareDeletePublicKeyPEM,
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					return textResponse(http.StatusOK, "application/activity+json", string(body)), nil
				case webfingerURL:
					if tt.webfingerXML {
						body := `<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0"><Subject>acct:pontyukidesu@misskey-square.net</Subject><Link rel="self" type="application/activity+json" href="` + tt.webfingerActor + `"/></XRD>`
						return textResponse(http.StatusOK, "application/xrd+xml", body), nil
					}
					body, err := json.Marshal(map[string]any{
						"subject": "acct:pontyukidesu@misskey-square.net",
						"links": []map[string]any{{
							"rel":  "self",
							"type": "application/activity+json",
							"href": tt.webfingerActor,
						}},
					})
					if err != nil {
						t.Fatal(err)
					}
					return textResponse(http.StatusOK, "application/jrd+json", string(body)), nil
				default:
					t.Fatalf("unexpected signature creator request: %s", request.URL)
					return nil, nil
				}
			})}

			err := server.performActivityPubInboxProcessingOnce(t.Context(), activityPubInboxProcessingJob{
				ActorID:   relay.ID,
				ActorType: "Account",
				Body:      json.RawMessage(misskeySquareDeleteBody),
			})
			if len(tt.wantError) == 0 {
				if err != nil {
					t.Errorf("relayed Misskey Square Delete error = %v", err)
				}
			} else if !errors.Is(err, errActivityPubEventNotApplied) {
				t.Errorf("relayed Misskey Square Delete error = %v, want activity not applied", err)
			} else {
				if !strings.Contains(err.Error(), fmt.Sprintf("activitypub processing actor_id=%d", relay.ID)) || !strings.Contains(err.Error(), `activity_type="Delete"`) {
					t.Errorf("processing error lacks task context: %v", err)
				}
				for _, fragment := range tt.wantError {
					if !strings.Contains(err.Error(), fragment) {
						t.Errorf("relayed Misskey Square Delete error = %v, want %q", err, fragment)
					}
				}
			}
			if strings.Join(requests, "\n") != strings.Join(tt.wantRequests, "\n") {
				t.Errorf("signature creator requests = %#v, want %#v", requests, tt.wantRequests)
			}
			var actors []models.Account
			if err := tx.Where("uri = ?", misskeySquareDeleteActorURI).Find(&actors).Error; err != nil {
				t.Fatal(err)
			}
			var tombstones []models.Tombstone
			if err := tx.Where("uri = ?", misskeySquareDeleteTargetURI).Find(&tombstones).Error; err != nil {
				t.Fatal(err)
			}
			if len(tt.wantError) == 0 {
				if len(actors) != 1 {
					t.Fatalf("resolved creator count = %d, want one", len(actors))
				}
				var keypair models.Keypair
				if err := tx.Where("uri = ? AND account_id = ?", misskeySquareDeleteActorURI+"#main-key", actors[0].ID).First(&keypair).Error; err != nil || keypair.PublicKey != misskeySquareDeletePublicKeyPEM {
					t.Fatalf("resolved creator did not retain the expected keypair: %v", err)
				}
				if len(tombstones) != 1 || !tombstones[0].AccountID.Valid || tombstones[0].AccountID.Int64 != actors[0].ID {
					t.Errorf("Delete tombstones = %#v, want one for actor %d", tombstones, actors[0].ID)
				}
			} else {
				if len(actors) != 0 {
					t.Errorf("failed creator resolution persisted %d actors", len(actors))
				}
				if len(tombstones) != 0 {
					t.Errorf("rejected activity created tombstones: %#v", tombstones)
				}
			}
		})
	}
}
