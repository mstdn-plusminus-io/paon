package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// newPreviewCardAuthorFakeDB answers the lookups of
// applyPreviewCardAuthorAttribution on a DryRun connection: the account whose
// domain is bound to the query, and the domain block of a bound domain.
func newPreviewCardAuthorFakeDB(t *testing.T, accounts map[string]models.Account, blocks map[string]models.DomainBlock) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: false,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	err = database.Callback().Query().After("gorm:query").Register("paon:test_preview_card_author_fake", func(db *gorm.DB) {
		if db.Error != nil {
			return
		}
		switch dest := db.Statement.Dest.(type) {
		case *models.Account:
			for _, value := range db.Statement.Vars {
				if domain, ok := value.(string); ok {
					if account, found := accounts[domain]; found {
						*dest = account
						return
					}
				}
			}
			db.AddError(gorm.ErrRecordNotFound)
		case *models.DomainBlock:
			for _, value := range db.Statement.Vars {
				if domain, ok := value.(string); ok {
					if block, found := blocks[domain]; found {
						*dest = block
						return
					}
				}
			}
			db.AddError(gorm.ErrRecordNotFound)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

// FetchLinkCardService resolves fediverse:creator with ResolveAccountService,
// which returns nil for an acct of a domain_not_allowed? domain even when the
// account is already known.
func TestPreviewCardAuthorAttributionSkipsDisallowedDomains(t *testing.T) {
	suspend := models.DomainBlock{ID: 1, Domain: "blocked.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}, CreatedAt: time.Now().UTC()}
	accounts := map[string]models.Account{
		"blocked.example": {ID: 41, Username: "alice", Domain: sql.NullString{String: "blocked.example", Valid: true}, AttributionDomains: models.StringArray{"news.example"}},
		"remote.example":  {ID: 42, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}, AttributionDomains: models.StringArray{"news.example"}},
	}
	for _, test := range []struct {
		name    string
		limited bool
		creator string
		wantID  int64
	}{
		{name: "suspended domain", creator: "@alice@blocked.example"},
		{name: "limited federation without allow", limited: true, creator: "@alice@remote.example"},
		{name: "allowed domain", creator: "@alice@remote.example", wantID: 42},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Server{
				cfg: config.Config{Scheme: "https", LocalDomain: "local.example", WebDomain: "local.example", LimitedFederationMode: test.limited},
				db:  newPreviewCardAuthorFakeDB(t, accounts, map[string]models.DomainBlock{"blocked.example": suspend}),
			}
			fetched := &fetchedPreviewCard{card: models.PreviewCard{URL: "https://news.example/article"}, creator: test.creator}
			s.applyPreviewCardAuthorAttribution(context.Background(), fetched)
			got := int64(0)
			if fetched.card.AuthorAccountID.Valid {
				got = fetched.card.AuthorAccountID.Int64
			}
			if got != test.wantID {
				t.Fatalf("author of %s = %d, want %d", strings.TrimPrefix(test.creator, "@"), got, test.wantID)
			}
		})
	}
}
