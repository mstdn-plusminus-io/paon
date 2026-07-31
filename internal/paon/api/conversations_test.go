package api

import (
	"database/sql"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestConversationPaginationLinkMatchesRailsRecordsContinue(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/conversations?limit=20&min_id=5&max_id=50", nil)
	req.Host = "social.example"
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	got := limitOnlyPaginationLink(c, 30, 20, "min_id", false)
	if strings.Contains(got, `rel="next"`) {
		t.Fatalf("Link should not include next when records do not continue: %q", got)
	}
	if !strings.Contains(got, `<http://social.example/api/v1/conversations?limit=20&min_id=30>; rel="prev"`) {
		t.Fatalf("Link missing Rails prev link: %q", got)
	}
	if strings.Contains(got, "max_id=") {
		t.Fatalf("Link should strip pagination cursor params: %q", got)
	}
}

func TestRailsStatusReplyTargetAndAccountIDMatchSetConversation(t *testing.T) {
	original := &models.Status{ID: 10, AccountID: 20}
	boost := &models.Status{
		ID:        11,
		AccountID: 30,
		ReblogOfID: sql.NullInt64{
			Int64: original.ID,
			Valid: true,
		},
		Reblog: original,
	}
	target, err := (&Server{}).railsStatusReplyTarget(boost)
	if err != nil {
		t.Fatal(err)
	}
	if target != original {
		t.Fatalf("reply target = %#v, want original status", target)
	}

	carried := railsStatusReplyAccountID(42, &models.Status{
		ID:                 12,
		AccountID:          42,
		Reply:              true,
		InReplyToAccountID: sql.NullInt64{Int64: 99, Valid: true},
	})
	if !carried.Valid || carried.Int64 != 99 {
		t.Fatalf("carried reply account = %#v, want 99", carried)
	}
	direct := railsStatusReplyAccountID(42, original)
	if !direct.Valid || direct.Int64 != original.AccountID {
		t.Fatalf("direct reply account = %#v, want %d", direct, original.AccountID)
	}
}

func TestOffsetPaginationLinkPreservesRailsLimitParamOnlyWhenProvided(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/trends/tags?offset=20", nil)
	req.Host = "social.example"
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	got := offsetPaginationLink(c, 20, 10, 10)
	if !strings.Contains(got, `<http://social.example/api/v1/trends/tags?offset=30>; rel="next"`) {
		t.Fatalf("Link missing Rails next link without default limit injection: %q", got)
	}
	if !strings.Contains(got, `<http://social.example/api/v1/trends/tags?offset=10>; rel="prev"`) {
		t.Fatalf("Link missing Rails prev link without default limit injection: %q", got)
	}
	if strings.Contains(got, "limit=") {
		t.Fatalf("Link should not add a default limit param Rails would omit: %q", got)
	}

	req = httptest.NewRequest("GET", "/api/v1/trends/tags?limit=15&offset=30", nil)
	req.Host = "social.example"
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	got = offsetPaginationLink(c, 30, 15, 15)
	if !strings.Contains(got, `limit=15&offset=45`) || !strings.Contains(got, `limit=15&offset=15`) {
		t.Fatalf("Link should preserve explicit Rails limit param: %q", got)
	}
}

func TestSetPaginationLinkHeaderOmitsRailsEmptyLink(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/trends/tags?offset=0", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	setPaginationLinkHeader(c, offsetPaginationLink(c, 0, 10, 3))
	if got := rec.Header().Values("Link"); len(got) != 0 {
		t.Fatalf("Link header should be omitted when Rails has no next or prev path: %#v", got)
	}

	setPaginationLinkHeader(c, offsetPaginationLink(c, 0, 10, 10))
	if got := rec.Header().Get("Link"); !strings.Contains(got, `offset=10`) || !strings.Contains(got, `rel="next"`) {
		t.Fatalf("Link header should be set when Rails has a next path: %q", got)
	}
}

func TestConversationsPaginationMatchesRailsMinIDMaxIDAndRecordsContinue(t *testing.T) {
	src, err := os.ReadFile("conversations.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"conversations", `if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id")`},
		{"conversations", `if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id")`},
		{"conversations", `if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id")`},
		{"conversations", `query = query.Where("account_conversations.last_status_id < ?", maxID)`},
		{"conversations", `limitValue := limit(c, 20, 40)`},
		{"conversations", `if queryParamValuePresent(c, "min_id")`},
		{"conversations", `len(rows) == limitValue`},
		{"conversations", `limitOnlyPaginationLink(c, first, last, "min_id", len(rows) == limitValue)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("conversations.go:%s does not contain %q", check.fn, check.want)
		}
	}
}

func TestDirectConversationParticipantIDsMatchRailsAccountConversation(t *testing.T) {
	got := conversationParticipantIDs(map[int64]struct{}{10: {}, 30: {}, 20: {}}, 20)
	want := models.Int64Array{10, 30}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("participants = %#v, want %#v", got, want)
	}
}
