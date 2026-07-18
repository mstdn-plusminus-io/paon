package api

import (
	"os"
	"strings"
	"testing"
)

func TestRelationshipCollectionAPIsCheckRailsScopes(t *testing.T) {
	type check struct {
		fn   string
		want string
	}
	checks := map[string][]check{
		"account_collections.go": {
			{"blocks", `s.requireAccountScope(c, "follow", "read", "read:blocks")`},
			{"mutes", `s.requireAccountScope(c, "follow", "read", "read:mutes")`},
		},
		"domain_blocks.go": {
			{"domainBlocks", `s.requireAccountScope(c, "follow", "read", "read:blocks")`},
			{"createDomainBlock", `s.requireAccountScope(c, "follow", "write", "write:blocks")`},
			{"deleteDomainBlock", `s.requireAccountScope(c, "follow", "write", "write:blocks")`},
		},
		"follow_requests.go": {
			{"followRequests", `s.requireAccountScope(c, "follow", "read", "read:follows")`},
			{"followRequestAccounts", `s.requireAccountScope(c, "follow", "write", "write:follows")`},
		},
		"account_discovery.go": {
			{"endorsements", `s.requireAccountScope(c, "read", "read:accounts")`},
			{"familiarFollowers", `s.requireAccountScope(c, "read", "read:follows")`},
		},
		"lists.go": {
			{"accountLists", `s.requireAccountScope(c, "read", "read:lists")`},
		},
	}
	for file, fileChecks := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range fileChecks {
			if !functionBodyContains(t, src, check.fn, check.want) {
				t.Fatalf("%s:%s does not contain %q", file, check.fn, check.want)
			}
		}
	}
}

func TestDirectFollowMergesRailsHomeFeedLikeMergeWorker(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"followAccount", `sourceNotFollowingAnyone := s.accountNotFollowingAnyone(c.Request().Context(), account.ID)`},
		{"followAccount", `s.markHomeFeedAsPartial(c.Request().Context(), account.ID)`},
		{"followAccount", `s.mergeAfterDirectFollowBestEffort(c.Request().Context(), target.ID, *account)`},
		{"mergeAfterDirectFollowBestEffort", `s.mergeAccountIntoHomeFeed(workerCtx, s.db, fromAccountID, intoAccount)`},
		{"mergeAfterDirectFollowBestEffort", `redisConfig(s.cfg).prefix+"account:"+strconv.FormatInt(intoAccount.ID, 10)+":regeneration"`},
		{"accountNotFollowingAnyone", `Model(&models.Follow{}).Where("account_id = ?", accountID).Count(&count)`},
		{"markHomeFeedAsPartial", `"SET", redisConfig(s.cfg).prefix+"account:"+strconv.FormatInt(accountID, 10)+":regeneration", "true", "NX", "EX"`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("relationships.go:%s missing Rails MergeWorker behavior %q", check.fn, check.want)
		}
	}
}

func TestRelationshipResponsesIncludeFollowLanguages(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, want := range map[string]string{
		"relationshipsForAccounts": `Languages:           languagesFromFollow(follow, req)`,
		"followMap":                `Languages: []string(row.Languages)`,
		"followRequestMap":         `Languages: []string(row.Languages)`,
		"languagesFromFollow":      `return append([]string{}, source.Languages...)`,
	} {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("relationships.go:%s does not contain %q", fn, want)
		}
	}
}

func TestRelationshipResponsesIncludeDomainBlocking(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"relationshipsForAccounts", `domainsByID := map[int64]string{}`},
		{"relationshipsForAccounts", `domainBlocking, err := s.domainBlockingMap(accountID, domainsByID)`},
		{"relationshipsForAccounts", `DomainBlocking:      domainBlocking[id]`},
		{"domainBlockingMap", `models.AccountDomainBlock{}`},
		{"domainBlockingMap", `account_id = ? AND lower(domain) IN ?`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("relationships.go:%s does not contain %q", check.fn, check.want)
		}
	}
}

func TestNormalizeRelationshipLanguagesMatchesRailsArrayParams(t *testing.T) {
	got := normalizeRelationshipLanguages([]string{"en,ja", "EN", " ja "})
	want := []string{"en,ja", "en", "ja"}
	if len(got) != len(want) {
		t.Fatalf("languages = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("languages = %#v, want %#v", got, want)
		}
	}
	if !validRelationshipLanguages([]string{"en", "ja"}) {
		t.Fatal("supported follow languages should be accepted")
	}
	if validRelationshipLanguages([]string{"en,ja"}) {
		t.Fatal("comma-combined follow language should be rejected like Rails LanguageValidator")
	}
}

func TestFollowAccountUsesRailsRequestFollowSemantics(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"followAccount", `if followRequiresRequest(account, target) {`},
		{"followAccount", `if target.Local() {`},
		{"followRequiresRequest", `target.Locked || source.SilencedAt.Valid || (!target.Local() && target.Protocol == 1)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("relationships.go:%s does not contain %q", check.fn, check.want)
		}
	}
}

func TestFollowAccountWiresRailsFollowLimitOnlyForNewRelationships(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "followAccount", `if reached, limit, err := s.followLimitReached(c.Request().Context(), *account); err != nil`) {
		t.Fatal("followAccount must enforce Rails FollowLimitValidator before creating follows/follow requests")
	}
	if !functionBodyContains(t, src, "followAccount", `return apiError(c, http.StatusUnprocessableEntity, followLimitReachedMessage(limit))`) {
		t.Fatal("followAccount must expose Rails follow-limit validation as a 422 API error")
	}
	body := functionBody(t, src, "followAccount")
	existingUpdate := strings.Index(body, `if existingRelationshipUpdated {`)
	limitCheck := strings.Index(body, `s.followLimitReached(c.Request().Context(), *account)`)
	createFollow := strings.Index(body, `models.Follow{CreatedAt: now`)
	createRequest := strings.Index(body, `models.FollowRequest{CreatedAt: now`)
	if existingUpdate < 0 || limitCheck < 0 || createFollow < 0 || createRequest < 0 {
		t.Fatalf("followAccount missing expected update/limit/create flow")
	}
	if !(existingUpdate < limitCheck && limitCheck < createRequest && limitCheck < createFollow) {
		t.Fatalf("follow limit must run after existing relationship updates and before new follow/request creation")
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"followAccount", `!errors.Is(err, gorm.ErrRecordNotFound)`},
		{"followLimitReachedInDB", `!errors.Is(err, gorm.ErrRecordNotFound)`},
		{"unfollowAccount", `!errors.Is(err, gorm.ErrRecordNotFound)`},
		{"removeFromFollowers", `errors.Is(err, gorm.ErrRecordNotFound)`},
		{"deleteFollowEdgeReturningFollow", `errors.Is(err, gorm.ErrRecordNotFound)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s must tolerate wrapped gorm.ErrRecordNotFound; missing %q", check.fn, check.want)
		}
	}
	for _, forbidden := range []string{`err == gorm.ErrRecordNotFound`, `err != gorm.ErrRecordNotFound`} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("relationships.go must not directly compare gorm.ErrRecordNotFound; found %q", forbidden)
		}
	}
}

func TestBlockAccountAppliesRailsAfterBlockCleanup(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"blockAccount", `s.clearAfterBlockFeedCaches(c.Request().Context(), account.ID, target.ID)`},
		{"afterBlockServiceCleanup", `Delete(&models.Notification{})`},
		{"afterBlockServiceCleanup", `DELETE FROM account_conversations`},
		{"afterBlockServiceCleanup", `ANY(participant_account_ids)`},
		{"clearAfterBlockFeedCaches", `s.enqueueBlockTask(accountID, targetID)`},
		{"runAfterBlockWorkerEffects", `afterBlockServiceCleanup(database, accountID, targetID)`},
		{"runAfterBlockWorkerEffects", `s.clearAccountFromHomeFeed(ctx, database, accountID, targetID)`},
		{"runAfterBlockWorkerEffects", `Where("lists.account_id = ?", accountID)`},
		{"runAfterBlockWorkerEffects", `s.clearAccountFromListFeed(ctx, database, listID, accountID, targetID)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("relationships.go:%s does not contain %q", check.fn, check.want)
		}
	}
}
