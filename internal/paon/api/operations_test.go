package api

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestCanonicalEmailHashMatchesRailsEmailHelper(t *testing.T) {
	plain, err := operationCanonicalEmailHash("user@example.test")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := operationCanonicalEmailHash("U.s.e.r+tag@EXAMPLE.TEST")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != plain {
		t.Fatalf("canonical hash = %s, plain = %s", canonical, plain)
	}
	if _, err := operationCanonicalEmailHash("not-an-email"); err == nil {
		t.Fatal("invalid email was accepted")
	}
}

func TestNormalizeOperationCIDR(t *testing.T) {
	for input, want := range map[string]string{
		"192.0.2.1":      "192.0.2.1/32",
		"192.0.2.9/24":   "192.0.2.0/24",
		"2001:db8::1":    "2001:db8::1/128",
		"2001:db8::f/64": "2001:db8::/64",
	} {
		got, err := normalizeOperationCIDR(input)
		if err != nil || got != want {
			t.Fatalf("normalizeOperationCIDR(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := normalizeOperationCIDR("bad"); err == nil {
		t.Fatal("invalid address was accepted")
	}
}

func TestRotatedAccountUpdateCarriesOldSigningKeyAndNewPublicKey(t *testing.T) {
	oldPrivate, oldPublic, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	_, newPublic, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	task, err := newAsynqAccountUpdateTask(42, oldPrivate)
	if err != nil {
		t.Fatal(err)
	}
	var payload asynqAccountPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AccountID != 42 || payload.OldPrivateKey != oldPrivate {
		t.Fatalf("account update payload = %#v", payload)
	}
	server := &Server{cfg: config.Config{LocalDomain: "example.test"}}
	fresh := models.Account{
		ID: 42, Username: "alice",
		PublicKey:  newPublic,
		PrivateKey: sql.NullString{String: "not-used-for-this-update", Valid: true},
	}
	signer := fresh
	signer.PrivateKey = sql.NullString{String: payload.OldPrivateKey, Valid: true}
	signed, err := server.signActivityPubLinkedDataSignaturePayloadWhenEnabled(signer, activityPubActorUpdate(server, fresh))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	oldPublicKey, err := activityPublicKey(oldPublic)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyActivityPubLinkedDataSignature(body, oldPublicKey) {
		t.Fatal("rotated actor Update was not signed by the old key")
	}
	newPublicKey, err := activityPublicKey(newPublic)
	if err != nil {
		t.Fatal(err)
	}
	if verifyActivityPubLinkedDataSignature(body, newPublicKey) {
		t.Fatal("rotated actor Update unexpectedly verified with the new key")
	}
	if !strings.Contains(string(body), strings.ReplaceAll(newPublic, "\n", "\\n")) {
		t.Fatal("rotated actor Update does not advertise the new public key")
	}
}
