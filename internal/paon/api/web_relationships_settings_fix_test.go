package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRelationshipsHTMLSelectAllCheckboxHasRailsID(t *testing.T) {
	out := relationshipsHTML(relationshipPageView{
		Accounts: []models.Account{{ID: 7, Username: "alice"}},
		Filters:  relationshipFilters{Relationship: "following", Order: "recent"},
	}, "", "")
	if !strings.Contains(out, `<input type="checkbox" name="batch_checkbox_all" id="batch_checkbox_all">`) {
		t.Fatalf("select-all checkbox must carry id=batch_checkbox_all for admin.js: %s", out)
	}
}

func TestRelationshipPageNumberMatchesKaminariPageParam(t *testing.T) {
	cases := map[string]int{
		"/relationships":          1,
		"/relationships?page=3":   3,
		"/relationships?page=0":   1,
		"/relationships?page=-2":  1,
		"/relationships?page=abc": 1,
	}
	for target, want := range cases {
		c := echo.NewContext(httptest.NewRequest(http.MethodGet, target, nil), httptest.NewRecorder(), echo.New())
		if got := relationshipPageNumber(c); got != want {
			t.Fatalf("relationshipPageNumber(%q) = %d, want %d", target, got, want)
		}
	}
}

func TestRelationshipPageAccountsAppliesRailsPageOffset(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	s := &Server{db: statusMentionDryRunDatabase(t, capture)}
	if _, err := s.relationshipPageAccounts(1, 2, relationshipFilters{Relationship: "following", Order: "recent"}, relationshipsPageSize, 2*relationshipsPageSize); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(capture.queries, "\n")
	if !strings.Contains(joined, "LIMIT 40") || !strings.Contains(joined, "OFFSET 80") {
		t.Fatalf("relationship page query must use Rails .page(3).per(40): %s", joined)
	}

	capture.queries = nil
	if _, err := s.relationshipPageAccounts(1, 2, relationshipFilters{Relationship: "following", Order: "recent"}, relationshipsPageSize, 0); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(capture.queries, "\n")
	if !strings.Contains(joined, "LIMIT 40") || strings.Contains(joined, "OFFSET") {
		t.Fatalf("first relationship page must not use an offset: %s", joined)
	}
}

func TestRelationshipsHTMLRendersPaginationPreservingFilters(t *testing.T) {
	accounts := make([]models.Account, relationshipsPageSize)
	for i := range accounts {
		accounts[i] = models.Account{ID: int64(i + 1), Username: "user" + strconv.Itoa(i)}
	}
	out := relationshipsHTML(relationshipPageView{
		Accounts: accounts,
		Filters:  relationshipFilters{Relationship: "followed_by", Order: "active", Status: "moved"},
		Page:     2,
		HasNext:  true,
	}, "", "")
	for _, want := range []string{
		`<nav class="pagination">`,
		`<span class="prev"><a rel="prev" href="/relationships?order=active&amp;relationship=followed_by&amp;status=moved">`,
		`<span class="next"><a rel="next" href="/relationships?order=active&amp;page=3&amp;relationship=followed_by&amp;status=moved">`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("relationships pagination missing %q: %s", want, out)
		}
	}
	if !strings.Contains(out, `</form><nav class="pagination">`) {
		t.Fatalf("pagination must follow the batch form like Rails `= paginate @accounts`: %s", out)
	}
}

func TestRelationshipsHTMLOmitsPaginationOnSingleShortPage(t *testing.T) {
	out := relationshipsHTML(relationshipPageView{
		Accounts: []models.Account{{ID: 7, Username: "alice"}},
		Filters:  relationshipFilters{Relationship: "following", Order: "recent"},
	}, "", "")
	if strings.Contains(out, `class="pagination"`) {
		t.Fatalf("a single short first page must not render pagination: %s", out)
	}
	last := relationshipsHTML(relationshipPageView{
		Accounts: []models.Account{{ID: 7, Username: "alice"}},
		Filters:  relationshipFilters{Relationship: "following", Order: "recent"},
		Page:     2,
	}, "", "")
	if !strings.Contains(last, `rel="prev" href="/relationships"`) || strings.Contains(last, `rel="next"`) {
		t.Fatalf("last page must only link back to page 1: %s", last)
	}
}

func TestRelationshipsHTMLOmitsNextLinkOnFullLastPage(t *testing.T) {
	accounts := make([]models.Account, relationshipsPageSize)
	for i := range accounts {
		accounts[i] = models.Account{ID: int64(i + 1), Username: "user" + strconv.Itoa(i)}
	}
	out := relationshipsHTML(relationshipPageView{
		Accounts: accounts,
		Filters:  relationshipFilters{Relationship: "following", Order: "recent"},
	}, "", "")
	if strings.Contains(out, `rel="next"`) {
		t.Fatalf("a full page without a following page must not link to an empty next page: %s", out)
	}
}
