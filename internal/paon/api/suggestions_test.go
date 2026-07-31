package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestDeleteSuggestionRequiresAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("DELETE", "/api/v1/suggestions/1", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	err := s.deleteSuggestion(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestStaffSuggestionRefsParsesRailsSetting(t *testing.T) {
	got := staffSuggestionRefs("example.test", " @Alice, bob@example.test, carol@remote.test, , @alice ")
	if len(got) != 4 {
		t.Fatalf("refs = %#v", got)
	}
	if got[0] != (staffSuggestionRef{Username: "alice", Domain: ""}) {
		t.Fatalf("first ref = %#v", got[0])
	}
	if got[1] != (staffSuggestionRef{Username: "bob", Domain: ""}) {
		t.Fatalf("second ref = %#v", got[1])
	}
	if got[2] != (staffSuggestionRef{Username: "carol", Domain: "remote.test"}) {
		t.Fatalf("third ref = %#v", got[2])
	}
	if got[3] != (staffSuggestionRef{Username: "alice", Domain: ""}) {
		t.Fatalf("fourth ref = %#v", got[3])
	}
}

func TestStaffSuggestionConditionMatchesLocalAndRemoteAccounts(t *testing.T) {
	condition, args := staffSuggestionCondition([]staffSuggestionRef{
		{Username: "alice"},
		{Username: "bob", Domain: "remote.test"},
	})
	if condition != "(lower(accounts.username) = ? AND accounts.domain IS NULL) OR (lower(accounts.username) = ? AND lower(accounts.domain) = ?)" {
		t.Fatalf("condition = %q", condition)
	}
	if len(args) != 3 || args[0] != "alice" || args[1] != "bob" || args[2] != "remote.test" {
		t.Fatalf("args = %#v", args)
	}
}

func TestAppendSuggestedAccountsPreservesOrderAndSkipsPreviousSources(t *testing.T) {
	skip := map[int64]struct{}{1: {}}
	selected := []suggestedAccount{{Account: models.Account{ID: 2}, Source: suggestionSourceStaff}}
	got := appendSuggestedAccounts(selected, []suggestedAccount{
		{Account: models.Account{ID: 1}, Source: suggestionSourceGlobal},
		{Account: models.Account{ID: 2}, Source: suggestionSourceGlobal},
		{Account: models.Account{ID: 3}, Source: suggestionSourceGlobal},
		{Account: models.Account{ID: 3}, Source: suggestionSourceGlobal},
		{Account: models.Account{ID: 4}, Source: suggestionSourceGlobal},
	}, skip, 4)
	if len(got) != 4 || got[0].Account.ID != 2 || got[1].Account.ID != 3 || got[2].Account.ID != 3 || got[3].Account.ID != 4 {
		t.Fatalf("suggestions = %#v", got)
	}
	if _, ok := skip[3]; !ok {
		t.Fatalf("skip was not updated: %#v", skip)
	}
}

func TestSuggestionInteractionsRedisKeyMatchesRailsNamespaceShape(t *testing.T) {
	got := suggestionInteractionsRedisKey(config.Config{RedisNamespace: "mastodon:"}, 42)
	if got != "mastodon:interactions:42" {
		t.Fatalf("key = %q", got)
	}
}

func TestPotentialFriendshipWeightsMatchRailsTracker(t *testing.T) {
	for action, want := range map[string]int{"reply": 1, "favourite": 10, "reblog": 20} {
		got, ok := potentialFriendshipWeight(action)
		if !ok || got != want {
			t.Fatalf("weight(%s) = %d, %v", action, got, ok)
		}
	}
	if _, ok := potentialFriendshipWeight("follow"); ok {
		t.Fatal("unexpected follow potential friendship weight")
	}
}

func TestPotentialFriendshipTrackerWritesRailsRedisShape(t *testing.T) {
	src, err := os.ReadFile("suggestions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`suggestionInteractionsRedisKey(s.cfg, accountID)`,
		`"ZINCRBY", key, strconv.Itoa(weight), strconv.FormatInt(targetAccountID, 10)`,
		`"ZREMRANGEBYRANK", key, "0", "-"+strconv.Itoa(potentialFriendshipMaxItems)`,
		`"EXPIRE", key, strconv.Itoa(potentialFriendshipExpireSeconds)`,
		`Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID)`,
	} {
		if !functionBodyContains(t, src, "recordPotentialFriendship", want) {
			t.Fatalf("recordPotentialFriendship missing %q", want)
		}
	}
}

func TestPotentialFriendshipRemoveUsesRailsRedisShape(t *testing.T) {
	src, err := os.ReadFile("suggestions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"ZREM", suggestionInteractionsRedisKey(s.cfg, accountID), strconv.FormatInt(targetAccountID, 10)`,
		`potentialFriendshipWriteContext(ctx)`,
	} {
		if !functionBodyContains(t, src, "removePotentialFriendship", want) {
			t.Fatalf("removePotentialFriendship missing %q", want)
		}
	}
}

func TestPotentialFriendshipTrackerCalledFromRailsInteractionEvents(t *testing.T) {
	checks := map[string][]string{
		"server.go": {
			`s.recordPotentialFriendship(c.Request().Context(), account.ID, target.AccountID, "reblog")`,
			`s.recordPotentialFriendship(c.Request().Context(), account.ID, joinStatus.AccountID, "favourite")`,
		},
		"local_status_postcommit.go": {
			`s.recordPotentialFriendship(ctx, effects.Account.ID, created.InReplyToAccountID.Int64, "reply")`,
		},
		"scheduled_status_publish.go": {
			`s.recordPotentialFriendship(ctx, account.ID, created.InReplyToAccountID.Int64, "reply")`,
		},
	}
	for file, wants := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(src), want) {
				t.Fatalf("%s missing %q", file, want)
			}
		}
	}
}

func TestPotentialFriendshipRemovedFromRailsRelationshipEvents(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(src), `s.removePotentialFriendship(c.Request().Context(), account.ID, target.ID)`); count < 3 {
		t.Fatalf("relationships.go removePotentialFriendship count = %d", count)
	}
	for _, want := range []string{
		`if existingRelationshipUpdated {`,
		`s.invalidateBlockRelationshipCaches(c.Request().Context(), account.ID, target.ID)`,
		`s.invalidateMuteRelationshipCaches(c.Request().Context(), account.ID, target.ID)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("relationships.go missing %q", want)
		}
	}
}

func TestSuggestionIDsFromRedisMembersFiltersInvalidSkippedAndDuplicates(t *testing.T) {
	skip := map[int64]struct{}{7: {}, 9: {}}
	got := suggestionIDsFromRedisMembers([]string{"9", "bad", "8", "8", "7", "6", "5"}, skip, 2)
	if len(got) != 2 || got[0] != 8 || got[1] != 6 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestSuggestionsUseRequestLocaleForGlobalRecommendations(t *testing.T) {
	src, err := os.ReadFile("suggestions.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"suggestionsV2", `requestSuggestionLocale(c, s.cfg.DefaultLocale)`},
		{"suggestedAccountsFromGlobalRecommendations", `s.suggestedAccountsFromRedisFollowRecommendations(accountID, skip, locale, limitValue)`},
		{"suggestedAccountsFromRedisFollowRecommendations", `followRecommendationsRedisKey(s.cfg.RedisNamespace, followRecommendationLocale(locale))`},
		{"suggestedAccountsFromRedisFollowRecommendations", `"ZREVRANGE", followRecommendationsRedisKey(s.cfg.RedisNamespace, followRecommendationLocale(locale)), "0", "-1"`},
		{"suggestedAccountsFromRedisFollowRecommendations", `suggestionIDsFromRedisMembers(members, skip, limitValue)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestSuggestionsV2UsesRailsSourcesWithoutDatabaseFallback(t *testing.T) {
	src, err := os.ReadFile("suggestions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.suggestedAccountsFromStaffSetting(accountID, skip, limitValue)`,
		`s.suggestedAccountsFromPastInteractions(accountID, skip, limitValue-len(selected))`,
		`s.suggestedAccountsFromGlobalRecommendations(accountID, skip, locale, limitValue-len(selected))`,
	} {
		if !functionBodyContains(t, src, "suggestedAccounts", want) {
			t.Fatalf("suggestedAccounts missing %q", want)
		}
	}
	for _, unexpected := range []string{
		`suggestedAccountsFallback`,
		`global_follow_recommendations`,
		`suggestion_account_stats`,
	} {
		if functionBodyContains(t, src, "suggestedAccounts", unexpected) || functionBodyContains(t, src, "suggestedAccountsFromGlobalRecommendations", unexpected) {
			t.Fatalf("suggestions API must not use Rails-incompatible fallback %q", unexpected)
		}
	}
}

func TestSuggestionsV1UsesRailsPastInteractionsSourceOnly(t *testing.T) {
	src, err := os.ReadFile("suggestions.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "suggestionsV1", `s.suggestedAccountsFromPastInteractions(account.ID, map[int64]struct{}{}, limit(c, 40, 80))`) {
		t.Fatal("suggestionsV1 must use Rails Api::V1::SuggestionsController PastInteractionsSource semantics")
	}
	if functionBodyContains(t, src, "suggestionsV1", `s.suggestedAccounts(account.ID`) {
		t.Fatal("suggestionsV1 must not use v2/global/staff/fallback suggestion aggregation")
	}
}

func TestSuggestionsUseRailsDefaultAccountsLimit(t *testing.T) {
	src, err := os.ReadFile("suggestions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"suggestionsV2", `s.suggestedAccounts(account.ID, requestSuggestionLocale(c, s.cfg.DefaultLocale), limit(c, 40, 80))`},
		{"suggestionsV1", `s.suggestedAccountsFromPastInteractions(account.ID, map[int64]struct{}{}, limit(c, 40, 80))`},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing Rails DEFAULT_ACCOUNTS_LIMIT fragment %q", check.functionName, check.want)
		}
	}
}

func TestSuggestionQueriesMatchRailsSourceScopes(t *testing.T) {
	src, err := os.ReadFile("suggestions.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Joins("LEFT JOIN users suggestion_users ON suggestion_users.account_id = accounts.id")`,
		`Where("accounts.suspended_at IS NULL")`,
		`Where("accounts.moved_to_account_id IS NULL")`,
		`Where("(accounts.domain IS NOT NULL OR (suggestion_users.approved = ? AND suggestion_users.confirmed_at IS NOT NULL))", true)`,
	} {
		if !functionBodyContains(t, src, "suggestionSearchableAccountQuery", want) {
			t.Fatalf("suggestionSearchableAccountQuery missing Rails Account.searchable condition %q", want)
		}
	}
	if !functionBodyContains(t, src, "suggestedAccountsFromPastInteractions", `applySuggestionSkipIDs(s.suggestionSearchableAccountQuery(), skip)`) {
		t.Fatal("past-interaction suggestions must use only Account.searchable like Rails")
	}
	for _, unexpected := range []string{
		`suggestion_existing_follows`,
		`suggestion_existing_follow_requests`,
		`suggestion_account_blocks`,
		`suggestion_account_mutes`,
		`suggestion_domain_blocks`,
	} {
		if functionBodyContains(t, src, "suggestionSearchableAccountQuery", unexpected) {
			t.Fatalf("suggestionSearchableAccountQuery must not include followable/exclusion condition %q", unexpected)
		}
	}
	if !functionBodyContains(t, src, "suggestionFollowableAccountQuery", `Joins("LEFT JOIN follows suggestion_existing_follows ON suggestion_existing_follows.account_id = ? AND suggestion_existing_follows.target_account_id = accounts.id", accountID)`) {
		t.Fatal("suggestionFollowableAccountQuery must exclude existing follows like Account.followable_by")
	}
	if !functionBodyContains(t, src, "suggestionFollowableAccountQuery", `Joins("LEFT JOIN follow_requests suggestion_existing_follow_requests ON suggestion_existing_follow_requests.account_id = ? AND suggestion_existing_follow_requests.target_account_id = accounts.id", accountID)`) {
		t.Fatal("suggestionFollowableAccountQuery must exclude existing follow requests like Account.followable_by")
	}
	if !functionBodyContains(t, src, "suggestionFollowableAccountQuery", `Joins("LEFT JOIN mutes suggestion_account_mutes ON suggestion_account_mutes.account_id = ? AND suggestion_account_mutes.target_account_id = accounts.id", accountID)`) {
		t.Fatal("suggestionAccountBaseQuery must exclude all mute rows like Account#excluded_from_timeline_account_ids")
	}
	if functionBodyContains(t, src, "suggestionFollowableAccountQuery", `suggestion_account_mutes.expires_at`) {
		t.Fatal("suggestionAccountBaseQuery must not filter mutes by expires_at")
	}
	if functionBodyContains(t, src, "suggestionSearchableAccountQuery", `accounts.silenced_at IS NULL`) || functionBodyContains(t, src, "suggestionFollowableAccountQuery", `accounts.silenced_at IS NULL`) {
		t.Fatal("suggestionAccountBaseQuery must not add without_silenced; Rails suggestion sources use Account.searchable")
	}
}
