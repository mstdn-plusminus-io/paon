package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminAccountsRequireAdminToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/accounts", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminAccounts(c); err == nil {
		t.Fatal("expected admin accounts to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestDestroyAdminAccountRequiresAdminToken(t *testing.T) {
	req := httptest.NewRequest("DELETE", "/api/v1/admin/accounts/123", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.destroyAdminAccount(c); err == nil {
		t.Fatal("expected admin account destroy to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestDestroyAdminAccountUsesRailsDeleteAccountServiceActivityPubDelivery(t *testing.T) {
	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "runAdminAccountDeletionWorkerEffects", `s.deliverAdminAccountDeletionActivities(s.db.WithContext(ctx), account)`) {
		t.Fatal("Admin::AccountDeletionWorker handler must use Rails DeleteAccountService-compatible ActivityPub deletion delivery")
	}
	if functionBodyContains(t, src, "runAdminAccountDeletionWorkerEffects", `s.deliverActivityPubAccountDelete(account)`) {
		t.Fatal("Admin::AccountDeletionWorker handler must not send Delete Actor for remote account deletion")
	}
}

func TestAdminReportsRequireAdminToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/reports", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminReports(c); err == nil {
		t.Fatal("expected admin reports to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestUpdateAdminReportRequiresAdminToken(t *testing.T) {
	req := httptest.NewRequest("PATCH", "/api/v1/admin/reports/123", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.updateAdminReport(c); err == nil {
		t.Fatal("expected admin report update to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestResolveAdminReportRequiresAdminToken(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/admin/reports/123/resolve", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.resolveAdminReport(c); err == nil {
		t.Fatal("expected admin report resolve to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestAdminTagsRequireAdminToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/tags", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminTags(c); err == nil {
		t.Fatal("expected admin tags to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestParseAdminTagPayloadUsesRailsBooleanSemanticsForJSONStrings(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PATCH", "/api/v1/admin/tags/1", strings.NewReader(`{
		"display_name":"GoLang",
		"trendable":"no",
		"usable":"off",
		"listable":"0"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseAdminTagPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.DisplayName == nil || *payload.DisplayName != "GoLang" {
		t.Fatalf("DisplayName = %#v", payload.DisplayName)
	}
	if payload.Trendable == nil || !*payload.Trendable {
		t.Fatalf("Trendable = %#v", payload.Trendable)
	}
	if payload.Usable == nil || *payload.Usable {
		t.Fatalf("Usable = %#v", payload.Usable)
	}
	if payload.Listable == nil || *payload.Listable {
		t.Fatalf("Listable = %#v", payload.Listable)
	}
}

func TestParseAdminTagPayloadUsesRailsBooleanSemanticsForFormValues(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PATCH", "/api/v1/admin/tags/1", strings.NewReader("display_name=GoLang&trendable=no&usable=off&listable="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseAdminTagPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Trendable == nil || !*payload.Trendable {
		t.Fatalf("Trendable = %#v", payload.Trendable)
	}
	if payload.Usable == nil || *payload.Usable {
		t.Fatalf("Usable = %#v", payload.Usable)
	}
	if payload.Listable == nil || *payload.Listable {
		t.Fatalf("Listable = %#v", payload.Listable)
	}
}

func TestUpdateAdminTagValidatesDisplayNameLikeRails(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if err := validateAdminTagDisplayName(*payload.DisplayName, tag.Name); err != nil {`,
		`return apiError(c, http.StatusUnprocessableEntity, err.Error())`,
		`s.meiliIndexTagsBestEffort(c.Request().Context(), []int64{tag.ID})`,
	} {
		if !functionBodyContains(t, src, "updateAdminTag", want) {
			t.Fatalf("updateAdminTag missing %q", want)
		}
	}
}

func TestAdminTagsIncludeRailsTagHistory(t *testing.T) {
	adminSrc, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"adminTags": {
			`out = append(out, s.adminTagFromModel(c, tag))`,
		},
		"showAdminTag": {
			`return c.JSON(http.StatusOK, s.adminTagFromModel(c, *tag))`,
		},
		"updateAdminTag": {
			`return c.JSON(http.StatusOK, s.adminTagFromModel(c, *tag))`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, adminSrc, fn, want) {
				t.Fatalf("admin.go:%s missing %q", fn, want)
			}
		}
	}
	tagsSrc, err := os.ReadFile("tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"adminTagFromModel": {
			`serializer.AdminTagFromModelWithHistoryAndTrendableDefault(s.cfg, tag, s.tagHistory(c.Request().Context(), tag.ID, time.Now().UTC()), s.settingBoolValue("trendable_by_default", false))`,
		},
		"tagHistory": {
			`out := make([]any, 0, 7)`,
			`args := []string{"EVAL", tagHistoryRedisScript, strconv.Itoa(len(days) * 2)}`,
			`s.redisCommand(redisCtx, args...)`,
			`"day":      strconv.FormatInt(day.Unix(), 10)`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, tagsSrc, fn, want) {
				t.Fatalf("tags.go:%s missing %q", fn, want)
			}
		}
	}
}

func TestReverseRowsKeepsNewestFirstForMinIDPagination(t *testing.T) {
	accounts := []models.Account{{ID: 101}, {ID: 102}, {ID: 103}}
	reverseRows(accounts)
	if accounts[0].ID != 103 || accounts[1].ID != 102 || accounts[2].ID != 101 {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func TestAdminIDPaginationUsesRailsMinIDWindow(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id")`,
		`query = query.Where(column+" > ?", minID).Order(column + " ASC")`,
		`if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id")`,
		`if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id")`,
		`query = query.Where(column+" < ?", maxID)`,
		`return query`,
	} {
		if !functionBodyContains(t, src, "applyIDPagination", want) {
			t.Fatalf("admin.go:applyIDPagination does not contain %q", want)
		}
	}
}

func TestAdminListAPIsUseRailsMinIDPagination(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"adminAccounts": {
			`limitValue := limit(c, 100, 200)`,
			`reverseRows(accounts)`,
			`paginationLinkWithAllowedParams(c, accounts[0].ID, accounts[len(accounts)-1].ID, "min_id", len(accounts) == limitValue, true, adminAccountsPaginationParamsForRequest(c))`,
		},
		"adminReports": {
			`limitValue := limit(c, 100, 200)`,
			`reverseRows(reports)`,
			`paginationLinkWithAllowedParams(c, reports[0].ID, reports[len(reports)-1].ID, "min_id", len(reports) == limitValue, true, adminReportsPaginationParams)`,
		},
		"adminTags": {
			`limitValue := limit(c, 100, 200)`,
			`reverseRows(tags)`,
			`paginationLinkWithAllowedParams(c, tags[0].ID, tags[len(tags)-1].ID, "min_id", len(tags) == limitValue, true, adminLimitPaginationParams)`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("admin.go:%s does not contain %q", fn, want)
			}
		}
	}
}

func TestAdminPaginationLinksUseRailsParamAllowLists(t *testing.T) {
	for _, tt := range []struct {
		name        string
		target      string
		allowed     []string
		want        []string
		notWant     []string
		include     bool
		prevParam   string
		first, last int64
	}{
		{
			name:      "accounts",
			target:    "/api/v1/admin/accounts?limit=20&username=alice&display_name=Alice&junk=1&role_ids[]=1&max_id=99",
			allowed:   adminAccountsPaginationParams,
			want:      []string{"limit=20", "username=alice", "display_name=Alice", "max_id=200", "min_id=300"},
			notWant:   []string{"junk=", "role_ids", "max_id=99"},
			include:   true,
			prevParam: "min_id",
			first:     300,
			last:      200,
		},
		{
			name:      "accounts v2",
			target:    "/api/v2/admin/accounts?origin=remote&status=disabled&permissions=staff&invited_by=123&limit=20&local=1&junk=1&max_id=99",
			allowed:   adminAccountsV2PaginationParams,
			want:      []string{"limit=20", "origin=remote", "status=disabled", "permissions=staff", "invited_by=123", "max_id=200", "min_id=300"},
			notWant:   []string{"local=", "junk=", "max_id=99"},
			include:   true,
			prevParam: "min_id",
			first:     300,
			last:      200,
		},
		{
			name:      "reports",
			target:    "/api/v1/admin/reports?limit=15&resolved=true&account_id=5&target_account_id=6&category=spam&since_id=1",
			allowed:   adminReportsPaginationParams,
			want:      []string{"limit=15", "resolved=true", "account_id=5", "target_account_id=6", "max_id=40", "min_id=50"},
			notWant:   []string{"category=", "since_id=1"},
			include:   true,
			prevParam: "min_id",
			first:     50,
			last:      40,
		},
		{
			name:      "limit only",
			target:    "/api/v1/admin/tags?limit=10&display_name=Go&junk=1&min_id=3",
			allowed:   adminLimitPaginationParams,
			want:      []string{"limit=10", "max_id=8", "min_id=9"},
			notWant:   []string{"display_name=", "junk=", "min_id=3"},
			include:   true,
			prevParam: "min_id",
			first:     9,
			last:      8,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			link := paginationLinkWithAllowedParams(c, tt.first, tt.last, tt.prevParam, tt.include, true, tt.allowed)
			for _, want := range tt.want {
				if !strings.Contains(link, want) {
					t.Fatalf("Link = %q, want %q", link, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(link, notWant) {
					t.Fatalf("Link = %q, must not contain %q", link, notWant)
				}
			}
		})
	}
}

func TestAdminReportRESTResponsesUseDetailedAdminAccounts(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"adminReports": {
			`item, err := s.adminReportFromModel(report, statuses, user)`,
			`out = append(out, item)`,
		},
		"renderAdminReport": {
			`out, err := s.adminReportFromModel(report, statuses, user)`,
			`return c.JSON(http.StatusOK, out)`,
		},
		"adminReportFromModel": {
			`account, err := s.adminAccountFromModel(report.Account)`,
			`targetAccount, err := s.adminAccountFromModel(report.TargetAccount)`,
			`out, err := s.adminAccountFromModel(report.AssignedAccount)`,
			`out, err := s.adminAccountFromModel(report.ActionTakenByAccount)`,
			`rules, err := s.reportRules(report)`,
			`currentAccount = &models.Account{ID: user.AccountID}`,
			`return serializer.AdminReportFromModelWithAdminAccountsAndCurrent(s.cfg, report, statuses, account, targetAccount, assignedAccount, actionTakenByAccount, rules, currentAccount), nil`,
		},
		"reportRules": {
			`if len(report.RuleIDs) == 0 {`,
			`if err := s.db.Where("id IN ?", []int64(report.RuleIDs)).Find(&rules).Error; err != nil {`,
			`for _, id := range report.RuleIDs {`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("admin.go:%s does not contain %q", fn, want)
			}
		}
	}
}

func TestAdminAccountRESTResponsesIncludeRailsAdminAccountDetails(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"adminAccounts": {
			`item, err := s.adminAccountFromModel(account)`,
		},
		"showAdminAccount": {
			`out, err := s.adminAccountFromModel(account)`,
		},
		"updateAdminAccount": {
			`out, err := s.adminAccountFromModel(*account)`,
		},
		"updateLocalAdminAccountUser": {
			`out, err := s.adminAccountFromModel(*account)`,
		},
		"adminAccountFromModel": {
			`rows, err := s.adminAccountIPHistory(account.User.ID)`,
			`role, everyone := s.adminAccountRole(account)`,
			`inviteRequest, invitedByAccountID, err := s.adminAccountInviteFields(account)`,
			`return serializer.AdminAccountFromModelWithOptions(s.cfg, account, serializer.AdminAccountOptions{`,
			`InviteRequest:      inviteRequest,`,
			`InvitedByAccountID: invitedByAccountID,`,
		},
		"adminAccountRole": {
			`everyone, _ := s.userRoleByID(-99)`,
			`return everyone, everyone`,
		},
		"adminAccountInviteFields": {
			`inviteRequest, err := s.adminAccountInviteRequest(account.User.ID)`,
			`invitedByAccountID, err := s.adminAccountInvitedByAccountID(account.User.InviteID)`,
		},
		"adminAccountInviteRequest": {
			`var request models.UserInviteRequest`,
			`if !request.Text.Valid {`,
			`return &request.Text.String, nil`,
		},
		"adminAccountInvitedByAccountID": {
			`var invite models.Invite`,
			`accountID := strconv.FormatInt(invite.User.AccountID, 10)`,
			`return &accountID, nil`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("admin.go:%s does not contain %q", fn, want)
			}
		}
	}
}

func TestAdminAccountRESTUnsuspendAppliesRailsUnsuspensionWorkerEffects(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if actionType == "unsuspend"`,
		`destroyCanonicalEmailBlocksForAccountTx(tx, account.ID)`,
		`Delete(&models.AccountDeletionRequest{})`,
		`s.enqueueAdminUnsuspensionOrRun(s.db, account.ID)`,
	} {
		if !functionBodyContains(t, src, "updateAdminAccount", want) {
			t.Fatalf("updateAdminAccount missing %q", want)
		}
	}
}

func TestAdminAccountRESTRejectDestroysLocalAccountRowsLikeRails(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "rejectAdminAccount", `s.deleteRejectedLocalAccountRows(c.Request().Context(), user.AccountID, account, time.Now().UTC())`) {
		t.Fatal("rejectAdminAccount should destroy local account/user rows like Rails admin reject")
	}
	if functionBodyContains(t, src, "rejectAdminAccount", `"approved":   false`) || functionBodyContains(t, src, "rejectAdminAccount", `"disabled":   true`) {
		t.Fatal("rejectAdminAccount should not only disable rejected accounts")
	}
}

func TestAdminAccountPolicyPredicateBoundaries(t *testing.T) {
	pendingLocal := &models.Account{User: models.User{ID: 10, Approved: false}}
	if !adminAccountRejectPermittedByRailsPolicy(pendingLocal) {
		t.Fatal("pending local account should be rejectable")
	}
	approvedLocal := &models.Account{User: models.User{ID: 10, Approved: true}}
	if adminAccountRejectPermittedByRailsPolicy(approvedLocal) {
		t.Fatal("approved local account must not be rejectable")
	}
	remotePending := &models.Account{Domain: sql.NullString{String: "remote.example", Valid: true}, User: models.User{ID: 10, Approved: false}}
	if adminAccountRejectPermittedByRailsPolicy(remotePending) {
		t.Fatal("remote account must not use local reject policy")
	}
	if !adminAccountUnsuspendPermittedByRailsPolicy(&models.Account{SuspensionOrigin: sql.NullInt64{Int64: 0, Valid: true}}) {
		t.Fatal("locally suspended account should be unsuspendable")
	}
	if adminAccountUnsuspendPermittedByRailsPolicy(&models.Account{SuspensionOrigin: sql.NullInt64{Int64: 1, Valid: true}}) {
		t.Fatal("remote-origin suspension must not be unsuspendable")
	}
	if adminAccountUnsuspendPermittedByRailsPolicy(&models.Account{}) {
		t.Fatal("unknown suspension origin must not be unsuspendable")
	}
}

func TestAdminAccountIPsForSerializerFormatsRailsIPHistory(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*60*60)
	rows := []adminAccountIPHistoryRow{{
		IP:     "192.0.2.10",
		UsedAt: sql.NullTime{Time: time.Date(2026, 6, 20, 13, 5, 6, 700000000, tokyo), Valid: true},
	}, {
		IP: "192.0.2.11",
	}}

	out := adminAccountIPsForSerializer(rows)
	if len(out) != 2 {
		t.Fatalf("IPs = %#v, want 2 rows", out)
	}
	if out[0].IP != "192.0.2.10" || out[0].UsedAt == nil || *out[0].UsedAt != "2026-06-20T04:05:06.7Z" {
		t.Fatalf("IPs[0] = %#v", out[0])
	}
	if out[1].IP != "192.0.2.11" || out[1].UsedAt != nil {
		t.Fatalf("IPs[1] = %#v, want nil used_at", out[1])
	}
}

func TestAdminAccountFiltersTranslateRailsV1AndV2Params(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		origin     string
		status     string
		staff      bool
		roleIDVals []string
	}{
		{
			name:   "v1 defaults to Rails local active",
			target: "/api/v1/admin/accounts",
			origin: "local",
			status: "active",
		},
		{
			name:   "v1 legacy flags override defaults",
			target: "/api/v1/admin/accounts?remote=1&pending=1&staff=1",
			origin: "remote",
			status: "pending",
			staff:  true,
		},
		{
			name:   "v2 does not force v1 defaults",
			target: "/api/v2/admin/accounts?by_domain=example.org",
		},
		{
			name:       "v2 accepts status permissions and role arrays",
			target:     "/api/v2/admin/accounts?status=disabled&permissions=staff&role_ids[]=3&role_ids[]=4",
			status:     "disabled",
			staff:      true,
			roleIDVals: []string{"3", "4"},
		},
	}
	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.target, nil)
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())
		if got := adminAccountOriginFilter(c); got != tt.origin {
			t.Fatalf("%s: origin = %q, want %q", tt.name, got, tt.origin)
		}
		if got := adminAccountStatusFilter(c); got != tt.status {
			t.Fatalf("%s: status = %q, want %q", tt.name, got, tt.status)
		}
		if got := adminAccountStaffFilter(c); got != tt.staff {
			t.Fatalf("%s: staff = %v, want %v", tt.name, got, tt.staff)
		}
		if tt.roleIDVals != nil {
			got := adminAccountRoleIDParams(c)
			if strings.Join(got, ",") != strings.Join(tt.roleIDVals, ",") {
				t.Fatalf("%s: role ids = %#v, want %#v", tt.name, got, tt.roleIDVals)
			}
		}
	}
}

func TestAdminAccountQueryUsesRailsAccountFilterSemantics(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`origin := adminAccountOriginFilter(c)`,
		`query.AddError(adminAccountInvalidFilterError("origin", origin))`,
		`status := adminAccountStatusFilter(c)`,
		`switch status`,
		`query.AddError(adminAccountInvalidFilterError("status", status))`,
		`query = query.Where("accounts.suspended_at IS NULL")`,
		`s.roleIDsWithPermission(s.db, rolePermissionManageReports)`,
		`roleIDs := adminAccountRoleIDParams(c)`,
		`query = adminAccountRoleScope(query, roleIDs)`,
		`if adminAccountValidIPFilter(ip)`,
		`Where("user_ips.ip <<= ?", ip)`,
		`query = query.Where("1 = 0")`,
		`Where("admin_account_invites.user_id = ?", invitedBy)`,
		`strings.TrimPrefix(username, "@")+"%"`,
	} {
		if !functionBodyContains(t, src, "adminAccountQuery", want) {
			t.Fatalf("admin.go:adminAccountQuery does not contain %q", want)
		}
	}
}

func TestAdminAccountInvalidFilterErrorMatchesRailsInvalidParameter(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "origin", value: "bad", want: "Unknown origin: bad"},
		{name: "status", value: "gone", want: "Unknown status: gone"},
		{name: "order", value: "garbage", want: "Unknown order: garbage"},
	} {
		err := adminAccountInvalidFilterError(tt.name, tt.value)
		apiErr, ok := err.(apiHTTPError)
		if !ok {
			t.Fatalf("error type = %T, want apiHTTPError", err)
		}
		if apiErr.status != http.StatusBadRequest || apiErr.message != tt.want {
			t.Fatalf("api error = %#v, want 400 %q", apiErr, tt.want)
		}
	}
}

func TestAdminAccountValidIPFilterMatchesRailsIPAddrValidation(t *testing.T) {
	for _, value := range []string{"192.0.2.10", "192.0.2.0/24", "2001:db8::1", "2001:db8::/32"} {
		if !adminAccountValidIPFilter(value) {
			t.Fatalf("adminAccountValidIPFilter(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "not-an-ip", "192.0.2.999", "2001:db8::/129"} {
		if adminAccountValidIPFilter(value) {
			t.Fatalf("adminAccountValidIPFilter(%q) = true, want false", value)
		}
	}
}

func TestAdminReportQueryDefaultsToRailsUnresolvedScope(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if queryParamPresent(c, "resolved")`,
		`query = query.Where("reports.action_taken_at IS NOT NULL")`,
		`query = query.Where("reports.action_taken_at IS NULL")`,
		`if queryParamPresent(c, "account_id")`,
		`query = query.Where("reports.account_id = ?", c.QueryParam("account_id"))`,
		`if queryParamPresent(c, "target_account_id")`,
		`query = query.Where("reports.target_account_id = ?", c.QueryParam("target_account_id"))`,
	} {
		if !functionBodyContains(t, src, "adminReportQuery", want) {
			t.Fatalf("admin.go:adminReportQuery does not contain %q", want)
		}
	}
	if functionBodyContains(t, src, "adminReportQuery", `resolved == "true" || resolved == "1"`) {
		t.Fatal("adminReportQuery must match Rails ReportFilter: presence of resolved selects resolved reports")
	}
	if functionBodyContains(t, src, "adminReportQuery", `if accountID := c.QueryParam("account_id"); accountID != ""`) ||
		functionBodyContains(t, src, "adminReportQuery", `if targetAccountID := c.QueryParam("target_account_id"); targetAccountID != ""`) {
		t.Fatal("adminReportQuery must match Rails ReportFilter: account ID filters are activated by key presence")
	}
}

func TestParseAdminReportPayloadAcceptsJSONRuleIDs(t *testing.T) {
	body := `{"category":"violation","rule_ids":[1,"2"]}`
	req := httptest.NewRequest("PATCH", "/api/v1/admin/reports/123", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminReportPayload(c)
	if err != nil {
		t.Fatalf("parseAdminReportPayload: %v", err)
	}
	if payload.Category == nil || *payload.Category != "violation" {
		t.Fatalf("category = %#v", payload.Category)
	}
	if len(payload.RuleIDs) != 2 || payload.RuleIDs[0] != "1" || payload.RuleIDs[1] != "2" {
		t.Fatalf("rule ids = %#v", payload.RuleIDs)
	}
}

func TestParseAdminReportPayloadAcceptsRailsNestedFormNames(t *testing.T) {
	body := "report%5Bcategory%5D=violation&report%5Brule_ids%5D%5B%5D=3&report%5Brule_ids%5D%5B%5D=4"
	req := httptest.NewRequest("POST", "/admin/reports/123", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminReportPayload(c)
	if err != nil {
		t.Fatalf("parseAdminReportPayload: %v", err)
	}
	if payload.Category == nil || *payload.Category != "violation" {
		t.Fatalf("category = %#v", payload.Category)
	}
	if len(payload.RuleIDs) != 2 || payload.RuleIDs[0] != "3" || payload.RuleIDs[1] != "4" {
		t.Fatalf("rule ids = %#v", payload.RuleIDs)
	}
}

func TestUpdateAdminReportRejectsUnknownCategoryLikeRailsEnum(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`category, ok := reportCategoryValueOK(*payload.Category)`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Category is not included in the list")`,
	} {
		if !functionBodyContains(t, src, "updateAdminReport", want) {
			t.Fatalf("admin.go:updateAdminReport does not contain %q", want)
		}
	}
}

func TestAdminAccountActionRequiresAdminToken(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/admin/accounts/123/action", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminAccountAction(c); err == nil {
		t.Fatal("expected admin account action to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestParseAdminAccountActionPayloadAcceptsJSONNumbers(t *testing.T) {
	body := `{"type":"silence","text":"custom warning","report_id":42,"warning_preset_id":"7"}`
	req := httptest.NewRequest("POST", "/api/v1/admin/accounts/123/action", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminAccountActionPayload(c)
	if err != nil {
		t.Fatalf("parseAdminAccountActionPayload: %v", err)
	}
	if payload.Type != "silence" || payload.Text != "custom warning" || payload.ReportID != 42 || payload.WarningPresetID != 7 {
		t.Fatalf("payload = %#v", payload)
	}
	if !payload.SendEmailNotification {
		t.Fatalf("send email notification default = false, want true")
	}
}

func TestParseAdminAccountActionPayloadCastsRailsBooleanEmailFlag(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "json false", body: `{"type":"none","send_email_notification":false}`, want: false},
		{name: "json zero", body: `{"type":"none","send_email_notification":0}`, want: false},
		{name: "json off", body: `{"type":"none","send_email_notification":"off"}`, want: false},
		{name: "json true", body: `{"type":"none","send_email_notification":"1"}`, want: true},
		{name: "json no is rails true", body: `{"type":"none","send_email_notification":"no"}`, want: true},
		{name: "json arbitrary string is rails true", body: `{"type":"none","send_email_notification":"bad"}`, want: true},
	}
	for _, tt := range tests {
		req := httptest.NewRequest("POST", "/api/v1/admin/accounts/123/action", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())

		payload, err := parseAdminAccountActionPayload(c)
		if err != nil {
			t.Fatalf("%s: parseAdminAccountActionPayload: %v", tt.name, err)
		}
		if payload.SendEmailNotification != tt.want {
			t.Fatalf("%s: send email notification = %v, want %v", tt.name, payload.SendEmailNotification, tt.want)
		}
	}
}

func TestParseAdminAccountActionPayloadCastsRailsFormBooleanEmailFlag(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/admin/accounts/123/action", strings.NewReader("type=none&send_email_notification="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminAccountActionPayload(c)
	if err != nil {
		t.Fatalf("parseAdminAccountActionPayload: %v", err)
	}
	if payload.SendEmailNotification {
		t.Fatal("blank Rails boolean form value should disable email notification")
	}
}

func TestAdminAccountActionHonorsSendEmailNotificationFlag(t *testing.T) {
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "adminAccountAction", `if payload.SendEmailNotification && target.User.ID != 0 {`) {
		t.Fatal("adminAccountAction must not send warning mail when send_email_notification is false")
	}
}

func TestAdminAccountActionCodeMatchesMastodonWarningEnum(t *testing.T) {
	cases := map[string]int{
		"none":      0,
		"disable":   1000,
		"sensitive": 2000,
		"silence":   3000,
		"suspend":   4000,
	}
	for actionType, want := range cases {
		got, ok := adminAccountActionCode(actionType)
		if !ok || got != want {
			t.Fatalf("adminAccountActionCode(%q) = %d, %v; want %d, true", actionType, got, ok, want)
		}
	}
	if _, ok := adminAccountActionCode("destroy"); ok {
		t.Fatal("unexpected support for unknown action")
	}
}

func TestReportStatusIDStrings(t *testing.T) {
	got := reportStatusIDStrings(models.Int64Array{10, 20})
	if len(got) != 2 || got[0] != "10" || got[1] != "20" {
		t.Fatalf("reportStatusIDStrings = %#v", got)
	}
}
