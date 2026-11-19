package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

// A list member added through a pending FollowRequest has follow_id NULL until
// the request is authorized. Like Rails lists_for_local_distribution, only
// accepted follows (and the owner's own account) may feed the list timeline;
// otherwise followers-only statuses of locked accounts leak to the requester.
func TestListTimelineStatusIDsRequiresAcceptedFollow(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	server := &Server{db: statusMentionDryRunDatabase(t, capture)}
	list := models.List{ID: 5, AccountID: 3, RepliesPolicy: 1}
	c := echo.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/timelines/list/5", nil), httptest.NewRecorder(), echo.New())

	// Raw(...).Scan cannot run in dry-run mode, but the SQL is still logged.
	if _, err := server.listTimelineStatusIDs(c, list); err != nil && !errors.Is(err, gorm.ErrDryRunModeUnsupported) {
		t.Fatal(err)
	}
	query := dryRunCapturedQuery(t, capture.queries, "FROM list_accounts", "CROSS JOIN LATERAL")
	if got, want := dryRunSQLOuterWhere(t, query), "list_accounts.list_id = 5 AND (list_accounts.follow_id IS NOT NULL OR list_accounts.account_id = 3)"; got != want {
		t.Fatalf("list timeline member condition = %q, want %q\n%s", got, want, query)
	}
	if normalized := strings.Join(strings.Fields(query), " "); !strings.HasSuffix(normalized, "ORDER BY list_statuses.id DESC LIMIT 20") {
		t.Fatalf("list timeline must keep binding the limit last: %s", normalized)
	}
}

// Status deletions must reach the same list streams as the original fan-out,
// which skips members whose follow request is still pending.
func TestStatusListStreamingChannelsRequireAcceptedFollow(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	database := statusMentionDryRunDatabase(t, capture)

	if got := (&Server{}).statusListStreamingChannels(context.Background(), database, "mastodon:", 7); len(got) != 0 {
		t.Fatalf("dry-run list streaming channels = %#v, want none", got)
	}
	query := dryRunCapturedQuery(t, capture.queries, `FROM "lists"`, "JOIN list_accounts")
	where := dryRunSQLOuterWhere(t, query)
	if !strings.HasPrefix(where, "list_accounts.account_id = 7 AND ") || dryRunSQLTopLevelIndex(where, " OR ") >= 0 {
		t.Fatalf("list streaming member condition must AND the follow check onto the member: %s", where)
	}
	if !strings.Contains(where, "(list_accounts.follow_id IS NOT NULL OR lists.account_id = 7)") {
		t.Fatalf("list streaming member condition missing the accepted follow check: %s", where)
	}
}
