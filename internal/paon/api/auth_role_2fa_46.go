package api

import (
	"errors"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const requiredTwoFactorSetupPath = "/settings/two_factor_authentication_methods"

// userMissingRequiredTwoFactor mirrors Mastodon 4.6's User#missing_2fa?. A
// custom role takes precedence; users without one inherit the Everyone role.
// Either TOTP or a registered WebAuthn credential satisfies the requirement.
func (s *Server) userMissingRequiredTwoFactor(user *models.User) bool {
	if user == nil {
		return false
	}
	role, err := s.requiredTwoFactorRole(user)
	if err != nil || role == nil || !role.Require2FA {
		return err != nil
	}
	if user.OTPRequiredForLogin {
		return false
	}
	if s == nil || s.db == nil {
		return true
	}
	hasWebAuthn, err := s.userHasWebauthnCredentials(user.ID)
	return err != nil || !hasWebAuthn
}

func (s *Server) requiredTwoFactorRole(user *models.User) (*models.UserRole, error) {
	if user == nil {
		return nil, nil
	}
	roleID := int64(-99)
	if user.RoleID.Valid {
		roleID = user.RoleID.Int64
	}
	if user.Role.ID == roleID {
		return &user.Role, nil
	}
	if s == nil || s.db == nil {
		return nil, nil
	}
	var role models.UserRole
	if err := s.db.Where("id = ?", roleID).First(&role).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &role, nil
}
