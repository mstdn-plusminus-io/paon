//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// REST::AccountSerializer emits moved wherever an account is nested
// (moved_and_not_nested? only checks moved?), so every REST surface that
// serializes accounts must preload MovedToAccount in batch.
func TestMovedAccountNestedSerializationIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2,
		Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example",
		SecretKeyBase: "moved-nested-serialization-integration-secret",
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

	t.Run("REST surfaces nest moved for a remote account that moved to Paon", func(t *testing.T) {
		f := newMovedNestedFixture(t, database, cfg)
		bobStatus := f.status(f.bob.ID, 0)
		carolStatus := f.status(f.carol.ID, 0)
		carolReblog := f.reblog(f.carol.ID, bobStatus.ID)
		f.reblog(f.bob.ID, carolStatus.ID)
		f.create(&models.Favourite{AccountID: f.bob.ID, StatusID: carolStatus.ID, CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.StatusEdit{StatusID: bobStatus.ID, AccountID: sql.NullInt64{Int64: f.bob.ID, Valid: true}, Text: "edited", CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.Follow{AccountID: f.bob.ID, TargetAccountID: f.carol.ID, CreatedAt: f.now, UpdatedAt: f.now})
		follow := models.Follow{AccountID: f.carol.ID, TargetAccountID: f.bob.ID, CreatedAt: f.now, UpdatedAt: f.now}
		f.create(&follow)
		f.create(&models.Follow{AccountID: f.bob.ID, TargetAccountID: f.dave.ID, CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.FollowRequest{AccountID: f.bob.ID, TargetAccountID: f.carol.ID, CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.Block{AccountID: f.carol.ID, TargetAccountID: f.bob.ID, CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.Mute{AccountID: f.carol.ID, TargetAccountID: f.bob.ID, CreatedAt: f.now, UpdatedAt: f.now})
		f.create(&models.AccountPin{AccountID: sql.NullInt64{Int64: f.carol.ID, Valid: true}, TargetAccountID: sql.NullInt64{Int64: f.bob.ID, Valid: true}, CreatedAt: f.now, UpdatedAt: f.now})
		list := models.List{AccountID: f.carol.ID, Title: "moved", CreatedAt: f.now, UpdatedAt: f.now}
		f.create(&list)
		f.create(&models.ListAccount{ListID: list.ID, AccountID: f.bob.ID, FollowID: sql.NullInt64{Int64: follow.ID, Valid: true}})

		status, err := f.server.findStatus(strconv.FormatInt(bobStatus.ID, 10))
		if err != nil {
			t.Fatal(err)
		}
		f.assertMoved("status account", f.marshal(serializer.StatusFromModel(cfg, *status, nil))["account"])
		reblog, err := f.server.findStatus(strconv.FormatInt(carolReblog.ID, 10))
		if err != nil {
			t.Fatal(err)
		}
		f.assertMoved("reblog account", f.marshal(serializer.StatusFromModel(cfg, *reblog, nil))["reblog"].(map[string]any)["account"])

		carolID := strconv.FormatInt(f.carol.ID, 10)
		bobStatusID := strconv.FormatInt(bobStatus.ID, 10)
		carolStatusID := strconv.FormatInt(carolStatus.ID, 10)
		for _, tc := range []struct {
			name    string
			handler func(*echo.Context) error
			target  string
			id      string
			token   bool
			account func(any) any
		}{
			{name: "followers", handler: f.server.accountFollowers, target: "/api/v1/accounts/" + carolID + "/followers", id: carolID},
			{name: "following", handler: f.server.accountFollowing, target: "/api/v1/accounts/" + carolID + "/following", id: carolID},
			{name: "favourited_by", handler: f.server.favouritedBy, target: "/api/v1/statuses/" + carolStatusID + "/favourited_by", id: carolStatusID},
			{name: "reblogged_by", handler: f.server.rebloggedBy, target: "/api/v1/statuses/" + carolStatusID + "/reblogged_by", id: carolStatusID},
			{name: "history", handler: f.server.statusHistory, target: "/api/v1/statuses/" + bobStatusID + "/history", id: bobStatusID, account: func(row any) any { return row.(map[string]any)["account"] }},
			{name: "follow_requests", handler: f.server.followRequests, target: "/api/v1/follow_requests", token: true},
			{name: "blocks", handler: f.server.blocks, target: "/api/v1/blocks", token: true},
			{name: "mutes", handler: f.server.mutes, target: "/api/v1/mutes", token: true},
			{name: "endorsements", handler: f.server.endorsements, target: "/api/v1/endorsements", token: true},
			{name: "list accounts", handler: f.server.listAccounts, target: "/api/v1/lists/" + strconv.FormatInt(list.ID, 10) + "/accounts", id: strconv.FormatInt(list.ID, 10), token: true},
			{name: "familiar_followers", handler: f.server.familiarFollowers, target: "/api/v1/accounts/familiar_followers?id[]=" + strconv.FormatInt(f.dave.ID, 10), token: true, account: func(row any) any { return row.(map[string]any)["accounts"].([]any)[0] }},
		} {
			rows := f.call(tc.name, tc.handler, tc.target, tc.id, tc.token)
			if len(rows) == 0 {
				t.Fatalf("%s returned no rows", tc.name)
			}
			account := rows[0]
			if tc.account != nil {
				account = tc.account(rows[0])
			}
			f.assertMoved(tc.name, account)
		}

		// account_conversations.conversation_id is NOT NULL in the 4.6 schema.
		parent := models.Conversation{CreatedAt: f.now, UpdatedAt: f.now}
		f.create(&parent)
		conversation := models.AccountConversation{AccountID: sql.NullInt64{Int64: f.carol.ID, Valid: true}, ConversationID: sql.NullInt64{Int64: parent.ID, Valid: true}, ParticipantAccountIDs: models.Int64Array{f.bob.ID}, StatusIDs: models.Int64Array{bobStatus.ID}, LastStatusID: sql.NullInt64{Int64: bobStatus.ID, Valid: true}}
		f.create(&conversation)
		conversations := []models.AccountConversation{}
		if err := f.server.conversationQuery().Where("account_conversations.id = ?", conversation.ID).Find(&conversations).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.server.hydrateConversationParticipants(conversations); err != nil {
			t.Fatal(err)
		}
		conversationJSON := f.marshal(serializer.ConversationFromModel(cfg, conversations[0], &f.carol))
		f.assertMoved("conversation participant", conversationJSON["accounts"].([]any)[0])
		f.assertMoved("conversation last_status", conversationJSON["last_status"].(map[string]any)["account"])

		report := models.Report{AccountID: f.carol.ID, TargetAccountID: f.bob.ID, StatusIDs: models.Int64Array{}, CreatedAt: f.now, UpdatedAt: f.now}
		f.create(&report)
		var loadedReport models.Report
		if err := f.server.adminReportBaseQuery().Where("reports.id = ?", report.ID).First(&loadedReport).Error; err != nil {
			t.Fatal(err)
		}
		f.assertMoved("report target_account", f.marshal(serializer.ReportFromModel(cfg, loadedReport))["target_account"])
		adminAccount, err := f.server.loadAdminAccount(strconv.FormatInt(f.bob.ID, 10))
		if err != nil {
			t.Fatal(err)
		}
		f.assertMoved("admin account", f.marshal(serializer.AccountFromModel(cfg, *adminAccount)))
		resolved, err := f.server.resolveKnownRemoteAccountFromDB(f.bob.URI)
		if err != nil || resolved == nil {
			t.Fatalf("resolveKnownRemoteAccountFromDB = %v, %v", resolved, err)
		}
		f.assertMoved("resolved account", f.marshal(serializer.AccountFromModel(cfg, *resolved)))
	})

	t.Run("status preloads load moved accounts in batch", func(t *testing.T) {
		f := newMovedNestedFixture(t, database, cfg)
		seq := 0
		movedStatuses := func(n int) []int64 {
			ids := make([]int64, 0, n*2)
			for i := 0; i < n; i++ {
				seq++
				name := "moved" + strconv.Itoa(seq)
				target := f.local("target"+strconv.Itoa(seq), false)
				moved := settingsResolveRemoteAccount(t, f.tx, name, name+".example", "https://"+name+".example/users/"+name, f.now)
				if err := f.tx.Model(&models.Account{}).Where("id = ?", moved.ID).Update("moved_to_account_id", target.ID).Error; err != nil {
					t.Fatal(err)
				}
				original := f.status(moved.ID, 0)
				ids = append(ids, original.ID, f.reblog(moved.ID, original.ID).ID)
			}
			return ids
		}
		plainOriginal := f.status(f.carol.ID, 0)
		plain := []int64{plainOriginal.ID, f.reblog(f.dave.ID, plainOriginal.ID).ID}
		few := movedStatuses(2)
		many := movedStatuses(6)

		queries := func(ids []int64) (int, []models.Status) {
			counter := &movedNestedQueryCounter{Interface: logger.Discard}
			server := &Server{cfg: cfg, db: f.tx.Session(&gorm.Session{Logger: counter})}
			var statuses []models.Status
			if err := server.statusQuery().Where("statuses.id IN ?", ids).Find(&statuses).Error; err != nil {
				t.Fatal(err)
			}
			return counter.count(), statuses
		}
		plainQueries, _ := queries(plain)
		fewQueries, _ := queries(few)
		manyQueries, statuses := queries(many)
		if fewQueries != manyQueries {
			t.Fatalf("statusQuery issued %d queries for %d statuses but %d for %d: moved preloads are not batched", fewQueries, len(few), manyQueries, len(many))
		}
		if plainQueries >= fewQueries {
			t.Fatalf("statusQuery issued %d queries without moved accounts and %d with them", plainQueries, fewQueries)
		}
		if len(statuses) != len(many) {
			t.Fatalf("loaded %d statuses, want %d", len(statuses), len(many))
		}
		for _, status := range statuses {
			account := status.Account
			if status.Reblog != nil {
				account = status.Reblog.Account
			}
			if account.MovedToAccount == nil || account.MovedToAccount.ID != account.MovedToAccountID.Int64 {
				t.Fatalf("status %d account moved_to = %+v, want preloaded %d", status.ID, account.MovedToAccount, account.MovedToAccountID.Int64)
			}
		}
	})
}

type movedNestedFixture struct {
	t      *testing.T
	tx     *gorm.DB
	server *Server
	now    time.Time
	alice  models.Account
	bob    models.Account
	carol  models.Account
	dave   models.Account
	token  string
}

func newMovedNestedFixture(t *testing.T, database *gorm.DB, cfg config.Config) *movedNestedFixture {
	t.Helper()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	f := &movedNestedFixture{t: t, tx: tx, server: &Server{cfg: cfg, db: tx}, now: time.Now().UTC()}
	f.alice = f.local("alice", false)
	f.bob = settingsResolveRemoteAccount(t, tx, "bob", "old.example", "https://old.example/users/bob", f.now)
	if err := tx.Model(&models.Account{}).Where("id = ?", f.bob.ID).Update("moved_to_account_id", f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.bob.MovedToAccountID = sql.NullInt64{Int64: f.alice.ID, Valid: true}
	f.carol = f.local("carol", true)
	f.dave = f.local("dave", false)
	return f
}

func (f *movedNestedFixture) create(value any) {
	f.t.Helper()
	if err := f.tx.Create(value).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *movedNestedFixture) local(username string, withToken bool) models.Account {
	f.t.Helper()
	account := models.Account{Username: username, CreatedAt: f.now, UpdatedAt: f.now}
	f.create(&account)
	user := models.User{AccountID: account.ID, Email: username + "@paon.example", EncryptedPassword: "x", Approved: true, ConfirmedAt: sql.NullTime{Time: f.now, Valid: true}, CreatedAt: f.now, UpdatedAt: f.now}
	f.create(&user)
	if withToken {
		f.token = "moved-nested-" + randomHex(8)
		f.create(&models.OAuthAccessToken{Token: f.token, ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true}, Scopes: "read write follow", CreatedAt: f.now})
	}
	return account
}

func (f *movedNestedFixture) status(accountID int64, visibility int) models.Status {
	f.t.Helper()
	status := models.Status{Text: "hello", AccountID: accountID, Visibility: visibility, CreatedAt: f.now, UpdatedAt: f.now}
	f.create(&status)
	return status
}

func (f *movedNestedFixture) reblog(accountID, statusID int64) models.Status {
	f.t.Helper()
	status := models.Status{AccountID: accountID, ReblogOfID: sql.NullInt64{Int64: statusID, Valid: true}, CreatedAt: f.now, UpdatedAt: f.now}
	f.create(&status)
	return status
}

func (f *movedNestedFixture) marshal(value any) map[string]any {
	f.t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *movedNestedFixture) call(name string, handler func(*echo.Context) error, target, id string, withToken bool) []any {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if withToken {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if id != "" {
		c.SetPathValues(echo.PathValues{{Name: "id", Value: id}})
	}
	if err := handler(c); err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	if rec.Code != http.StatusOK {
		f.t.Fatalf("%s: status = %d body=%s", name, rec.Code, rec.Body.String())
	}
	var rows []any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		f.t.Fatalf("%s: %v body=%s", name, err, rec.Body.String())
	}
	return rows
}

func (f *movedNestedFixture) assertMoved(name string, value any) {
	f.t.Helper()
	account, ok := value.(map[string]any)
	if !ok || account["id"] != strconv.FormatInt(f.bob.ID, 10) {
		f.t.Fatalf("%s: account = %#v, want bob %d", name, value, f.bob.ID)
	}
	moved, ok := account["moved"].(map[string]any)
	if !ok || moved["id"] != strconv.FormatInt(f.alice.ID, 10) {
		f.t.Fatalf("%s: moved = %#v, want alice %d", name, account["moved"], f.alice.ID)
	}
	if _, nested := moved["moved"]; nested {
		f.t.Fatalf("%s: moved account must not nest moved: %#v", name, moved)
	}
}

type movedNestedQueryCounter struct {
	logger.Interface
	mu      sync.Mutex
	queries []string
}

func (c *movedNestedQueryCounter) LogMode(logger.LogLevel) logger.Interface { return c }

func (c *movedNestedQueryCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, sql)
}

func (c *movedNestedQueryCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, query := range c.queries {
		if strings.HasPrefix(strings.TrimSpace(query), "SELECT") {
			n++
		}
	}
	return n
}
