package api

import (
	"database/sql"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRoleRequiredTwoFactorRedirectsUntilConfigured(t *testing.T) {
	server := &Server{}
	user := &models.User{
		ID:     7,
		RoleID: sql.NullInt64{Int64: 3, Valid: true},
		Role:   models.UserRole{ID: 3, Require2FA: true},
	}
	if !server.userMissingRequiredTwoFactor(user) {
		t.Fatal("role-required user without TOTP or WebAuthn was treated as functional")
	}
	if got := server.afterSignInRedirectPath(user, "/oauth/authorize"); got != requiredTwoFactorSetupPath {
		t.Fatalf("required 2FA redirect = %q", got)
	}
	user.OTPRequiredForLogin = true
	if server.userMissingRequiredTwoFactor(user) {
		t.Fatal("TOTP-enabled user still failed the role requirement")
	}
}

func TestEveryoneRoleCanRequireTwoFactor(t *testing.T) {
	server := &Server{}
	user := &models.User{ID: 9, Role: models.UserRole{ID: -99, Require2FA: true}}
	if !server.userMissingRequiredTwoFactor(user) {
		t.Fatal("Everyone role requirement was ignored")
	}
}

func TestRoleTwoFactorSetupRoutesDoNotRedirectToThemselves(t *testing.T) {
	for _, path := range []string{
		"/settings/two_factor_authentication_methods",
		"/settings/otp_authentication/new",
		"/settings/security_keys/7",
	} {
		if !roleTwoFactorSetupRequest(path) {
			t.Fatalf("setup path %q was not allowed", path)
		}
	}
	if roleTwoFactorSetupRequest("/settings/preferences") {
		t.Fatal("unrelated settings path bypassed role-required 2FA")
	}
}
