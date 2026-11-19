//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// A locked account added to a list through a pending follow request must not
// expose its statuses (followers-only ones in particular) on the list timeline
// until the request is authorized.
func TestListTimelineHidesPendingFollowRequestMemberIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		LocalDomain:          "paon.example",
		WebDomain:            "paon.example",
		SecretKeyBase:        "integration-secret",
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
	owner := models.Account{Username: "owner", CreatedAt: now, UpdatedAt: now}
	followed := models.Account{Username: "followed", CreatedAt: now, UpdatedAt: now}
	locked := models.Account{Username: "locked", Locked: true, CreatedAt: now, UpdatedAt: now}
	for _, account := range []*models.Account{&owner, &followed, &locked} {
		if err := database.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	// List streams are only published for lists of local users.
	if err := database.Create(&models.User{AccountID: owner.ID, Email: "owner@paon.example", EncryptedPassword: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Follow{AccountID: owner.ID, TargetAccountID: followed.ID, ShowReblogs: true, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.FollowRequest{AccountID: owner.ID, TargetAccountID: locked.ID, ShowReblogs: true, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	list := models.List{AccountID: owner.ID, Title: "pending", RepliesPolicy: 1, CreatedAt: now, UpdatedAt: now}
	if err := database.Create(&list).Error; err != nil {
		t.Fatal(err)
	}
	for _, memberID := range []int64{owner.ID, followed.ID, locked.ID} {
		item, err := listAccountRow(database, list, memberID)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	lockedMember := func(t *testing.T) models.ListAccount {
		t.Helper()
		var member models.ListAccount
		if err := database.Where("list_id = ? AND account_id = ?", list.ID, locked.ID).First(&member).Error; err != nil {
			t.Fatal(err)
		}
		return member
	}
	if member := lockedMember(t); member.FollowID.Valid || !member.FollowRequestID.Valid {
		t.Fatalf("locked member must join the list through its pending follow request: %#v", member)
	}

	const (
		ownerPrivateID    = int64(1001)
		followedPrivateID = int64(1002)
		lockedPublicID    = int64(1003)
		lockedPrivateID   = int64(1004)
	)
	for _, status := range []struct {
		id         int64
		accountID  int64
		visibility int
	}{
		{id: ownerPrivateID, accountID: owner.ID, visibility: 2},
		{id: followedPrivateID, accountID: followed.ID, visibility: 2},
		{id: lockedPublicID, accountID: locked.ID, visibility: 0},
		{id: lockedPrivateID, accountID: locked.ID, visibility: 2},
	} {
		if err := database.Exec(`INSERT INTO statuses (id, text, created_at, updated_at, account_id, visibility) VALUES (?, 'status', now(), now(), ?, ?)`, status.id, status.accountID, status.visibility).Error; err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{cfg: cfg, db: database}
	listTimeline := func(t *testing.T) []int64 {
		t.Helper()
		c := echo.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/timelines/list/"+strconv.FormatInt(list.ID, 10), nil), httptest.NewRecorder(), echo.New())
		ids, err := server.listTimelineStatusIDs(c, list)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	listChannel := "test:timeline:list:" + strconv.FormatInt(list.ID, 10)
	listChannels := func(accountID int64) []string {
		return server.statusListStreamingChannels(context.Background(), database, "test:", accountID)
	}

	if got, want := listTimeline(t), []int64{followedPrivateID, ownerPrivateID}; !slices.Equal(got, want) {
		t.Fatalf("list timeline with a pending member = %v, want %v", got, want)
	}
	if got := listChannels(locked.ID); len(got) != 0 {
		t.Fatalf("pending member list streams = %v, want none", got)
	}
	if got, want := listChannels(followed.ID), []string{listChannel}; !slices.Equal(got, want) {
		t.Fatalf("followed member list streams = %v, want %v", got, want)
	}

	if err := server.authorizeFollowRequestPairNow(context.Background(), owner.ID, locked.ID); err != nil {
		t.Fatal(err)
	}
	if member := lockedMember(t); !member.FollowID.Valid || member.FollowRequestID.Valid {
		t.Fatalf("authorized member must move from follow_request_id to follow_id: %#v", member)
	}
	if got, want := listTimeline(t), []int64{lockedPrivateID, lockedPublicID, followedPrivateID, ownerPrivateID}; !slices.Equal(got, want) {
		t.Fatalf("list timeline after authorizing the member = %v, want %v", got, want)
	}
	if got, want := listChannels(locked.ID), []string{listChannel}; !slices.Equal(got, want) {
		t.Fatalf("authorized member list streams = %v, want %v", got, want)
	}
}
