package api

import (
	"context"
	"database/sql"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

// previewCardCreatorDB is a dry-run database on which every lookup misses,
// except that domain_blocks queries matching blockedDomain return a suspension.
// Unknown creators therefore go through remote resolution.
func previewCardCreatorDB(t *testing.T, blockedDomain string) *gorm.DB {
	t.Helper()
	database := actorTrustDryRunDB(t)
	err := database.Callback().Query().After("gorm:query").Register("paon_test:preview_card_creator", func(db *gorm.DB) {
		if block, ok := db.Statement.Dest.(*models.DomainBlock); ok {
			for _, value := range db.Statement.Vars {
				if value == blockedDomain {
					*block = models.DomainBlock{Domain: blockedDomain, Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}}
					db.RowsAffected = 1
					return
				}
			}
		}
		_ = db.AddError(gorm.ErrRecordNotFound)
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestPreviewCardCreatorAttributionHonorsDomainNotAllowed(t *testing.T) {
	const blockedActor = "https://blocked.example/users/alice"
	const allowedActor = "https://allowed.example/users/alice"
	cases := []struct {
		name      string
		creator   string
		responses map[string]string
		forbidden string
	}{
		{
			// ResolveAccountService returns before WebFinger for a disallowed domain.
			name:    "creator on a suspended domain",
			creator: "@alice@blocked.example",
			responses: map[string]string{
				actorTrustWebFingerURL("alice@blocked.example"): actorTrustWebFinger("alice@blocked.example", blockedActor),
				blockedActor: actorTrustActor(blockedActor, "alice", "https://blocked.example/inbox", "KEY"),
			},
			forbidden: actorTrustWebFingerURL("alice@blocked.example"),
		},
		{
			// domain_not_allowed? is checked again after a WebFinger redirect.
			name:    "WebFinger redirect to a suspended domain",
			creator: "@alice@allowed.example",
			responses: map[string]string{
				actorTrustWebFingerURL("alice@allowed.example"): actorTrustWebFinger("alice@blocked.example", blockedActor),
				actorTrustWebFingerURL("alice@blocked.example"): actorTrustWebFinger("alice@blocked.example", blockedActor),
				blockedActor: actorTrustActor(blockedActor, "alice", "https://blocked.example/inbox", "KEY"),
			},
			forbidden: blockedActor,
		},
		{
			// FetchRemoteActorService checks domain_not_allowed? on the actor URI.
			name:    "self link on a suspended domain",
			creator: "@alice@allowed.example",
			responses: map[string]string{
				actorTrustWebFingerURL("alice@allowed.example"): actorTrustWebFinger("alice@allowed.example", blockedActor),
				blockedActor: actorTrustActor(blockedActor, "alice", "https://blocked.example/inbox", "KEY"),
			},
			forbidden: blockedActor,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := actorTrustMockRemote(t, tc.responses)
			server := &Server{cfg: config.Config{LocalDomain: "paon.example"}, db: previewCardCreatorDB(t, "blocked.example")}
			fetched := &fetchedPreviewCard{card: models.PreviewCard{URL: "https://news.example/article"}, creator: tc.creator}
			server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
			if requests.contains(tc.forbidden) {
				t.Fatalf("requests = %v, %s must not be fetched for a disallowed domain", requests.list(), tc.forbidden)
			}
			if fetched.card.AuthorAccountID.Valid {
				t.Fatalf("author account = %#v, want no attribution", fetched.card.AuthorAccountID)
			}
		})
	}

	t.Run("creator on an allowed domain is still resolved", func(t *testing.T) {
		// The actor has no public key, so resolution stops right after the fetch
		// that proves the domain checks let it through.
		requests := actorTrustMockRemote(t, map[string]string{
			actorTrustWebFingerURL("alice@allowed.example"): actorTrustWebFinger("alice@allowed.example", allowedActor),
			allowedActor: actorTrustActor(allowedActor, "alice", "https://allowed.example/inbox", ""),
		})
		server := &Server{cfg: config.Config{LocalDomain: "paon.example"}, db: previewCardCreatorDB(t, "blocked.example")}
		fetched := &fetchedPreviewCard{card: models.PreviewCard{URL: "https://news.example/article"}, creator: "@alice@allowed.example"}
		server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
		if !requests.contains(allowedActor) {
			t.Fatalf("requests = %v, want the allowed actor fetched", requests.list())
		}
	})
}
