package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestNormalizeDomainBlockParamRejectsInvalidDomains(t *testing.T) {
	for _, input := range []string{"", " ", "https://example.com", "bad domain", "name@example.com"} {
		if got, err := normalizeDomainBlockParam(input); err == nil {
			t.Fatalf("normalizeDomainBlockParam(%q) = %q, want error", input, got)
		}
	}
}

func TestAccountDomainBlockDisplayDomainNormalizesLegacyRows(t *testing.T) {
	if got := accountDomainBlockDisplayDomain(" @Remote.Example. "); got != "remote.example" {
		t.Fatalf("display domain = %q", got)
	}
	if got := accountDomainBlockDisplayDomain("BAD DOMAIN "); got != "bad domain" {
		t.Fatalf("fallback display domain = %q", got)
	}
}

func TestDomainBlockRequestValueAcceptsJSON(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/domain_blocks", strings.NewReader(`{"domain":"remote.example"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if got := domainBlockRequestValue(c); got != "remote.example" {
		t.Fatalf("domainBlockRequestValue = %q", got)
	}
}

func TestDomainBlocksUseRailsMaxIDPaginationOnly(t *testing.T) {
	src, err := os.ReadFile("domain_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id")`,
		`query = query.Where("id < ?", maxID)`,
		`if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id")`,
		`query = query.Where("id > ?", sinceID)`,
		`query = query.Order("id DESC")`,
		`limitValue := limit(c, 100, 200)`,
		`out = append(out, accountDomainBlockDisplayDomain(row.Domain))`,
		`limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue)`,
	} {
		if !functionBodyContains(t, src, "domainBlocks", want) {
			t.Fatalf("domain_blocks.go:domainBlocks does not contain %q", want)
		}
	}
	for _, unexpected := range []string{
		`QueryParam("min_id")`,
		`reverseAccountDomainBlocks`,
	} {
		if strings.Contains(string(src), unexpected) {
			t.Fatalf("domain_blocks.go must ignore unsupported Rails domain-blocks param/branch %q", unexpected)
		}
	}
}

func TestDomainBlockMutationsUseRailsParamParsing(t *testing.T) {
	src, err := os.ReadFile("domain_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "createDomainBlock", `rawDomain := domainBlockRequestValue(c)`) ||
		!functionBodyContains(t, src, "createDomainBlock", `normalizeDomainBlockParam(rawDomain)`) {
		t.Fatal("createDomainBlock must read domain through Rails-compatible params parsing before blank/invalid validation")
	}
	if !functionBodyContains(t, src, "deleteDomainBlock", `normalizeDomainBlockParam(domainBlockRequestValue(c))`) {
		t.Fatal("deleteDomainBlock must read domain through Rails-compatible params parsing")
	}
	if functionBodyContains(t, src, "deleteDomainBlock", `normalizeDomainBlockParam(c.QueryParam("domain"))`) {
		t.Fatal("deleteDomainBlock must not be limited to query-string domain params")
	}
}

func TestDomainBlockDeletionUsesCaseInsensitiveExistingRows(t *testing.T) {
	src, err := os.ReadFile("domain_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "deleteDomainBlock", `Where("account_id = ? AND lower(domain) = ?", account.ID, domain)`) {
		t.Fatal("deleteDomainBlock must delete case-insensitive legacy account_domain_blocks rows")
	}
	if functionBodyContains(t, src, "deleteDomainBlock", `Where("account_id = ? AND domain = ?", account.ID, domain)`) {
		t.Fatal("deleteDomainBlock should not rely on case-sensitive domain equality")
	}
}

func TestDomainBlockRejectDeliveriesKeepFollowRequestIDs(t *testing.T) {
	src, err := os.ReadFile("domain_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ID        int64  ` + "`gorm:\"column:id\"`",
		`Select("follow_requests.account_id, follow_requests.id, follow_requests.uri")`,
		`domainBlockRejectDelivery{Remote: remote, FollowID: row.ID, FollowURI: row.URI}`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("domain_blocks.go missing %q", want)
		}
	}
}

func TestAccountDomainBlockCacheRedisKeysMatchRailsCacheNamespaceCandidates(t *testing.T) {
	got := accountDomainBlockCacheRedisKeys(config.Config{RedisNamespace: "mastodon:"}, 42, []string{"Remote.Example"})
	want := []string{
		"exclude_domains_for:42",
		"cache:exclude_domains_for:42",
		"mastodon:exclude_domains_for:42",
		"mastodon_cache:exclude_domains_for:42",
		"exclude_domains/42/remote.example",
		"cache:exclude_domains/42/remote.example",
		"mastodon:exclude_domains/42/remote.example",
		"mastodon_cache:exclude_domains/42/remote.example",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("account domain block cache keys = %#v, want %#v", got, want)
	}
}

func TestDomainBlockCreationClearsRailsFeedCaches(t *testing.T) {
	sources := map[string]string{}
	for _, file := range []string{"domain_blocks.go", "imports.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sources[file] = string(src)
	}
	for _, check := range []struct {
		file string
		fn   string
		want string
	}{
		{"domain_blocks.go", "createDomainBlock", `s.enqueueAfterAccountDomainBlockOrRun(c.Request().Context(), account.ID, domain)`},
		{"domain_blocks.go", "runAfterAccountDomainBlockEffects", `s.clearDomainBlockFeedCaches(ctx, accountID, []string{domain})`},
		{"imports.go", "processDomainBlockImport", `s.clearDomainBlockFeedCaches(context.Background(), bulkImport.AccountID, invalidateDomains)`},
		{"domain_blocks.go", "clearDomainBlockFeedCaches", `s.clearHomeFeedCacheContext(ctx, accountID)`},
		{"domain_blocks.go", "clearDomainBlockFeedCaches", `JOIN list_accounts ON list_accounts.list_id = lists.id`},
		{"domain_blocks.go", "clearDomainBlockFeedCaches", `JOIN accounts ON accounts.id = list_accounts.account_id`},
		{"domain_blocks.go", "clearDomainBlockFeedCaches", `lower(accounts.domain) IN ?`},
		{"domain_blocks.go", "clearDomainBlockFeedCaches", `s.clearListFeedCacheContext(ctx, listID)`},
	} {
		if !functionBodyContains(t, []byte(sources[check.file]), check.fn, check.want) {
			t.Fatalf("%s:%s does not contain %q", check.file, check.fn, check.want)
		}
	}
}

func TestDeliverDomainBlockRejectsSendsSignedRejectForRemoteFollowers(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	previous := activityHTTPClient
	defer func() { activityHTTPClient = previous }()

	var delivered string
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		delivered = string(body)
		if r.URL.String() != "https://remote.example/inbox" {
			t.Fatalf("delivery URL = %s", r.URL.String())
		}
		if r.Header.Get("Signature") == "" || r.Header.Get("Digest") == "" {
			t.Fatalf("missing signed delivery headers: %#v", r.Header)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "local.example", LocalDomain: "local.example"}}
	server.deliverDomainBlockRejects(models.Account{
		Username:   "alice",
		PrivateKey: sql.NullString{String: privatePEM, Valid: true},
	}, []domainBlockRejectDelivery{{
		Remote: models.Account{
			Username: "bob",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
			URI:      "https://remote.example/users/bob",
			InboxURL: "https://remote.example/inbox",
		},
		FollowID:  42,
		FollowURI: "https://remote.example/activities/follow",
	}})

	if !strings.Contains(delivered, `"type":"Reject"`) || !strings.Contains(delivered, `"id":"https://local.example/users/alice#rejects/follows/42"`) || !strings.Contains(delivered, `"id":"https://remote.example/activities/follow"`) {
		t.Fatalf("delivered payload = %s", delivered)
	}
}

func TestDeliverDomainBlockRejectsSkipsWithoutLocalPrivateKey(t *testing.T) {
	previous := activityHTTPClient
	defer func() { activityHTTPClient = previous }()
	called := false
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "local.example", LocalDomain: "local.example"}}
	server.deliverDomainBlockRejects(models.Account{Username: "alice"}, []domainBlockRejectDelivery{{
		Remote: models.Account{Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}, InboxURL: "https://remote.example/inbox"},
	}})
	if called {
		t.Fatal("delivery should not be attempted without a local private key")
	}
}
