package api

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/logger"
)

// linkCardCreatorFixture is the database state seen by the fediverse:creator
// attribution: suspended domain blocks, the DomainAllow list used in limited
// federation mode, and already known accounts.
type linkCardCreatorFixture struct {
	limitedFederation bool
	blocked           []string
	allowed           []string
	accounts          []models.Account
}

// linkCardCreatorTestServer answers the queries of applyPreviewCardAuthorAttribution
// from the fixture and fails the test on any write, so a resolved creator can
// never be stored.
func linkCardCreatorTestServer(t *testing.T, fixture linkCardCreatorFixture) *Server {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=paon dbname=paon sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	contains := func(list []string, vars []any) bool {
		for _, value := range vars {
			text, ok := value.(string)
			if !ok {
				continue
			}
			for _, item := range list {
				if strings.EqualFold(text, item) {
					return true
				}
			}
		}
		return false
	}
	if err := database.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		callbacks.BuildQuerySQL(tx)
		switch dest := tx.Statement.Dest.(type) {
		case *models.DomainBlock:
			if !contains(fixture.blocked, tx.Statement.Vars) {
				tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			*dest = models.DomainBlock{ID: 1, Domain: fmt.Sprint(tx.Statement.Vars[0]), Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}}
			tx.RowsAffected = 1
		case *int64:
			if _, ok := tx.Statement.Model.(*models.DomainAllow); !ok {
				tx.AddError(fmt.Errorf("unexpected count: %s", tx.Statement.SQL.String()))
				return
			}
			// gorm's Count keeps the scanned value only when one row was read.
			*dest = 0
			if contains(fixture.allowed, tx.Statement.Vars) {
				*dest = 1
			}
			tx.RowsAffected = 1
		case *models.Account:
			// lower(username) = ? AND lower(domain) = ? ... LIMIT ?
			if len(tx.Statement.Vars) < 2 || !strings.Contains(tx.Statement.SQL.String(), "lower(domain) =") {
				tx.AddError(fmt.Errorf("unexpected account query: %s", tx.Statement.SQL.String()))
				return
			}
			for _, account := range fixture.accounts {
				if strings.EqualFold(account.Username, fmt.Sprint(tx.Statement.Vars[0])) && strings.EqualFold(account.Domain.String, fmt.Sprint(tx.Statement.Vars[1])) {
					*dest = account
					tx.RowsAffected = 1
					return
				}
			}
			tx.AddError(gorm.ErrRecordNotFound)
		case *models.PreviewCardProvider:
			tx.AddError(gorm.ErrRecordNotFound)
		default:
			tx.AddError(fmt.Errorf("unexpected query: %s", tx.Statement.SQL.String()))
		}
	}); err != nil {
		t.Fatal(err)
	}
	forbid := func(tx *gorm.DB) {
		t.Errorf("unexpected database write: %s", tx.Statement.SQL.String())
		_ = tx.AddError(fmt.Errorf("database writes are forbidden in this test"))
	}
	for name, register := range map[string]func(string, func(*gorm.DB)) error{
		"create": database.Callback().Create().Register,
		"update": database.Callback().Update().Register,
		"delete": database.Callback().Delete().Register,
		"raw":    database.Callback().Raw().Register,
		"row":    database.Callback().Row().Register,
	} {
		if err := register("paon_test:forbid_"+name, forbid); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{db: database, cfg: config.Config{Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example", LimitedFederationMode: fixture.limitedFederation}}
}

func linkCardCreatorStubLookup(t *testing.T) {
	t.Helper()
	previous := activityLookupIP
	t.Cleanup(func() { activityLookupIP = previous })
	activityLookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.114.10")}, nil
	}
}

func linkCardCreatorCard(creator string) *fetchedPreviewCard {
	return &fetchedPreviewCard{card: models.PreviewCard{URL: "https://news.example/article"}, creator: creator}
}

func linkCardCreatorRemoteAccount(id int64, username string, domain string) models.Account {
	return models.Account{
		ID:                 id,
		Username:           username,
		Domain:             sql.NullString{String: domain, Valid: true},
		URI:                "https://" + domain + "/users/" + username,
		AttributionDomains: models.StringArray{"news.example"},
	}
}

func assertLinkCardCreatorNotRequested(t *testing.T, requests *actorTrustRequests, hosts ...string) {
	t.Helper()
	for _, requested := range requests.list() {
		for _, host := range hosts {
			if strings.Contains(requested, "://"+host+"/") {
				t.Fatalf("requested %s, want no request to %s", requested, host)
			}
		}
	}
}

func assertLinkCardCreatorNotAttributed(t *testing.T, fetched *fetchedPreviewCard) {
	t.Helper()
	if fetched.card.AuthorAccountID.Valid {
		t.Fatalf("author_account_id = %d, want no attribution", fetched.card.AuthorAccountID.Int64)
	}
}

// ResolveAccountService#call returns nil when domain_not_allowed? holds for the
// creator's domain, so the account is neither fetched nor attributed.
func TestPreviewCardCreatorOnSuspendedDomainIsNeitherFetchedNorAttributed(t *testing.T) {
	linkCardCreatorStubLookup(t)
	requests := actorTrustMockRemote(t, map[string]string{
		actorTrustWebFingerURL("mallory@blocked.example"): actorTrustWebFinger("mallory@blocked.example", "https://blocked.example/users/mallory"),
		"https://blocked.example/users/mallory":           actorTrustActor("https://blocked.example/users/mallory", "mallory", "https://blocked.example/inbox", "PEM"),
	})
	server := linkCardCreatorTestServer(t, linkCardCreatorFixture{blocked: []string{"blocked.example"}})
	fetched := linkCardCreatorCard("@mallory@blocked.example")
	server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
	assertLinkCardCreatorNotRequested(t, requests, "blocked.example")
	assertLinkCardCreatorNotAttributed(t, fetched)
}

func TestPreviewCardCreatorKnownOnSuspendedDomainIsNotAttributed(t *testing.T) {
	linkCardCreatorStubLookup(t)
	requests := actorTrustMockRemote(t, map[string]string{})
	server := linkCardCreatorTestServer(t, linkCardCreatorFixture{
		blocked:  []string{"blocked.example"},
		accounts: []models.Account{linkCardCreatorRemoteAccount(42, "mallory", "blocked.example")},
	})
	fetched := linkCardCreatorCard("@mallory@blocked.example")
	server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
	assertLinkCardCreatorNotRequested(t, requests, "blocked.example")
	assertLinkCardCreatorNotAttributed(t, fetched)
}

func TestPreviewCardCreatorOutsideLimitedFederationAllowListIsNotAttributed(t *testing.T) {
	linkCardCreatorStubLookup(t)
	requests := actorTrustMockRemote(t, map[string]string{
		actorTrustWebFingerURL("eve@other.example"): actorTrustWebFinger("eve@other.example", "https://other.example/users/eve"),
		"https://other.example/users/eve":           actorTrustActor("https://other.example/users/eve", "eve", "https://other.example/inbox", "PEM"),
	})
	server := linkCardCreatorTestServer(t, linkCardCreatorFixture{
		limitedFederation: true,
		allowed:           []string{"social.example"},
		accounts:          []models.Account{linkCardCreatorRemoteAccount(43, "alice", "social.example"), linkCardCreatorRemoteAccount(44, "bob", "other.example")},
	})
	for _, creator := range []string{"@eve@other.example", "@bob@other.example"} {
		fetched := linkCardCreatorCard(creator)
		server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
		assertLinkCardCreatorNotRequested(t, requests, "other.example")
		assertLinkCardCreatorNotAttributed(t, fetched)
	}
	allowed := linkCardCreatorCard("@alice@social.example")
	server.applyPreviewCardAuthorAttribution(context.Background(), allowed)
	if !allowed.card.AuthorAccountID.Valid || allowed.card.AuthorAccountID.Int64 != 43 {
		t.Fatalf("author_account_id = %#v, want the allowed creator 43", allowed.card.AuthorAccountID)
	}
}

// ResolveAccountService checks domain_not_allowed? again for the acct that
// WebFinger confirms, and FetchRemoteActorService checks it for the actor URI.
func TestPreviewCardCreatorResolvedOntoSuspendedDomainIsNotFetched(t *testing.T) {
	linkCardCreatorStubLookup(t)
	const redirectedActor = "https://elsewhere.example/users/mallory"
	const blockedActor = "https://blocked.example/users/x"
	requests := actorTrustMockRemote(t, map[string]string{
		// x@allowed.example redirects to mallory@blocked.example.
		actorTrustWebFingerURL("x@allowed.example"):       actorTrustWebFinger("mallory@blocked.example", redirectedActor),
		actorTrustWebFingerURL("mallory@blocked.example"): actorTrustWebFinger("mallory@blocked.example", redirectedActor),
		redirectedActor: actorTrustActor(redirectedActor, "mallory", "https://elsewhere.example/inbox", "PEM"),
		// y@allowed.example names an actor URI on the suspended domain.
		actorTrustWebFingerURL("y@allowed.example"): actorTrustWebFinger("y@allowed.example", blockedActor),
		blockedActor: actorTrustActor(blockedActor, "y", "https://blocked.example/inbox", "PEM"),
	})
	server := linkCardCreatorTestServer(t, linkCardCreatorFixture{blocked: []string{"blocked.example"}})

	redirected := linkCardCreatorCard("@x@allowed.example")
	server.applyPreviewCardAuthorAttribution(context.Background(), redirected)
	if requests.contains(redirectedActor) {
		t.Fatalf("fetched %s for an acct confirmed on a suspended domain", redirectedActor)
	}
	assertLinkCardCreatorNotAttributed(t, redirected)

	blocked := linkCardCreatorCard("@y@allowed.example")
	server.applyPreviewCardAuthorAttribution(context.Background(), blocked)
	if requests.contains(blockedActor) {
		t.Fatalf("fetched %s, an actor URI on a suspended domain", blockedActor)
	}
	assertLinkCardCreatorNotAttributed(t, blocked)
}

func TestPreviewCardCreatorKnownOnAllowedDomainIsStillAttributed(t *testing.T) {
	linkCardCreatorStubLookup(t)
	actorTrustMockRemote(t, map[string]string{})
	server := linkCardCreatorTestServer(t, linkCardCreatorFixture{
		blocked:  []string{"blocked.example"},
		accounts: []models.Account{linkCardCreatorRemoteAccount(45, "alice", "social.example")},
	})
	fetched := linkCardCreatorCard("@alice@social.example")
	server.applyPreviewCardAuthorAttribution(context.Background(), fetched)
	if !fetched.card.AuthorAccountID.Valid || fetched.card.AuthorAccountID.Int64 != 45 {
		t.Fatalf("author_account_id = %#v, want 45", fetched.card.AuthorAccountID)
	}
}
