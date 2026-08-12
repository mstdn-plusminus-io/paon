//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRedisRetryLeaseAndOwnerFencingAgainstRedis(t *testing.T) {
	redisURL := os.Getenv("PAON_TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("PAON_TEST_REDIS_URL is required for integration tests")
	}
	server := &Server{cfg: config.Config{RedisURL: redisURL}}
	ctx := context.Background()
	base := "paon:integration:lease:" + randomHex(8)
	now := time.Now().UTC()
	if _, err := server.redisCommand(ctx, "ZADD", base, strconv.FormatInt(now.Unix(), 10), "job-1"); err != nil {
		t.Fatal(err)
	}
	claims, err := server.claimRedisRetryJobs(ctx, base, 10, now)
	if err != nil || len(claims) != 1 || claims[0].Member != "job-1" {
		t.Fatalf("claims = %#v, %v", claims, err)
	}
	wrongOwner := claims[0]
	wrongOwner.Owner = "not-the-owner"
	if err := server.acknowledgeRedisRetryJob(ctx, base, wrongOwner); err == nil {
		t.Fatal("wrong owner acknowledged a leased job")
	}
	if err := server.replaceRedisRetryJob(ctx, base, claims[0], "job-2", now); err != nil {
		t.Fatal(err)
	}
	claims, err = server.claimRedisRetryJobs(ctx, base, 10, now)
	if err != nil || len(claims) != 1 || claims[0].Member != "job-2" {
		t.Fatalf("successor claims = %#v, %v", claims, err)
	}
	if err := server.acknowledgeRedisRetryJob(ctx, base, claims[0]); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateAndMissingStatusResponsesAreIndistinguishable(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		LocalDomain:          "example.com",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	var accountID int64
	if err := database.Raw(`
		INSERT INTO accounts (username, created_at, updated_at)
		VALUES ('private-author', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING id
	`).Scan(&accountID).Error; err != nil {
		t.Fatal(err)
	}
	var privateStatusID int64
	if err := database.Raw(`
		INSERT INTO statuses (account_id, text, visibility, local, created_at, updated_at)
		VALUES (?, 'private', 2, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING id
	`, accountID).Scan(&privateStatusID).Error; err != nil {
		t.Fatal(err)
	}
	var privateStatus models.Status
	if err := database.First(&privateStatus, privateStatusID).Error; err != nil {
		t.Fatalf("load seeded private status: %v", err)
	}
	if privateStatus.Visibility != 2 {
		t.Fatalf("seeded status visibility = %d, want private (2)", privateStatus.Visibility)
	}

	server, err := NewServer(cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	requestStatus := func(id int64) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/statuses/"+strconv.FormatInt(id, 10), nil)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Language", "ja")
		rec := httptest.NewRecorder()
		server.echo.ServeHTTP(rec, req)
		return rec
	}

	privateResponse := requestStatus(privateStatusID)
	missingResponse := requestStatus(privateStatusID + 1)
	if privateResponse.Code != http.StatusNotFound || missingResponse.Code != http.StatusNotFound {
		t.Fatalf("status codes = private %d, missing %d; want both 404", privateResponse.Code, missingResponse.Code)
	}
	if privateResponse.Header().Get("Content-Type") != missingResponse.Header().Get("Content-Type") {
		t.Fatalf("content types differ: private %q, missing %q", privateResponse.Header().Get("Content-Type"), missingResponse.Header().Get("Content-Type"))
	}
	if privateResponse.Body.String() != missingResponse.Body.String() {
		t.Fatalf("response bodies differ: private %q, missing %q", privateResponse.Body.String(), missingResponse.Body.String())
	}
}

func TestEnsureRepresentativeAccountStatConcurrentAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 16,
		DatabaseMaxIdleConns: 4,
		LocalDomain:          "example.test",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}
	var representative models.Account
	if err := database.Where("id = ?", int64(-99)).First(&representative).Error; err != nil {
		t.Fatalf("load seeded representative account: %v", err)
	}
	if representative.Username != instanceActorUsername {
		t.Fatalf("seeded representative username = %q, want %q", representative.Username, instanceActorUsername)
	}
	var initialCount int64
	if err := database.Model(&models.AccountStat{}).Where("account_id = ?", representative.ID).Count(&initialCount).Error; err != nil {
		t.Fatal(err)
	}
	if initialCount != 0 {
		t.Fatalf("initial representative account_stats rows = %d, want 0", initialCount)
	}

	server := &Server{cfg: cfg, db: database}
	const workers = 12
	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errorsByWorker <- server.ensureRepresentativeAccountStat(representative.ID)
		}()
	}
	close(start)
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent ensureRepresentativeAccountStat: %v", err)
		}
	}

	var count int64
	if err := database.Model(&models.AccountStat{}).Where("account_id = ?", representative.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("representative account_stats rows = %d, want 1", count)
	}
}

func TestCreateActivityPubAccountStatConcurrentAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 16,
		DatabaseMaxIdleConns: 4,
		LocalDomain:          "example.test",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}
	now := time.Now().UTC()
	var accountID int64
	if err := database.Raw(`
		INSERT INTO accounts (username, domain, uri, protocol, created_at, updated_at)
		VALUES ('remote', 'remote.example', 'https://remote.example/users/remote', 1, ?, ?)
		RETURNING id
	`, now, now).Scan(&accountID).Error; err != nil {
		t.Fatal(err)
	}

	const workers = 12
	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errorsByWorker <- createActivityPubAccountStatIfMissing(database, models.AccountStat{
				AccountID: accountID,
				CreatedAt: now,
				UpdatedAt: now,
			})
		}()
	}
	close(start)
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent ActivityPub account stat creation: %v", err)
		}
	}

	var count int64
	if err := database.Model(&models.AccountStat{}).Where("account_id = ?", accountID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("ActivityPub account_stats rows = %d, want 1", count)
	}
}

func TestRemoteActivityPubActorDeletionCommitsAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	redisURL := os.Getenv("PAON_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL and PAON_TEST_REDIS_URL are required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		RedisURL:             redisURL,
		LocalDomain:          "example.test",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	var actor models.Account
	if err := database.Raw(`
		INSERT INTO accounts (username, domain, uri, protocol, created_at, updated_at)
		VALUES ('remote-delete', 'remote.example', 'https://remote.example/users/remote-delete', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING *
	`).Scan(&actor).Error; err != nil {
		t.Fatal(err)
	}
	var peer models.Account
	if err := database.Raw(`
		INSERT INTO accounts (username, domain, uri, protocol, created_at, updated_at)
		VALUES ('remote-peer', 'peer.example', 'https://peer.example/users/remote-peer', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING *
	`).Scan(&peer).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`
		INSERT INTO account_stats (account_id, statuses_count, following_count, followers_count, created_at, updated_at)
		VALUES (?, 0, 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		       (?, 2, 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, peer.ID, actor.ID).Error; err != nil {
		t.Fatal(err)
	}
	var parentStatusID int64
	if err := database.Raw(`
		INSERT INTO statuses (account_id, uri, text, visibility, local, created_at, updated_at)
		VALUES (?, 'https://peer.example/users/remote-peer/statuses/1', 'parent', 0, false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING id
	`, peer.ID).Scan(&parentStatusID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`
		INSERT INTO status_stats (status_id, replies_count, reblogs_count, favourites_count, created_at, updated_at)
		VALUES (?, 1, 1, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, parentStatusID).Error; err != nil {
		t.Fatal(err)
	}
	var statusID int64
	if err := database.Raw(`
		INSERT INTO statuses (account_id, uri, text, visibility, local, in_reply_to_id, reply, created_at, updated_at)
		VALUES (?, 'https://remote.example/users/remote-delete/statuses/1', 'delete me', 0, false, ?, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING id
	`, actor.ID, parentStatusID).Scan(&statusID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`
		INSERT INTO statuses (account_id, uri, text, visibility, local, reblog_of_id, created_at, updated_at)
		VALUES (?, 'https://remote.example/users/remote-delete/statuses/2', '', 0, false, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, actor.ID, parentStatusID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`
		INSERT INTO follows (account_id, target_account_id, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		       (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, peer.ID, actor.ID, actor.ID, peer.ID).Error; err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: cfg, db: database}
	if err := server.deleteRemoteActivityPubActorNow(context.Background(), &actor, time.Now().UTC()); err != nil {
		t.Fatalf("deleteRemoteActivityPubActorNow error = %v", err)
	}
	var accountCount int64
	if err := database.Model(&models.Account{}).Where("id = ?", actor.ID).Count(&accountCount).Error; err != nil {
		t.Fatal(err)
	}
	if accountCount != 0 {
		t.Fatalf("remote actor remains after Delete: count = %d", accountCount)
	}
	var statusCount int64
	if err := database.Model(&models.Status{}).Where("id = ?", statusID).Count(&statusCount).Error; err != nil {
		t.Fatal(err)
	}
	if statusCount != 0 {
		t.Fatalf("remote actor status remains after Delete: count = %d", statusCount)
	}
	var peerCounts struct {
		Following int64
		Followers int64
	}
	if err := database.Raw(`
		SELECT following_count AS following, followers_count AS followers
		FROM account_stats
		WHERE account_id = ?
	`, peer.ID).Scan(&peerCounts).Error; err != nil {
		t.Fatal(err)
	}
	if peerCounts.Following != 0 || peerCounts.Followers != 0 {
		t.Fatalf("peer relationship counters after remote Delete = %#v", peerCounts)
	}
	var parentCounts struct {
		Replies int64
		Reblogs int64
	}
	if err := database.Raw(`
		SELECT replies_count AS replies, reblogs_count AS reblogs
		FROM status_stats
		WHERE status_id = ?
	`, parentStatusID).Scan(&parentCounts).Error; err != nil {
		t.Fatal(err)
	}
	if parentCounts.Replies != 0 || parentCounts.Reblogs != 0 {
		t.Fatalf("parent status counters after remote Delete = %#v", parentCounts)
	}
}

func TestOperationalAccountAndSettingsCommandsAgainstRailsSchema(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	redisURL := os.Getenv("PAON_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL and PAON_TEST_REDIS_URL are required for integration tests")
	}
	cfg := config.Config{DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2, RedisURL: redisURL, SidekiqRedisURL: redisURL, LocalDomain: "example.test", SecretKeyBase: "integration-secret"}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}
	operations := NewOperations(cfg, database)
	defer operations.Close()
	user, err := operations.CreateAccount(context.Background(), OperationAccountCreate{
		Username: "operator", Email: "operator@example.test", Password: "correct-horse-battery", Role: "Owner", Confirmed: true, Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.Account == nil || user.Account.Username != "operator" || !user.ConfirmedAt.Valid || !user.Approved || !user.RoleID.Valid {
		t.Fatalf("created user = %#v", user)
	}
	if err := database.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"otp_required_for_login": true,
		"otp_secret":             "integration-otp-secret",
		"otp_backup_codes":       models.StringArray{"backup-code"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.WebauthnCredential{
		ExternalID: "integration-credential",
		PublicKey:  "integration-public-key",
		Nickname:   "integration-key",
		UserID:     models.WebauthnCredentialUserID(user.ID),
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	modified, err := operations.ModifyAccount(context.Background(), "operator", OperationAccountModify{Disable: true, Disable2FA: true, ResetPassword: true})
	if err != nil {
		t.Fatal(err)
	}
	if !modified.User.Disabled || modified.GeneratedPassword == "" {
		t.Fatalf("modified user = %#v", modified)
	}
	if modified.User.OTPRequiredForLogin || modified.User.OTPSecret.Valid || len(modified.User.OTPBackupCodes) != 0 {
		t.Fatalf("two-factor fields were not cleared: %#v", modified.User)
	}
	var webauthnCredentialCount int64
	if err := database.Model(&models.WebauthnCredential{}).Where("user_id = ?", user.ID).Count(&webauthnCredentialCount).Error; err != nil || webauthnCredentialCount != 0 {
		t.Fatalf("webauthn credentials after --disable-2fa = %d, %v", webauthnCredentialCount, err)
	}
	requireReason := true
	if err := operations.SetRegistrationsMode(context.Background(), "approved", &requireReason); err != nil {
		t.Fatal(err)
	}
	var settingsCount int64
	if err := database.Raw(`SELECT COUNT(*) FROM settings WHERE var IN ('registrations_mode', 'require_invite_text')`).Scan(&settingsCount).Error; err != nil || settingsCount != 2 {
		t.Fatalf("registration settings count = %d, %v", settingsCount, err)
	}
	if added, err := operations.AddEmailDomainBlocks(context.Background(), []string{"blocked.example", "other.example"}); err != nil || added != 2 {
		t.Fatalf("add email domain blocks = %d, %v", added, err)
	}
	if domains, err := operations.ListEmailDomainBlocks(context.Background()); err != nil || len(domains) != 2 || domains[0] != "blocked.example" {
		t.Fatalf("email domain blocks = %#v, %v", domains, err)
	}
	if removed, err := operations.RemoveEmailDomainBlocks(context.Background(), []string{"other.example"}); err != nil || removed != 1 {
		t.Fatalf("remove email domain blocks = %d, %v", removed, err)
	}
	if added, err := operations.AddIPBlocks(context.Background(), []OperationIPBlock{{CIDR: "192.0.2.9/24", Severity: "no_access", Comment: "integration"}}, false); err != nil || added != 1 {
		t.Fatalf("add IP block = %d, %v", added, err)
	}
	if rows, err := operations.ListIPBlocks(context.Background()); err != nil || len(rows) != 1 || rows[0].IP != "192.0.2.0/24" {
		t.Fatalf("IP blocks = %#v, %v", rows, err)
	}
	if removed, err := operations.RemoveIPBlocks(context.Background(), []string{"192.0.2.1/24"}); err != nil || removed != 1 {
		t.Fatalf("remove IP blocks = %d, %v", removed, err)
	}
	hash, _ := operationCanonicalEmailHash("u.ser+tag@example.test")
	if err := database.Exec(`INSERT INTO canonical_email_blocks (canonical_email_hash, created_at, updated_at) VALUES (?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, hash).Error; err != nil {
		t.Fatal(err)
	}
	if found, err := operations.CanonicalEmailBlockExists(context.Background(), "user@example.test"); err != nil || !found {
		t.Fatalf("canonical block exists = %v, %v", found, err)
	}
	if removed, err := operations.RemoveCanonicalEmailBlock(context.Background(), "user@example.test"); err != nil || !removed {
		t.Fatalf("remove canonical block = %v, %v", removed, err)
	}
	var remoteID int64
	if err := database.Raw(`INSERT INTO accounts (username, domain, uri, created_at, updated_at) VALUES ('remote', 'remote.example', 'https://remote.example/users/remote', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id`).Scan(&remoteID).Error; err != nil {
		t.Fatal(err)
	}
	var originalID int64
	if err := database.Raw(`INSERT INTO statuses (account_id, text, visibility, local, created_at, updated_at) VALUES (?, 'public', 0, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id`, user.AccountID).Scan(&originalID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO statuses (account_id, text, visibility, local, created_at, updated_at) VALUES (?, 'direct', 3, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, user.AccountID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO statuses (account_id, text, visibility, in_reply_to_id, reply, created_at, updated_at) VALUES (?, 'reply', 0, ?, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, remoteID, originalID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO statuses (account_id, text, visibility, reblog_of_id, created_at, updated_at) VALUES (?, '', 0, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, remoteID, originalID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO favourites (account_id, status_id, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, remoteID, originalID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO follows (account_id, target_account_id, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, user.AccountID, remoteID).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := operations.RecountCache(context.Background(), "accounts"); err != nil || count < 1 {
		t.Fatalf("recount accounts = %d, %v", count, err)
	}
	var accountCounts struct {
		Statuses  int64
		Following int64
		Followers int64
	}
	if err := database.Raw(`SELECT statuses_count AS statuses, following_count AS following, followers_count AS followers FROM account_stats WHERE account_id = ?`, user.AccountID).Scan(&accountCounts).Error; err != nil {
		t.Fatal(err)
	}
	if accountCounts.Statuses != 1 || accountCounts.Following != 1 || accountCounts.Followers != 0 {
		t.Fatalf("account recount = %#v", accountCounts)
	}
	if count, err := operations.RecountCache(context.Background(), "statuses"); err != nil || count < 4 {
		t.Fatalf("recount statuses = %d, %v", count, err)
	}
	var statusCounts struct {
		Replies    int64
		Reblogs    int64
		Favourites int64
	}
	if err := database.Raw(`SELECT replies_count AS replies, reblogs_count AS reblogs, favourites_count AS favourites FROM status_stats WHERE status_id = ?`, originalID).Scan(&statusCounts).Error; err != nil {
		t.Fatal(err)
	}
	if statusCounts.Replies != 1 || statusCounts.Reblogs != 1 || statusCounts.Favourites != 1 {
		t.Fatalf("status recount = %#v", statusCounts)
	}
	cullOrigin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/users/gone" {
			writer.WriteHeader(http.StatusGone)
			return
		}
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer cullOrigin.Close()
	oldPrivateExceptions := activityPrivateAddressExceptions
	activityPrivateAddressExceptions = parseActivityPrivateAddressExceptions("127.0.0.1/32")
	defer func() { activityPrivateAddressExceptions = oldPrivateExceptions }()
	if err := database.Exec(`INSERT INTO accounts (username, domain, uri, protocol, created_at, updated_at) VALUES
('gone', 'gone.example', ?, 1, CURRENT_TIMESTAMP - INTERVAL '8 days', CURRENT_TIMESTAMP - INTERVAL '8 days'),
('error', 'error.example', ?, 1, CURRENT_TIMESTAMP - INTERVAL '8 days', CURRENT_TIMESTAMP - INTERVAL '8 days')`, cullOrigin.URL+"/users/gone", cullOrigin.URL+"/users/error").Error; err != nil {
		t.Fatal(err)
	}
	cullDryRun, err := operations.CullRemoteAccounts(context.Background(), []string{"gone.example", "error.example"}, 2, true)
	if err != nil || cullDryRun.Visited != 2 || cullDryRun.Removed != 1 || len(cullDryRun.UnavailableDomains) != 0 {
		t.Fatalf("cull dry run = %#v, %v", cullDryRun, err)
	}
	var goneCount int64
	if err := database.Model(&models.Account{}).Where("domain = ?", "gone.example").Count(&goneCount).Error; err != nil || goneCount != 1 {
		t.Fatalf("dry run gone count = %d, %v", goneCount, err)
	}
	if err := database.Exec(`UPDATE accounts SET updated_at = CURRENT_TIMESTAMP - INTERVAL '8 days' WHERE domain = 'gone.example'`).Error; err != nil {
		t.Fatal(err)
	}
	cull, err := operations.CullRemoteAccounts(context.Background(), []string{"gone.example"}, 1, false)
	if err != nil || cull.Visited != 1 || cull.Removed != 1 {
		t.Fatalf("cull = %#v, %v", cull, err)
	}
	if err := database.Model(&models.Account{}).Where("domain = ?", "gone.example").Count(&goneCount).Error; err != nil || goneCount != 0 {
		t.Fatalf("culled gone count = %d, %v", goneCount, err)
	}
}

func TestSchedulerCadenceMarkerAllowsOneReplica(t *testing.T) {
	redisURL := os.Getenv("PAON_TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("PAON_TEST_REDIS_URL is required for integration tests")
	}
	name := "integration_scheduler_" + randomHex(8)
	servers := []*Server{{cfg: config.Config{RedisURL: redisURL}}, {cfg: config.Config{RedisURL: redisURL}}}
	var executions atomic.Int64
	results := make(chan bool, len(servers))
	for _, server := range servers {
		go func(server *Server) {
			results <- server.runSchedulerWithRedisLock(context.Background(), name, time.Minute, func() {
				executions.Add(1)
				time.Sleep(50 * time.Millisecond)
			})
		}(server)
	}
	for range servers {
		<-results
	}
	if executions.Load() != 1 {
		t.Fatalf("scheduler executions = %d", executions.Load())
	}
}
