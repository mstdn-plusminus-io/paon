package api

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestCompactInt64ArrayMatchesRailsArrayParams(t *testing.T) {
	got := compactInt64Array([]string{"1,2", "2", "bad", " 3 "})
	want := []int64{2, 3}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %#v, want %#v", got, want)
		}
	}
}

func TestParseReportPayloadAcceptsJSONArrays(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(`{
		"account_id":"42",
		"status_ids":["100","101"],
		"rule_ids":["3"],
		"comment":"bad post",
		"category":"violation",
		"forward":true,
		"forward_to_domains":["remote.example"]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseReportPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.AccountID != "42" || payload.Comment != "bad post" || payload.Category != "violation" {
		t.Fatalf("payload = %#v", payload)
	}
	if len(payload.StatusIDs) != 2 || payload.StatusIDs[1] != "101" {
		t.Fatalf("StatusIDs = %#v", payload.StatusIDs)
	}
	if len(payload.RuleIDs) != 1 || payload.RuleIDs[0] != "3" {
		t.Fatalf("RuleIDs = %#v", payload.RuleIDs)
	}
	if !payload.Forward || len(payload.ForwardToDomains) != 1 {
		t.Fatalf("forward = %#v domains=%#v", payload.Forward, payload.ForwardToDomains)
	}
}

func TestParseReportPayloadUsesRailsTruthyForwardJSONValue(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(`{
		"account_id":42,
		"status_ids":[100,"101"],
		"rule_ids":"3",
		"comment":"bad post",
		"category":"violation",
		"forward":"no",
		"forward_to_domains":"remote.example"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseReportPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.AccountID != "42" || payload.Comment != "bad post" || payload.Category != "violation" {
		t.Fatalf("payload = %#v", payload)
	}
	if len(payload.StatusIDs) != 2 || payload.StatusIDs[0] != "100" || payload.StatusIDs[1] != "101" {
		t.Fatalf("StatusIDs = %#v", payload.StatusIDs)
	}
	if len(payload.RuleIDs) != 0 {
		t.Fatalf("RuleIDs = %#v", payload.RuleIDs)
	}
	if !payload.Forward {
		t.Fatalf("Forward = false")
	}
	if len(payload.ForwardToDomains) != 0 {
		t.Fatalf("ForwardToDomains = %#v", payload.ForwardToDomains)
	}
}

func TestParseReportPayloadUsesRailsTruthyForwardFormValue(t *testing.T) {
	e := echo.New()
	for _, value := range []string{"true", "1", "on", "yes", "t", "bad", "no"} {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader("account_id=42&forward="+value))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		payload, err := parseReportPayload(c)
		if err != nil {
			t.Fatal(err)
		}
		if !payload.Forward {
			t.Fatalf("forward=%q parsed as false", value)
		}
	}
	for _, value := range []string{"false", "0", "off", "f"} {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader("account_id=42&forward="+value))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		payload, err := parseReportPayload(c)
		if err != nil {
			t.Fatal(err)
		}
		if payload.Forward {
			t.Fatalf("forward=%q parsed as true", value)
		}
	}
}

func TestReportCategoryValueMatchesRailsEnum(t *testing.T) {
	cases := map[string]int{"other": 0, "spam": 1000, "legal": 1500, "violation": 2000, "": 0}
	for value, want := range cases {
		if got := reportCategoryValue(value); got != want {
			t.Fatalf("reportCategoryValue(%q) = %d, want %d", value, got, want)
		}
	}
}

func TestReportCategoryValueOKRejectsUnknownAdminUpdateCategory(t *testing.T) {
	for _, value := range []string{"other", "spam", "legal", "violation", ""} {
		if _, ok := reportCategoryValueOK(value); !ok {
			t.Fatalf("reportCategoryValueOK(%q) ok = false, want true", value)
		}
	}
	if got := reportCategoryValue("unknown"); got != 0 {
		t.Fatalf("reportCategoryValue keeps create fallback = %d, want 0", got)
	}
	if _, ok := reportCategoryValueOK("unknown"); ok {
		t.Fatal("reportCategoryValueOK must reject unknown categories for admin updates")
	}
}

func TestReportStaffNotificationRoleHelpers(t *testing.T) {
	roleIDs := []int64{-99, 4, 5}
	if !roleIDsIncludeEveryone(roleIDs) {
		t.Fatal("expected everyone role to be detected")
	}
	filtered := roleIDsWithoutEveryone(roleIDs)
	if len(filtered) != 2 || filtered[0] != 4 || filtered[1] != 5 {
		t.Fatalf("filtered role IDs = %#v", filtered)
	}
	if roleIDsIncludeEveryone(filtered) {
		t.Fatal("did not expect everyone role after filtering")
	}
}

func TestReportForwardDomainsDefaultToTargetDomain(t *testing.T) {
	target := models.Account{Domain: sql.NullString{String: "Remote.Example", Valid: true}}
	got := reportForwardDomains(reportPayload{}, target)
	if len(got) != 1 || got[0] != "remote.example" {
		t.Fatalf("domains = %#v", got)
	}
	if !reportForwardDomainsIncludeTarget(got, target) {
		t.Fatal("target domain was not detected")
	}
}

func TestReportForwardDomainsNormalizeAndDeduplicate(t *testing.T) {
	payload := reportPayload{ForwardToDomains: []string{" Remote.Example/ ", "remote.example", "https://bad.example", "reply.example"}}
	target := models.Account{Domain: sql.NullString{String: "remote.example", Valid: true}}
	got := reportForwardDomains(payload, target)
	if len(got) != 2 || got[0] != "remote.example" || got[1] != "reply.example" {
		t.Fatalf("domains = %#v", got)
	}
}

func TestActivityPubFlagReportFallbackIDUsesRailsPayloadURI(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https"}}
	target := models.Account{ID: 7, Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}, URI: "https://remote.example/users/bob"}
	report := models.Report{ID: 42, Comment: "bad post"}
	actor := models.Account{ID: -99, Username: instanceActorUsername}

	payload := activityPubFlagReport(server, report, target, nil, actor)
	id, ok := payload["id"].(string)
	if !ok || !strings.HasPrefix(id, "https://example.com/payloads/") {
		t.Fatalf("fallback Flag id = %#v", payload["id"])
	}
	if strings.Contains(id, "/reports/") {
		t.Fatalf("fallback Flag id must not expose Rails-incompatible report route: %q", id)
	}
}

func TestOrderReportForwardStatusesPreservesReportStatusIDOrder(t *testing.T) {
	statuses := []models.Status{{ID: 3}, {ID: 1}, {ID: 2}}
	got := orderReportForwardStatuses(models.Int64Array{2, 1, 2, 9}, statuses)
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
		t.Fatalf("ordered statuses = %#v", got)
	}
}
