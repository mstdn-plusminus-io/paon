//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestPublicStatusHTMLStatusMatchesRailsStatusPolicyIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		Scheme:               "https",
		LocalDomain:          "paon.example",
		WebDomain:            "paon.example",
		SecretKeyBase:        "public-status-integration-secret",
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

	now := time.Now().UTC()
	create := func(value any) {
		t.Helper()
		if err := database.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{}
	accounts := map[string]models.Account{}
	for _, username := range []string{"alice", "follower", "disabled_follower", "stranger", "mentioned"} {
		account := models.Account{Username: username, CreatedAt: now, UpdatedAt: now}
		create(&account)
		// Rails skips require_functional! on statuses#show, so disabled users keep their access.
		user := models.User{AccountID: account.ID, Email: username + "@paon.example", EncryptedPassword: "x", Approved: true, Disabled: username == "disabled_follower", ConfirmedAt: sql.NullTime{Time: now, Valid: true}, CreatedAt: now, UpdatedAt: now}
		create(&user)
		tokens[username] = "public-status-" + username + "-" + randomHex(8)
		create(&models.OAuthAccessToken{Token: tokens[username], ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true}, Scopes: "read write", CreatedAt: now})
		accounts[username] = account
	}
	alice := accounts["alice"]
	for _, follower := range []string{"follower", "disabled_follower"} {
		create(&models.Follow{AccountID: accounts[follower].ID, TargetAccountID: alice.ID, CreatedAt: now, UpdatedAt: now})
	}

	statuses := map[string]models.Status{}
	for _, fixture := range []struct {
		name       string
		visibility int
		mention    bool
	}{
		{"public", 0, false},
		{"unlisted", 1, false},
		{"private", 2, false},
		{"private_mention", 2, true},
		{"direct", 3, true},
		{"limited", 4, true},
	} {
		status := models.Status{Text: fixture.name, AccountID: alice.ID, Visibility: fixture.visibility, CreatedAt: now, UpdatedAt: now}
		create(&status)
		if fixture.mention {
			create(&models.Mention{StatusID: sql.NullInt64{Int64: status.ID, Valid: true}, AccountID: sql.NullInt64{Int64: accounts["mentioned"].ID, Valid: true}, CreatedAt: now, UpdatedAt: now})
		}
		statuses[fixture.name] = status
	}

	poll := models.Poll{AccountID: sql.NullInt64{Int64: alice.ID, Valid: true}, StatusID: sql.NullInt64{Int64: statuses["private_mention"].ID, Valid: true}, Options: models.StringArray{"yes", "no"}, CachedTallies: models.Int64Array{0, 0}, CreatedAt: now, UpdatedAt: now}
	create(&poll)

	server := &Server{cfg: cfg, db: database}
	request := func(username, statusName, viewer, auth string) error {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/@"+username+"/"+strconv.FormatInt(statuses[statusName].ID, 10), nil)
		if viewer != "" {
			switch auth {
			case "cookie":
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tokens[viewer]})
			case "websocket":
				req.Header.Set("Sec-WebSocket-Protocol", tokens[viewer])
			default:
				req.Header.Set("Authorization", "Bearer "+tokens[viewer])
			}
		}
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())
		c.SetPathValues(echo.PathValues{{Name: "username", Value: username}, {Name: "id", Value: strconv.FormatInt(statuses[statusName].ID, 10)}})
		status, err := server.publicStatusHTMLStatus(c)
		if err == nil && (status == nil || status.ID != statuses[statusName].ID) {
			t.Fatalf("%s as %q returned status %+v", statusName, viewer, status)
		}
		if err == nil && status.Visibility > 1 {
			publicHTMLCacheIfUnauthenticated(c, 10, 0)
			if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "public") {
				t.Fatalf("%s as %q (%s): Cache-Control = %q, want no public caching", statusName, viewer, auth, got)
			}
		}
		return err
	}

	visible := map[string][]string{
		"public":          {"", "alice", "follower", "disabled_follower", "stranger", "mentioned"},
		"unlisted":        {"", "alice", "follower", "disabled_follower", "stranger", "mentioned"},
		"private":         {"alice", "follower", "disabled_follower"},
		"private_mention": {"alice", "follower", "disabled_follower", "mentioned"},
		"direct":          {"alice", "mentioned"},
		"limited":         {"alice", "mentioned"},
	}
	for statusName, allowed := range visible {
		for _, viewer := range []string{"", "alice", "follower", "disabled_follower", "stranger", "mentioned"} {
			want := false
			for _, candidate := range allowed {
				want = want || candidate == viewer
			}
			for _, auth := range []string{"bearer", "cookie"} {
				err := request("alice", statusName, viewer, auth)
				if want && err != nil {
					t.Fatalf("%s as %q (%s): err = %v, want visible", statusName, viewer, auth, err)
				}
				if !want && !apiErrorStatus(err, http.StatusNotFound) {
					t.Fatalf("%s as %q (%s): err = %v, want 404", statusName, viewer, auth, err)
				}
			}

			// The REST API applies the same StatusPolicy#show? rules through visibleStatusQuery.
			var account *models.Account
			if viewer != "" {
				viewerAccount := accounts[viewer]
				account = &viewerAccount
			}
			_, err := server.findVisibleStatusForAccount(account, strconv.FormatInt(statuses[statusName].ID, 10))
			if want && err != nil {
				t.Fatalf("API %s as %q: err = %v, want visible", statusName, viewer, err)
			}
			if !want && !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("API %s as %q: err = %v, want record not found", statusName, viewer, err)
			}
		}
	}

	if err := request("follower", "private", "follower", "bearer"); !apiErrorStatus(err, http.StatusNotFound) {
		t.Fatalf("private status under another account path: err = %v, want 404", err)
	}
	// Tokens the HTML cache policy does not treat as authentication must not
	// unlock hidden statuses, or the response could be cached publicly.
	if err := request("alice", "private", "follower", "websocket"); !apiErrorStatus(err, http.StatusNotFound) {
		t.Fatalf("private status via Sec-WebSocket-Protocol token: err = %v, want 404", err)
	}

	// Rails StatusPolicy#favourite? and PollPolicy#vote? add block checks on top of show?.
	act := func(handler func(*echo.Context) error, target string, id int64, viewer string) (int, error) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[viewer])
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())
		c.SetPathValues(echo.PathValues{{Name: "id", Value: strconv.FormatInt(id, 10)}})
		err := handler(c)
		return rec.Code, err
	}
	mentionedStatusID := statuses["private_mention"].ID
	if code, err := act(server.favouriteStatus, "/api/v1/statuses/"+strconv.FormatInt(mentionedStatusID, 10)+"/favourite", mentionedStatusID, "mentioned"); err != nil || code != http.StatusOK {
		t.Fatalf("favourite private mention as mentioned non-follower: code = %d, err = %v", code, err)
	}
	blocks := []models.Block{
		{AccountID: accounts["mentioned"].ID, TargetAccountID: alice.ID, CreatedAt: now, UpdatedAt: now},
		{AccountID: alice.ID, TargetAccountID: accounts["mentioned"].ID, CreatedAt: now, UpdatedAt: now},
	}
	for i := range blocks {
		create(&blocks[i])
		if _, err := server.findVisibleStatusForAccount(&models.Account{ID: accounts["mentioned"].ID}, strconv.FormatInt(mentionedStatusID, 10)); err != nil {
			t.Fatalf("block %d: private mention should stay visible like StatusPolicy#show?: %v", i, err)
		}
		_, err := act(server.favouriteStatus, "/api/v1/statuses/"+strconv.FormatInt(mentionedStatusID, 10)+"/favourite", mentionedStatusID, "mentioned")
		viewerBlocksAuthor := blocks[i].AccountID == accounts["mentioned"].ID
		if viewerBlocksAuthor && !apiErrorStatus(err, http.StatusForbidden) {
			t.Fatalf("favourite while blocking the author: err = %v, want 403", err)
		}
		if !viewerBlocksAuthor && err != nil {
			t.Fatalf("favourite while blocked by the author: err = %v, want allowed like Rails", err)
		}
		if _, err := act(server.votePoll, "/api/v1/polls/"+strconv.FormatInt(poll.ID, 10)+"/votes?choices[]=0", poll.ID, "mentioned"); !apiErrorStatus(err, http.StatusForbidden) {
			t.Fatalf("block %d: vote err = %v, want 403", i, err)
		}
		if err := database.Delete(&blocks[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
}
