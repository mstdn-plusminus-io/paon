package api

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestHideFollowCollectionForSuspendedAccount(t *testing.T) {
	s := &Server{}
	hidden, err := s.hideFollowCollection(&models.Account{SuspendedAt: sql.NullTime{Time: time.Now(), Valid: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hidden {
		t.Fatal("expected suspended account collections to be hidden")
	}
}

func TestHideFollowCollectionForHiddenCollectionsUnlessSelf(t *testing.T) {
	s := &Server{}
	target := &models.Account{ID: 10, HideCollections: sql.NullBool{Bool: true, Valid: true}}

	hidden, err := s.hideFollowCollection(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hidden {
		t.Fatal("expected hidden collections to be hidden for anonymous users")
	}

	hidden, err = s.hideFollowCollection(target, &models.Account{ID: 10})
	if err != nil {
		t.Fatal(err)
	}
	if hidden {
		t.Fatal("expected own hidden collections to be visible")
	}
}

func TestAccountFollowCollectionsUseRailsMaxIDPaginationOnly(t *testing.T) {
	src, err := os.ReadFile("account_follows.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"accountFollowers": {
			`limitValue := limit(c, 40, 80)`,
			`limitOnlyPaginationLink(c, follows[0].ID, follows[len(follows)-1].ID, "since_id", len(follows) == limitValue)`,
		},
		"accountFollowing": {
			`limitValue := limit(c, 40, 80)`,
			`limitOnlyPaginationLink(c, follows[0].ID, follows[len(follows)-1].ID, "since_id", len(follows) == limitValue)`,
		},
		"applyFollowPagination": {
			`if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id")`,
			`if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id")`,
			`return db.Order(table + ".id DESC")`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("account_follows.go:%s does not contain %q", fn, want)
			}
		}
	}
	for _, unexpected := range []string{
		`QueryParam("min_id")`,
		`reverseFollows`,
	} {
		if strings.Contains(string(src), unexpected) {
			t.Fatalf("account_follows.go must ignore unsupported Rails follow collection param/branch %q", unexpected)
		}
	}
}

func TestAccountFollowCollectionsMatchRailsAccountIDExclusionsOnly(t *testing.T) {
	src, err := os.ReadFile("account_follows.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "applyFollowCollectionExclusions", `return applyFollowCollectionExclusionsByIDExpression(query, current, accountTable+".id")`) {
		t.Fatal("follow collection exclusion wrapper must keep Rails account-table id semantics")
	}
	for _, want := range []string{
		`blocks.account_id = ? AND blocks.target_account_id = ` + "`+accountIDExpression+`",
		`blocks.account_id = ` + "`+accountIDExpression+`" + ` AND blocks.target_account_id = ?`,
		`mutes.account_id = ? AND mutes.target_account_id = ` + "`+accountIDExpression+`",
	} {
		if !functionBodyContains(t, src, "applyFollowCollectionExclusionsByIDExpression", want) {
			t.Fatalf("follow collection exclusion missing Rails account-id condition %q", want)
		}
	}
	if functionBodyContains(t, src, "applyFollowCollectionExclusionsByIDExpression", "account_domain_blocks") {
		t.Fatal("follow collection exclusions must match Rails excluded_from_timeline_account_ids and not add domain-block filtering")
	}
}

func TestAccountFollowCollectionsSkipTimelineExclusionsForOwnCollections(t *testing.T) {
	src, err := os.ReadFile("account_follows.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"accountFollowers", "accountFollowing"} {
		if !functionBodyContains(t, src, fn, "if current != nil && current.ID != target.ID {") {
			t.Fatalf("%s must match Rails and only apply excluded_from_timeline filters when viewing another account", fn)
		}
		if !functionBodyContains(t, src, fn, `query = applyFollowCollectionExclusions(query, current, "accounts")`) {
			t.Fatalf("%s must still apply excluded_from_timeline filters for other accounts", fn)
		}
	}
}
