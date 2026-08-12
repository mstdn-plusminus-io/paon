package api

import (
	"database/sql"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMastodon44CreateRelevancyChecksMatchVisibility(t *testing.T) {
	tests := []struct {
		name       string
		visibility int
		want       activityPubCreateRelevancyChecks
	}{
		{name: "public", visibility: 0, want: activityPubCreateRelevancyChecks{Followed: true, Relay: true, Reply: true}},
		{name: "unlisted", visibility: 1, want: activityPubCreateRelevancyChecks{Followed: true, Relay: true, Reply: true}},
		{name: "followers-only", visibility: 2, want: activityPubCreateRelevancyChecks{Followed: true}},
		{name: "direct", visibility: 3, want: activityPubCreateRelevancyChecks{}},
		{name: "limited", visibility: 4, want: activityPubCreateRelevancyChecks{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := activityPubCreateRelevancyChecksForVisibility(test.visibility); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("checks = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMastodon44CreateRejectsKnownStatusAuthorshipChange(t *testing.T) {
	actor := &models.Account{ID: 10}
	if !activityPubExistingCreateStatusBelongsToActor(&models.Status{AccountID: actor.ID}, actor) {
		t.Fatal("status owned by the verified actor was rejected")
	}
	if activityPubExistingCreateStatusBelongsToActor(&models.Status{AccountID: 11}, actor) {
		t.Fatal("known status attributed to another actor was accepted")
	}
	if activityPubExistingCreateStatusBelongsToActor(nil, actor) || activityPubExistingCreateStatusBelongsToActor(&models.Status{AccountID: actor.ID}, nil) {
		t.Fatal("missing status or actor was accepted")
	}
}

func TestMastodon44SuspendedActorCannotCreateUnknownStatusThroughUpdate(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	recent := activityObject{Published: now.Add(-time.Hour).Format(time.RFC3339)}
	old := activityObject{Published: now.Add(-25 * time.Hour).Format(time.RFC3339)}
	active := &models.Account{ID: 1}
	suspended := &models.Account{ID: 2, SuspendedAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}}

	if activityPubUpdateShouldIgnoreUnknownStatus(true, active, recent, now) {
		t.Fatal("recent unknown Update from an active actor was ignored")
	}
	if !activityPubUpdateShouldIgnoreUnknownStatus(true, suspended, recent, now) {
		t.Fatal("unknown Update from a suspended actor was accepted")
	}
	if activityPubUpdateShouldIgnoreUnknownStatus(false, suspended, recent, now) {
		t.Fatal("known Update from a suspended actor must remain processable for existing objects")
	}
	if !activityPubUpdateShouldIgnoreUnknownStatus(true, active, old, now) {
		t.Fatal("old unknown Update was accepted")
	}
}

func TestMastodon44ActivityPubSecurityGuardsAreWiredIntoRuntimePaths(t *testing.T) {
	inbox, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	for function, fragments := range map[string][]string{
		"processActivityPubCreateNote": {
			"findActivityPubExistingCreateStatus(tx, note)",
			"activityPubExistingCreateStatusBelongsToActor(existing, actor)",
		},
		"processActivityPubUpdate": {
			"activityPubUpdateShouldIgnoreUnknownStatus(statusMissing, actor, object, now)",
		},
		"activityPubCreateRelatedToLocalActivity": {
			"activityPubCreateRelevancyChecksForVisibility(activityPubVisibility(note.To, note.CC, actor))",
			"return s.activityPubAddressesLocalAccounts(note)",
		},
	} {
		for _, fragment := range fragments {
			if !functionBodyContains(t, inbox, function, fragment) {
				t.Fatalf("%s missing %q", function, fragment)
			}
		}
	}
	if functionBodyContains(t, inbox, "activityPubCreateRelatedToLocalActivity", "deliveredTo.Local()") {
		t.Fatal("personal-inbox delivery bypasses the visibility-specific audience relevancy check")
	}
	if functionBodyContains(t, inbox, "findActivityPubExistingCreateStatus", `account_id = ?`) {
		t.Fatal("known Create lookup must be global so an actor change cannot bypass it")
	}

	feeds, err := os.ReadFile("list_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, function := range []string{"fanOutStatusToLocalRecipientsSkipNotifications", "fanOutStatusUpdateToLocalRecipients"} {
		if !functionBodyContains(t, feeds, function, "statusProperAuthorCanFanOut(ctx, database, status)") {
			t.Fatalf("%s does not fail closed for a suspended author", function)
		}
	}
	if !functionBodyContains(t, feeds, "statusProperAuthorCanFanOut", "status.ReblogOfID.Int64") {
		t.Fatal("fan-out guard does not check the proper author of a reblog")
	}
}
