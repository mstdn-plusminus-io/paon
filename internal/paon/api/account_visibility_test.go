package api

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAccountHiddenFromAccountsShowMatchesRailsApprovalAndConfirmationGuards(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		account models.Account
		hidden  bool
	}{
		{
			name:    "remote account is visible without local user",
			account: models.Account{ID: 1, Domain: sql.NullString{String: "remote.example", Valid: true}},
		},
		{
			name:    "local confirmed approved account is visible",
			account: models.Account{ID: 2, User: models.User{ID: 20, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}}},
		},
		{
			name:    "local pending account is hidden",
			account: models.Account{ID: 3, User: models.User{ID: 30, Approved: false, ConfirmedAt: sql.NullTime{Time: now, Valid: true}}},
			hidden:  true,
		},
		{
			name:    "local unconfirmed account is hidden",
			account: models.Account{ID: 4, User: models.User{ID: 40, Approved: true}},
			hidden:  true,
		},
		{
			name:    "local account without user is hidden",
			account: models.Account{ID: 5},
			hidden:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accountHiddenFromAccountsShow(&tt.account); got != tt.hidden {
				t.Fatalf("hidden = %v, want %v", got, tt.hidden)
			}
		})
	}
}

func TestGetAccountAppliesRailsApprovalAndConfirmationGuards(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "getAccount", "accountHiddenFromAccountsShow(account)") {
		t.Fatal("getAccount must hide local pending and unconfirmed accounts like Rails AccountsController")
	}
	if functionBodyContains(t, src, "lookupAccount", "accountHiddenFromAccountsShow") {
		t.Fatal("lookupAccount should keep its ResolveAccountService-specific visibility rules")
	}
}

func TestAccountRelationshipActionsApplyRailsApprovalAndConfirmationGuards(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"followAccount", "unfollowAccount", "relationshipAccounts"} {
		if !functionBodyContains(t, src, fn, "accountHiddenFromAccountsShow(target)") {
			t.Fatalf("%s must hide local pending and unconfirmed targets like Rails AccountsController", fn)
		}
	}
}

func TestRelationshipMutationsAllowSuspendedTargetsLikeRailsSetAccount(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	if functionBodyContains(t, src, "relationshipAccounts", "target.SuspendedAt.Valid") {
		t.Fatal("relationship mutation target lookup must not hide suspended accounts; Rails set_account only applies approval and confirmation guards")
	}
	if !functionBodyContains(t, src, "followAccount", "target.SuspendedAt.Valid") {
		t.Fatal("followAccount must keep Rails FollowService suspended-target rejection")
	}
	for _, fn := range []string{"unfollowAccount", "removeFromFollowers", "blockAccount", "unblockAccount", "muteAccount", "unmuteAccount"} {
		if !functionBodyContains(t, src, fn, "relationshipsForAccounts") && !functionBodyContains(t, src, fn, "relationshipResponse(c, account.ID, target)") {
			t.Fatalf("%s must render the resolved target relationship instead of reusing /relationships suspended filtering", fn)
		}
	}
}
