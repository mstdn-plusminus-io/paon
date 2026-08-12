package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func TestNotificationGroupHourBucketReusesFirstBucketForTwelveHours(t *testing.T) {
	first := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC).Unix() / int64(time.Hour/time.Second)
	if got := notificationGroupHourBucket(first+11, first); got != first {
		t.Fatalf("bucket within the group span = %d, want %d", got, first)
	}
	if got := notificationGroupHourBucket(first+12, first); got != first+12 {
		t.Fatalf("bucket at the group boundary = %d, want %d", got, first+12)
	}
	if got := notificationGroupHourBucket(first, 0); got != first {
		t.Fatalf("missing Redis bucket = %d, want %d", got, first)
	}
}

func TestNotificationGroupPageRangeMatchesMastodon43Pagination(t *testing.T) {
	tests := []struct {
		name     string
		rawQuery string
		rows     []notificationGroupRow
		limit    int
		want     *notificationPageRange
	}{
		{
			name:  "empty page",
			limit: 2,
		},
		{
			name:  "complete page uses returned bounds",
			rows:  []notificationGroupRow{{ID: 90}, {ID: 70}},
			limit: 2,
			want:  &notificationPageRange{MinID: 70, HasMinID: true, MaxID: 90, HasMaxID: true},
		},
		{
			name:  "incomplete first page has no lower bound",
			rows:  []notificationGroupRow{{ID: 90}},
			limit: 2,
			want:  &notificationPageRange{MaxID: 90, HasMaxID: true},
		},
		{
			name:     "incomplete since page starts after since id",
			rawQuery: "since_id=50",
			rows:     []notificationGroupRow{{ID: 90}},
			limit:    2,
			want:     &notificationPageRange{MinID: 51, HasMinID: true, MaxID: 90, HasMaxID: true},
		},
		{
			name:     "incomplete upward page excludes requested max id",
			rawQuery: "min_id=50&max_id=100",
			rows:     []notificationGroupRow{{ID: 90}},
			limit:    2,
			want:     &notificationPageRange{MinID: 90, HasMinID: true, MaxID: 100, HasMaxID: true, MaxExclusive: true},
		},
		{
			name:     "incomplete upward page without max has no upper bound",
			rawQuery: "min_id=50",
			rows:     []notificationGroupRow{{ID: 90}},
			limit:    2,
			want:     &notificationPageRange{MinID: 90, HasMinID: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v2/notifications?"+test.rawQuery, nil)
			context := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			if got := notificationGroupPageRange(context, test.rows, test.limit); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("notificationGroupPageRange() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNotificationPartialAvatarAccountsKeepsFirstSampleFull(t *testing.T) {
	groups := []notificationGroupEntity{
		{SampleAccountIDs: []string{"1", "2", "3"}},
		{SampleAccountIDs: []string{"2", "3", "4"}},
	}
	accounts := []serializer.Account{
		{ID: "1", Acct: "one"},
		{ID: "2", Acct: "two"},
		{ID: "3", Acct: "three"},
		{ID: "4", Acct: "four"},
	}

	full, partial := notificationPartialAvatarAccounts(groups, accounts)
	if len(full) != 2 || len(partial) != 2 {
		t.Fatalf("account partition lengths = full %d, partial %d", len(full), len(partial))
	}
	if got := []string{full[0].ID, full[1].ID}; !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("full account IDs = %#v", got)
	}
	if got := []string{partial[0].ID, partial[1].ID}; !reflect.DeepEqual(got, []string{"3", "4"}) {
		t.Fatalf("partial account IDs = %#v", got)
	}
}

func TestNotificationExpandAccountsDefaultsOnlyWhenParameterIsAbsent(t *testing.T) {
	tests := []struct {
		rawQuery string
		want     string
		valid    bool
	}{
		{want: "full", valid: true},
		{rawQuery: "expand_accounts=full", want: "full", valid: true},
		{rawQuery: "expand_accounts=partial_avatars", want: "partial_avatars", valid: true},
		{rawQuery: "expand_accounts=", want: "", valid: false},
		{rawQuery: "expand_accounts=avatars", want: "avatars", valid: false},
	}
	for _, test := range tests {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/notifications?"+test.rawQuery, nil)
		context := echo.NewContext(req, httptest.NewRecorder(), echo.New())
		got, valid := notificationExpandAccounts(context)
		if got != test.want || valid != test.valid {
			t.Fatalf("notificationExpandAccounts(%q) = (%q, %v), want (%q, %v)", test.rawQuery, got, valid, test.want, test.valid)
		}
	}
}

func TestNotificationV2PaginationLinkDoesNotPersistExpandAccounts(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/notifications?limit=20&expand_accounts=partial_avatars&types%5B%5D=follow", nil)
	context := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	link := notificationV2PaginationLink(context, 90, 70)
	if strings.Contains(link, "expand_accounts") {
		t.Fatalf("pagination Link persisted expand_accounts: %s", link)
	}
	if !strings.Contains(link, "types%5B%5D=follow") {
		t.Fatalf("pagination Link dropped types[]: %s", link)
	}
}

func TestBulkNotificationRequestDismissDoesNotScheduleFilteredCleanup(t *testing.T) {
	src, err := os.ReadFile("notifications_43.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "mutateNotificationRequests")
	bulkGuard := strings.Index(body, "if bulk {")
	cleanup := strings.Index(body, "s.enqueueFilteredNotificationCleanupTask")
	if bulkGuard < 0 || cleanup < 0 || bulkGuard > cleanup {
		t.Fatal("bulk dismiss must skip cleanup-task scheduling before the single-dismiss cleanup branch")
	}
}

func TestNotificationPolicyActionsUseMastodon43Values(t *testing.T) {
	for name, want := range map[string]int{"accept": 0, "filter": 1, "drop": 2} {
		got, ok := notificationPolicyAction(name)
		if !ok || got != want {
			t.Fatalf("notificationPolicyAction(%q) = (%d, %v), want (%d, true)", name, got, ok, want)
		}
		if gotName := notificationPolicyActionName(got); gotName != name {
			t.Fatalf("notificationPolicyActionName(%d) = %q, want %q", got, gotName, name)
		}
	}
	if _, ok := notificationPolicyAction("unknown"); ok {
		t.Fatal("unknown notification policy action was accepted")
	}
}

func TestNotification43WireTypesMatchRails(t *testing.T) {
	latest := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
	body, err := json.Marshal(notificationGroupEntity{
		GroupKey:                 "favourite-1",
		NotificationsCount:       1,
		Type:                     "favourite",
		MostRecentNotificationID: 2401,
		LatestPageNotificationAt: &latest,
		SampleAccountIDs:         []string{"1004"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var group map[string]any
	if err := json.Unmarshal(body, &group); err != nil {
		t.Fatal(err)
	}
	if group["most_recent_notification_id"] != float64(2401) {
		t.Fatalf("most_recent_notification_id = %#v, want JSON number", group["most_recent_notification_id"])
	}
	if group["latest_page_notification_at"] != "2026-01-15T12:00:00.000Z" {
		t.Fatalf("latest_page_notification_at = %#v", group["latest_page_notification_at"])
	}

	policyBody, err := json.Marshal(notificationPolicyV1{Summary: notificationPolicySummary{}})
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err := json.Unmarshal(policyBody, &policy); err != nil {
		t.Fatal(err)
	}
	summary, ok := policy["summary"].(map[string]any)
	if !ok || summary["pending_notifications_count"] != float64(0) || summary["pending_requests_count"] != float64(0) {
		t.Fatalf("policy summary = %#v", policy["summary"])
	}
}

func TestNotification44AnnualReportGroupUsesStringYear(t *testing.T) {
	body, err := json.Marshal(notificationGroupEntity{
		GroupKey:                 "ungrouped-44",
		NotificationsCount:       1,
		Type:                     "annual_report",
		MostRecentNotificationID: 44,
		SampleAccountIDs:         []string{"7"},
		AnnualReport:             &annualReportEventEntity{Year: "2025"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"annual_report":{"year":"2025"}`) {
		t.Fatalf("annual report notification = %s", body)
	}
	if _, ok := notificationTypes["annual_report"]; !ok {
		t.Fatal("annual_report is not accepted by notification type filters")
	}
}

func TestUniquePositiveRequestIDsAcceptsJSONArraysAndDeduplicates(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/requests/accept?id[]=7", strings.NewReader(`{"id":["3",3,0,-2,"7"]}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if got, want := uniquePositiveRequestIDs(c), []int64{7, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request IDs = %#v, want %#v", got, want)
	}
}

func TestAnnualReportReferencedIDsExcludeInvalidAndZeroValues(t *testing.T) {
	reports := []models.GeneratedAnnualReport{{Data: []byte(`{
		"most_reblogged_accounts":[{"account_id":"12"},{"account_id":0}],
		"commonly_interacted_with_accounts":[{"account_id":12},{"account_id":-1}],
		"top_statuses":{"first":"34","second":0,"third":-5}
	}`)}}
	accounts, statuses := annualReportReferencedIDs(reports)
	if !reflect.DeepEqual(accounts, []int64{12}) {
		t.Fatalf("account IDs = %#v, want [12]", accounts)
	}
	if !reflect.DeepEqual(statuses, []int64{34}) {
		t.Fatalf("status IDs = %#v, want [34]", statuses)
	}
}

func TestApplyStatusPreviewCardOriginalURLsUsesAssociationURL(t *testing.T) {
	status := models.Status{
		PreviewCards:        []models.PreviewCard{{ID: 1, URL: "https://canonical.example/article"}},
		PreviewCardStatuses: []models.PreviewCardStatus{{PreviewCardID: 1, URL: sql.NullString{String: "https://original.example/article", Valid: true}}},
		Reblog: &models.Status{
			PreviewCards:        []models.PreviewCard{{ID: 2, URL: "https://canonical.example/reblog"}},
			PreviewCardStatuses: []models.PreviewCardStatus{{PreviewCardID: 2, URL: sql.NullString{String: "https://original.example/reblog", Valid: true}}},
		},
	}
	applyStatusPreviewCardOriginalURLs(&status)
	if got := status.PreviewCards[0].URL; got != "https://original.example/article" {
		t.Fatalf("preview card URL = %q", got)
	}
	if got := status.Reblog.PreviewCards[0].URL; got != "https://original.example/reblog" {
		t.Fatalf("reblog preview card URL = %q", got)
	}
}

type oidcPKCERoundTripper func(*http.Request) (*http.Response, error)

func (fn oidcPKCERoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestOIDCPKCEAddsS256ChallengeAndVerifier(t *testing.T) {
	cfg := config.Config{
		OIDCEnabled:          true,
		OIDCUsePKCE:          true,
		OIDCClientID:         "client",
		OIDCClientSecret:     "secret",
		OIDCRedirectURI:      "https://paon.example/auth/auth/openid_connect/callback",
		OIDCAuthEndpoint:     "https://idp.example/authorize",
		OIDCTokenEndpoint:    "https://idp.example/token",
		OIDCResponseType:     "code",
		OIDCClientAuthMethod: "post",
	}
	challenge := strings.Repeat("c", 43)
	location, err := openIDConnectAuthorizationURLWithPKCE(cfg, "state", "nonce", challenge)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("code_challenge"); got != challenge {
		t.Fatalf("code_challenge = %q", got)
	}
	if got := parsed.Query().Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q", got)
	}

	verifier := strings.Repeat("v", 64)
	oldClient := oidcHTTPClient
	t.Cleanup(func() { oidcHTTPClient = oldClient })
	oidcHTTPClient = &http.Client{Transport: oidcPKCERoundTripper(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := req.Form.Get("code_verifier"); got != verifier {
			t.Fatalf("code_verifier = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"token"}`)),
			Request:    req,
		}, nil
	})}
	if _, err := exchangeOpenIDConnectCodeWithVerifier(t.Context(), cfg, "code", verifier); err != nil {
		t.Fatal(err)
	}
	if _, err := exchangeOpenIDConnectCodeWithVerifier(t.Context(), cfg, "code", ""); err == nil {
		t.Fatal("missing OIDC PKCE verifier was accepted")
	}
}
