package api

import (
	"os"
	"testing"
	"time"
)

func TestFollowRecommendationsWorkerConstantsMatchRailsScheduler(t *testing.T) {
	if followRecommendationsWorkerInterval != 24*time.Hour {
		t.Fatalf("followRecommendationsWorkerInterval = %s", followRecommendationsWorkerInterval)
	}
	if followRecommendationsSetSize != 100 {
		t.Fatalf("followRecommendationsSetSize = %d", followRecommendationsSetSize)
	}
}

func TestMergeFollowRecommendationFallbacksBoostsLocalizedRanks(t *testing.T) {
	got := mergeFollowRecommendationFallbacks(
		[]followRecommendationRef{{AccountID: 1, Rank: 2}, {AccountID: 2, Rank: 1}},
		[]followRecommendationRef{{AccountID: 3, Rank: 10}, {AccountID: 2, Rank: 9}, {AccountID: 4, Rank: 8}},
		4,
	)
	if len(got) != 4 {
		t.Fatalf("recommendations = %#v", got)
	}
	if got[0].AccountID != 1 || got[0].Rank != 12 || got[1].AccountID != 2 || got[1].Rank != 11 {
		t.Fatalf("localized ranks were not boosted above fallback: %#v", got)
	}
	if got[2].AccountID != 3 || got[3].AccountID != 4 {
		t.Fatalf("fallback merge order = %#v", got)
	}
}

func TestFollowRecommendationLocaleUsesRailsLanguageOnlyShape(t *testing.T) {
	cases := map[string]string{
		"ja":    "ja",
		"en-US": "en",
		"pt_BR": "pt",
		"":      "en",
	}
	for in, want := range cases {
		if got := followRecommendationLocale(in); got != want {
			t.Fatalf("locale %q = %q, want %q", in, got, want)
		}
	}
}

func TestFollowRecommendationsWorkerUsesRailsSchedulerShape(t *testing.T) {
	src, err := os.ReadFile("follow_recommendations_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runFollowRecommendationsWorker", `s.refreshFollowRecommendations(ctx)`},
		{"refreshFollowRecommendations", `s.refreshFollowRecommendationMaterializedViews(ctx)`},
		{"refreshFollowRecommendations", `s.followRecommendationRefs(ctx, "", followRecommendationsSetSize)`},
		{"refreshFollowRecommendations", `for _, locale := range railsI18nAvailableLocales`},
		{"refreshFollowRecommendationMaterializedViews", `s.refreshFollowRecommendationMaterializedView(ctx, "account_summaries")`},
		{"refreshFollowRecommendationMaterializedViews", `s.refreshFollowRecommendationMaterializedView(ctx, "global_follow_recommendations")`},
		{"refreshFollowRecommendationMaterializedView", `name != "account_summaries" && name != "global_follow_recommendations"`},
		{"refreshFollowRecommendationMaterializedView", `FROM pg_matviews`},
		{"refreshFollowRecommendationMaterializedView", `REFRESH MATERIALIZED VIEW CONCURRENTLY`},
		{"refreshFollowRecommendations", `s.invalidateAllSuggestionCaches(ctx)`},
		{"followRecommendationRefs", `Table("global_follow_recommendations")`},
		{"followRecommendationRefs", `JOIN account_summaries ON account_summaries.account_id = global_follow_recommendations.account_id`},
		{"writeFollowRecommendationRedisSet", `followRecommendationsRedisKey(s.cfg.RedisNamespace, locale)`},
		{"writeFollowRecommendationRedisSet", `args := []string{"ZADD", key}`},
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
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runFollowRecommendationsWorker)") {
		t.Fatal("StartBackgroundWorkers does not start follow recommendations worker")
	}
}
