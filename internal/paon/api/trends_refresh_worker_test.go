package api

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestTrendsRefreshWorkerUsesRailsTrendTables(t *testing.T) {
	src, err := os.ReadFile("trends_refresh_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runTrendsRefreshWorker", `s.refreshTrends(ctx, now.UTC())`},
		{"runTrendsRefreshWorker", `s.requestTrendsReview(ctx, now.UTC())`},
		{"refreshTrends", `s.refreshTagTrends(ctx, now)`},
		{"refreshTrends", `s.refreshStatusTrends(ctx, now)`},
		{"refreshTrends", `s.refreshPreviewCardTrends(ctx, now)`},
		{"recordTagTrendUse", `"INCRBY", usesKey, "1"`},
		{"recordTagTrendUse", `"PFADD", accountsKey, strconv.FormatInt(accountID, 10)`},
		{"recordTagTrendUse", `"SADD", usedKey, strconv.FormatInt(tagID, 10)`},
		{"recordStatusTrendUse", `trendUsedKey(s.cfg.RedisNamespace, "trending_statuses", now)`},
		{"recordStatusTrendUse", `"SADD", usedKey, strconv.FormatInt(statusID, 10)`},
		{"recordStatusTrendUse", `"EXPIRE", usedKey, "86400"`},
		{"statusTrendCandidateIDs", `"SMEMBERS", trendUsedKey(s.cfg.RedisNamespace, "trending_statuses", now)`},
		{"recordPreviewCardTrendUse", `trendUsedKey(s.cfg.RedisNamespace, "trending_links", now)`},
		{"recordPreviewCardTrendUse", `linkHistoryRedisKey(s.cfg.RedisNamespace, previewCardID, day, false)`},
		{"recordPreviewCardTrendUse", `"PFADD", accountsKey, strconv.FormatInt(accountID, 10)`},
		{"recordPreviewCardTrendUse", `"SADD", usedKey, strconv.FormatInt(previewCardID, 10)`},
		{"recordPreviewCardTrendUseForStatus", `Joins("JOIN preview_cards_statuses ON preview_cards_statuses.preview_card_id = preview_cards.id")`},
		{"recordPreviewCardTrendUseForStatus", `Where("statuses.id = ? AND statuses.deleted_at IS NULL AND statuses.visibility = ?", statusID, 0)`},
		{"recordPreviewCardTrendUseForStatus", `s.recordPreviewCardTrendUse(ctx, accountID, previewCardID, now)`},
		{"previewCardTrendCandidateIDs", `"SMEMBERS", trendUsedKey(s.cfg.RedisNamespace, "trending_links", now)`},
		{"previewCardTrendScore", `s.previewCardTrendRedisCounts(ctx, card.ID, now)`},
		{"refreshTagTrends", `s.replaceTrendZSets(ctx, "trending_tags", items, allowed)`},
		{"replaceTrendZSets", `s.cfg.RedisNamespace+prefix+":all"`},
		{"replaceTrendZSets", `s.cfg.RedisNamespace+prefix+":allowed"`},
		{"calculateStatusTrendScores", `Columns:   []clause.Column{{Name: "status_id"}}`},
		{"calculatePreviewCardTrendScores", `Columns:   []clause.Column{{Name: "preview_card_id"}}`},
		{"recalculateStatusTrendRanks", `UPDATE status_trends SET rank = t0.calculated_rank`},
		{"recalculatePreviewCardTrendRanks", `UPDATE preview_card_trends SET rank = t0.calculated_rank`},
		{"requestTrendsReview", `s.requestTagTrendReviews(ctx, now)`},
		{"requestTrendsReview", `!s.trendsEnabled()`},
		{"requestTrendsReview", `s.settingBoolValue("trendable_by_default", false)`},
		{"requestTrendsReview", `s.sendTrendsReviewMails(items)`},
		{"requestTagTrendReviews", `s.trendZSetMembersWithScores(ctx, "trending_tags:all", 0, -1)`},
		{"requestTagTrendReviews", `"requested_review_at": now`},
		{"requestStatusTrendReviews", `!statusRequiresTrendReviewNotification(trend.Status)`},
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
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runTrendsRefreshWorker)") {
		t.Fatal("StartBackgroundWorkers does not start trends refresh worker")
	}
}

func TestStatusCreatePathsRecordTagTrendUse(t *testing.T) {
	checks := []struct {
		file string
		fn   string
		want string
	}{
		{"server.go", "createStatus", "s.recordTagTrendUse(c.Request().Context(), account.ID, created.Visibility, indexedTagIDs, now)"},
		{"scheduled_status_publish.go", "publishScheduledStatus", "s.recordTagTrendUse(ctx, account.ID, created.Visibility, indexedTagIDs, now)"},
		{"activitypub_inbox.go", "processActivityPubCreateNote", "s.recordTagTrendUse(context.Background(), actor.ID, status.Visibility, affectedTagIDs, status.CreatedAt)"},
	}
	for _, check := range checks {
		src, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s:%s missing %q", check.file, check.fn, check.want)
		}
	}
}

func TestStatusTrendUsedIDsRecordedFromRailsStatusEvents(t *testing.T) {
	checks := []struct {
		file string
		fn   string
		want string
	}{
		{"server.go", "createStatus", "s.recordStatusTrendUse(c.Request().Context(), created.ID, created.CreatedAt)"},
		{"server.go", "reblogStatus", "s.recordStatusTrendUse(c.Request().Context(), target.ID, createdStatus.CreatedAt)"},
		{"server.go", "toggleStatusJoin", "s.recordStatusTrendUse(c.Request().Context(), status.ID, favourite.CreatedAt)"},
		{"scheduled_status_publish.go", "publishScheduledStatus", "s.recordStatusTrendUse(ctx, created.ID, created.CreatedAt)"},
		{"activitypub_inbox.go", "processActivityPubLike", "s.recordStatusTrendUse(context.Background(), status.ID, now)"},
		{"activitypub_inbox.go", "processActivityPubAnnounce", "s.recordStatusTrendUse(context.Background(), target.ID, reblog.CreatedAt)"},
	}
	for _, check := range checks {
		src, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s:%s missing %q", check.file, check.fn, check.want)
		}
	}
}

func TestLinkTrendUseRecordedFromRailsLinkEvents(t *testing.T) {
	checks := []struct {
		file string
		fn   string
		want string
	}{
		{"link_cards.go", "fetchLinkCardForStatus", "s.recordPreviewCardTrendUseForStatus(ctx, status.AccountID, status.ID, status.Visibility, time.Now().UTC())"},
		{"server.go", "reblogStatus", "s.recordPreviewCardTrendUseForStatus(c.Request().Context(), account.ID, target.ID, createdStatus.Visibility, createdStatus.CreatedAt)"},
		{"activitypub_inbox.go", "processActivityPubAnnounce", "s.recordPreviewCardTrendUseForStatus(context.Background(), actor.ID, target.ID, reblog.Visibility, reblog.CreatedAt)"},
	}
	for _, check := range checks {
		src, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s:%s missing %q", check.file, check.fn, check.want)
		}
	}
}

func TestStatusTrendScoreMatchesRailsDecayShape(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	status := models.Status{
		ID:         10,
		CreatedAt:  now.Add(-time.Hour),
		Visibility: 0,
		Language:   sql.NullString{String: "en", Valid: true},
		Account: models.Account{
			Discoverable: sql.NullBool{Bool: true, Valid: true},
		},
		StatusStat: models.StatusStat{ReblogsCount: 3, FavouritesCount: 3},
	}
	score := statusTrendScore(status, now)
	if score <= 0 || score >= 25 {
		t.Fatalf("score = %f", score)
	}
	status.StatusStat = models.StatusStat{ReblogsCount: 1, FavouritesCount: 1}
	if got := statusTrendScore(status, now); got != 0 {
		t.Fatalf("below threshold score = %f", got)
	}
	status.StatusStat = models.StatusStat{ReblogsCount: 3, FavouritesCount: 3}
	status.Account.SilencedAt = sql.NullTime{Time: now, Valid: true}
	if got := statusTrendScore(status, now); got != 0 {
		t.Fatalf("silenced account score = %f", got)
	}
}

func TestTrendHelpers(t *testing.T) {
	if !statusTrendAllowed(models.Status{Trendable: sql.NullBool{Bool: true, Valid: true}}) {
		t.Fatal("status explicit trendable should be allowed")
	}
	if !statusTrendAllowed(models.Status{Account: models.Account{Trendable: sql.NullBool{Bool: true, Valid: true}}}) {
		t.Fatal("status should inherit account trendable")
	}
	if previewCardTrendAllowed(models.PreviewCard{}) {
		t.Fatal("preview card without explicit trendable should not be allowed")
	}
	if !tagTrendUsable(models.Tag{}) {
		t.Fatal("tag without explicit usable should be usable")
	}
	if tagTrendUsable(models.Tag{Usable: sql.NullBool{Bool: false, Valid: true}}) {
		t.Fatal("tag with explicit unusable should not be usable")
	}
	if got := domainFromURL("https://News.Example/path"); got != "news.example" {
		t.Fatalf("domain = %q", got)
	}
	if got := trendUsedKey("mastodon:", "trending_tags", time.Unix(1, 0).UTC()); got != "mastodon:trending_tags:used:0" {
		t.Fatalf("trendUsedKey = %q", got)
	}
	if got := linkHistoryRedisKey("mastodon:", 42, time.Unix(1, 0).UTC(), false); got != "mastodon:activity:links:42:0" {
		t.Fatalf("linkHistoryRedisKey uses = %q", got)
	}
	if got := linkHistoryRedisKey("mastodon:", 42, time.Unix(1, 0).UTC(), true); got != "mastodon:activity:links:42:0:accounts" {
		t.Fatalf("linkHistoryRedisKey accounts = %q", got)
	}
	batches := int64Batches([]int64{1, 2, 3}, 2)
	if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 1 {
		t.Fatalf("batches = %#v", batches)
	}
}
