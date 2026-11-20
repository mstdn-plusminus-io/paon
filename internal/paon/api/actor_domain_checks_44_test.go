package api

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

// actorDomainTestDB is a dry-run database for the 4.4 actor lookups. Account
// lookups return known (or record not found when known is nil), and a
// domain_blocks lookup returns a suspension when one of its candidate domains
// is listed in suspendedDomains. A domain_allows count is always zero, so every
// domain is outside the LIMITED_FEDERATION_MODE allowlist.
func actorDomainTestDB(t *testing.T, known *models.Account, suspendedDomains ...string) *gorm.DB {
	t.Helper()
	database := actorTrustDryRunDB(t)
	err := database.Callback().Query().After("gorm:query").Register("paon_test:actor_domain_rows", func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "accounts":
			if known == nil {
				_ = tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			if dest, ok := tx.Statement.Dest.(*models.Account); ok {
				*dest = *known
			}
		case "domain_blocks":
			dest, ok := tx.Statement.Dest.(*models.DomainBlock)
			if !ok {
				return
			}
			// GORM binds each candidate of "lower(domain) IN ?" as its own variable.
			for _, value := range tx.Statement.Vars {
				if variant, ok := value.(string); ok && slices.Contains(suspendedDomains, variant) {
					dest.Domain = variant
					dest.Severity = models.DomainBlockSeverity(domainBlockSeverityCode("suspend"))
					return
				}
			}
			_ = tx.AddError(gorm.ErrRecordNotFound)
		case "preview_card_providers":
			_ = tx.AddError(gorm.ErrRecordNotFound)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

// Fasp::AccountSearchWorker and Fasp::FollowRecommendationWorker resolve
// provider-supplied URIs with ActivityPub::FetchRemoteActorService, which
// returns nil for a domain that is not allowed, before fetching anything.
func TestFASPResolveAccountURIChecksDomainBeforeFetching(t *testing.T) {
	const actorURI = "https://remote.example/users/alice"
	responses := map[string]string{
		actorURI: actorTrustActor(actorURI, "alice", "https://remote.example/inbox", "PEM"),
		actorTrustWebFingerURL("alice@remote.example"): actorTrustWebFinger("alice@remote.example", actorURI),
	}

	for _, tc := range []struct {
		name     string
		cfg      config.Config
		suspend  []string
		disallow bool
	}{
		{name: "suspended domain", cfg: config.Config{LocalDomain: "paon.example"}, suspend: []string{"remote.example"}, disallow: true},
		{name: "suspended parent domain", cfg: config.Config{LocalDomain: "paon.example"}, suspend: []string{"example"}, disallow: true},
		{name: "outside the limited federation allowlist", cfg: config.Config{LocalDomain: "paon.example", LimitedFederationMode: true}, disallow: true},
		{name: "allowed domain", cfg: config.Config{LocalDomain: "paon.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := actorTrustMockRemote(t, responses)
			server := &Server{cfg: tc.cfg, db: actorDomainTestDB(t, nil, tc.suspend...)}
			account, created, err := server.faspResolveAccountURI(actorURI)
			if !tc.disallow {
				if !requests.contains(actorURI) {
					t.Fatalf("requests = %v, want the allowed actor fetched", requests.list())
				}
				return
			}
			if err != nil || account != nil || created {
				t.Fatalf("faspResolveAccountURI = %v, %v, %v, want an unresolved account", account, created, err)
			}
			if got := requests.list(); len(got) != 0 {
				t.Fatalf("requests = %v, disallowed domains must not be fetched", got)
			}
		})
	}
}

func previewCardAttributionFixture(creator string) *fetchedPreviewCard {
	return &fetchedPreviewCard{
		card:    models.PreviewCard{URL: "https://news.example/articles/1"},
		creator: creator,
	}
}

// FetchLinkCardService resolves fediverse:creator with ResolveAccountService,
// which returns nil for an account on a domain that is not allowed, whether
// the account is already known or not.
func TestPreviewCardAuthorAttributionChecksCreatorDomain(t *testing.T) {
	blockedAuthor := models.Account{
		ID:                 42,
		Username:           "alice",
		Domain:             sql.NullString{String: "blocked.example", Valid: true},
		URI:                "https://blocked.example/users/alice",
		AttributionDomains: models.StringArray{"news.example"},
	}

	t.Run("known account on a suspended domain", func(t *testing.T) {
		requests := actorTrustMockRemote(t, map[string]string{})
		server := &Server{cfg: config.Config{LocalDomain: "paon.example"}, db: actorDomainTestDB(t, &blockedAuthor, "blocked.example")}
		fetched := previewCardAttributionFixture("@alice@blocked.example")
		server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
		if fetched.card.AuthorAccountID.Valid {
			t.Fatalf("author_account_id = %d, want no attribution to a suspended domain", fetched.card.AuthorAccountID.Int64)
		}
		if got := requests.list(); len(got) != 0 {
			t.Fatalf("requests = %v, want none", got)
		}
	})

	t.Run("known account outside the limited federation allowlist", func(t *testing.T) {
		server := &Server{cfg: config.Config{LocalDomain: "paon.example", LimitedFederationMode: true}, db: actorDomainTestDB(t, &blockedAuthor)}
		fetched := previewCardAttributionFixture("@alice@blocked.example")
		server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
		if fetched.card.AuthorAccountID.Valid {
			t.Fatalf("author_account_id = %d, want no attribution outside the allowlist", fetched.card.AuthorAccountID.Int64)
		}
	})

	t.Run("known account on an allowed domain", func(t *testing.T) {
		allowedAuthor := blockedAuthor
		allowedAuthor.Domain = sql.NullString{String: "remote.example", Valid: true}
		server := &Server{cfg: config.Config{LocalDomain: "paon.example"}, db: actorDomainTestDB(t, &allowedAuthor, "blocked.example")}
		fetched := previewCardAttributionFixture("@alice@remote.example")
		server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
		if !fetched.card.AuthorAccountID.Valid || fetched.card.AuthorAccountID.Int64 != allowedAuthor.ID {
			t.Fatalf("author_account_id = %v, want %d", fetched.card.AuthorAccountID, allowedAuthor.ID)
		}
	})

	t.Run("unknown account outside the limited federation allowlist", func(t *testing.T) {
		requests := actorTrustMockRemote(t, map[string]string{
			actorTrustWebFingerURL("alice@remote.example"): actorTrustWebFinger("alice@remote.example", "https://remote.example/users/alice"),
		})
		server := &Server{cfg: config.Config{LocalDomain: "paon.example", LimitedFederationMode: true}, db: actorDomainTestDB(t, nil)}
		fetched := previewCardAttributionFixture("@alice@remote.example")
		server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
		if fetched.card.AuthorAccountID.Valid {
			t.Fatalf("author_account_id = %d, want no attribution outside the allowlist", fetched.card.AuthorAccountID.Int64)
		}
		if got := requests.list(); len(got) != 0 {
			t.Fatalf("requests = %v, disallowed domains must not be resolved", got)
		}
	})

	const blockedActorURL = "https://blocked.example/users/alice"
	for _, tc := range []struct {
		name      string
		responses map[string]string
	}{
		{
			name: "WebFinger redirects to a suspended domain",
			responses: map[string]string{
				actorTrustWebFingerURL("alice@remote.example"):  actorTrustWebFinger("alice@blocked.example", blockedActorURL),
				actorTrustWebFingerURL("alice@blocked.example"): actorTrustWebFinger("alice@blocked.example", blockedActorURL),
				blockedActorURL: actorTrustActor(blockedActorURL, "alice", "https://blocked.example/inbox", "PEM"),
			},
		},
		{
			name: "self link on a suspended domain",
			responses: map[string]string{
				actorTrustWebFingerURL("alice@remote.example"): actorTrustWebFinger("alice@remote.example", blockedActorURL),
				blockedActorURL: actorTrustActor(blockedActorURL, "alice", "https://blocked.example/inbox", "PEM"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := actorTrustMockRemote(t, tc.responses)
			server := &Server{cfg: config.Config{LocalDomain: "paon.example"}, db: actorDomainTestDB(t, nil, "blocked.example")}
			fetched := previewCardAttributionFixture("@alice@remote.example")
			server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
			if fetched.card.AuthorAccountID.Valid {
				t.Fatalf("author_account_id = %d, want no attribution", fetched.card.AuthorAccountID.Int64)
			}
			if !requests.contains(actorTrustWebFingerURL("alice@remote.example")) {
				t.Fatalf("requests = %v, want the creator WebFinger lookup", requests.list())
			}
			if requests.contains(blockedActorURL) {
				t.Fatalf("requests = %v, an actor on a suspended domain must not be fetched", requests.list())
			}
		})
	}
}
