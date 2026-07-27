//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestAcceptFollowUsesPersonalInboxRecipientWithoutHostSpecificLogic(t *testing.T) {
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
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	now := time.Now().UTC()
	local := models.Account{Username: "alice", CreatedAt: now, UpdatedAt: now}
	if err := database.Create(&local).Error; err != nil {
		t.Fatal(err)
	}
	remote := models.Account{
		Username:  "bob",
		Domain:    sql.NullString{String: "remote.example", Valid: true},
		URI:       "https://remote.example/users/bob",
		Protocol:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := database.Create(&remote).Error; err != nil {
		t.Fatal(err)
	}
	request := models.FollowRequest{
		AccountID:       local.ID,
		TargetAccountID: remote.ID,
		ShowReblogs:     true,
		URI:             models.NullSafeString("https://paon.example/payloads/original-follow"),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := database.Create(&request).Error; err != nil {
		t.Fatal(err)
	}

	body := []byte(`{
		"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1"],
		"id":"https://remote.example/activities/accept-follow",
		"type":"Accept",
		"actor":"https://remote.example/users/bob",
		"object":{
			"id":"https://remote.example/follows/rewritten-id",
			"type":"Follow",
			"object":"https://remote.example/users/bob"
		}
	}`)
	server := &Server{cfg: cfg, db: database}
	if err := server.processActivityPubInboxForDeliveredToWithContext(context.Background(), body, &remote, nil, local.ID); err != nil {
		t.Fatal(err)
	}

	var follow models.Follow
	if err := database.Where("account_id = ? AND target_account_id = ?", local.ID, remote.ID).First(&follow).Error; err != nil {
		t.Fatalf("accepted follow was not created: %v", err)
	}
	if string(follow.URI) != string(request.URI) {
		t.Fatalf("follow URI = %q, want %q", follow.URI, request.URI)
	}
	if err := database.Where("id = ?", request.ID).First(&models.FollowRequest{}).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("follow request still exists or lookup failed: %v", err)
	}

	if err := server.processActivityPubInboxForDeliveredToWithContext(context.Background(), body, &remote, nil, local.ID); err != nil {
		t.Fatalf("duplicate Accept should be idempotent: %v", err)
	}

	if err := database.Where("account_id = ? AND target_account_id = ?", local.ID, remote.ID).Delete(&models.Follow{}).Error; err != nil {
		t.Fatal(err)
	}
	err = server.processActivityPubInboxForDeliveredToWithContext(context.Background(), body, &remote, nil, local.ID)
	if !errors.Is(err, errActivityPubEventNotApplied) {
		t.Fatalf("unmatched Accept error = %v, want errActivityPubEventNotApplied", err)
	}

	sharedInboxRequest := models.FollowRequest{
		AccountID:       local.ID,
		TargetAccountID: remote.ID,
		ShowReblogs:     true,
		URI:             models.NullSafeString("https://paon.example/payloads/shared-inbox-follow"),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := database.Create(&sharedInboxRequest).Error; err != nil {
		t.Fatal(err)
	}
	sharedInboxBody := []byte(`{
		"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1"],
		"id":"https://remote.example/activities/shared-inbox-accept",
		"type":"Accept",
		"actor":"https://remote.example/users/bob",
		"object":{
			"id":"https://remote.example/follows/another-rewritten-id",
			"type":"Follow",
			"object":"https://remote.example/users/bob"
		}
	}`)
	if err := server.processActivityPubInboxForDeliveredToWithContext(context.Background(), sharedInboxBody, &remote, nil, 0); err != nil {
		t.Fatalf("shared-inbox Accept with one pending request: %v", err)
	}
	if err := database.Where("id = ?", sharedInboxRequest.ID).First(&models.FollowRequest{}).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("shared-inbox follow request still exists or lookup failed: %v", err)
	}

	for _, username := range []string{"carol", "dave"} {
		account := models.Account{Username: username, CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		request := models.FollowRequest{
			AccountID:       account.ID,
			TargetAccountID: remote.ID,
			ShowReblogs:     true,
			URI:             models.NullSafeString("https://paon.example/payloads/" + username + "-follow"),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := database.Create(&request).Error; err != nil {
			t.Fatal(err)
		}
	}
	err = server.processActivityPubInboxForDeliveredToWithContext(context.Background(), sharedInboxBody, &remote, nil, 0)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous shared-inbox Accept error = %v", err)
	}
}
