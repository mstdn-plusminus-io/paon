//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRelayedMisskeyActivityAuthenticatesBeforeForwardingFinalization(t *testing.T) {
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
		URI:       "https://origin.example/users/alice",
		Protocol:  1,
		PublicKey: publicKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
	relay := models.Account{
		Username:  "relay",
		Domain:    sql.NullString{String: "relay.example", Valid: true},
		URI:       "https://relay.example/actor",
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
	signingServer := &Server{cfg: config.Config{
		Scheme:      "https",
		LocalDomain: "origin.example",
		WebDomain:   "origin.example",
	}}
	originSigner := origin
	originSigner.PrivateKey = sql.NullString{String: privateKey, Valid: true}
	activity := map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			map[string]any{
				"misskey":          "https://misskey-hub.net/ns#",
				"_misskey_summary": "misskey:_misskey_summary",
			},
		},
		"id":    origin.URI + "#updates/1",
		"type":  "Update",
		"actor": origin.URI,
		"to":    []any{activityPubPublicIRI},
		"object": map[string]any{
			"id":               origin.URI + "/unsupported",
			"type":             "Tombstone",
			"_misskey_summary": "Misskey profile source",
		},
	}
	signed, err := signingServer.signActivityPubLinkedDataSignaturePayload(originSigner, activity)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}

	err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, &relay, nil, 0)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), `Update object type "Tombstone" is unsupported`) {
		t.Fatalf("valid relayed activity error = %v", err)
	}

	var tampered map[string]any
	if err := json.Unmarshal(body, &tampered); err != nil {
		t.Fatal(err)
	}
	tamperedObject, _ := tampered["object"].(map[string]any)
	tamperedObject["_misskey_summary"] = "tampered"
	tamperedBody, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), tamperedBody, &relay, nil, 0)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "activity actor does not match verified HTTP signature actor") {
		t.Fatalf("tampered relayed activity error = %v", err)
	}

	forgedCollection := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       origin.URI + "/collections/forged",
		"type":     "Collection",
		"actor":    "https://victim.example/users/bob",
		"items": []any{map[string]any{
			"id":     origin.URI + "/activities/delete",
			"type":   "Delete",
			"object": origin.URI + "/statuses/1",
		}},
	}
	forgedSigned, err := signingServer.signActivityPubLinkedDataSignaturePayload(originSigner, forgedCollection)
	if err != nil {
		t.Fatal(err)
	}
	forgedBody, err := json.Marshal(forgedSigned)
	if err != nil {
		t.Fatal(err)
	}
	err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), forgedBody, &relay, nil, 0)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature actor does not match activity actor") {
		t.Fatalf("forged collection actor binding error = %v", err)
	}

	var unknownCreatorCount int64
	if err := database.Model(&models.Account{}).Where("uri = ?", misskeyDeleteActorURI).Count(&unknownCreatorCount).Error; err != nil {
		t.Fatal(err)
	}
	if unknownCreatorCount != 0 {
		t.Fatalf("Misskey signature creator already exists before relayed delivery: %d", unknownCreatorCount)
	}

	oldClient := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = oldClient })
	requests := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		switch request.URL.String() {
		case misskeyDeleteActorURI:
			body := `{
				"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1"],
				"id":"` + misskeyDeleteActorURI + `",
				"type":"Person",
				"preferredUsername":"nox_",
				"inbox":"` + misskeyDeleteActorURI + `/inbox",
				"publicKey":{
					"id":"` + misskeyDeleteKeyID + `",
					"owner":"` + misskeyDeleteActorURI + `",
					"publicKeyPem":` + strconv.Quote(misskeyDeletePublicKeyPEM) + `
				}
			}`
			return textResponse(http.StatusOK, "application/activity+json", body), nil
		case "https://misskey.day/.well-known/webfinger?resource=acct%3Anox_%40misskey.day":
			body := `{"subject":"acct:nox_@misskey.day","links":[{"rel":"self","type":"application/activity+json","href":"` + misskeyDeleteActorURI + `"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		default:
			t.Fatalf("unexpected unknown signature creator fetch: %s", request.URL.String())
			return nil, nil
		}
	})}

	if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), []byte(misskeyDeleteBody), &relay, nil, 0); err != nil {
		t.Fatalf("relayed Misskey Delete with unknown signature creator error = %v", err)
	}
	if strings.Join(requests, "\n") != strings.Join([]string{
		misskeyDeleteActorURI,
		"https://misskey.day/.well-known/webfinger?resource=acct%3Anox_%40misskey.day",
	}, "\n") {
		t.Fatalf("unknown signature creator requests = %#v", requests)
	}
	var fetchedCreator models.Account
	if err := database.Where("uri = ?", misskeyDeleteActorURI).First(&fetchedCreator).Error; err != nil {
		t.Fatalf("load fetched signature creator: %v", err)
	}
	if fetchedCreator.PublicKey != misskeyDeletePublicKeyPEM {
		t.Fatal("fetched signature creator did not retain the verified public key")
	}
	var tombstone models.Tombstone
	if err := database.Where("account_id = ? AND uri = ?", fetchedCreator.ID, misskeyDeleteTargetURI).First(&tombstone).Error; err != nil {
		t.Fatalf("load Misskey Delete tombstone: %v", err)
	}
}
