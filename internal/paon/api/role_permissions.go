package api

import (
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	rolePermissionAdministrator       = int64(1 << 0)
	rolePermissionViewDevops          = int64(1 << 1)
	rolePermissionViewAuditLog        = int64(1 << 2)
	rolePermissionViewDashboard       = int64(1 << 3)
	rolePermissionManageReports       = int64(1 << 4)
	rolePermissionManageFederation    = int64(1 << 5)
	rolePermissionManageSettings      = int64(1 << 6)
	rolePermissionManageBlocks        = int64(1 << 7)
	rolePermissionManageTaxonomies    = int64(1 << 8)
	rolePermissionManageAppeals       = int64(1 << 9)
	rolePermissionManageUsers         = int64(1 << 10)
	rolePermissionManageInvites       = int64(1 << 11)
	rolePermissionManageRules         = int64(1 << 12)
	rolePermissionManageAnnouncements = int64(1 << 13)
	rolePermissionManageCustomEmojis  = int64(1 << 14)
	rolePermissionManageWebhooks      = int64(1 << 15)
	rolePermissionInviteUsers         = int64(1 << 16)
	rolePermissionManageRoles         = int64(1 << 17)
	rolePermissionManageUserAccess    = int64(1 << 18)
	rolePermissionDeleteUserData      = int64(1 << 19)
	rolePermissionViewFeeds           = int64(1 << 20)
	rolePermissionsAll                = int64((1 << 21) - 1)
)

func (s *Server) userCan(user *models.User, permission int64) bool {
	permissions, err := s.computedUserPermissions(user)
	return err == nil && permissions&permission == permission
}

func (s *Server) userCanAny(user *models.User, permissions ...int64) bool {
	computed, err := s.computedUserPermissions(user)
	if err != nil {
		return false
	}
	for _, permission := range permissions {
		if computed&permission == permission {
			return true
		}
	}
	return false
}

func (s *Server) computedUserPermissions(user *models.User) (int64, error) {
	if user == nil {
		return 0, nil
	}
	everyone, err := s.userRoleByID(-99)
	if err != nil {
		return 0, err
	}
	if user.RoleID.Valid {
		role, err := s.userRoleByID(user.RoleID.Int64)
		if err != nil {
			return 0, err
		}
		return computedRolePermissionsForUser(role, everyone), nil
	}
	return computedRolePermissionsForUser(nil, everyone), nil
}

func computedRolePermissionsForUser(role *models.UserRole, everyone *models.UserRole) int64 {
	if role != nil && role.ID == -99 {
		return role.Permissions
	}
	permissions := int64(0)
	if everyone != nil {
		permissions |= everyone.Permissions
	} else {
		permissions |= rolePermissionInviteUsers
	}
	if role != nil {
		permissions |= role.Permissions
	}
	if permissions&rolePermissionAdministrator == rolePermissionAdministrator {
		return rolePermissionsAll
	}
	return permissions
}

func adminRolePermissionsForScopes(scopes []string, write bool) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0)
	add := func(permission int64) {
		if _, ok := seen[permission]; !ok {
			seen[permission] = struct{}{}
			out = append(out, permission)
		}
	}
	for _, scope := range scopes {
		switch scope {
		case "admin:read:accounts", "admin:write:accounts":
			add(rolePermissionManageUsers)
		case "admin:read:reports", "admin:write:reports":
			add(rolePermissionManageReports)
		case "admin:read:domain_allows", "admin:write:domain_allows", "admin:read:domain_blocks", "admin:write:domain_blocks":
			add(rolePermissionManageFederation)
		case "admin:read:email_domain_blocks", "admin:write:email_domain_blocks", "admin:read:canonical_email_blocks", "admin:write:canonical_email_blocks", "admin:read:ip_blocks", "admin:write:ip_blocks":
			add(rolePermissionManageBlocks)
		}
	}
	if len(out) == 0 {
		add(rolePermissionViewDashboard)
		add(rolePermissionManageReports)
		add(rolePermissionManageUsers)
		add(rolePermissionManageTaxonomies)
		add(rolePermissionManageAppeals)
		add(rolePermissionManageFederation)
		add(rolePermissionManageBlocks)
		add(rolePermissionManageRules)
		add(rolePermissionManageAnnouncements)
		add(rolePermissionManageCustomEmojis)
		add(rolePermissionManageWebhooks)
		add(rolePermissionManageRoles)
		add(rolePermissionManageUserAccess)
		add(rolePermissionDeleteUserData)
		if write {
			add(rolePermissionManageInvites)
		}
	}
	return out
}
