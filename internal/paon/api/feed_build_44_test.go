package api

import (
	"os"
	"strings"
	"testing"
)

func TestFeedBuildSupportsMastodon44SkipFilledTimelines(t *testing.T) {
	operationsSource, err := os.ReadFile("operations.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`type OperationBuildHomeFeedsOptions struct`,
		`SkipFilledTimelines bool`,
		`operations.buildHomeFeedsSkippingFilledTimelines(ctx, user)`,
		`operations.server.feedTimelineMoreThanHalfFull(ctx, "home", user.AccountID)`,
		`operations.server.feedTimelineMoreThanHalfFull(ctx, "list", list.ID)`,
	} {
		if !strings.Contains(string(operationsSource), want) {
			t.Fatalf("operations.go missing skip-filled behavior %q", want)
		}
	}

	feedSource, err := os.ReadFile("list_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	body := mustFunctionBody(t, string(feedSource), "feedTimelineMoreThanHalfFull")
	for _, want := range []string{`"ZCARD"`, `redisInt(value) > int64(feedMaxItems/2)`} {
		if !strings.Contains(body, want) {
			t.Fatalf("feedTimelineMoreThanHalfFull missing %q", want)
		}
	}
}
