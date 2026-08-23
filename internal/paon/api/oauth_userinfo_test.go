package api

import (
	"database/sql"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestOAuthUserInfoMatchesMastodon4422Claims(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.test", LocalDomain: "example.test"}}
	got := s.oauthUserInfoFromAccount(models.Account{ID: 42, Username: "alice", DisplayName: "Alice"})
	if got.Issuer != "https://example.test/" || got.Subject != "https://example.test/users/alice" {
		t.Fatalf("issuer/subject = %#v", got)
	}
	if got.Name != "Alice" || got.PreferredUsername != "alice" || got.Profile != "https://example.test/@alice" {
		t.Fatalf("profile claims = %#v", got)
	}
	if got.Picture == "" {
		t.Fatalf("picture must contain the default avatar URL: %#v", got)
	}
}

func TestOAuthUserInfoUsesNumericActivityPubSubjectForNewAccounts(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.test", LocalDomain: "example.test"}}
	got := s.oauthUserInfoFromAccount(models.Account{
		ID:       42,
		Username: "alice",
		IDScheme: sql.NullInt64{Int64: 1, Valid: true},
	})
	if got.Subject != "https://example.test/ap/users/42" {
		t.Fatalf("subject = %q, want numeric ActivityPub actor URI", got.Subject)
	}
	if got.Profile != "https://example.test/@alice" {
		t.Fatalf("profile = %q, want retained web profile URL", got.Profile)
	}
}
