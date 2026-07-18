package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

func TestDeviseTokenLookupValuesKeepsRawToken(t *testing.T) {
	values := deviseTokenLookupValues(" raw-token ", deviseResetPasswordTokenColumn, "")
	if len(values) != 1 || values[0] != "raw-token" {
		t.Fatalf("lookup values = %#v", values)
	}
}

func TestDeviseTokenDigestMatchesDeviseTokenGeneratorShape(t *testing.T) {
	raw := "reset-token"
	secret := "secret-key-base"
	column := deviseResetPasswordTokenColumn

	key := pbkdf2.Key([]byte(secret), []byte("Devise "+column), 1000, 64, sha1.New)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(raw))
	want := hex.EncodeToString(mac.Sum(nil))

	got, ok := deviseTokenDigest(raw, column, secret)
	if !ok || got != want {
		t.Fatalf("digest = %q ok=%v, want %q", got, ok, want)
	}
}

func TestDeviseTokenLookupValuesIncludesRailsHashDigestRotationCandidate(t *testing.T) {
	values := deviseTokenLookupValues("confirm-token", deviseConfirmationTokenColumn, "secret-key-base")
	if len(values) != 3 {
		t.Fatalf("lookup values = %#v", values)
	}
	if values[0] != "confirm-token" {
		t.Fatalf("raw token not first: %#v", values)
	}
	if len(values[1]) != sha256.Size*2 || len(values[2]) != sha256.Size*2 || values[1] == values[2] {
		t.Fatalf("digest candidates = %#v", values)
	}
}
