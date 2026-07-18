//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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
	modified, err := operations.ModifyAccount(context.Background(), "operator", OperationAccountModify{Disable: true, ResetPassword: true})
	if err != nil {
		t.Fatal(err)
	}
	if !modified.User.Disabled || modified.GeneratedPassword == "" {
		t.Fatalf("modified user = %#v", modified)
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
