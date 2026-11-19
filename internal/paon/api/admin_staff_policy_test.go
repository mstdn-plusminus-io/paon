package api

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/logger"
)

// The roles seeded by migrate.seedFreshDatabase, matching Rails config/roles.yml.
var (
	adminPolicyEveryoneRole  = models.UserRole{ID: -99, Position: -1, Permissions: rolePermissionInviteUsers}
	adminPolicyModeratorRole = models.UserRole{ID: 1, Name: "Moderator", Position: 10, Permissions: 1308}
	adminPolicyAdminRole     = models.UserRole{ID: 2, Name: "Admin", Position: 100, Permissions: 983036}
	adminPolicyOwnerRole     = models.UserRole{ID: 3, Name: "Owner", Position: 1000, Permissions: rolePermissionAdministrator}
)

// adminPolicyTestServer answers the user_roles lookups made by the Rails policy
// helpers from the default roles, so the helpers run without a database.
func adminPolicyTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=paon dbname=paon",
	}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	roles := map[int64]models.UserRole{}
	for _, role := range []models.UserRole{adminPolicyEveryoneRole, adminPolicyModeratorRole, adminPolicyAdminRole, adminPolicyOwnerRole} {
		roles[role.ID] = role
	}
	if err := database.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		callbacks.BuildQuerySQL(tx)
		dest, ok := tx.Statement.Dest.(*models.UserRole)
		if !ok || len(tx.Statement.Vars) == 0 {
			tx.AddError(fmt.Errorf("unexpected query in policy test: %s", tx.Statement.SQL.String()))
			return
		}
		id, _ := tx.Statement.Vars[0].(int64)
		role, found := roles[id]
		if !found {
			tx.AddError(gorm.ErrRecordNotFound)
			return
		}
		*dest = role
		tx.RowsAffected = 1
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{db: database}
}

func adminPolicyStaff(role models.UserRole) *models.User {
	return &models.User{ID: 100 + role.ID, AccountID: 200 + role.ID, RoleID: sql.NullInt64{Int64: role.ID, Valid: true}}
}

// adminPolicyTargetUser builds a target user the way loadAdminAccount preloads it
// (User.Role); a nil role is a user without a role.
func adminPolicyTargetUser(id int64, role *models.UserRole) models.User {
	user := models.User{ID: id, AccountID: 1000 + id, ConfirmedAt: sql.NullTime{Valid: true}}
	if role != nil {
		user.RoleID = sql.NullInt64{Int64: role.ID, Valid: true}
		user.Role = *role
	}
	return user
}

func adminPolicyLocalAccount(id int64, role *models.UserRole) *models.Account {
	return &models.Account{ID: 1000 + id, Username: fmt.Sprintf("user%d", id), User: adminPolicyTargetUser(id, role)}
}

func TestAdminUserAccessPolicyMatchesRailsDefaultRoles(t *testing.T) {
	s := adminPolicyTestServer(t)
	moderator, admin, owner := adminPolicyModeratorRole, adminPolicyAdminRole, adminPolicyOwnerRole

	// UserPolicy#change_email?, #reset_password? and #confirm? all require
	// manage_user_access, which the default Moderator role does not have.
	for _, tc := range []struct {
		role models.UserRole
		want bool
	}{
		{moderator, false},
		{admin, true},
		{owner, true},
	} {
		if got := s.userCan(adminPolicyStaff(tc.role), rolePermissionManageUserAccess); got != tc.want {
			t.Fatalf("%s manage_user_access = %v, want %v", tc.role.Name, got, tc.want)
		}
	}

	// change_email? and reset_password? also require role.overrides?(record.role).
	for _, tc := range []struct {
		name   string
		actor  models.UserRole
		target *models.UserRole
		want   bool
	}{
		{"moderator over owner", moderator, &owner, false},
		{"moderator over admin", moderator, &admin, false},
		{"admin over owner", admin, &owner, false},
		{"admin over another admin", admin, &admin, false},
		{"admin over moderator", admin, &moderator, true},
		{"admin over a user without a role", admin, nil, true},
		{"owner over admin", owner, &admin, true},
	} {
		got, err := s.adminUserOverridesTarget(adminPolicyStaff(tc.actor), adminPolicyTargetUser(50, tc.target))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: overrides = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAdminUserAccessHandlersUseRailsUserPolicy(t *testing.T) {
	src, err := os.ReadFile("admin_account_subroutes.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"adminAccountChangeEmailPage",
		"updateAdminAccountChangeEmail",
		"resetAdminAccountPasswordWeb",
		"confirmAdminAccountWeb",
		"resendAdminAccountConfirmationWeb",
	} {
		if !functionBodyContains(t, src, name, "s.requireAdminUserAccessWebUser(c)") {
			t.Fatalf("%s must require manage_user_access like Rails UserPolicy", name)
		}
		if functionBodyContains(t, src, name, "s.requireAdminAccountsWebUser(c)") {
			t.Fatalf("%s must not accept manage_users alone", name)
		}
	}
	for _, name := range []string{"adminAccountChangeEmailPage", "updateAdminAccountChangeEmail", "resetAdminAccountPasswordWeb"} {
		body := functionBody(t, src, name)
		check := strings.Index(body, "s.requireAdminUserOverridesTarget(c, user, account.User)")
		if check < 0 {
			t.Fatalf("%s must require role.overrides?(record.role)", name)
		}
		for _, sideEffect := range []string{"s.db.Transaction(", "adminAccountChangeEmailHTML("} {
			if index := strings.Index(body, sideEffect); index >= 0 && index < check {
				t.Fatalf("%s reaches %s before the overrides check", name, sideEffect)
			}
		}
	}
	body := functionBody(t, src, "updateAdminAccountConfirmation")
	check := strings.Index(body, "if confirm && account.User.ConfirmedAt.Valid {")
	if check < 0 || check > strings.Index(body, "s.db.Transaction(") {
		t.Fatal("confirming must reject already confirmed users before writing, like UserPolicy#confirm?")
	}
}

func TestAdminReportAccountActionsUseRailsAccountPolicy(t *testing.T) {
	s := adminPolicyTestServer(t)
	moderator, admin, owner := adminPolicyModeratorRole, adminPolicyAdminRole, adminPolicyOwnerRole
	instanceActor := &models.Account{ID: -99, Username: "mastodon.internal"}
	for _, tc := range []struct {
		name   string
		actor  models.UserRole
		target *models.Account
		action string
		want   bool
	}{
		{"moderator suspends owner", moderator, adminPolicyLocalAccount(1, &owner), "suspend", false},
		{"moderator silences owner", moderator, adminPolicyLocalAccount(1, &owner), "silence", false},
		{"moderator suspends admin", moderator, adminPolicyLocalAccount(2, &admin), "suspend", false},
		{"moderator silences admin", moderator, adminPolicyLocalAccount(2, &admin), "silence", false},
		{"admin suspends owner", admin, adminPolicyLocalAccount(1, &owner), "suspend", false},
		{"owner suspends the instance actor", owner, instanceActor, "suspend", false},
		{"moderator suspends a user without a role", moderator, adminPolicyLocalAccount(3, nil), "suspend", true},
		{"moderator silences a user without a role", moderator, adminPolicyLocalAccount(3, nil), "silence", true},
		{"admin suspends moderator", admin, adminPolicyLocalAccount(4, &moderator), "suspend", true},
		{"owner silences admin", owner, adminPolicyLocalAccount(2, &admin), "silence", true},
	} {
		got, err := s.adminAccountActionPermittedByRailsPolicy(adminPolicyStaff(tc.actor), tc.target, tc.action)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: permitted = %v, want %v", tc.name, got, tc.want)
		}
	}

	src, err := os.ReadFile("admin_reports_web.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "createAdminReportActionWeb")
	check := strings.Index(body, "s.adminAccountActionPermittedByRailsPolicy(user, target, action)")
	if check < 0 || check > strings.Index(body, "s.db.Transaction(") {
		t.Fatal("report silence/suspend must authorize AccountPolicy before changing the account")
	}
}

func TestAdminAccountMemorializeAndImageRemovalUseRailsAccountPolicy(t *testing.T) {
	s := adminPolicyTestServer(t)
	moderator, admin, owner := adminPolicyModeratorRole, adminPolicyAdminRole, adminPolicyOwnerRole
	remote := adminPolicyLocalAccount(5, nil)
	remote.Domain = sql.NullString{String: "remote.example", Valid: true}
	remote.User = models.User{}
	for _, tc := range []struct {
		name   string
		actor  models.UserRole
		target *models.Account
		want   bool
	}{
		{"admin memorializes owner", admin, adminPolicyLocalAccount(1, &owner), false},
		{"admin memorializes another admin", admin, adminPolicyLocalAccount(2, &admin), false},
		{"owner memorializes the instance actor", owner, &models.Account{ID: -99, Username: "mastodon.internal"}, false},
		{"owner memorializes a remote account", owner, remote, false},
		{"owner memorializes a local account without a user", owner, &models.Account{ID: 1006, Username: "orphan"}, false},
		{"admin memorializes moderator", admin, adminPolicyLocalAccount(4, &moderator), true},
		{"owner memorializes admin", owner, adminPolicyLocalAccount(2, &admin), true},
	} {
		got, err := s.adminAccountMemorializePermittedByRailsPolicy(adminPolicyStaff(tc.actor), tc.target)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: permitted = %v, want %v", tc.name, got, tc.want)
		}
	}

	// AccountPolicy#remove_avatar? / #remove_header? require role.overrides?(record.user_role).
	for _, tc := range []struct {
		name   string
		actor  models.UserRole
		target *models.Account
		want   bool
	}{
		{"moderator over admin", moderator, adminPolicyLocalAccount(2, &admin), false},
		{"admin over owner", admin, adminPolicyLocalAccount(1, &owner), false},
		{"moderator over a user without a role", moderator, adminPolicyLocalAccount(3, nil), true},
	} {
		got, err := s.adminAccountRoleOverriddenByRailsPolicy(adminPolicyStaff(tc.actor), tc.target)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: overrides = %v, want %v", tc.name, got, tc.want)
		}
	}

	src, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`case "remove_avatar", "remove_header":`,
		`s.adminAccountRoleOverriddenByRailsPolicy(user, account)`,
		`s.adminAccountMemorializePermittedByRailsPolicy(user, account)`,
	} {
		if !functionBodyContains(t, src, "applyAdminAccountWebAction", want) {
			t.Fatalf("applyAdminAccountWebAction missing %q", want)
		}
	}
}

func TestAdminRoleUpdateUsesRailsUserRolePolicy(t *testing.T) {
	s := adminPolicyTestServer(t)
	everyone, moderator, admin, owner := adminPolicyEveryoneRole, adminPolicyModeratorRole, adminPolicyAdminRole, adminPolicyOwnerRole
	for _, tc := range []struct {
		name   string
		actor  *models.User
		target models.UserRole
		want   bool
	}{
		{"admin edits owner role", adminPolicyStaff(admin), owner, false},
		{"moderator edits admin role", adminPolicyStaff(moderator), admin, false},
		{"admin edits own role", adminPolicyStaff(admin), admin, true},
		{"owner edits own role", adminPolicyStaff(owner), owner, true},
		{"admin edits moderator role", adminPolicyStaff(admin), moderator, true},
		{"admin edits everyone role", adminPolicyStaff(admin), everyone, true},
		{"owner edits admin role", adminPolicyStaff(owner), admin, true},
		// A user without a role acts with UserRole.everyone, so record.id matches.
		{"user without a role edits everyone role", &models.User{ID: 9}, everyone, true},
		{"user without a role edits moderator role", &models.User{ID: 9}, moderator, false},
		{"no actor", nil, everyone, false},
	} {
		got, err := s.adminRoleUpdatePermittedByRailsPolicy(tc.actor, tc.target)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: permitted = %v, want %v", tc.name, got, tc.want)
		}
	}

	src, err := os.ReadFile("admin_roles.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "editAdminRolePage", "s.requireAdminRoleUpdatePermitted(c, user, role)") {
		t.Fatal("editAdminRolePage must authorize UserRolePolicy#update?")
	}
	body := functionBody(t, src, "updateAdminRole")
	check := strings.Index(body, "s.requireAdminRoleUpdatePermitted(c, user, current)")
	if check < 0 || check > strings.Index(body, "s.db.Transaction(") {
		t.Fatal("updateAdminRole must authorize UserRolePolicy#update? before saving")
	}
	// The existing elevation checks stay in place.
	for _, want := range []string{
		"Permissions include privileges you do not have",
		"You cannot change your own role permissions or position",
		"Position must be lower than your current role",
	} {
		if !functionBodyContains(t, src, "adminRoleFromRequest", want) {
			t.Fatalf("adminRoleFromRequest missing %q", want)
		}
	}
}
