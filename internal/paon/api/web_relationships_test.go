package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRelationshipPageFilterDefaultsAndValidates(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/relationships", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if got := relationshipPageFilter(c); got != "following" {
		t.Fatalf("filter = %q", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/relationships?relationship=mutual", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if got := relationshipPageFilter(c); got != "mutual" {
		t.Fatalf("filter = %q", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/relationships?relationship=invited", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if got := relationshipPageFilter(c); got != "invited" {
		t.Fatalf("filter = %q", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/relationships?relationship=bad", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := relationshipPageFilters(c); err == nil {
		t.Fatal("invalid relationship should return an error like Rails")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusBadRequest || apiErr.message != "Unknown relationship: bad" {
		t.Fatalf("error = %#v", err)
	}
}

func TestRelationshipPageFiltersPreserveRailsFilterParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/relationships?relationship=followed_by&status=moved&location=remote&activity=dormant&order=active&by_domain=remote.example", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	got, err := relationshipPageFilters(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Relationship != "followed_by" || got.Status != "moved" || got.Location != "remote" || got.Activity != "dormant" || got.Order != "active" || got.ByDomain != "remote.example" {
		t.Fatalf("filters = %#v", got)
	}
	hidden := relationshipFilterHiddenFields(c)
	for _, want := range []string{`name="page" value="1"`, `name="relationship" value="followed_by"`, `name="status" value="moved"`, `name="location" value="remote"`, `name="activity" value="dormant"`, `name="order" value="active"`, `name="by_domain" value="remote.example"`} {
		if !strings.Contains(hidden, want) {
			t.Fatalf("hidden fields missing %q: %s", want, hidden)
		}
	}
}

func TestRelationshipPageFiltersReadSubmittedHiddenFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/relationships", strings.NewReader("relationship=followed_by&location=local&order=active"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	got, err := relationshipPageFilters(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Relationship != "followed_by" || got.Location != "local" || got.Order != "active" {
		t.Fatalf("filters = %#v", got)
	}
}

func TestRelationshipsHTMLRendersAccountsAndActions(t *testing.T) {
	html := relationshipsHTML(relationshipPageView{
		Config:             config.Config{Scheme: "https", WebDomain: "example.test"},
		Accounts:           []models.Account{{ID: 7, Username: "alice", AccountStat: models.AccountStat{StatusesCount: 3, FollowersCount: 4}}},
		Interrelationships: relationshipPageInterrelationships{Following: map[int64]bool{7: true}, FollowedBy: map[int64]bool{}},
		Filters:            relationshipFilters{Relationship: "following", Order: "recent"},
	}, "", "")
	for _, want := range []string{`value="7"`, "alice", `name="unfollow"`, `class="filters"`, `class="filter-subset"`, `class="batch-table"`, `class="batch-table__row"`, `class="batch-table__row__content batch-table__row__content--unpadded"`, `class="accounts-table"`, `class="accounts-table__count optional"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `name="select_all_matching"`) || strings.Contains(html, `batch-table__select-all`) {
		t.Fatalf("relationships HTML must not expose Go-only select-all-matching controls absent from Rails: %s", html)
	}
	if strings.Contains(html, `name="remove_from_followers"`) {
		t.Fatalf("following relationships should hide follower-removal action like Rails: %s", html)
	}
}

func TestRelationshipAccountRowHTMLMatchesRailsAccountLinkAndInterrelationships(t *testing.T) {
	cfg := config.Config{Scheme: "https", WebDomain: "example.test"}
	account := models.Account{
		ID:                7,
		Username:          "alice",
		DisplayName:       "Alice <Admin>",
		AvatarFileName:    sql.NullString{String: "avatar.png", Valid: true},
		AvatarContentType: sql.NullString{String: "image/png", Valid: true},
		AccountStat:       models.AccountStat{StatusesCount: 3, FollowersCount: 4},
	}
	interrelationships := relationshipPageInterrelationships{
		Following:  map[int64]bool{7: true},
		FollowedBy: map[int64]bool{7: true},
	}
	html := relationshipAccountRowHTML(cfg, account, interrelationships, false, "en")
	for _, want := range []string{
		`<i title="Mutual" class="fa-fw active passive fa fa-exchange"></i>`,
		`<div class="account account--minimal"><div class="account__wrapper">`,
		`<a class="account__display-name" href="https://example.test/@alice">`,
		`<div class="account__avatar-wrapper"><img class="account__avatar" width="46" height="46" src="https://example.test/system/accounts/avatars/000/000/007/original/avatar.png"></div>`,
		`<strong class="display-name__html emojify">Alice &lt;Admin&gt;</strong>`,
		`<span class="display-name__account">@alice</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("relationship account row missing Rails markup %q: %s", want, html)
		}
	}
	if strings.Contains(html, `class="name-tag"`) || strings.Contains(html, `fa-users`) {
		t.Fatalf("relationship account row retained non-Rails account markup: %s", html)
	}
}

func TestRelationshipInterrelationshipsFromFollows(t *testing.T) {
	got := relationshipInterrelationshipsFromFollows(1, []models.Follow{
		{AccountID: 1, TargetAccountID: 2},
		{AccountID: 2, TargetAccountID: 1},
		{AccountID: 3, TargetAccountID: 1},
	})
	if !got.Following[2] || !got.FollowedBy[2] || !got.FollowedBy[3] || got.Following[3] {
		t.Fatalf("interrelationships = %#v", got)
	}
	if icon := relationshipInterrelationshipsIconHTML(got, 2, "en"); !strings.Contains(icon, `fa-exchange`) {
		t.Fatalf("mutual icon = %s", icon)
	}
	if icon := relationshipInterrelationshipsIconHTML(got, 3, "en"); !strings.Contains(icon, `fa-arrow-left`) || !strings.Contains(icon, `class="fa-fw passive`) {
		t.Fatalf("follower icon = %s", icon)
	}
	if icon := relationshipInterrelationshipsIconHTML(relationshipPageInterrelationships{Following: map[int64]bool{4: true}, FollowedBy: map[int64]bool{}}, 4, "ar"); !strings.Contains(icon, `fa-arrow-left`) {
		t.Fatalf("RTL following icon = %s", icon)
	}
}

func TestRelationshipFiltersHTMLUsesRailsSelectedLinks(t *testing.T) {
	html := relationshipFiltersHTML(relationshipFilters{Relationship: "following", Order: "recent"}, "en")
	if got := strings.Count(html, `class="selected"`); got != 4 {
		t.Fatalf("default filter selected link count = %d, want 4: %s", got, html)
	}
	for _, want := range []string{
		`<li><a class="selected" href="/relationships">Following</a></li>`,
		`<li><a class="selected" href="/relationships">All</a></li>`,
		`<li><a class="selected" href="/relationships">Most recent</a></li>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("default filters missing Rails selected-link markup %q: %s", want, html)
		}
	}
	if strings.Contains(html, `<li class="active">`) {
		t.Fatalf("filter active state must be on the anchor like Rails: %s", html)
	}
}

func TestRelationshipFilterLinksPreserveOtherRailsFilters(t *testing.T) {
	filters := relationshipFilters{
		Relationship: "followed_by",
		Status:       "moved",
		ByDomain:     "remote.example",
		Activity:     "dormant",
		Order:        "active",
		Location:     "remote",
	}
	html := relationshipFiltersHTML(filters, "en")
	currentHref := `/relationships?activity=dormant&amp;by_domain=remote.example&amp;location=remote&amp;order=active&amp;relationship=followed_by&amp;status=moved`
	if got := strings.Count(html, `class="selected" href="`+currentHref+`"`); got != 4 {
		t.Fatalf("filtered selected links pointing at current Rails URL = %d, want 4: %s", got, html)
	}

	href := relationshipFilterHref(filters, "status", "")
	parsed, err := url.Parse(href)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("status") != "" || query.Get("relationship") != "followed_by" || query.Get("activity") != "dormant" || query.Get("order") != "active" || query.Get("by_domain") != "remote.example" || query.Get("location") != "remote" {
		t.Fatalf("status reset link did not preserve the other Rails filters: %s", href)
	}
}

func TestRelationshipPageFiltersMatchRailsInvalidParameterBoundaries(t *testing.T) {
	for _, tt := range []struct {
		target string
		want   string
	}{
		{"/relationships?relationship=bad", "Unknown relationship: bad"},
		{"/relationships?status=gone", "Unknown status: gone"},
		{"/relationships?location=elsewhere", "Unknown location: elsewhere"},
		{"/relationships?activity=busy", "Unknown activity: busy"},
		{"/relationships?order=random", "Unknown order: random"},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.target, nil)
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
		_, err := relationshipPageFilters(c)
		apiErr, ok := err.(apiHTTPError)
		if !ok || apiErr.status != http.StatusBadRequest || apiErr.message != tt.want {
			t.Fatalf("%s error = %#v, want 400 %q", tt.target, err, tt.want)
		}
	}
}

func TestRelationshipsRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/relationships", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.relationshipsPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/relationships")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRelationshipBatchActionsFanOutRemoteActivityPub(t *testing.T) {
	src, err := os.ReadFile("web_relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`deliveries := []relationshipBatchDelivery{}`,
		`notificationPayloads := []asynqLocalNotificationPayload{}`,
		`followCacheEffects := []followRelationshipCacheEffect{}`,
		`unmergeTargets := []int64{}`,
		`delivery, notificationPayload, err := s.createRelationshipFollow(ctx, tx, current, target, time.Now().UTC())`,
		`delivery, deleted, err := s.deleteRelationshipFollowForBatch(tx, current, target)`,
		`followCacheEffects = append(followCacheEffects, followRelationshipCacheEffect{Source: current, TargetID: target.ID})`,
		`delivery, effect, err := removeFollowerForBatch(tx, current, target)`,
		`followCacheEffects = appendFollowRelationshipCacheEffect(followCacheEffects, effect)`,
		`domainDeliveries, domainEffects, err := deleteFollowersByDomain(tx, current, target.Domain.String)`,
		`followCacheEffects = append(followCacheEffects, domainEffects...)`,
		`notificationPayloads = appendRelationshipBatchNotificationPayload(notificationPayloads, notificationPayload)`,
		`s.unmergeAfterUnfollowBestEffort(ctx, targetID, current)`,
		`s.invalidateFollowRelationshipCaches(ctx, effect.Source, effect.TargetID)`,
		`s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)`,
		`s.publishNotificationIDs(notificationIDs)`,
		`s.deliverRelationshipBatchDeliveries(deliveries)`,
	} {
		if !functionBodyContains(t, src, "applyRelationshipBatch", want) {
			t.Fatalf("applyRelationshipBatch missing %q", want)
		}
	}
	for _, want := range []string{
		`s.deliverActivityPubFollow(delivery.Local, delivery.Remote, delivery.ID, delivery.URI)`,
		`s.deliverActivityPubUndoFollow(delivery.Local, delivery.Remote, delivery.ID, delivery.URI)`,
		`s.deliverActivityPubFollowResponse("Reject", delivery.Local, delivery.Remote, delivery.ID, delivery.URI)`,
	} {
		if !functionBodyContains(t, src, "deliverRelationshipBatchDeliveries", want) {
			t.Fatalf("deliverRelationshipBatchDeliveries missing %q", want)
		}
	}
}

func TestDeleteFollowersByDomainUsesCaseInsensitiveDomain(t *testing.T) {
	src, err := os.ReadFile("web_relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`domain = strings.TrimSpace(domain)`,
		`if domain == ""`,
		`follows.target_account_id = ? AND lower(accounts.domain) = lower(?)`,
		`effects = append(effects, followRelationshipCacheEffect{Source: follow.Account, TargetID: current.ID})`,
	} {
		if !functionBodyContains(t, src, "deleteFollowersByDomain", want) {
			t.Fatalf("deleteFollowersByDomain missing %q", want)
		}
	}
	if functionBodyContains(t, src, "deleteFollowersByDomain", `follows.target_account_id = ? AND accounts.domain = ?`) {
		t.Fatal("deleteFollowersByDomain must not use case-sensitive domain equality")
	}
}

func TestRelationshipBatchRedirectPathPreservesRailsFilterParamsOnly(t *testing.T) {
	form := "page=2&relationship=followed_by&status=moved&by_domain=Remote.EXAMPLE&activity=dormant&order=active&location=remote&notice=bad"
	req := httptest.NewRequest(http.MethodPost, "/relationships?relationship=mutual", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	got := relationshipBatchRedirectPath(c, "", "")
	for _, want := range []string{
		"/relationships?",
		"page=2",
		"relationship=followed_by",
		"status=moved",
		"by_domain=Remote.EXAMPLE",
		"activity=dormant",
		"order=active",
		"location=remote",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("redirect path missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "notice=") {
		t.Fatalf("redirect path should not preserve flash params: %s", got)
	}

	got = relationshipBatchRedirectPath(c, "error", "Could not follow selected accounts")
	if !strings.Contains(got, "error=Could+not+follow+selected+accounts") {
		t.Fatalf("redirect path missing encoded error flash: %s", got)
	}
}
