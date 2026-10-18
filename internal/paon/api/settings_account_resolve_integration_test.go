//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestSettingsAccountResolveIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "web.paon.example",
		SecretKeyBase: "settings-account-resolve-integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	t.Run("alias stores the remote actor URI for a same-username account", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		requests := settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("misskey.example", "alice@misskey.example"): settingsResolveWebFinger("alice@misskey.example", "https://misskey.example/users/9abcdef"),
			"https://misskey.example/users/9abcdef":                                 settingsResolveActor("https://misskey.example/users/9abcdef", "alice", nil),
		})
		uri, err := server.aliasURIForAcct("alice@misskey.example", &alice)
		if err != nil {
			t.Fatal(err)
		}
		if uri != "https://misskey.example/users/9abcdef" {
			t.Fatalf("alias uri = %q, want the remote actor URI", uri)
		}
		if requests.count() == 0 {
			t.Fatal("unknown alias target must be resolved through WebFinger")
		}
	})

	t.Run("alias uses a fresh cached account without refetching", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		settingsResolveRemoteAccount(t, server.db, "bob", "remote.example", "https://remote.example/actors/bob", time.Now().UTC())
		requests := settingsResolveMockRemote(t, nil)
		uri, err := server.aliasURIForAcct("bob@remote.example", &alice)
		if err != nil {
			t.Fatal(err)
		}
		if uri != "https://remote.example/actors/bob" || requests.count() != 0 {
			t.Fatalf("alias uri = %q requests = %d", uri, requests.count())
		}
	})

	t.Run("alias refreshes a stale cached account", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		stale := settingsResolveRemoteAccount(t, server.db, "carol", "remote.example", "https://remote.example/users/carol-old", time.Now().UTC().Add(-48*time.Hour))
		settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("remote.example", "carol@remote.example"): settingsResolveWebFinger("carol@remote.example", "https://remote.example/users/carol-new"),
			"https://remote.example/users/carol-new":                              settingsResolveActor("https://remote.example/users/carol-new", "carol", nil),
		})
		uri, err := server.aliasURIForAcct("carol@remote.example", &alice)
		if err != nil {
			t.Fatal(err)
		}
		if uri != "https://remote.example/users/carol-new" {
			t.Fatalf("alias uri = %q, want refreshed actor URI", uri)
		}
		var refreshed models.Account
		if err := server.db.First(&refreshed, stale.ID).Error; err != nil {
			t.Fatal(err)
		}
		if refreshed.URI != uri || !refreshed.LastWebfingeredAt.Valid || refreshed.LastWebfingeredAt.Time.Before(time.Now().UTC().Add(-time.Hour)) {
			t.Fatalf("stale account was not refreshed: uri=%q last_webfingered_at=%v", refreshed.URI, refreshed.LastWebfingeredAt)
		}
	})

	t.Run("alias treats a gone stale account as not found and suspends it", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		stale := settingsResolveRemoteAccount(t, server.db, "dave", "remote.example", "https://remote.example/users/dave", time.Now().UTC().Add(-48*time.Hour))
		settingsResolveMockRemoteStatus(t, map[string]int{
			settingsResolveWebFingerURL("remote.example", "dave@remote.example"): http.StatusGone,
		})
		if _, err := server.aliasURIForAcct("dave@remote.example", &alice); err != errAliasNotFound {
			t.Fatalf("error = %v, want not_found", err)
		}
		// ResolveAccountService#queue_deletion! suspends and deletes the account
		// without reserving its username.
		var gone models.Account
		err := server.db.First(&gone, stale.ID).Error
		if err == nil && !gone.SuspendedAt.Valid {
			t.Fatal("gone account must be suspended for deletion")
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal(err)
		}
	})

	t.Run("alias resolves the canonical acct of a split-domain server", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		webfinger := settingsResolveWebFinger("alice@example.com", "https://gts.example.com/users/alice")
		settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("example.com", "alice@example.com"):         webfinger,
			settingsResolveWebFingerURL("gts.example.com", "alice@gts.example.com"): webfinger,
			"https://gts.example.com/users/alice":                                   settingsResolveActor("https://gts.example.com/users/alice", "alice", nil),
		})
		uri, err := server.aliasURIForAcct("alice@example.com", &alice)
		if err != nil {
			t.Fatal(err)
		}
		if uri != "https://gts.example.com/users/alice" {
			t.Fatalf("alias uri = %q", uri)
		}
	})

	t.Run("alias rejects WebFinger pointing at an actor that does not confirm the acct", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("attacker.example", "alice@attacker.example"): settingsResolveWebFinger("alice@attacker.example", "https://victim.example/users/bob"),
			settingsResolveWebFingerURL("victim.example", "bob@victim.example"):       settingsResolveWebFinger("bob@victim.example", "https://victim.example/users/bob"),
			"https://victim.example/users/bob":                                        settingsResolveActor("https://victim.example/users/bob", "bob", nil),
		})
		if _, err := server.aliasURIForAcct("alice@attacker.example", &alice); err != errAliasNotFound {
			t.Fatalf("error = %v, want not_found", err)
		}
	})

	t.Run("alias does not store an actor whose own WebFinger does not loop back", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		victim := settingsResolveRemoteAccount(t, server.db, "admin", "victim.example", "https://victim.example/users/admin", time.Now().UTC())
		forged := strings.Replace(settingsResolveActor("https://victim.example/uploads/forged.json", "admin", nil), `"publicKeyPem":"PEM"`, `"publicKeyPem":"ATTACKER"`, 1)
		settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("evil.example", "x@evil.example"):         settingsResolveWebFinger("x@evil.example", "https://victim.example/uploads/forged.json"),
			settingsResolveWebFingerURL("victim.example", "admin@victim.example"): settingsResolveWebFinger("admin@victim.example", "https://victim.example/users/admin"),
			"https://victim.example/uploads/forged.json":                          forged,
		})
		if _, err := server.aliasURIForAcct("x@evil.example", &alice); err != errAliasNotFound {
			t.Fatalf("error = %v, want not_found", err)
		}
		// FetchRemoteActorService#check_webfinger! fails before ProcessAccountService.
		var reloaded models.Account
		if err := server.db.First(&reloaded, victim.ID).Error; err != nil {
			t.Fatal(err)
		}
		if reloaded.URI != victim.URI || reloaded.PublicKey != victim.PublicKey {
			t.Fatalf("victim account uri=%q public_key=%q, want it untouched", reloaded.URI, reloaded.PublicKey)
		}
	})

	t.Run("alias rejects a remote WebFinger pointing at a local actor without fetching it", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		settingsResolveLocalAccount(t, server.db, "bob")
		for _, actorURL := range []string{"https://web.paon.example/users/bob", "https://paon.example:443/users/bob"} {
			requests := settingsResolveMockRemote(t, map[string]string{
				settingsResolveWebFingerURL("evil.example", "x@evil.example"): settingsResolveWebFinger("x@evil.example", actorURL),
				actorURL: settingsResolveActor(actorURL, "bob", nil),
			})
			if _, err := server.aliasURIForAcct("x@evil.example", &alice); err != errAliasNotFound {
				t.Fatalf("%s: error = %v, want not_found", actorURL, err)
			}
			if requests.count() != 1 {
				t.Fatalf("%s: requests = %d, want only the WebFinger lookup", actorURL, requests.count())
			}
		}
	})

	t.Run("alias resolves local usernames without a domain", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		bob := settingsResolveLocalAccount(t, server.db, "bob")
		settingsResolveMockRemote(t, nil)
		for _, acct := range []string{"bob", "bob@paon.example", "bob@web.paon.example"} {
			uri, err := server.aliasURIForAcct(acct, &alice)
			if err != nil {
				t.Fatalf("%s: %v", acct, err)
			}
			if uri != activityPubActorURL(server, bob) {
				t.Fatalf("%s: alias uri = %q", acct, uri)
			}
		}
		for _, acct := range []string{"alice", "alice@web.paon.example"} {
			if _, err := server.aliasURIForAcct(acct, &alice); err != errAliasMoveToSelf {
				t.Fatalf("%s: error = %v, want move_to_self", acct, err)
			}
		}
		if _, err := server.aliasURIForAcct("nobody", &alice); err != errAliasNotFound {
			t.Fatalf("unknown local username error = %v, want not_found", err)
		}
	})

	t.Run("alias rejects disallowed domains", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		now := time.Now().UTC()
		settingsResolveRemoteAccount(t, server.db, "eve", "blocked.example", "https://blocked.example/users/eve", now)
		if err := server.db.Create(&models.DomainBlock{Domain: "blocked.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		requests := settingsResolveMockRemote(t, nil)
		if _, err := server.aliasURIForAcct("eve@blocked.example", &alice); err != errAliasNotFound {
			t.Fatalf("suspended domain error = %v, want not_found", err)
		}
		settingsResolveRemoteAccount(t, server.db, "frank", "remote.example", "https://remote.example/users/frank", now)
		server.cfg.LimitedFederationMode = true
		if _, err := server.aliasURIForAcct("frank@remote.example", &alice); err != errAliasNotFound {
			t.Fatalf("limited federation error = %v, want not_found", err)
		}
		if requests.count() != 0 {
			t.Fatalf("disallowed domains must not be fetched, requests = %d", requests.count())
		}
	})

	t.Run("migration refetches a cached target like skip_cache", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		cached := settingsResolveRemoteAccount(t, server.db, "alice", "new.example", "https://new.example/users/alice", time.Now().UTC())
		sourceURI := activityPubActorURL(server, alice)
		requests := settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("new.example", "alice@new.example"): settingsResolveWebFinger("alice@new.example", "https://new.example/users/alice"),
			"https://new.example/users/alice":                               settingsResolveActor("https://new.example/users/alice", "alice", []string{sourceURI}),
		})
		target, err := server.migrationTargetAccount("alice@new.example", &alice)
		if err != nil {
			t.Fatal(err)
		}
		if requests.count() == 0 || target.ID != cached.ID || !stringArrayContains(target.AlsoKnownAs, sourceURI) {
			t.Fatalf("target id=%d also_known_as=%v requests=%d, want refetched cached target", target.ID, target.AlsoKnownAs, requests.count())
		}
	})

	t.Run("migration resolves local and split-domain targets and rejects disallowed ones", func(t *testing.T) {
		server, alice := settingsResolveFixture(t, database, cfg)
		bob := settingsResolveLocalAccount(t, server.db, "bob")
		webfinger := settingsResolveWebFinger("alice@example.com", "https://gts.example.com/users/alice")
		settingsResolveMockRemote(t, map[string]string{
			settingsResolveWebFingerURL("example.com", "alice@example.com"):         webfinger,
			settingsResolveWebFingerURL("gts.example.com", "alice@gts.example.com"): webfinger,
			"https://gts.example.com/users/alice":                                   settingsResolveActor("https://gts.example.com/users/alice", "alice", nil),
		})
		for _, acct := range []string{"bob", "bob@web.paon.example"} {
			target, err := server.migrationTargetAccount(acct, &alice)
			if err != nil || target.ID != bob.ID {
				t.Fatalf("%s: target=%v err=%v, want local bob", acct, target, err)
			}
		}
		if _, err := server.migrationTargetAccount("alice@web.paon.example", &alice); err != errMigrationMoveToSelf {
			t.Fatalf("self via WEB_DOMAIN error = %v, want move_to_self", err)
		}
		target, err := server.migrationTargetAccount("alice@example.com", &alice)
		if err != nil || target.URI != "https://gts.example.com/users/alice" {
			t.Fatalf("split-domain target=%v err=%v", target, err)
		}
		now := time.Now().UTC()
		settingsResolveRemoteAccount(t, server.db, "eve", "blocked.example", "https://blocked.example/users/eve", now)
		if err := server.db.Create(&models.DomainBlock{Domain: "blocked.example", Severity: sql.NullInt64{Int64: int64(domainBlockSeverityCode("suspend")), Valid: true}, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := server.migrationTargetAccount("eve@blocked.example", &alice); err != errMigrationNotFound {
			t.Fatalf("suspended domain error = %v, want not_found", err)
		}
	})
}

func settingsResolveFixture(t *testing.T, database *gorm.DB, cfg config.Config) (*Server, models.Account) {
	t.Helper()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	alice := settingsResolveLocalAccount(t, tx, "alice")
	return &Server{cfg: cfg, db: tx}, alice
}

func settingsResolveLocalAccount(t *testing.T, tx *gorm.DB, username string) models.Account {
	t.Helper()
	now := time.Now().UTC()
	account := models.Account{Username: username, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func settingsResolveRemoteAccount(t *testing.T, tx *gorm.DB, username, domain, uri string, lastWebfingeredAt time.Time) models.Account {
	t.Helper()
	now := time.Now().UTC()
	account := models.Account{
		Username: username, Domain: sql.NullString{String: domain, Valid: true}, URI: uri,
		InboxURL: uri + "/inbox", PublicKey: "PEM", Protocol: 1, CreatedAt: now, UpdatedAt: now,
		LastWebfingeredAt: sql.NullTime{Time: lastWebfingeredAt, Valid: true},
	}
	if err := tx.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func settingsResolveWebFingerURL(domain, acct string) string {
	return activityWebFingerURL(domain, "acct:"+acct)
}

func settingsResolveWebFinger(subject, actorURL string) string {
	return `{"subject":"acct:` + subject + `","links":[{"rel":"self","type":"application/activity+json","href":"` + actorURL + `"}]}`
}

func settingsResolveActor(id, username string, alsoKnownAs []string) string {
	aliases := "[]"
	if len(alsoKnownAs) > 0 {
		aliases = `["` + strings.Join(alsoKnownAs, `","`) + `"]`
	}
	return activityTestJSON(`{
		"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1"],
		"id":"` + id + `",
		"type":"Person",
		"preferredUsername":"` + username + `",
		"inbox":"` + id + `/inbox",
		"alsoKnownAs":` + aliases + `,
		"publicKey":{"id":"` + id + `#main-key","owner":"` + id + `","publicKeyPem":"PEM"}
	}`)
}

type settingsResolveRequests struct {
	mu sync.Mutex
	n  int
}

func (r *settingsResolveRequests) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func settingsResolveMockRemote(t *testing.T, responses map[string]string) *settingsResolveRequests {
	t.Helper()
	requests := &settingsResolveRequests{}
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.mu.Lock()
		requests.n++
		requests.mu.Unlock()
		body, ok := responses[r.URL.String()]
		if !ok {
			return textResponse(http.StatusNotFound, "text/plain", "not found"), nil
		}
		contentType := "application/activity+json"
		if strings.Contains(r.URL.Path, "/.well-known/webfinger") {
			contentType = "application/jrd+json"
		}
		return textResponse(http.StatusOK, contentType, body), nil
	})}
	return requests
}

func settingsResolveMockRemoteStatus(t *testing.T, statuses map[string]int) {
	t.Helper()
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		status, ok := statuses[r.URL.String()]
		if !ok {
			status = http.StatusNotFound
		}
		return textResponse(status, "text/plain", ""), nil
	})}
}
