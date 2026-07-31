package api

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestComputedRolePermissionsForUserUsesEveryoneDefaults(t *testing.T) {
	everyone := &models.UserRole{ID: -99, Permissions: rolePermissionInviteUsers}

	if permissions := computedRolePermissionsForUser(nil, everyone); permissions != rolePermissionInviteUsers {
		t.Fatalf("permissions = %d", permissions)
	}
}

func TestComputedRolePermissionsForUserCombinesEveryoneAndRole(t *testing.T) {
	everyone := &models.UserRole{ID: -99, Permissions: rolePermissionInviteUsers}
	role := &models.UserRole{ID: 4, Permissions: rolePermissionManageInvites}

	permissions := computedRolePermissionsForUser(role, everyone)
	if permissions&rolePermissionInviteUsers == 0 || permissions&rolePermissionManageInvites == 0 {
		t.Fatalf("permissions = %d", permissions)
	}
}

func TestComputedRolePermissionsForUserExpandsAdministrator(t *testing.T) {
	permissions := computedRolePermissionsForUser(&models.UserRole{ID: 1, Permissions: rolePermissionAdministrator}, nil)
	if permissions != rolePermissionsAll {
		t.Fatalf("permissions = %d", permissions)
	}
}

func TestComputedRolePermissionsForUserDoesNotExpandEveryoneRole(t *testing.T) {
	everyone := &models.UserRole{ID: -99, Permissions: rolePermissionAdministrator}
	permissions := computedRolePermissionsForUser(everyone, everyone)
	if permissions != rolePermissionAdministrator {
		t.Fatalf("permissions = %d", permissions)
	}
}

func TestAdminRolePermissionsForScopesMapsResourceScopes(t *testing.T) {
	cases := []struct {
		scope string
		want  int64
	}{
		{"admin:read:accounts", rolePermissionManageUsers},
		{"admin:write:reports", rolePermissionManageReports},
		{"admin:read:domain_blocks", rolePermissionManageFederation},
		{"admin:write:domain_allows", rolePermissionManageFederation},
		{"admin:read:email_domain_blocks", rolePermissionManageBlocks},
		{"admin:write:ip_blocks", rolePermissionManageBlocks},
	}

	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			got := adminRolePermissionsForScopes([]string{tc.scope}, false)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("permissions = %#v, want %d", got, tc.want)
			}
		})
	}
}

func TestAdminRolePermissionsForScopesUsesFallbackForGenericAdminScope(t *testing.T) {
	got := adminRolePermissionsForScopes([]string{"admin:read"}, false)
	if len(got) == 0 {
		t.Fatal("expected generic admin scope to require at least one admin role permission")
	}
	if got[0] != rolePermissionViewDashboard {
		t.Fatalf("first fallback permission = %d", got[0])
	}
}

func railsUserRolePermissionFlags(t *testing.T, src string) map[string]int64 {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s+([a-z_]+):\s+\(1 << ([0-9]+)\),$`)
	matches := re.FindAllStringSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatal("Rails UserRole::FLAGS parser found no permissions")
	}
	flags := make(map[string]int64, len(matches))
	for _, match := range matches {
		shift, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatal(err)
		}
		flags[match[1]] = int64(1 << shift)
	}
	return flags
}

func goUserRolePermissionFlags(t *testing.T) map[string]int64 {
	t.Helper()
	flags := make(map[string]int64, len(adminRolePermissions))
	for _, permission := range adminRolePermissions {
		if permission.Bit == 0 {
			t.Fatalf("permission %q has zero bit", permission.Key)
		}
		if prior, ok := flags[permission.Key]; ok {
			t.Fatalf("duplicate Go role permission key %q: %d and %d", permission.Key, prior, permission.Bit)
		}
		flags[permission.Key] = permission.Bit
	}
	return flags
}
