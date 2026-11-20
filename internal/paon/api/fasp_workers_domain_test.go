package api

import (
	"database/sql"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// newFASPResolveFakeDB answers the lookups faspResolveAccountURI makes on a
// DryRun connection: no account is known, and a domain block is returned when
// one of the bound domain variants is in blocks.
func newFASPResolveFakeDB(t *testing.T, blocks map[string]models.DomainBlock) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: false,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	err = database.Callback().Query().After("gorm:query").Register("paon:test_fasp_resolve_fake", func(db *gorm.DB) {
		if db.Error != nil {
			return
		}
		switch dest := db.Statement.Dest.(type) {
		case *models.Account:
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

// Fasp::AccountSearchWorker and Fasp::FollowRecommendationWorker resolve the
// URIs a provider returns with FetchRemoteActorService#call, which returns
// before fetching anything when domain_not_allowed?(uri).
func TestFASPResolveAccountURISkipsDisallowedDomains(t *testing.T) {
	previousClient := activityHTTPClient
	previousLookup := activityLookupIP
	t.Cleanup(func() {
		activityHTTPClient = previousClient
		activityLookupIP = previousLookup
	})
	requests := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	activityLookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	suspend := models.DomainBlock{ID: 1, Domain: "blocked.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}, CreatedAt: time.Now().UTC()}
	silence := models.DomainBlock{ID: 2, Domain: "silenced.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("silence")), Valid: true}, CreatedAt: time.Now().UTC()}

	for _, test := range []struct {
		name      string
		limited   bool
		uri       string
		wantFetch bool
	}{
		{name: "suspended domain", uri: "https://blocked.example/users/alice"},
		{name: "suspended parent domain", uri: "https://social.blocked.example/users/alice"},
		{name: "limited federation without allow", limited: true, uri: "https://remote.example/users/alice"},
		{name: "silenced domain is still fetched", uri: "https://silenced.example/users/alice", wantFetch: true},
		{name: "unblocked domain is fetched", uri: "https://remote.example/users/alice", wantFetch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests = requests[:0]
			s := &Server{
				cfg: config.Config{Scheme: "https", LocalDomain: "local.example", WebDomain: "local.example", LimitedFederationMode: test.limited},
				db:  newFASPResolveFakeDB(t, map[string]models.DomainBlock{"blocked.example": suspend, "silenced.example": silence}),
			}
			account, created, err := s.faspResolveAccountURI(test.uri)
			if err != nil || account != nil || created {
				t.Fatalf("faspResolveAccountURI(%q) = %+v, %v, %v; want no account", test.uri, account, created, err)
			}
			if fetched := len(requests) > 0; fetched != test.wantFetch {
				t.Fatalf("faspResolveAccountURI(%q) fetched = %v (%v), want %v", test.uri, fetched, requests, test.wantFetch)
			}
		})
	}
}
