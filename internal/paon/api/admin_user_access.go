package api

import (
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) adminUserRolePage(c *echo.Context) error {
	user, handled, err := s.requireAdminUserRoleWebUser(c)
	if handled || err != nil {
		return err
	}
	target, err := s.findAdminUser(c.Param("user_id"))
	if err != nil {
		return err
	}
	if ok, err := s.adminUserOverridesTarget(user, target); err != nil {
		return err
	} else if !ok {
		locale := s.webLocale(c, user)
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.accounts.change_role.label", "Change role"), "", adminT(locale, "admin.accounts.change_role.not_permitted", "You cannot change this user's role."), "", locale))
	}
	roles, err := s.assignableAdminRoles()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminUserRoleHTML(target, roles, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) updateAdminUserRole(c *echo.Context) error {
	user, handled, err := s.requireAdminUserRoleWebUser(c)
	if handled || err != nil {
		return err
	}
	target, err := s.findAdminUser(c.Param("user_id"))
	if err != nil {
		return err
	}
	if ok, err := s.adminUserOverridesTarget(user, target); err != nil {
		return err
	} else if !ok {
		locale := s.webLocale(c, user)
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.accounts.change_role.label", "Change role"), "", adminT(locale, "admin.accounts.change_role.not_permitted", "You cannot change this user's role."), "", locale))
	}
	roleID, errText, err := s.adminUserRoleIDFromRequest(c, user)
	if errors.Is(err, errAdminUserRoleParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	if errText != "" {
		roles, err := s.assignableAdminRoles()
		if err != nil {
			return err
		}
		return c.HTML(http.StatusOK, adminUserRoleHTML(target, roles, errText, s.webLocale(c, user)))
	}
	updates := map[string]any{"role_id": nil}
	if roleID.Valid {
		updates["role_id"] = roleID.Int64
	}
	now := time.Now().UTC()
	updates["updated_at"] = now
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "change_role", userAuditLogTarget(target), now)
	}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+strconv.FormatInt(target.AccountID, 10)+"?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.accounts.change_role.changed_msg", "Role successfully changed!")))
}

var errAdminUserRoleParamsMissing = errors.New("admin user role root parameter is missing")

func (s *Server) destroyAdminUserTwoFactor(c *echo.Context) error {
	user, handled, err := s.requireAdminUserAccessWebUser(c)
	if handled || err != nil {
		return err
	}
	target, err := s.findAdminUser(c.Param("user_id"))
	if err != nil {
		return err
	}
	if ok, err := s.adminUserOverridesTarget(user, target); err != nil {
		return err
	} else if !ok {
		locale := s.webLocale(c, user)
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.accounts.disable_two_factor_authentication", "Disable two-factor authentication"), "", adminT(locale, "admin.accounts.two_factor_authentication.not_permitted", "You cannot disable two-factor authentication for this user."), "", locale))
	}
	now := time.Now().UTC()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := disableTwoFactorForUserTx(tx, target.ID, now); err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "disable_2fa", userAuditLogTarget(target), now)
	}); err != nil {
		return err
	}
	if err := s.sendTwoFactorDisabledMail(target); err != nil {
		return mailDeliveryError("two-factor disabled", err)
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+strconv.FormatInt(target.AccountID, 10))
}

func (s *Server) requireAdminUserRoleWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageRoles) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.accounts.change_role.label", "Change role"), "", adminT(locale, "admin.roles.not_permitted", "You are not allowed to manage roles."), "", locale))
	}
	return user, false, nil
}

func (s *Server) requireAdminUserAccessWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageUserAccess) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.accounts.user_access", "User access"), "", adminT(locale, "admin.accounts.user_access_not_permitted", "You are not allowed to manage user access."), "", locale))
	}
	return user, false, nil
}

func (s *Server) findAdminUser(rawID string) (models.User, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil {
		return models.User{}, echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	var user models.User
	if s.db == nil {
		return user, echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	if err := s.db.Preload("Account").Preload("Role").Where("id = ?", id).First(&user).Error; err != nil {
		return user, echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	return user, nil
}

func (s *Server) assignableAdminRoles() ([]models.UserRole, error) {
	if s.db == nil {
		return []models.UserRole{}, nil
	}
	var roles []models.UserRole
	err := s.db.Where("id <> ?", -99).Order("position ASC, id ASC").Find(&roles).Error
	return roles, err
}

func (s *Server) adminUserOverridesTarget(actor *models.User, target models.User) (bool, error) {
	if actor == nil {
		return false, nil
	}
	actorRole := models.UserRole{Position: -1}
	if actor.RoleID.Valid {
		role, err := s.userRoleByID(actor.RoleID.Int64)
		if err != nil {
			return false, err
		}
		actorRole = *role
	}
	targetPosition := -1
	if target.RoleID.Valid {
		targetPosition = target.Role.Position
		if target.Role.ID == 0 {
			role, err := s.userRoleByID(target.RoleID.Int64)
			if err != nil {
				return false, err
			}
			targetPosition = role.Position
		}
	}
	return actorRole.Position > targetPosition, nil
}

func (s *Server) adminUserRoleIDFromRequest(c *echo.Context, actor *models.User) (sql.NullInt64, string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return sql.NullInt64{}, "", err
	}
	if !formHasNestedPrefix(req.Form, "user") {
		return sql.NullInt64{}, "", errAdminUserRoleParamsMissing
	}
	raw := strings.TrimSpace(lastFormValue(req.Form, "user[role_id]"))
	if raw == "" {
		return sql.NullInt64{}, "", nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return sql.NullInt64{}, "Role is invalid", nil
	}
	role, err := s.userRoleByID(id)
	if err != nil || role.ID == -99 {
		return sql.NullInt64{}, "Role is invalid", nil
	}
	actorRole := models.UserRole{Position: -1}
	if actor != nil && actor.RoleID.Valid {
		currentRole, err := s.userRoleByID(actor.RoleID.Int64)
		if err != nil {
			return sql.NullInt64{}, "", err
		}
		actorRole = *currentRole
	}
	if role.Position > actorRole.Position {
		return sql.NullInt64{}, "Role is too high for your account", nil
	}
	return sql.NullInt64{Int64: id, Valid: true}, "", nil
}

func adminUserRoleHTML(user models.User, roles []models.UserRole, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	username := "user"
	if user.Account != nil && user.Account.ID != 0 {
		username = user.Account.Acct()
	}
	body := `<form method="post" action="/admin/users/` + strconv.FormatInt(user.ID, 10) + `/role" class="simple_form edit_user">
  <input type="hidden" name="_method" value="patch">
	<div class="fields-group"><div class="input select optional user_role"><div class="label_input"><label for="user_role_id">` + html.EscapeString(adminT(loc, "admin.accounts.change_role.label", "Role")) + `</label>` + adminUserRoleSelect(user.RoleID, roles, loc) + `</div></div></div>
	<div class="actions"><button class="button" type="submit">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
</form>`
	title := adminTVars(loc, "admin.accounts.change_role.title", "Change role: %{username}", map[string]string{"username": username})
	return authPageHTML(title, "", errorText, body, loc)
}

func adminUserRoleSelect(current sql.NullInt64, roles []models.UserRole, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var out strings.Builder
	out.WriteString(`<select class="select optional" name="user[role_id]" id="user_role_id"><option value="">` + html.EscapeString(adminT(loc, "admin.accounts.change_role.no_role", "No role")) + `</option>`)
	for _, role := range roles {
		selected := ""
		if current.Valid && current.Int64 == role.ID {
			selected = " selected"
		}
		out.WriteString(`<option value="` + strconv.FormatInt(role.ID, 10) + `"` + selected + `>` + html.EscapeString(role.Name) + `</option>`)
	}
	out.WriteString(`</select>`)
	return out.String()
}
