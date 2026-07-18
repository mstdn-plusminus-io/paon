package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminAccountsWebRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts?status=pending", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/accounts?status=pending")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminAccountBatchIDs(t *testing.T) {
	form := url.Values{}
	form.Add("form_account_batch[account_ids][]", "4")
	form.Add("form_account_batch[account_ids][]", "bad")
	form.Add("form_account_batch[account_ids]", "4")
	form.Add("form_account_batch[account_ids]", "5")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminAccountBatchIDs(c)
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminAccountBatchSelectsAllMatching(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch?select_all_matching=0", strings.NewReader("select_all_matching=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if !adminAccountBatchSelectsAllMatching(c) {
		t.Fatal("select_all_matching form value was not honored")
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/accounts/batch?select_all_matching=1", nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if !adminAccountBatchSelectsAllMatching(c) {
		t.Fatal("select_all_matching query value was not honored")
	}
}

func TestAdminAccountWebRejectDestroysLocalAccountRowsLikeRails(t *testing.T) {
	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "applyAdminAccountWebAction", `return s.deleteRejectedLocalAccountRows(context.Background(), user.AccountID, account, now)`) {
		t.Fatal("admin web reject should destroy local account/user rows like Rails admin reject")
	}
	if functionBodyContains(t, src, "applyAdminAccountWebAction", `return updateUserAndLog(map[string]any{"approved": false, "disabled": true})`) {
		t.Fatal("admin web reject should not only disable rejected accounts")
	}
}

func TestAdminAccountModelsUseRailsKaminariPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Offset(adminRailsPageOffset(c))",
		"Limit(adminRailsDefaultPageSize)",
	} {
		if !functionBodyContains(t, src, "adminAccountModels", want) {
			t.Fatalf("adminAccountModels missing %q", want)
		}
	}
}

func TestAdminAccountBatchRedirectURLPreservesFilters(t *testing.T) {
	form := url.Values{}
	form.Set("page", "3")
	form.Set("origin", "remote")
	form.Set("status", "pending")
	form.Set("order", "active")
	form.Set("username", "ali")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch?by_domain=example.com", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminAccountBatchRedirectURL(c, "", "")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/admin/accounts" {
		t.Fatalf("path = %q", parsed.Path)
	}
	values := parsed.Query()
	for key, want := range map[string]string{
		"page":      "3",
		"origin":    "remote",
		"status":    "pending",
		"order":     "active",
		"username":  "ali",
		"by_domain": "example.com",
	} {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q in %s", key, got, want, parsed.RawQuery)
		}
	}
	if values.Has("notice") || values.Has("error") {
		t.Fatalf("success redirect should only preserve Rails filter params, got %s", parsed.RawQuery)
	}
}

func TestAdminAccountsHTMLIncludesRailsFieldsAndBatchActions(t *testing.T) {
	html := adminAccountsHTML([]models.Account{{
		ID:             7,
		Username:       "alice",
		DisplayName:    "Alice",
		AvatarFileName: sql.NullString{String: "avatar.png", Valid: true},
		CreatedAt:      time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		User:           models.User{ID: 9, Approved: false},
	}}, "saved", "", adminAccountFilters{Origin: "local", Status: "pending", Order: "recent", Username: "ali"})
	for _, want := range []string{
		"Accounts",
		`action="/admin/accounts"`,
		`name="status"`,
		`value="pending" selected`,
		`name="username" value="ali"`,
		`action="/admin/accounts/batch"`,
		`name="select_all_matching" value="0"`,
		`name="page" value="1"`,
		`name="status" value="pending"`,
		`name="order" value="recent"`,
		`name="username" value="ali"`,
		`name="form_account_batch[account_ids][]" value="7"`,
		`name="approve" value="1"`,
		`href="/admin/accounts/7"`,
		"Pending",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("accounts html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountsHTMLIncludesSelectAllMatchingControlsAtPageLimit(t *testing.T) {
	accounts := make([]models.Account, adminRailsDefaultPageSize)
	for i := range accounts {
		accounts[i] = models.Account{ID: int64(i + 1), Username: "user" + strconv.Itoa(i+1), CreatedAt: time.Now().UTC()}
	}
	html := adminAccountsHTML(accounts, "", "", adminAccountFilters{})
	for _, want := range []string{
		`class="batch-table__select-all"`,
		`class="not-selected active"`,
		"Select all matching accounts",
		`All <strong>40</strong> items on this page are selected.`,
		`All <strong>41</strong> items matching your search are selected.`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("accounts html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `&lt;strong&gt;40&lt;/strong&gt;`) {
		t.Fatalf("accounts html escaped Rails batch-selection HTML locale: %s", html)
	}
}

func TestAdminAccountHTMLIncludesMajorActions(t *testing.T) {
	account := models.Account{
		ID:             7,
		Username:       "alice",
		DisplayName:    "Alice",
		AvatarFileName: sql.NullString{String: "avatar.png", Valid: true},
		CreatedAt:      time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		AccountStat:    models.AccountStat{StatusesCount: 12},
		User:           models.User{ID: 9, Email: "alice@example.test", Approved: true},
	}
	html := adminAccountHTML(account, "", "")
	for _, want := range []string{
		"alice",
		`href="/admin/accounts/7/statuses"`,
		`href="/admin/reports?target_account_id=7"`,
		`href="/admin/accounts/7/action/new?type=disable"`,
		`href="/admin/accounts/7/action/new?type=silence"`,
		`href="/admin/accounts/7/action/new?type=suspend"`,
		`data-method="post" href="/admin/accounts/7/remove_avatar"`,
		"alice@example.test",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("account html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountHTMLShowsRedownloadOnlyForRemoteAccounts(t *testing.T) {
	local := adminAccountHTML(models.Account{ID: 7, Username: "alice", User: models.User{ID: 9, Approved: true}}, "", "")
	if strings.Contains(local, `href="/admin/accounts/7/redownload"`) {
		t.Fatalf("local account should not show redownload: %s", local)
	}

	remote := adminAccountHTML(models.Account{ID: 8, Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}}, "", "")
	if !strings.Contains(remote, `href="/admin/accounts/8/redownload"`) {
		t.Fatalf("remote account should show redownload: %s", remote)
	}

	localSuspension := adminAccountHTML(models.Account{ID: 9, Username: "carol", Domain: sql.NullString{String: "remote.example", Valid: true}, SuspendedAt: sql.NullTime{Time: time.Now(), Valid: true}, SuspensionOrigin: sql.NullInt64{Int64: 0, Valid: true}}, "", "")
	if strings.Contains(localSuspension, `href="/admin/accounts/9/redownload"`) {
		t.Fatalf("locally suspended remote account should not show redownload: %s", localSuspension)
	}

	remoteSuspension := adminAccountHTML(models.Account{ID: 10, Username: "dave", Domain: sql.NullString{String: "remote.example", Valid: true}, SuspendedAt: sql.NullTime{Time: time.Now(), Valid: true}, SuspensionOrigin: sql.NullInt64{Int64: 1, Valid: true}}, "", "")
	if !strings.Contains(remoteSuspension, `href="/admin/accounts/10/redownload"`) {
		t.Fatalf("remotely suspended account should show redownload: %s", remoteSuspension)
	}
}

func TestAdminAccountHTMLIncludesIPHistory(t *testing.T) {
	account := models.Account{
		ID:        7,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		User:      models.User{ID: 9, Email: "alice@example.test", Approved: true},
	}
	html := adminAccountHTMLWithIPHistory(account, "", "", []adminAccountIPHistoryRow{{
		IP:     "192.0.2.10",
		UsedAt: sql.NullTime{Time: time.Date(2026, 6, 19, 12, 34, 56, 0, time.UTC), Valid: true},
	}}, "en", config.Config{})
	for _, want := range []string{
		"Most recent IP",
		`class="table-wrapper"`,
		`class="ellipsized-ip"`,
		"192.0.2.10",
		`href="/admin/accounts?ip=192.0.2.10"`,
		"Other users with the same IP",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("account html missing IP history %q: %s", want, html)
		}
	}
}

func TestAdminAccountIPHistoryHTMLAllowsNullUsedAt(t *testing.T) {
	html := adminAccountIPHistoryHTML([]adminAccountIPHistoryRow{{
		IP: "192.0.2.10",
	}}, "en")
	if !strings.Contains(html, "192.0.2.10") {
		t.Fatalf("account html missing IP history row: %s", html)
	}
	if strings.Contains(html, "0001-") {
		t.Fatalf("null used_at must render blank instead of zero time: %s", html)
	}
}

func TestAdminAccountIPHistoryQueryUsesRailsUserIPSources(t *testing.T) {
	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "UsedAt sql.NullTime") {
		t.Fatal("adminAccountIPHistoryRow.UsedAt must stay nullable for Rails-compatible user_ips rows")
	}
	for _, want := range []string{
		"SELECT sign_up_ip AS ip",
		"FROM session_activations",
		"FROM login_activities",
		"success = true",
		"GROUP BY ip",
	} {
		if !functionBodyContains(t, src, "adminAccountIPHistory", want) {
			t.Fatalf("adminAccountIPHistory does not contain %q", want)
		}
	}
}

func TestAdminAccountActionFormHTMLIncludesRailsRoute(t *testing.T) {
	html := adminAccountActionFormHTMLWithPresets(models.Account{ID: 7, Username: "alice", Domain: sql.NullString{}}, "suspend", 9, []models.AccountWarningPreset{{ID: 3, Title: "Spam warning"}}, "bad")
	for _, want := range []string{
		"alice",
		`action="/admin/accounts/7/action"`,
		`class="simple_form new_admin_account_action"`,
		`name="admin_account_action[type]" value="none"`,
		`name="admin_account_action[type]" value="disable"`,
		`name="admin_account_action[type]" value="sensitive"`,
		`name="admin_account_action[type]" value="silence"`,
		`name="admin_account_action[type]" value="suspend" checked`,
		`name="admin_account_action[report_id]" value="9"`,
		`name="admin_account_action[send_email_notification]" value="1" checked`,
		`name="admin_account_action[include_statuses]" value="1" checked`,
		`name="admin_account_action[warning_preset_id]"`,
		`>Spam warning</option>`,
		`name="admin_account_action[text]"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("account action html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountUnsuspendAppliesRailsUnsuspensionWorkerEffects(t *testing.T) {
	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if actionType == "unsuspend"`,
		`destroyCanonicalEmailBlocksForAccountTx(tx, account.ID)`,
		`Delete(&models.AccountDeletionRequest{})`,
		`s.enqueueAdminUnsuspensionOrRun(s.db, account.ID)`,
	} {
		if !functionBodyContains(t, src, "applyAdminAccountWebAction", want) {
			t.Fatalf("applyAdminAccountWebAction missing %q", want)
		}
	}
}

func TestAdminAccountRemoveHeaderWritesRailsAuditLog(t *testing.T) {
	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`case "remove_avatar":`,
		`s.removeAccountImageObjects(models.Account{ID: account.ID, AvatarFileName: account.AvatarFileName})`,
		`s.removeAccountLocalImageFilesForKind(account.ID, "avatar")`,
		`case "remove_header":`,
		`s.removeAccountImageObjects(models.Account{ID: account.ID, HeaderFileName: account.HeaderFileName})`,
		`s.removeAccountLocalImageFilesForKind(account.ID, "header")`,
		`return updateAccountAndLog(map[string]any{"header_file_name": nil`,
	} {
		if !functionBodyContains(t, src, "applyAdminAccountWebAction", want) {
			t.Fatalf("remove_header handling does not contain %q", want)
		}
	}
}

func TestAdminAccountStateLabel(t *testing.T) {
	if got := adminAccountStateLabel(models.Account{User: models.User{ID: 1, Approved: false}}); got != "Pending review" {
		t.Fatalf("state = %q", got)
	}
	if got := adminAccountStateLabel(models.Account{SilencedAt: sql.NullTime{Valid: true}}); got != "Limited" {
		t.Fatalf("state = %q", got)
	}
}
