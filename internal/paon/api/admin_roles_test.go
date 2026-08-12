package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminRolesRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/roles", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/roles")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminRolePermissionsFromForm(t *testing.T) {
	values := url.Values{}
	values.Add("user_role[permissions_as_keys][]", "manage_users")
	values.Add("user_role[permissions_as_keys][]", "manage_roles")
	values.Add("user_role[permissions_as_keys]", "invite_users")
	values.Add("user_role[permissions_as_keys][]", "unknown")
	values.Add("user_role[permissions_as_keys][]", "manage_users")

	got := adminRolePermissionsFromForm(values)
	want := rolePermissionManageUsers | rolePermissionManageRoles | rolePermissionInviteUsers
	if got != want {
		t.Fatalf("permissions = %d, want %d", got, want)
	}
}

func TestAdminRolePositionMatchesPostgreSQLIntegerLimit(t *testing.T) {
	for _, position := range []int{-adminRolePositionLimit, 0, adminRolePositionLimit} {
		if !validAdminRolePosition(position) {
			t.Fatalf("position %d should be valid", position)
		}
	}
	for _, position := range []int{-adminRolePositionLimit - 1, adminRolePositionLimit + 1} {
		if validAdminRolePosition(position) {
			t.Fatalf("position %d should be invalid", position)
		}
	}
}

func TestAdminRolePermissionKeys(t *testing.T) {
	keys := strings.Join(adminRolePermissionKeys(rolePermissionManageUsers|rolePermissionManageRoles|rolePermissionInviteUsers), ",")
	for _, want := range []string{"invite_users", "manage_users", "manage_roles"} {
		if !strings.Contains(keys, want) {
			t.Fatalf("keys = %q, missing %q", keys, want)
		}
	}
}

func TestAdminRolesHTMLIncludesRailsRoutesAndFields(t *testing.T) {
	html := adminRolesHTML([]models.UserRole{
		{ID: -99, Permissions: rolePermissionInviteUsers, Position: -1},
		{ID: 3, Name: "Moderators", Position: 10, Permissions: rolePermissionManageUsers | rolePermissionManageReports},
	}, map[int64]int64{-99: 0, 3: 2}, "saved", "", "1")
	for _, want := range []string{
		"Roles",
		`href="/admin/roles/new"`,
		`href="/admin/roles/3/edit"`,
		`class="applications-list"`,
		`class="announcements-list__item"`,
		`href="/admin/accounts?role_ids=3"`,
		"Moderators",
		"Manage Users",
		"Manage Reports",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("roles html missing %q: %s", want, html)
		}
	}
}

func TestAdminRolesHTMLDoesNotRenderNonRailsPagination(t *testing.T) {
	roles := make([]models.UserRole, adminRailsDefaultPageSize)
	for i := range roles {
		roles[i] = models.UserRole{ID: int64(i + 1), Name: "Role", Position: i}
	}
	html := adminRolesHTML(roles, map[int64]int64{}, "", "", "2")
	for _, forbidden := range []string{`href="/admin/roles?page=1"`, `href="/admin/roles?page=3"`, `class="pagination"`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("roles html contains non-Rails pagination %q: %s", forbidden, html)
		}
	}
}

func TestAdminRoleModelsLoadAllRolesLikeRails(t *testing.T) {
	src, err := os.ReadFile("admin_roles.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{{"adminRolesPage", "s.adminRoleModels(c)"}, {"adminRoleModels", `Order("position DESC, id ASC").Find(&roles)`}} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	for _, forbidden := range []string{"Offset(adminRailsPageOffset(c))", "Limit(adminRailsDefaultPageSize)"} {
		if functionBodyContains(t, src, "adminRoleModels", forbidden) {
			t.Fatalf("adminRoleModels contains non-Rails pagination %q", forbidden)
		}
	}
}

func TestAdminRoleFormHTMLIncludesRailsFields(t *testing.T) {
	html := adminRoleFormHTML(models.UserRole{
		ID:          3,
		Name:        "Mods",
		Color:       "ff0000",
		Position:    7,
		Permissions: rolePermissionManageUsers | rolePermissionManageRoles,
		Highlighted: true,
	}, false, "bad", "en")
	for _, want := range []string{
		"Edit &#39;Mods&#39; role",
		`action="/admin/roles/3"`,
		`name="_method" value="patch"`,
		`name="user_role[name]" value="Mods"`,
		`name="user_role[position]"`,
		`value="7"`,
		`name="user_role[color]" type="color" value="#ff0000"`,
		`name="user_role[highlighted]" value="1" checked`,
		`name="user_role[permissions_as_keys][]" value="manage_users" checked`,
		`name="user_role[permissions_as_keys][]" value="manage_roles" checked`,
		"Save changes",
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("role form missing %q: %s", want, html)
		}
	}
	newHTML := adminRoleFormHTML(models.UserRole{Name: "New"}, true, "", "en")
	if !strings.Contains(newHTML, "Add role") || strings.Contains(newHTML, "Save changes") {
		t.Fatalf("role new submit label mismatch: %s", newHTML)
	}
}

func TestAdminEveryoneRoleFormOnlyShowsInvitePermission(t *testing.T) {
	html := adminRoleFormHTML(models.UserRole{ID: -99, Permissions: rolePermissionInviteUsers}, false, "", "en")
	if !strings.Contains(html, `value="invite_users" checked`) {
		t.Fatalf("everyone form missing invite permission: %s", html)
	}
	for _, unwanted := range []string{`value="manage_users"`, `value="administrator"`, `name="user_role[name]"`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("everyone form contained %q: %s", unwanted, html)
		}
	}
}

func TestAdminRoleDestroyErrorsUseLocaleKeys(t *testing.T) {
	goSrc, err := os.ReadFile("admin_roles.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{
		`adminRoleMessage(s.webLocale(c, user), "errors.everyone_cannot_be_deleted", "Everyone role cannot be deleted")`,
		`adminRoleMessage(s.webLocale(c, user), "errors.own_role_cannot_be_deleted", "You cannot delete your own role")`,
		`adminRoleMessage(s.webLocale(c, user), "errors.higher_role_cannot_be_deleted", "You cannot delete a role higher than or equal to your own")`,
	} {
		if !functionBodyContains(t, goSrc, "destroyAdminRole", check) {
			t.Fatalf("destroyAdminRole must use localized error helper %q", check)
		}
	}
	for _, stale := range []string{
		`QueryEscape("Everyone role cannot be deleted")`,
		`QueryEscape("You cannot delete your own role")`,
		`QueryEscape("You cannot delete a role higher than or equal to your own")`,
	} {
		if functionBodyContains(t, goSrc, "destroyAdminRole", stale) {
			t.Fatalf("destroyAdminRole must not use fixed Go-only English error %q", stale)
		}
	}
	if got := adminRoleMessage("ja", "errors.own_role_cannot_be_deleted", "You cannot delete your own role"); got == "You cannot delete your own role" || !strings.Contains(got, "ロール") {
		t.Fatalf("Japanese admin role error did not resolve locale key: %q", got)
	}
}

func TestAdminRoleCreateUniqueErrorUsesLocaleKey(t *testing.T) {
	goSrc, err := os.ReadFile("admin_roles.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, goSrc, "createAdminRole", `adminRoleMessage(locale, "errors.invalid", "Role could not be saved")`) {
		t.Fatal("createAdminRole unique-constraint error must use admin.roles.errors.invalid")
	}
	if functionBodyContains(t, goSrc, "createAdminRole", `adminRoleFormHTML(role, true, "Role could not be saved"`) {
		t.Fatal("createAdminRole must not pass fixed English role-save error directly to HTML")
	}
	if got := adminRoleMessage("ja", "errors.invalid", "Role could not be saved"); got == "Role could not be saved" || !strings.Contains(got, "ロール") {
		t.Fatalf("Japanese admin role invalid error did not resolve locale key: %q", got)
	}
}
