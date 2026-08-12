package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestReadListParamsAcceptsJSON(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/v1/lists/1", strings.NewReader(`{"title":"Friends","replies_policy":"followed","exclusive":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := readListParams(c)
	if got.Title == nil || *got.Title != "Friends" {
		t.Fatalf("Title = %#v", got.Title)
	}
	if got.RepliesPolicy == nil || *got.RepliesPolicy != 1 {
		t.Fatalf("RepliesPolicy = %#v", got.RepliesPolicy)
	}
	if got.Exclusive == nil || !*got.Exclusive {
		t.Fatalf("Exclusive = %#v", got.Exclusive)
	}
}

func TestReadListParamsTracksExplicitBlankTitleLikeRailsValidation(t *testing.T) {
	e := echo.New()
	for name, req := range map[string]*http.Request{
		"json null":  httptest.NewRequest("PUT", "/api/v1/lists/1", strings.NewReader(`{"title":null}`)),
		"form blank": httptest.NewRequest("PUT", "/api/v1/lists/1", strings.NewReader("title=")),
	} {
		if strings.HasPrefix(name, "json") {
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)

		got := readListParams(c)
		if got.Title == nil || *got.Title != "" {
			t.Fatalf("%s title = %#v", name, got.Title)
		}
	}
}

func TestReadListParamsTracksExplicitBlankExclusiveLikeRailsBooleanCast(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/v1/lists/1", strings.NewReader("exclusive="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	got := readListParams(c)
	if got.Exclusive == nil || *got.Exclusive {
		t.Fatalf("blank exclusive should be explicit false like Rails boolean cast, got %#v", got.Exclusive)
	}
}

func TestListAccountIDsAcceptsJSONNumbersAndStrings(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/lists/1/accounts", strings.NewReader(`{"account_ids":[1,"2",1]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, invalid := listAccountIDs(c)
	want := []int64{1, 2}
	if invalid || !reflect.DeepEqual(got, want) {
		t.Fatalf("listAccountIDs = %#v invalid=%v, want %#v false", got, invalid, want)
	}
}

func TestListAccountIDsRejectsInvalidValuesLikeRailsAccountFind(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/lists/1/accounts", strings.NewReader(`{"account_ids":["bad"]}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if got, invalid := listAccountIDs(c); !invalid || len(got) != 0 {
		t.Fatalf("json invalid listAccountIDs = %#v invalid=%v, want empty true", got, invalid)
	}

	req = httptest.NewRequest("POST", "/api/v1/lists/1/accounts", strings.NewReader("account_ids%5B%5D=1%2C2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got, invalid := listAccountIDs(c); !invalid || len(got) != 0 {
		t.Fatalf("form invalid listAccountIDs = %#v invalid=%v, want empty true", got, invalid)
	}
}

func TestAddListAccountsMissingFollowMatchesRailsNotFound(t *testing.T) {
	src, err := os.ReadFile("lists.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "addListAccounts", `return apiError(c, http.StatusNotFound, "Record not found")`) {
		t.Fatal("addListAccounts must return 404 when Rails ListAccount#set_follow raises RecordNotFound")
	}
	if functionBodyContains(t, src, "addListAccounts", `Validation failed: Account follow relationship missing`) {
		t.Fatal("addListAccounts should not expose a Rails-incompatible 422 for missing follow relationship")
	}
}

func TestAddListAccountsRejectsDuplicateLikeRailsValidation(t *testing.T) {
	src, err := os.ReadFile("lists.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if isUniqueConstraintError(err) {`,
		`return errListAccountDuplicate`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Account has already been taken")`,
	} {
		if !functionBodyContains(t, src, "addListAccounts", want) {
			t.Fatalf("addListAccounts missing Rails duplicate validation behavior %q", want)
		}
	}
	if functionBodyContains(t, src, "addListAccounts", "OnConflict") || functionBodyContains(t, src, "addListAccounts", "DoNothing") {
		t.Fatal("addListAccounts must not silently ignore duplicate list memberships")
	}
}

func TestListAccountRowUsesRailsAccountFindWithoutSuspensionFilter(t *testing.T) {
	src, err := os.ReadFile("lists.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "listAccountRow", `tx.Where("id = ?", targetID).First(&account)`) {
		t.Fatal("listAccountRow should match Rails Account.find(account_ids)")
	}
	if functionBodyContains(t, src, "listAccountRow", `suspended_at IS NULL`) {
		t.Fatal("listAccountRow must not reject suspended accounts before Rails ListAccount validation runs")
	}
}

func TestParseRepliesPolicy(t *testing.T) {
	tests := map[string]int{
		"list":     0,
		"followed": 1,
		"none":     2,
		"2":        2,
	}
	for input, want := range tests {
		got, ok := parseRepliesPolicy(input)
		if !ok || got != want {
			t.Fatalf("parseRepliesPolicy(%q) = %d, %v; want %d, true", input, got, ok, want)
		}
	}
	if _, ok := parseRepliesPolicy("bad"); ok {
		t.Fatal("invalid replies_policy was accepted")
	}
}

func TestListTimelineRepliesPolicyMatchesRailsFeedManager(t *testing.T) {
	src, err := os.ReadFile("lists.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`case 2:`,
		`(statuses.reply = false OR statuses.in_reply_to_account_id = statuses.account_id)`,
		`case 1:`,
		`query = query.Where("1 = 1")`,
		`OR statuses.in_reply_to_account_id = statuses.account_id`,
		`FROM list_accounts reply_members`,
	} {
		if !functionBodyContains(t, src, "listTimelineQuery", want) {
			t.Fatalf("listTimelineQuery missing Rails replies policy behavior %q", want)
		}
	}
	for _, forbidden := range []string{
		`follows.target_account_id = statuses.in_reply_to_account_id`,
		`statuses.in_reply_to_account_id IS NULL`,
	} {
		if functionBodyContains(t, src, "listTimelineQuery", forbidden) {
			t.Fatalf("listTimelineQuery contains Rails-incompatible replies policy fragment %q", forbidden)
		}
	}
}

func TestListFeedRedisKeysMatchRailsFeedManager(t *testing.T) {
	cfg := config.Config{RedisNamespace: "mastodon:"}
	if got, want := homeFeedRedisKey(cfg.RedisNamespace, 42), "mastodon:feed:home:42"; got != want {
		t.Fatalf("home feed key = %q, want %q", got, want)
	}
	if got, want := listFeedRedisKey(cfg.RedisNamespace, 7), "mastodon:feed:list:7"; got != want {
		t.Fatalf("list feed key = %q, want %q", got, want)
	}
	if got, want := listFeedReblogRedisKey(cfg.RedisNamespace, 7), "mastodon:feed:list:7:reblogs"; got != want {
		t.Fatalf("list reblog key = %q, want %q", got, want)
	}
	if got, want := listFeedReblogStatusRedisKey(cfg.RedisNamespace, 7, "42"), "mastodon:feed:list:7:reblogs:42"; got != want {
		t.Fatalf("list reblog status key = %q, want %q", got, want)
	}
}

func TestListFeedRedisDeleteKeysIncludeReblogTrackingSets(t *testing.T) {
	generic := feedRedisDeleteKeys("mastodon:", "home", 42, []string{"100"})
	if !reflect.DeepEqual(generic, []string{"mastodon:feed:home:42", "mastodon:feed:home:42:reblogs", "mastodon:feed:home:42:reblogs:100"}) {
		t.Fatalf("home feed delete keys = %#v", generic)
	}
	got := listFeedRedisDeleteKeys("mastodon:", 7, []string{"42", "", "43"})
	want := []string{
		"mastodon:feed:list:7",
		"mastodon:feed:list:7:reblogs",
		"mastodon:feed:list:7:reblogs:42",
		"mastodon:feed:list:7:reblogs:43",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list feed delete keys = %#v, want %#v", got, want)
	}
}

func TestMergeAccountIntoHomeFeedMatchesRailsMergeWorkerShape(t *testing.T) {
	cacheSrc, err := os.ReadFile("list_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"mergeAccountIntoHomeFeed", `userSignedInRecently(user, time.Now().UTC())`},
		{"mergeAccountIntoHomeFeed", `s.homeTimelineQuery(&intoAccount)`},
		{"mergeAccountIntoHomeFeed", `Where("statuses.account_id = ?", fromAccountID)`},
		{"mergeAccountIntoHomeFeed", `Where("statuses.visibility IN ?", []int{0, 1, 2})`},
		{"mergeAccountIntoHomeFeed", `Limit(feedMaxItems / 4)`},
		{"mergeAccountIntoHomeFeed", `"ZCARD", timelineKey`},
		{"mergeAccountIntoHomeFeed", `query = query.Where("statuses.id > ?", oldest)`},
		{"mergeAccountIntoHomeFeed", `s.addStatusToFeedContext(ctx, "home", intoAccount.ID, status, aggregateReblogs)`},
		{"mergeAccountIntoHomeFeed", `s.trimFeedContext(ctx, "home", intoAccount.ID)`},
		{"mergeAccountIntoListFeed", `userSignedInRecently(user, time.Now().UTC())`},
		{"mergeAccountIntoListFeed", `Where("statuses.account_id = ?", fromAccountID)`},
		{"mergeAccountIntoListFeed", `Where("statuses.visibility IN ?", []int{0, 1, 2})`},
		{"mergeAccountIntoListFeed", `Limit(feedMaxItems / 4)`},
		{"mergeAccountIntoListFeed", `"ZCARD", timelineKey`},
		{"mergeAccountIntoListFeed", `query = query.Where("statuses.id > ?", oldest)`},
		{"mergeAccountIntoListFeed", `s.filterStatusFromList(ctx, database, status, list)`},
		{"mergeAccountIntoListFeed", `s.addStatusToFeedContext(ctx, "list", list.ID, status, aggregateReblogs)`},
		{"mergeAccountIntoListFeed", `s.trimFeedContext(ctx, "list", list.ID)`},
		{"unmergeAccountFromListFeed", `"ZRANGE", listFeedRedisKey(redisConfig(s.cfg).prefix, list.ID), "0", "-1"`},
		{"unmergeAccountFromListFeed", `Where("account_id = ?", fromAccountID)`},
		{"unmergeAccountFromListFeed", `Where("id IN ?", timelineIDs)`},
		{"unmergeAccountFromListFeed", `s.removeStatusFromFeedContext(ctx, "list", list.ID, status, aggregateReblogs)`},
		{"filterStatusFromList", `case 2:`},
		{"filterStatusFromList", `case 1:`},
		{"filterStatusFromList", `Table("list_accounts")`},
		{"addStatusToFeedContext", `"ZREVRANK", timelineKey, rebloggedID`},
		{"addStatusToFeedContext", `redisOptionalInt(rank, rankErr)`},
		{"addStatusToFeedContext", `"ZADD", reblogKey, "NX", statusID, rebloggedID`},
		{"addStatusToFeedContext", `"SADD", reblogSetKey, statusID`},
		{"addStatusToFeedContext", `"ZSCORE", reblogKey, statusID`},
		{"addStatusToFeedContext", `redisOptionalInt(score, scoreErr)`},
		{"addStatusToFeedContext", `feedType = feedStorageType(feedType)`},
		{"trimFeedContext", `"ZREMRANGEBYRANK", timelineKey, "0", "-"+strconv.Itoa(feedMaxItems+1)`},
		{"trimFeedContext", `"ZREVRANGE", timelineKey, strconv.Itoa(feedReblogFalloff), strconv.Itoa(feedReblogFalloff), "WITHSCORES"`},
		{"trimFeedContext", `"ZRANGEBYSCORE", reblogKey, "0", strconv.FormatInt(falloffScore, 10)`},
		{"trimFeedContext", `feedType = feedStorageType(feedType)`},
		{"removeStatusFromFeedContext", `feedType = feedStorageType(feedType)`},
		{"feedStorageType", `if feedType == "tags"`},
		{"feedStorageType", `return "home"`},
	} {
		if !functionBodyContains(t, cacheSrc, check.fn, check.want) {
			t.Fatalf("list_feed_cache.go:%s missing Rails feed merge behavior %q", check.fn, check.want)
		}
	}
	if feedMaxItems != 800 || feedReblogFalloff != 40 {
		t.Fatalf("feed constants = %d/%d, want Rails FeedManager 800/40", feedMaxItems, feedReblogFalloff)
	}
}

func TestReturningUserHomeFeedRegenerationMatchesRailsWorkerShape(t *testing.T) {
	authSrc, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, authSrc, "recordSignInWithMethod", `s.regenerateHomeFeedForReturningUser(context.Background(), *user, now)`) {
		t.Fatal("recordSignInWithMethod must trigger Rails returning-user home feed regeneration")
	}

	cacheSrc, err := os.ReadFile("list_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"regenerateHomeFeedForReturningUser", `!user.ConfirmedAt.Valid`},
		{"regenerateHomeFeedForReturningUser", `!returningUserNeedsFeedUpdate(user, now)`},
		{"regenerateHomeFeedForReturningUser", `"SET", key, "true", "NX", "EX"`},
		{"regenerateHomeFeedForReturningUser", `s.populateAccountFeeds(workerCtx, s.db, user.AccountID, user.Settings)`},
		{"regenerateHomeFeedForReturningUser", `"DEL", key`},
		{"populateHomeFeed", `s.homeTimelineQuery(&account)`},
		{"populateHomeFeed", `Limit(feedMaxItems / 2)`},
		{"populateHomeFeed", `s.addStatusToFeedContext(ctx, "home", account.ID, status, aggregateReblogs)`},
		{"populateHomeFeed", `s.trimFeedContext(ctx, "home", account.ID)`},
		{"populateAccountFeeds", `s.populateHomeFeed(ctx, database, accountID, settings)`},
		{"populateAccountFeeds", `s.populateListFeed(ctx, list, settings)`},
	} {
		if !functionBodyContains(t, cacheSrc, check.fn, check.want) {
			t.Fatalf("list_feed_cache.go:%s missing Rails regeneration behavior %q", check.fn, check.want)
		}
	}
}

func TestReturningUserNeedsFeedUpdateUsesRailsActiveDuration(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if returningUserNeedsFeedUpdate(models.User{}, now) {
		t.Fatal("missing current_sign_in_at should not regenerate")
	}
	if returningUserNeedsFeedUpdate(models.User{CurrentSignInAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}}, now) {
		t.Fatal("recent sign-in should not regenerate")
	}
	if !returningUserNeedsFeedUpdate(models.User{CurrentSignInAt: sql.NullTime{Time: now.Add(-8 * 24 * time.Hour), Valid: true}}, now) {
		t.Fatal("dormant returning user should regenerate")
	}
}

func TestRedisOptionalIntTreatsRailsRedisNilAsMissing(t *testing.T) {
	if _, ok := redisOptionalInt(nil, nil); ok {
		t.Fatal("Redis RESP nil must be treated as missing")
	}
	if _, ok := redisOptionalInt("", nil); ok {
		t.Fatal("empty bulk string from Redis nil must be treated as missing")
	}
	if got, ok := redisOptionalInt(int64(3), nil); !ok || got != 3 {
		t.Fatalf("integer redis value = %d, %v", got, ok)
	}
	if got, ok := redisOptionalInt("12", nil); !ok || got != 12 {
		t.Fatalf("string redis value = %d, %v", got, ok)
	}
	if _, ok := redisOptionalInt("bad", nil); ok {
		t.Fatal("invalid integer string must be treated as missing")
	}
	if _, ok := redisOptionalInt(int64(1), context.Canceled); ok {
		t.Fatal("redis error must be treated as missing")
	}
}

func TestFeedTargetsUseRailsAggregateReblogSetting(t *testing.T) {
	rows := []feedTargetSettingsRow{
		{ID: 42},
		{ID: 43, Settings: sql.NullString{String: `{"aggregate_reblogs":false}`, Valid: true}},
		{ID: 43, Settings: sql.NullString{String: `{"aggregate_reblogs":true}`, Valid: true}},
	}
	got := feedTargetsFromUserSettings(rows)
	want := []feedTarget{
		{ID: 42, AggregateReblogs: true},
		{ID: 43, AggregateReblogs: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("feed targets = %#v, want %#v", got, want)
	}
}

func TestExclusiveListFiltersHomeFeedInsertAtWorkerExecution(t *testing.T) {
	workerSrc, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"handleAsynqFeedInsert", `filterResult, err := s.asynqFeedInsertFilter(ctx, p, status)`},
		{"handleAsynqFeedInsert", `filterResult == feedInsertSkipHome`},
		{"handleAsynqFeedInsert", `return s.notifyFeedInsertedStatus(ctx, p, status)`},
		{"asynqFeedInsertFilter", `case "home":`},
		{"asynqFeedInsertFilter", `statusAuthorInExclusiveList(ctx, s.db, p.FeedID, status.AccountID)`},
		{"asynqFeedInsertFilter", `return feedInsertSkipHome, nil`},
	} {
		if !functionBodyContains(t, workerSrc, check.fn, check.want) {
			t.Fatalf("asynq_workers.go:%s missing exclusive home behavior %q", check.fn, check.want)
		}
	}

	cacheSrc, err := os.ReadFile("list_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Joins("JOIN list_accounts ON list_accounts.list_id = lists.id")`,
		`Where("lists.account_id = ? AND lists.exclusive = true", recipientID)`,
		`Where("list_accounts.account_id = ?", statusAccountID)`,
	} {
		if !functionBodyContains(t, cacheSrc, "statusAuthorInExclusiveList", want) {
			t.Fatalf("list_feed_cache.go:statusAuthorInExclusiveList missing %q", want)
		}
	}
}

func TestExclusiveListDoesNotFilterOwnStatuses(t *testing.T) {
	excluded, err := statusAuthorInExclusiveList(context.Background(), nil, 42, 42)
	if err != nil {
		t.Fatal(err)
	}
	if excluded {
		t.Fatal("an account's own status must remain in its home timeline")
	}
}

func TestStatusDeleteUnpushesRailsFeedCaches(t *testing.T) {
	serverSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`_ = s.removeStatusFromRailsFeeds(ctx, s.db, status)`,
		`Order(clause.Expr{SQL: "CASE WHEN id = ? THEN 0 ELSE 1 END, id", Vars: []any{statusID}})`,
	} {
		if !strings.Contains(string(serverSrc), want) {
			t.Fatalf("status deletion missing %q", want)
		}
	}
	asynqSrc, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, asynqSrc, "applyDeletedStatusRemovalSideEffects", `_ = s.removeStatusFromRailsFeeds(ctx, s.db, status)`) {
		t.Fatal("RemovalWorker side effects must unpush already-discarded statuses from Rails feeds")
	}

	cacheSrc, err := os.ReadFile("list_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.redisCommand(ctx, "ZREM", timelineKey, statusID)`,
		`s.redisCommand(ctx, "SREM", reblogSetKey, statusID)`,
		`s.redisCommand(ctx, "ZREM", reblogKey, rebloggedID)`,
		`s.redisCommand(ctx, "SMEMBERS", reblogSetKey)`,
		`s.redisCommand(ctx, "ZADD", timelineKey, otherReblogID, otherReblogID)`,
		`s.redisCommand(ctx, "DEL", reblogSetKey)`,
		`s.redisCommand(ctx, "ZREM", reblogKey, statusID)`,
	} {
		if !strings.Contains(string(cacheSrc), want) {
			t.Fatalf("Rails feed unpush missing %q", want)
		}
	}
}

func restoreUnsetEnv(t *testing.T, key string) {
	t.Helper()
	previous, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, previous)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestFeedVacuumWorkerUsesRailsInactiveFeedShape(t *testing.T) {
	if feedVacuumWorkerInterval != 24*time.Hour {
		t.Fatalf("feedVacuumWorkerInterval = %s", feedVacuumWorkerInterval)
	}
	if feedVacuumBatchSize != 1000 {
		t.Fatalf("feedVacuumBatchSize = %d", feedVacuumBatchSize)
	}
	src, err := os.ReadFile("feed_vacuum_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runFeedVacuumWorker", `s.vacuumInactiveFeeds(ctx, now.UTC())`},
		{"vacuumInactiveFeeds", `cutoff := now.Add(-time.Duration(userActiveDays()) * 24 * time.Hour)`},
		{"vacuumInactiveFeeds", `s.vacuumInactiveHomeFeeds(ctx, cutoff)`},
		{"vacuumInactiveFeeds", `s.vacuumInactiveListFeeds(ctx, cutoff)`},
		{"vacuumInactiveHomeFeeds", `Model(&models.User{})`},
		{"vacuumInactiveHomeFeeds", `Where("id > ? AND confirmed_at IS NOT NULL AND current_sign_in_at < ?", lastID, cutoff)`},
		{"vacuumInactiveHomeFeeds", `s.clearHomeFeedCacheContext(ctx, row.AccountID)`},
		{"vacuumInactiveListFeeds", `Model(&models.List{})`},
		{"vacuumInactiveListFeeds", `Joins("JOIN users ON users.account_id = lists.account_id")`},
		{"vacuumInactiveListFeeds", `Where("lists.id > ? AND users.confirmed_at IS NOT NULL AND users.current_sign_in_at < ?", lastID, cutoff)`},
		{"vacuumInactiveListFeeds", `s.clearListFeedCacheContext(ctx, listID)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runFeedVacuumWorker)") {
		t.Fatal("StartBackgroundWorkers does not start feed vacuum worker")
	}
}
