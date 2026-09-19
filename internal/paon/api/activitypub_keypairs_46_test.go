package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMastodon46ActorParsesMultipleInlinePublicKeys(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"@context":          []any{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
		"id":                "https://remote.example/users/alice",
		"type":              "Person",
		"preferredUsername": "alice",
		"inbox":             "https://remote.example/users/alice/inbox",
		"publicKey": []any{
			map[string]any{"id": "https://remote.example/users/alice#first", "owner": "https://remote.example/users/alice", "publicKeyPem": "FIRST"},
			map[string]any{"id": "https://remote.example/users/alice#second", "owner": "https://remote.example/users/alice", "publicKeyPem": "SECOND", "expires": "2030-01-02T03:04:05Z", "revoked": true},
			map[string]any{"id": "https://remote.example/users/alice#wrong-owner", "owner": "https://attacker.example/users/mallory", "publicKeyPem": "MALLORY"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := parseRemoteActivityActor(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(actor.PublicKeys) != 2 {
		t.Fatalf("public key count = %d, want 2: %#v", len(actor.PublicKeys), actor.PublicKeys)
	}
	if actor.PublicKey.ID != "https://remote.example/users/alice#first" || actor.PublicKey.PublicKeyPem != "FIRST" {
		t.Fatalf("legacy first public key = %#v", actor.PublicKey)
	}
	second := actor.PublicKeys[1]
	if second.ID != "https://remote.example/users/alice#second" || second.PublicKeyPem != "SECOND" || !second.Revoked || !second.ExpiresAt.Valid {
		t.Fatalf("second public key = %#v", second)
	}
}

func TestMastodon46UnknownSecondaryKeySelectsExactActorEntryAndSkipsUnavailablePeer(t *testing.T) {
	actorID := "https://remote.example/users/alice"
	requested := actorID + "#secondary"
	fetches := []string{}
	keys := activityRemoteActorPublicKeys(actorID, []any{
		actorID + "#unavailable",
		requested,
	}, func(uri string) (map[string]any, error) {
		fetches = append(fetches, uri)
		if uri != requested {
			return nil, errors.New("404")
		}
		return map[string]any{
			"@context": []any{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
			"id":       actorID,
			"type":     "Person",
			"publicKey": []any{
				map[string]any{"id": actorID + "#first", "owner": actorID, "publicKeyPem": "WRONG-FIRST"},
				map[string]any{"id": requested, "owner": actorID, "publicKeyPem": "SECONDARY"},
			},
		}, nil
	})
	if len(fetches) != 2 || fetches[0] != actorID+"#unavailable" || fetches[1] != requested {
		t.Fatalf("fetches = %#v", fetches)
	}
	if len(keys) != 1 || keys[0].ID != requested || keys[0].PublicKeyPem != "SECONDARY" {
		t.Fatalf("resolved keys = %#v", keys)
	}
}

func TestMastodon46RemoteActorPublicKeysAreCappedAtTen(t *testing.T) {
	actorID := "https://remote.example/users/alice"
	values := make([]any, 0, 12)
	for index := 0; index < 12; index++ {
		values = append(values, map[string]any{
			"id":           fmt.Sprintf("%s#key-%02d", actorID, index),
			"owner":        actorID,
			"publicKeyPem": fmt.Sprintf("KEY-%02d", index),
		})
	}
	keys := activityRemoteActorPublicKeys(actorID, values, nil)
	if len(keys) != mastodon46MaxRemotePublicKeys {
		t.Fatalf("public key count = %d, want %d", len(keys), mastodon46MaxRemotePublicKeys)
	}
	if keys[len(keys)-1].ID != actorID+"#key-09" {
		t.Fatalf("last retained key = %#v", keys[len(keys)-1])
	}
}

func TestMastodon46SeparateKeyAcceptsSecurityContextOnlyForKeyDocuments(t *testing.T) {
	contextValue := []any{"https://w3id.org/security/v1"}
	key := map[string]any{
		"@context":     contextValue,
		"id":           "https://remote.example/keys/secondary",
		"owner":        "https://remote.example/users/alice",
		"publicKeyPem": "KEY",
	}
	if !activityRemotePublicKeySecurityContext(key, contextValue) {
		t.Fatal("security-context key document was rejected")
	}
	delete(key, "owner")
	if activityRemotePublicKeySecurityContext(key, contextValue) {
		t.Fatal("security-context document without an owner was accepted")
	}
}

func TestMastodon46KeypairRevocationExpiryAndAllKeyChangeAreFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	usable := models.Keypair{PublicKey: "OLD"}
	revoked := models.Keypair{PublicKey: "OLD", Revoked: true}
	expired := models.Keypair{PublicKey: "OLD", ExpiresAt: sqlNullTime(now.Add(-time.Second))}
	future := models.Keypair{PublicKey: "OLD", ExpiresAt: sqlNullTime(now.Add(time.Second))}
	if !activityPubKeypairUsableAt(usable, now) || !activityPubKeypairUsableAt(future, now) {
		t.Fatal("usable key was rejected")
	}
	if activityPubKeypairUsableAt(revoked, now) || activityPubKeypairUsableAt(expired, now) {
		t.Fatal("revoked or expired key was accepted")
	}
	if activityPubAllPublicKeysChanged([]string{"OLD"}, []models.Keypair{usable}, now) {
		t.Fatal("retaining one usable old key was treated as an all-key change")
	}
	if err := activityPubResolvedKeypairValidityError("https://remote.example/unavailable", nil, now); err == nil {
		t.Fatal("unavailable requested key passed validity check")
	}
	if !activityPubAllPublicKeysChanged([]string{"OLD"}, []models.Keypair{revoked, expired, {PublicKey: "NEW"}}, now) {
		t.Fatal("revoked/expired old keys prevented all-key change detection")
	}
	for _, keypair := range []models.Keypair{revoked, expired} {
		resolved := &activityPubResolvedKeypair{Keypair: keypair}
		if err := activityPubResolvedKeypairValidityError("https://remote.example/key", resolved, now); err == nil {
			t.Fatalf("invalid key %#v passed validity check", keypair)
		}
	}
}

func sqlNullTime(value time.Time) (out sql.NullTime) {
	out.Time = value
	out.Valid = true
	return out
}
