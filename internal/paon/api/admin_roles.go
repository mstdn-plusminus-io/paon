package api

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var adminRoleColorPattern = regexp.MustCompile(`^#?(?:[A-Fa-f0-9]{3}){1,2}$`)

const adminRolePositionLimit = (1 << 31) - 1

var errAdminRoleParamsMissing = errors.New("admin role root parameter is missing")

type adminRolePermission struct {
	Key  string
	Bit  int64
	Area string
}

var adminRolePermissions = []adminRolePermission{
	{Key: "invite_users", Bit: rolePermissionInviteUsers, Area: "invites"},
	{Key: "view_dashboard", Bit: rolePermissionViewDashboard, Area: "moderation"},
	{Key: "view_audit_log", Bit: rolePermissionViewAuditLog, Area: "moderation"},
	{Key: "manage_users", Bit: rolePermissionManageUsers, Area: "moderation"},
	{Key: "manage_user_access", Bit: rolePermissionManageUserAccess, Area: "moderation"},
	{Key: "delete_user_data", Bit: rolePermissionDeleteUserData, Area: "moderation"},
	{Key: "view_feeds", Bit: rolePermissionViewFeeds, Area: "moderation"},
	{Key: "manage_reports", Bit: rolePermissionManageReports, Area: "moderation"},
	{Key: "manage_appeals", Bit: rolePermissionManageAppeals, Area: "moderation"},
	{Key: "manage_federation", Bit: rolePermissionManageFederation, Area: "moderation"},
	{Key: "manage_blocks", Bit: rolePermissionManageBlocks, Area: "moderation"},
	{Key: "manage_taxonomies", Bit: rolePermissionManageTaxonomies, Area: "moderation"},
	{Key: "manage_invites", Bit: rolePermissionManageInvites, Area: "moderation"},
	{Key: "manage_settings", Bit: rolePermissionManageSettings, Area: "administration"},
	{Key: "manage_rules", Bit: rolePermissionManageRules, Area: "administration"},
	{Key: "manage_roles", Bit: rolePermissionManageRoles, Area: "administration"},
	{Key: "manage_webhooks", Bit: rolePermissionManageWebhooks, Area: "administration"},
	{Key: "manage_custom_emojis", Bit: rolePermissionManageCustomEmojis, Area: "administration"},
	{Key: "manage_announcements", Bit: rolePermissionManageAnnouncements, Area: "administration"},
	{Key: "view_devops", Bit: rolePermissionViewDevops, Area: "devops"},
	{Key: "administrator", Bit: rolePermissionAdministrator, Area: "special"},
}

func (s *Server) adminRolesPage(c *echo.Context) error {
	user, handled, err := s.requireAdminRolesWebUser(c)
	if handled || err != nil {
		return err
	}
	roles, counts, err := s.adminRoleModels(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRolesHTML(roles, counts, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) newAdminRolePage(c *echo.Context) error {
	user, handled, err := s.requireAdminRolesWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRoleFormHTML(models.UserRole{}, true, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) editAdminRolePage(c *echo.Context) error {
	user, handled, err := s.requireAdminRolesWebUser(c)
	if handled || err != nil {
		return err
	}
	role, err := s.findAdminRole(c.Param("id"))
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRoleFormHTML(role, false, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminRole(c *echo.Context) error {
	user, handled, err := s.requireAdminRolesWebUser(c)
	if handled || err != nil {
		return err
	}
	role, errText, err := s.adminRoleFromRequest(c, models.UserRole{}, true, user)
	if err != nil {
		if errors.Is(err, errAdminRoleParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return err
	}
	if errText != "" {
		return c.HTML(http.StatusOK, adminRoleFormHTML(role, true, errText, s.webLocale(c, user)))
	}
	now := time.Now().UTC()
	role.CreatedAt = now
	role.UpdatedAt = now
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&role).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "create", userRoleAuditLogTarget(role), now)
	}); err != nil {
		if isUniqueConstraintError(err) {
			locale := s.webLocale(c, user)
			return c.HTML(http.StatusOK, adminRoleFormHTML(role, true, adminRoleMessage(locale, "errors.invalid", "Role could not be saved"), locale))
		}
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/roles")
}

func (s *Server) updateAdminRole(c *echo.Context) error {
	user, handled, err := s.requireAdminRolesWebUser(c)
	if handled || err != nil {
		return err
	}
	current, err := s.findAdminRole(c.Param("id"))
	if err != nil {
		return err
	}
	role, errText, err := s.adminRoleFromRequest(c, current, false, user)
	if err != nil {
		if errors.Is(err, errAdminRoleParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return err
	}
	if errText != "" {
		return c.HTML(http.StatusOK, adminRoleFormHTML(role, false, errText, s.webLocale(c, user)))
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"name":        role.Name,
		"color":       role.Color,
		"position":    role.Position,
		"permissions": role.Permissions,
		"highlighted": role.Highlighted,
		"updated_at":  now,
	}
	if role.ID == -99 {
		updates = map[string]any{
			"permissions": role.Permissions,
			"position":    -1,
			"updated_at":  now,
		}
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.UserRole{}).Where("id = ?", role.ID).Updates(updates).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "update", userRoleAuditLogTarget(role), now)
	}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/roles")
}

func (s *Server) destroyAdminRole(c *echo.Context) error {
	user, handled, err := s.requireAdminRolesWebUser(c)
	if handled || err != nil {
		return err
	}
	role, err := s.findAdminRole(c.Param("id"))
	if err != nil {
		return err
	}
	if role.ID == -99 {
		return c.Redirect(http.StatusFound, "/admin/roles?error="+url.QueryEscape(adminRoleMessage(s.webLocale(c, user), "errors.everyone_cannot_be_deleted", "Everyone role cannot be deleted")))
	}
	if user.RoleID.Valid && user.RoleID.Int64 == role.ID {
		return c.Redirect(http.StatusFound, "/admin/roles?error="+url.QueryEscape(adminRoleMessage(s.webLocale(c, user), "errors.own_role_cannot_be_deleted", "You cannot delete your own role")))
	}
	if ok, err := s.adminRoleOverridesTarget(user, role); err != nil {
		return err
	} else if !ok {
		return c.Redirect(http.StatusFound, "/admin/roles?error="+url.QueryEscape(adminRoleMessage(s.webLocale(c, user), "errors.higher_role_cannot_be_deleted", "You cannot delete a role higher than or equal to your own")))
	}
	now := time.Now().UTC()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("role_id = ?", role.ID).Update("role_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.UserRole{}, role.ID).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "destroy", userRoleAuditLogTarget(role), now)
	}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/roles")
}

func (s *Server) adminRoleMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminRole(c)
	}
	return s.updateAdminRole(c)
}

func (s *Server) requireAdminRolesWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageRoles) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.roles.title", "Roles"), "", adminT(locale, "admin.roles.not_permitted", "You are not allowed to manage roles."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminRoleModels(_ *echo.Context) ([]models.UserRole, map[int64]int64, error) {
	if s.db == nil {
		return []models.UserRole{}, map[int64]int64{}, nil
	}
	var roles []models.UserRole
	if err := s.db.Order("position DESC, id ASC").Find(&roles).Error; err != nil {
		return nil, nil, err
	}
	counts := map[int64]int64{}
	for _, role := range roles {
		var count int64
		if err := s.db.Model(&models.User{}).Where("role_id = ?", role.ID).Count(&count).Error; err != nil {
			return nil, nil, err
		}
		counts[role.ID] = count
	}
	return roles, counts, nil
}

func (s *Server) findAdminRole(rawID string) (models.UserRole, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil {
		return models.UserRole{}, echo.NewHTTPError(http.StatusNotFound, "role not found")
	}
	var role models.UserRole
	if s.db == nil {
		return role, echo.NewHTTPError(http.StatusNotFound, "role not found")
	}
	if err := s.db.Where("id = ?", id).First(&role).Error; err != nil {
		return role, echo.NewHTTPError(http.StatusNotFound, "role not found")
	}
	return role, nil
}

func (s *Server) adminRoleFromRequest(c *echo.Context, current models.UserRole, create bool, user *models.User) (models.UserRole, string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return current, "", err
	}
	prefix := "user_role"
	if !formHasNestedPrefix(req.Form, prefix) {
		return current, "", errAdminRoleParamsMissing
	}
	role := current
	role.Name = lastFormValue(req.Form, "user_role[name]")
	role.Color = lastFormValue(req.Form, "user_role[color]")
	role.Highlighted = adminSettingsCheckbox(req.Form, "user_role[highlighted]")
	if create {
		role.Position = 0
	}
	if rawPosition := strings.TrimSpace(lastFormValue(req.Form, "user_role[position]")); rawPosition != "" {
		position, err := strconv.Atoi(rawPosition)
		if err != nil || !validAdminRolePosition(position) {
			return role, "Position is invalid", nil
		}
		role.Position = position
	}
	role.Permissions = adminRolePermissionsFromForm(req.Form)
	if role.ID == -99 {
		role.Name = ""
		role.Color = ""
		role.Position = -1
		role.Highlighted = false
		if role.Permissions&^rolePermissionInviteUsers != 0 {
			return role, "Everyone role can only grant invite users", nil
		}
		return role, "", nil
	}
	if strings.TrimSpace(role.Name) == "" {
		return role, "Name is required", nil
	}
	if strings.TrimSpace(role.Color) != "" && !adminRoleColorPattern.MatchString(role.Color) {
		return role, "Color is invalid", nil
	}
	currentPermissions, err := s.computedUserPermissions(user)
	if err != nil {
		return role, "", err
	}
	if currentPermissions&rolePermissionAdministrator == 0 && role.Permissions&^currentPermissions != 0 {
		return role, "Permissions include privileges you do not have", nil
	}
	currentUserRole := models.UserRole{Position: -1}
	if user != nil && user.RoleID.Valid {
		loadedRole, err := s.userRoleByID(user.RoleID.Int64)
		if err != nil {
			return role, "", err
		}
		currentUserRole = *loadedRole
		if !create && user.RoleID.Int64 == role.ID && (current.Permissions != role.Permissions || current.Position != role.Position) {
			return role, "You cannot change your own role permissions or position", nil
		}
	}
	if role.Position > currentUserRole.Position {
		return role, "Position must be lower than your current role", nil
	}
	return role, "", nil
}

func validAdminRolePosition(position int) bool {
	return position >= -adminRolePositionLimit && position <= adminRolePositionLimit
}

func (s *Server) adminRoleOverridesTarget(actor *models.User, target models.UserRole) (bool, error) {
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
	return actorRole.Position > target.Position, nil
}

func adminRolePermissionsFromForm(values map[string][]string) int64 {
	items := append([]string{}, values["user_role[permissions_as_keys]"]...)
	items = append(items, values["user_role[permissions_as_keys][]"]...)
	seen := map[string]struct{}{}
	var permissions int64
	for _, item := range items {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		for _, permission := range adminRolePermissions {
			if permission.Key == key {
				permissions |= permission.Bit
				break
			}
		}
	}
	return permissions
}

func adminRolePermissionKeys(mask int64) []string {
	keys := make([]string, 0)
	for _, permission := range adminRolePermissions {
		if mask&permission.Bit == permission.Bit {
			keys = append(keys, permission.Key)
		}
	}
	return keys
}

func adminRolesHTML(roles []models.UserRole, counts map[int64]int64, notice string, errorText string, page string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><a class="button" href="/admin/roles/new">` + html.EscapeString(adminT(loc, "admin.roles.add_new", "Add role")) + `</a></div><p>` + adminT(loc, "admin.roles.description_html", "Manage role permissions stored in the existing user_roles table.") + `</p><hr class="spacer"><div class="applications-list">`)
	for _, role := range roles {
		if role.ID == -99 {
			body.WriteString(adminRoleRowHTML(role, counts[role.ID], loc))
		}
	}
	body.WriteString(`</div><hr class="spacer"><div class="applications-list">`)
	for _, role := range roles {
		if role.ID != -99 {
			body.WriteString(adminRoleRowHTML(role, counts[role.ID], loc))
		}
	}
	body.WriteString(`</div>`)
	return authPageHTML(adminT(loc, "admin.roles.title", "Roles"), notice, errorText, body.String(), loc)
}

func adminRolesPaginationHTML(page string, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := url.Values{"page": []string{strconv.Itoa(pageNum - 1)}}
		links = append(links, `<a href="/admin/roles?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.prev", "Previous"))+`</a>`)
	}
	if hasNext {
		params := url.Values{"page": []string{strconv.Itoa(pageNum + 1)}}
		links = append(links, `<a href="/admin/roles?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminRoleRowHTML(role models.UserRole, userCount int64, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	label := role.Name
	if role.ID == -99 {
		label = adminT(loc, "admin.roles.everyone", "Everyone")
	}
	keys := adminRolePermissionKeys(role.Permissions)
	id := strconv.FormatInt(role.ID, 10)
	meta := adminT(loc, "admin.roles.everyone_full_description_html", "Default permissions for all users")
	if role.ID != -99 {
		permissionLabels := make([]string, 0, len(keys))
		for _, key := range keys {
			permissionLabels = append(permissionLabels, adminRolePermissionLabel(loc, key))
		}
		assigned := adminTVars(loc, "admin.roles.assigned_users.other", "%{count} users", map[string]string{"count": strconv.FormatInt(userCount, 10)})
		permissionCount := adminTVars(loc, "admin.roles.permissions_count.other", "%{count} permissions", map[string]string{"count": strconv.Itoa(len(keys))})
		meta = `<a href="/admin/accounts?role_ids=` + id + `">` + html.EscapeString(assigned) + `</a> &middot; <abbr title="` + html.EscapeString(strings.Join(permissionLabels, ", ")) + `">` + html.EscapeString(permissionCount) + `</abbr>`
	}
	return `<div class="announcements-list__item"><a class="announcements-list__item__title" href="/admin/roles/` + id + `/edit"><span class="user-role user-role-` + id + `"><i class="fa fa-users fa-fw"></i> ` + html.EscapeString(label) + `</span></a><div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta">` + meta + `</div><div><a class="table-action-link" href="/admin/roles/` + id + `/edit"><i class="fa fa-pencil fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.accounts.edit", "Edit")) + `</a></div></div></div>`
}

func adminRoleFormHTML(role models.UserRole, create bool, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	title := adminT(loc, "admin.roles.add_new", "Add role")
	action := "/admin/roles"
	if !create {
		title = adminTVars(loc, "admin.roles.edit", "Edit '%{name}' role", map[string]string{"name": role.Name})
		action = "/admin/roles/" + strconv.FormatInt(role.ID, 10)
	}
	var body strings.Builder
	methodOverride := "post"
	if !create {
		methodOverride = "patch"
	}
	body.WriteString(simpleFormOpen(action, methodOverride))
	if role.ID != -99 {
		body.WriteString(simpleTextInput(adminT(loc, "simple_form.labels.user_role.name", "Name"), "user_role[name]", role.Name, "text", `required`))
		body.WriteString(simpleTextInput(adminT(loc, "simple_form.labels.user_role.position", "Position"), "user_role[position]", strconv.Itoa(role.Position), "number", ""))
		body.WriteString(`<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.user_role.color", "Color")) + `</label><input name="user_role[color]" type="color" value="` + adminRoleHTMLColor(role.Color) + `"></div></div></div>`)
		body.WriteString(`<div class="fields-group"><div class="input boolean with_label"><label class="boolean"><input type="hidden" name="user_role[highlighted]" value="0"><input type="checkbox" name="user_role[highlighted]" value="1"` + adminRoleCheckedAttr(role.Highlighted) + `> ` + html.EscapeString(adminT(loc, "simple_form.labels.user_role.highlighted", "Highlighted")) + `</label></div></div>`)
	} else {
		body.WriteString(`<p class="lead">` + html.EscapeString(adminT(loc, "admin.roles.everyone_permissions_hint", "Everyone permissions apply to all users and are limited to invite access.")) + `</p>`)
	}
	body.WriteString(adminRolePermissionCheckboxes(role, loc))
	submitLabel := adminT(loc, "generic.save_changes", "Save changes")
	if create {
		submitLabel = adminT(loc, "admin.roles.add_new", "Add role")
	}
	body.WriteString(simpleSubmit(submitLabel))
	body.WriteString(simpleFormClose())
	return authPageHTML(title, "", errorText, body.String(), loc)
}

func adminRolePermissionCheckboxes(role models.UserRole, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	allowedArea := ""
	if role.ID == -99 {
		allowedArea = "invites"
	}
	var out strings.Builder
	currentArea := ""
	for _, permission := range adminRolePermissions {
		if allowedArea != "" && permission.Area != allowedArea {
			continue
		}
		if permission.Area != currentArea {
			if currentArea != "" {
				out.WriteString(`</fieldset>`)
			}
			currentArea = permission.Area
			out.WriteString(`<fieldset><legend>` + html.EscapeString(adminRolePermissionAreaLabel(loc, permission.Area)) + `</legend>`)
		}
		out.WriteString(`<label><input type="checkbox" name="user_role[permissions_as_keys][]" value="` + permission.Key + `"` + adminRoleCheckedAttr(role.Permissions&permission.Bit == permission.Bit) + `> ` + html.EscapeString(adminRolePermissionLabel(loc, permission.Key)) + `</label>`)
		description := adminRolePermissionDescription(loc, permission.Key)
		if description != "" {
			out.WriteString(`<p class="hint">` + html.EscapeString(description) + `</p>`)
		}
	}
	if currentArea != "" {
		out.WriteString(`</fieldset>`)
	}
	return out.String()
}

func adminRolePermissionAreaLabel(locale string, area string) string {
	return adminT(locale, "admin.roles.categories."+area, strings.Title(area))
}

func adminRolePermissionLabel(locale string, key string) string {
	return adminT(locale, "admin.roles.privileges."+key, key)
}

func adminRolePermissionDescription(locale string, key string) string {
	value := adminT(locale, "admin.roles.privileges."+key+"_description", "")
	if value == "admin.roles.privileges."+key+"_description" {
		return ""
	}
	return value
}

func adminRoleCheckedAttr(value bool) string {
	if value {
		return " checked"
	}
	return ""
}

func adminRoleHTMLColor(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "#000000"
	}
	if strings.HasPrefix(value, "#") {
		return html.EscapeString(value)
	}
	return "#" + html.EscapeString(value)
}

func adminRoleMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.roles."+key, fallback)
}
