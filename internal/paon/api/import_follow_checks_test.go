package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestFollowNotAllowedInDBChecksBlocksAndDomainBlocksOnGivenDB(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	tx := statusMentionDryRunDatabase(t, capture)
	// The server has no pool of its own: every lookup must go through tx.
	s := &Server{}
	source := &models.Account{ID: 7}
	target := &models.Account{ID: 9, Domain: sql.NullString{String: "Remote.Example", Valid: true}, Protocol: 1}

	disallowed, err := s.followNotAllowedInDB(tx, source, target)
	if err != nil {
		t.Fatal(err)
	}
	if disallowed {
		t.Fatal("dry-run lookups unexpectedly reported a block")
	}
	queries := strings.Join(capture.queries, "\n")
	for _, want := range []string{
		`FROM "domain_blocks"`,
		`lower(domain) IN ('remote.example','example')`,
		`FROM "account_domain_blocks"`,
		`account_id = 7 AND lower(domain) = 'remote.example'`,
		`FROM "blocks"`,
		`account_id = 9 AND target_account_id = 7`,
		`account_id = 7 AND target_account_id = 9`,
	} {
		if !strings.Contains(queries, want) {
			t.Fatalf("follow permission SQL missing %q:\n%s", want, queries)
		}
	}
}

func TestCreateRelationshipFollowRejectsDisallowedTargetsBeforeWriting(t *testing.T) {
	current := models.Account{ID: 7}
	for name, target := range map[string]models.Account{
		"moved":          {ID: 9, MovedToAccountID: sql.NullInt64{Int64: 10, Valid: true}},
		"remote OStatus": {ID: 9, Domain: sql.NullString{String: "remote.example", Valid: true}, Protocol: 0},
	} {
		t.Run(name, func(t *testing.T) {
			capture := &statusMentionSQLCapture{}
			tx := statusMentionDryRunDatabase(t, capture)
			s := &Server{}
			delivery, notification, err := s.createRelationshipFollow(context.Background(), tx, current, target, time.Unix(1_700_000_000, 0).UTC())
			if !errors.Is(err, errRelationshipFollowNotPermitted) {
				t.Fatalf("err = %v, want errRelationshipFollowNotPermitted", err)
			}
			if delivery != nil || notification != nil {
				t.Fatalf("disallowed follow returned delivery %+v / notification %+v", delivery, notification)
			}
			if queries := strings.Join(capture.queries, "\n"); strings.Contains(queries, "INSERT") {
				t.Fatalf("disallowed follow must not write:\n%s", queries)
			}
		})
	}
}

func TestCreateRelationshipFollowUsesFollowServiceRequestRules(t *testing.T) {
	for _, tt := range []struct {
		name        string
		current     models.Account
		target      models.Account
		wantRequest bool
	}{
		{name: "remote ActivityPub target", current: models.Account{ID: 7}, target: models.Account{ID: 9, Domain: sql.NullString{String: "remote.example", Valid: true}, Protocol: 1, InboxURL: "https://remote.example/inbox"}, wantRequest: true},
		{name: "silenced source", current: models.Account{ID: 7, SilencedAt: sql.NullTime{Time: time.Unix(1_600_000_000, 0), Valid: true}}, target: models.Account{ID: 9}, wantRequest: true},
		{name: "locked target", current: models.Account{ID: 7}, target: models.Account{ID: 9, Locked: true}, wantRequest: true},
		{name: "unlocked local target", current: models.Account{ID: 7}, target: models.Account{ID: 9}, wantRequest: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capture := &statusMentionSQLCapture{}
			tx := statusMentionDryRunDatabase(t, capture)
			s := &Server{}
			if _, _, err := s.createRelationshipFollow(context.Background(), tx, tt.current, tt.target, time.Unix(1_700_000_000, 0).UTC()); err != nil {
				t.Fatal(err)
			}
			queries := strings.Join(capture.queries, "\n")
			blockCheck := strings.Index(queries, `FROM "blocks"`)
			requestInsert := strings.Index(queries, `INSERT INTO "follow_requests"`)
			followInsert := strings.Index(queries, `INSERT INTO "follows"`)
			if blockCheck < 0 {
				t.Fatalf("batch follow must check blocks on the transaction:\n%s", queries)
			}
			if tt.wantRequest {
				if requestInsert < blockCheck || followInsert >= 0 {
					t.Fatalf("want a follow request after the block check and no follow:\n%s", queries)
				}
				return
			}
			if followInsert < blockCheck || requestInsert >= 0 {
				t.Fatalf("want a direct follow after the block check and no follow request:\n%s", queries)
			}
		})
	}
}

func TestApplyRelationshipImportRowFailsDisallowedFollowRows(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	tx := statusMentionDryRunDatabase(t, capture)
	s := &Server{}
	target := &models.Account{ID: 9, MovedToAccountID: sql.NullInt64{Int64: 10, Valid: true}}
	values := map[string]any{"show_reblogs": false, "notify": true, "languages": []any{"ja"}}

	imported, notification, delivery, err := s.applyRelationshipImportRow(tx, 7, target, "following", values, time.Unix(1_700_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if imported || notification != nil || delivery != nil {
		t.Fatalf("disallowed follow row = imported %v, notification %+v, delivery %+v; want a failed row", imported, notification, delivery)
	}
	queries := strings.Join(capture.queries, "\n")
	if !strings.Contains(queries, `FROM "accounts" WHERE id = 7`) {
		t.Fatalf("follow import must load the importing account on the transaction:\n%s", queries)
	}
	if strings.Contains(queries, "INSERT") || strings.Contains(queries, "UPDATE") {
		t.Fatalf("disallowed follow row must not write:\n%s", queries)
	}
}

func TestApplyRelationshipImportRowRequestsRemoteActivityPubTargets(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	tx := statusMentionDryRunDatabase(t, capture)
	s := &Server{}
	target := &models.Account{ID: 9, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}, Protocol: 1, InboxURL: "https://remote.example/inbox"}
	values := map[string]any{"show_reblogs": false, "notify": true, "languages": []any{"ja"}}

	if _, _, _, err := s.applyRelationshipImportRow(tx, 7, target, "following", values, time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	queries := strings.Join(capture.queries, "\n")
	blockCheck := strings.Index(queries, `FROM "blocks"`)
	requestInsert := strings.Index(queries, `INSERT INTO "follow_requests"`)
	if blockCheck < 0 || requestInsert < blockCheck {
		t.Fatalf("remote follow import must check blocks and then create a follow request:\n%s", queries)
	}
	if strings.Contains(queries, `INSERT INTO "follows"`) {
		t.Fatalf("remote follow import must not follow before the remote Accept:\n%s", queries)
	}
	for _, want := range []string{`"show_reblogs","notify"`, `"languages"`, `7,9,false,true,`, `'{"ja"}'`} {
		if !strings.Contains(queries[requestInsert:], want) {
			t.Fatalf("follow request insert missing CSV option %q:\n%s", want, queries)
		}
	}
}

func TestRelationshipImportsDeliverRemoteFollowsAfterCommit(t *testing.T) {
	src, err := os.ReadFile("imports.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"processRelationshipImportRow", "processListImportRow", "processLegacyImportRelationship"} {
		body := functionBody(t, src, fn)
		collect := strings.Index(body, `deliveries = appendRelationshipBatchDelivery(deliveries, delivery)`)
		commit := strings.Index(body, `s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)`)
		deliver := strings.Index(body, `s.deliverRelationshipBatchDeliveries(deliveries)`)
		if collect < 0 || commit < 0 || deliver < 0 {
			t.Fatalf("%s must collect remote Follow deliveries in the transaction and send them afterwards", fn)
		}
		if !(collect < commit && commit < deliver) {
			t.Fatalf("%s must deliver the Follow only after the row transaction commits", fn)
		}
	}
	if !functionBodyContains(t, src, "processListImportRow", `s.relationshipImportFollowNotAllowed(tx, bulkImport.AccountID, target)`) {
		t.Fatal("processListImportRow must apply FollowService#following_not_allowed? before adding the account to the list")
	}
}

func TestImportFollowOptionUpdatesKeepOptionsTheRowLeavesOut(t *testing.T) {
	now := time.Now().UTC()
	if updates := importFollowOptionUpdates(map[string]any{"acct": "carol"}, now); updates != nil {
		t.Fatalf("row without option columns updates = %#v, want none", updates)
	}
	if updates := importFollowOptionUpdates(map[string]any{"acct": "carol", "show_reblogs": nil}, now); updates != nil {
		t.Fatalf("row with a nil option updates = %#v, want none", updates)
	}
	updates := importFollowOptionUpdates(map[string]any{"acct": "carol", "show_reblogs": "false"}, now)
	if len(updates) != 2 || updates["show_reblogs"] != false || updates["updated_at"] != now {
		t.Fatalf("row with only show_reblogs updates = %#v", updates)
	}
	updates = importFollowOptionUpdates(map[string]any{"show_reblogs": true, "notify": "true", "languages": []any{"ja", " en "}}, now)
	if updates["show_reblogs"] != true || updates["notify"] != true {
		t.Fatalf("row with all options updates = %#v", updates)
	}
	if languages, ok := updates["languages"].(models.StringArray); !ok || len(languages) != 2 || languages[0] != "ja" || languages[1] != "en" {
		t.Fatalf("languages update = %#v", updates["languages"])
	}
}
