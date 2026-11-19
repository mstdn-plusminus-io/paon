//go:build integration

package api

import (
	"context"
	"database/sql"
	"io"
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
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminStaffActionsFollowRailsRoleHierarchyIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		Scheme:               "https",
		LocalDomain:          "paon.example",
		WebDomain:            "paon.example",
		SecretKeyBase:        "admin-staff-policy-integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	now := time.Now().UTC()
	create := func(value any) {
		t.Helper()
		if err := database.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	type staffFixture struct {
		account models.Account
		user    models.User
		token   string
	}
	fixtures := map[string]staffFixture{}
	// Role ids 1, 2 and 3 are the Moderator, Admin and Owner roles seeded like Rails config/roles.yml.
	for _, fixture := range []struct {
		name      string
		roleID    int64
		confirmed bool
	}{
		{"owner", 3, true},
		{"admin", 2, true},
		{"moderator", 1, true},
		{"member", 0, true},
		{"pending", 0, false},
		{"spammer", 0, true},
	} {
		account := models.Account{Username: fixture.name, CreatedAt: now, UpdatedAt: now}
		create(&account)
		user := models.User{AccountID: account.ID, Email: fixture.name + "@paon.example", EncryptedPassword: "unchanged", Approved: true, CreatedAt: now, UpdatedAt: now}
		if fixture.confirmed {
			user.ConfirmedAt = sql.NullTime{Time: now, Valid: true}
		}
		if fixture.roleID != 0 {
			user.RoleID = sql.NullInt64{Int64: fixture.roleID, Valid: true}
		}
		create(&user)
		token := "admin-staff-policy-" + fixture.name + "-" + randomHex(8)
		accessToken := models.OAuthAccessToken{Token: token, ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true}, Scopes: "read write", CreatedAt: now}
		create(&accessToken)
		// The admin pages are browser routes, so they only accept a session
		// cookie bound to a session activation, like a Devise session in Rails.
		activation := sessionActivationForRequest(user.ID, accessToken.ID, "192.0.2.1", "integration", now)
		create(&activation)
		fixtures[fixture.name] = staffFixture{account: account, user: user, token: token}
	}

	server := &Server{cfg: cfg, db: database}
	call := func(handler func(*echo.Context) error, method string, actor string, params echo.PathValues, form url.Values) int {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req := httptest.NewRequest(method, "/admin", body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: fixtures[actor].token})
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())
		c.SetPathValues(params)
		if err := handler(c); err != nil {
			if status := echo.StatusCode(err); status != 0 {
				return status
			}
			t.Fatalf("%s as %s: %v", method, actor, err)
		}
		return rec.Code
	}
	accountParam := func(name string) echo.PathValues {
		return echo.PathValues{{Name: "account_id", Value: strconv.FormatInt(fixtures[name].account.ID, 10)}}
	}
	idParam := func(id int64) echo.PathValues {
		return echo.PathValues{{Name: "id", Value: strconv.FormatInt(id, 10)}}
	}
	reloadUser := func(name string) models.User {
		t.Helper()
		var user models.User
		if err := database.First(&user, fixtures[name].user.ID).Error; err != nil {
			t.Fatal(err)
		}
		return user
	}
	reloadAccount := func(id int64) models.Account {
		t.Helper()
		var account models.Account
		if err := database.First(&account, id).Error; err != nil {
			t.Fatal(err)
		}
		return account
	}

	// (a) UserPolicy#change_email? / #reset_password? need manage_user_access, which
	// the default Moderator lacks, and role.overrides?(record.role), which Admin lacks
	// over Owner.
	hijack := url.Values{"user[unconfirmed_email]": {"attacker@evil.example"}}
	for _, tc := range []struct{ actor, target string }{
		{"moderator", "owner"},
		{"moderator", "admin"},
		{"moderator", "member"},
		{"admin", "owner"},
	} {
		if code := call(server.adminAccountChangeEmailPage, http.MethodGet, tc.actor, accountParam(tc.target), nil); code != http.StatusForbidden {
			t.Fatalf("%s opens change_email for %s: status = %d, want 403", tc.actor, tc.target, code)
		}
		if code := call(server.updateAdminAccountChangeEmail, http.MethodPatch, tc.actor, accountParam(tc.target), hijack); code != http.StatusForbidden {
			t.Fatalf("%s changes the e-mail of %s: status = %d, want 403", tc.actor, tc.target, code)
		}
		if code := call(server.resetAdminAccountPasswordWeb, http.MethodPost, tc.actor, accountParam(tc.target), nil); code != http.StatusForbidden {
			t.Fatalf("%s resets the password of %s: status = %d, want 403", tc.actor, tc.target, code)
		}
		target := reloadUser(tc.target)
		if target.Email != tc.target+"@paon.example" || target.UnconfirmedEmail.Valid || target.EncryptedPassword != "unchanged" || target.ResetPasswordToken.Valid {
			t.Fatalf("%s changed %s: email=%q unconfirmed=%v password=%q reset_token=%v", tc.actor, tc.target, target.Email, target.UnconfirmedEmail, target.EncryptedPassword, target.ResetPasswordToken.Valid)
		}
	}
	// UserPolicy#confirm? needs manage_user_access too, for both confirm and resend.
	for _, target := range []string{"pending", "owner"} {
		for _, handler := range []func(*echo.Context) error{server.confirmAdminAccountWeb, server.resendAdminAccountConfirmationWeb} {
			if code := call(handler, http.MethodPost, "moderator", accountParam(target), nil); code != http.StatusForbidden {
				t.Fatalf("moderator confirms %s: status = %d, want 403", target, code)
			}
		}
	}
	if pending := reloadUser("pending"); pending.ConfirmedAt.Valid || pending.ConfirmationToken.Valid {
		t.Fatalf("moderator confirmed the pending user: confirmed_at=%v token=%v", pending.ConfirmedAt.Valid, pending.ConfirmationToken.Valid)
	}
	// confirm? also rejects confirmed users, so a pending e-mail change cannot be discarded.
	if err := database.Model(&models.User{}).Where("id = ?", fixtures["owner"].user.ID).Update("unconfirmed_email", "owner-new@paon.example").Error; err != nil {
		t.Fatal(err)
	}
	if code := call(server.confirmAdminAccountWeb, http.MethodPost, "admin", accountParam("owner"), nil); code != http.StatusForbidden {
		t.Fatalf("admin confirms a confirmed owner: status = %d, want 403", code)
	}
	if owner := reloadUser("owner"); owner.UnconfirmedEmail.String != "owner-new@paon.example" {
		t.Fatalf("owner unconfirmed_email = %v, want the pending change kept", owner.UnconfirmedEmail)
	}

	// (d) Higher roles keep managing lower ones.
	if code := call(server.adminAccountChangeEmailPage, http.MethodGet, "owner", accountParam("admin"), nil); code != http.StatusOK {
		t.Fatalf("owner opens change_email for admin: status = %d, want 200", code)
	}
	if code := call(server.updateAdminAccountChangeEmail, http.MethodPatch, "admin", accountParam("member"), url.Values{"user[unconfirmed_email]": {"member-new@paon.example"}}); code != http.StatusFound {
		t.Fatalf("admin changes the e-mail of member: status = %d, want 302", code)
	}
	if member := reloadUser("member"); member.UnconfirmedEmail.String != "member-new@paon.example" {
		t.Fatalf("member unconfirmed_email = %v", member.UnconfirmedEmail)
	}
	if code := call(server.confirmAdminAccountWeb, http.MethodPost, "admin", accountParam("pending"), nil); code != http.StatusFound {
		t.Fatalf("admin confirms the pending user: status = %d, want 302", code)
	}
	if !reloadUser("pending").ConfirmedAt.Valid {
		t.Fatal("admin could not confirm the pending user")
	}
	if code := call(server.resetAdminAccountPasswordWeb, http.MethodPost, "admin", accountParam("member"), nil); code != http.StatusFound {
		t.Fatalf("admin resets the password of member: status = %d, want 302", code)
	}
	if member := reloadUser("member"); member.EncryptedPassword == "unchanged" || !member.ResetPasswordToken.Valid {
		t.Fatal("admin could not reset the password of member")
	}

	// (b) Report silence/suspend authorize AccountPolicy#silence? / #suspend? against the target.
	newReport := func(targetAccountID int64) echo.PathValues {
		t.Helper()
		report := models.Report{AccountID: fixtures["member"].account.ID, TargetAccountID: targetAccountID, StatusIDs: models.Int64Array{}, CreatedAt: now, UpdatedAt: now}
		create(&report)
		return echo.PathValues{{Name: "report_id", Value: strconv.FormatInt(report.ID, 10)}}
	}
	for _, tc := range []struct{ actor, target string }{
		{"moderator", "owner"},
		{"moderator", "admin"},
		{"admin", "owner"},
	} {
		report := newReport(fixtures[tc.target].account.ID)
		for _, action := range []string{"suspend", "silence"} {
			if code := call(server.createAdminReportActionWeb, http.MethodPost, tc.actor, report, url.Values{action: {"1"}}); code != http.StatusForbidden {
				t.Fatalf("%s %s %s through a report: status = %d, want 403", tc.actor, action, tc.target, code)
			}
		}
		if account := reloadAccount(fixtures[tc.target].account.ID); account.SuspendedAt.Valid || account.SilencedAt.Valid {
			t.Fatalf("%s was limited by %s through a report", tc.target, tc.actor)
		}
	}
	if code := call(server.createAdminReportActionWeb, http.MethodPost, "owner", newReport(-99), url.Values{"suspend": {"1"}}); code != http.StatusForbidden {
		t.Fatalf("owner suspends the instance actor through a report: status = %d, want 403", code)
	}
	if reloadAccount(-99).SuspendedAt.Valid {
		t.Fatal("the instance actor was suspended through a report")
	}
	if code := call(server.createAdminReportActionWeb, http.MethodPost, "moderator", newReport(fixtures["spammer"].account.ID), url.Values{"silence": {"1"}}); code != http.StatusFound {
		t.Fatalf("moderator silences spammer through a report: status = %d, want 302", code)
	}
	if !reloadAccount(fixtures["spammer"].account.ID).SilencedAt.Valid {
		t.Fatal("moderator could not silence spammer through a report")
	}

	// (c) AccountPolicy#memorialize?, #remove_avatar? and #remove_header?.
	for _, tc := range []struct {
		name    string
		handler func(*echo.Context) error
		actor   string
		target  int64
	}{
		{"admin memorializes owner", server.memorializeAdminAccountWeb, "admin", fixtures["owner"].account.ID},
		{"owner memorializes the instance actor", server.memorializeAdminAccountWeb, "owner", -99},
		{"moderator removes the avatar of admin", server.removeAvatarAdminAccountWeb, "moderator", fixtures["admin"].account.ID},
		{"admin removes the header of owner", server.removeHeaderAdminAccountWeb, "admin", fixtures["owner"].account.ID},
	} {
		if code := call(tc.handler, http.MethodPost, tc.actor, idParam(tc.target), nil); code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", tc.name, code)
		}
	}
	if reloadAccount(fixtures["owner"].account.ID).Memorial || reloadAccount(-99).Memorial {
		t.Fatal("a lower role memorialized a protected account")
	}
	if code := call(server.memorializeAdminAccountWeb, http.MethodPost, "admin", idParam(fixtures["spammer"].account.ID), nil); code != http.StatusFound {
		t.Fatalf("admin memorializes spammer: status = %d, want 302", code)
	}
	if !reloadAccount(fixtures["spammer"].account.ID).Memorial {
		t.Fatal("admin could not memorialize spammer")
	}

	// (c) UserRolePolicy#update? needs role.overrides?(record) or the actor's own role.
	demoteOwner := url.Values{"user_role[name]": {"Owner"}, "user_role[position]": {"1"}, "user_role[permissions_as_keys][]": {"view_dashboard"}}
	if code := call(server.editAdminRolePage, http.MethodGet, "admin", idParam(3), nil); code != http.StatusForbidden {
		t.Fatalf("admin opens the owner role: status = %d, want 403", code)
	}
	if code := call(server.updateAdminRole, http.MethodPatch, "admin", idParam(3), demoteOwner); code != http.StatusForbidden {
		t.Fatalf("admin updates the owner role: status = %d, want 403", code)
	}
	var ownerRole models.UserRole
	if err := database.First(&ownerRole, 3).Error; err != nil {
		t.Fatal(err)
	}
	if ownerRole.Position != 1000 || ownerRole.Permissions != rolePermissionAdministrator {
		t.Fatalf("owner role = position %d permissions %d, want it untouched", ownerRole.Position, ownerRole.Permissions)
	}
	for _, id := range []int64{2, 1, -99} {
		if code := call(server.editAdminRolePage, http.MethodGet, "admin", idParam(id), nil); code != http.StatusOK {
			t.Fatalf("admin opens role %d: status = %d, want 200", id, code)
		}
	}
	renameModerator := url.Values{
		"user_role[name]":                  {"Moderators"},
		"user_role[position]":              {"10"},
		"user_role[permissions_as_keys][]": {"view_dashboard", "view_audit_log", "manage_users", "manage_reports", "manage_taxonomies"},
	}
	if code := call(server.updateAdminRole, http.MethodPatch, "admin", idParam(1), renameModerator); code != http.StatusFound {
		t.Fatalf("admin updates the moderator role: status = %d, want 302", code)
	}
	var moderatorRole models.UserRole
	if err := database.First(&moderatorRole, 1).Error; err != nil {
		t.Fatal(err)
	}
	if moderatorRole.Name != "Moderators" || moderatorRole.Position != 10 || moderatorRole.Permissions != 1308 {
		t.Fatalf("moderator role = %+v", moderatorRole)
	}
}
