//go:build integration

package api

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestMastodon44ActivityPubRelevancyAuthorshipAndSuspensionAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		LocalDomain:          "example.test",
		WebDomain:            "example.test",
		Scheme:               "https",
		SecretKeyBase:        "activitypub-44-security-integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	local := createActivityPub44SecurityAccount(t, database, "local", "", sql.NullTime{}, now)
	remote := createActivityPub44SecurityAccount(t, database, "remote", "remote.example", sql.NullTime{}, now)
	other := createActivityPub44SecurityAccount(t, database, "other", "other.example", sql.NullTime{}, now)
	suspended := createActivityPub44SecurityAccount(t, database, "suspended", "suspended.example", sql.NullTime{Time: now.Add(-time.Hour), Valid: true}, now)
	if err := database.Create(&models.Follow{
		AccountID:       local.ID,
		TargetAccountID: remote.ID,
		ShowReblogs:     true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	server := &Server{cfg: cfg, db: database}
	public := activityObject{To: []string{activityPubPublicIRI}}
	if related, err := server.activityPubCreateRelatedToLocalActivity(public, &remote, nil, nil); err != nil || !related {
		t.Fatalf("public post from followed actor related=%v err=%v", related, err)
	}
	private := activityObject{To: []string{remote.FollowersURL}}
	if related, err := server.activityPubCreateRelatedToLocalActivity(private, &remote, nil, nil); err != nil || !related {
		t.Fatalf("followers-only post from followed actor related=%v err=%v", related, err)
	}
	direct := activityObject{To: []string{other.URI}}
	if related, err := server.activityPubCreateRelatedToLocalActivity(direct, &remote, nil, nil); err != nil || related {
		t.Fatalf("unaddressed direct post from followed actor related=%v err=%v", related, err)
	}
	if related, err := server.activityPubCreateRelatedToLocalActivity(direct, &remote, &local, nil); err != nil || related {
		t.Fatalf("unaddressed direct post delivered to a personal inbox related=%v err=%v", related, err)
	}
	addressedDirect := activityObject{To: []string{activityPubAccountTagManagerURI(server, local)}}
	if related, err := server.activityPubCreateRelatedToLocalActivity(addressedDirect, &remote, &local, nil); err != nil || !related {
		t.Fatalf("direct post addressed to a local account related=%v err=%v", related, err)
	}

	knownURI := "https://remote.example/users/remote/statuses/1"
	var statusID int64
	if err := database.Raw(`INSERT INTO statuses (uri, account_id, text, created_at, updated_at, local) VALUES (?, ?, '', ?, ?, false) RETURNING id`, knownURI, remote.ID, now, now).Row().Scan(&statusID); err != nil {
		t.Fatal(err)
	}
	known, err := findActivityPubExistingCreateStatus(database, activityObject{ID: knownURI})
	if err != nil {
		t.Fatal(err)
	}
	if known == nil || known.ID != statusID || known.AccountID != remote.ID {
		t.Fatalf("known status = %#v", known)
	}
	if activityPubExistingCreateStatusBelongsToActor(known, &other) {
		t.Fatal("global known-status lookup allowed an authorship change")
	}

	if allowed, err := statusAuthorCanFanOut(context.Background(), database, remote.ID); err != nil || !allowed {
		t.Fatalf("active author fan-out allowed=%v err=%v", allowed, err)
	}
	if allowed, err := statusAuthorCanFanOut(context.Background(), database, suspended.ID); err != nil || allowed {
		t.Fatalf("suspended author fan-out allowed=%v err=%v", allowed, err)
	}
	if err := server.fanOutStatusToLocalRecipientsSkipNotifications(context.Background(), database, models.Status{ID: statusID + 1, AccountID: suspended.ID}); err != nil {
		t.Fatalf("suspended fan-out should stop before Redis work: %v", err)
	}

	var suspendedStatusID int64
	if err := database.Raw(`INSERT INTO statuses (uri, account_id, text, created_at, updated_at, local) VALUES (?, ?, '', ?, ?, false) RETURNING id`, "https://suspended.example/users/suspended/statuses/1", suspended.ID, now, now).Row().Scan(&suspendedStatusID); err != nil {
		t.Fatal(err)
	}
	reblog := models.Status{
		ID:         suspendedStatusID + 1,
		AccountID:  local.ID,
		ReblogOfID: sql.NullInt64{Int64: suspendedStatusID, Valid: true},
	}
	if allowed, err := statusProperAuthorCanFanOut(context.Background(), database, reblog); err != nil || allowed {
		t.Fatalf("reblog of suspended author's status fan-out allowed=%v err=%v", allowed, err)
	}
	if err := server.fanOutStatusToLocalRecipientsSkipNotifications(context.Background(), database, reblog); err != nil {
		t.Fatalf("reblog of suspended author's status should stop before Redis work: %v", err)
	}
}

func createActivityPub44SecurityAccount(t *testing.T, database *gorm.DB, username string, domain string, suspendedAt sql.NullTime, now time.Time) models.Account {
	t.Helper()
	account := models.Account{
		Username:    username,
		CreatedAt:   now,
		UpdatedAt:   now,
		SuspendedAt: suspendedAt,
	}
	if domain != "" {
		account.Domain = sql.NullString{String: domain, Valid: true}
		account.URI = "https://" + domain + "/users/" + username
		account.URL = sql.NullString{String: account.URI, Valid: true}
		account.FollowersURL = account.URI + "/followers"
	}
	if err := database.Raw(
		`INSERT INTO accounts (username, domain, uri, url, followers_url, suspended_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		account.Username,
		nullableStringValue(account.Domain),
		account.URI,
		nullableStringValue(account.URL),
		account.FollowersURL,
		nullableTimeValue(account.SuspendedAt),
		account.CreatedAt,
		account.UpdatedAt,
	).Row().Scan(&account.ID); err != nil {
		t.Fatal(err)
	}
	return account
}
