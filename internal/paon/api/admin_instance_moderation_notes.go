package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const instanceModerationNoteContentLimit = 2000

var errAdminInstanceModerationNoteParamsMissing = errors.New("admin instance moderation note root parameter is missing")

func (s *Server) createAdminInstanceModerationNote(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	row, err := s.findAdminInstance(c.Param("instance_id"))
	if err != nil {
		return err
	}
	content, err := adminInstanceModerationNoteContent(c)
	if errors.Is(err, errAdminInstanceModerationNoteParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if strings.TrimSpace(content) == "" || len([]rune(content)) > instanceModerationNoteContentLimit {
		notes, _ := s.adminInstanceModerationNotes(row.Instance.Domain)
		return c.HTML(http.StatusUnprocessableEntity, adminInstanceHTMLWithOptions(row, s.cfg.LimitedFederationMode, "", adminT(locale, "admin.instances.moderation_notes.invalid_msg", "Moderation note could not be saved"), adminInstanceHTMLOptions{
			Locale:        locale,
			ShowDashboard: s.userCan(user, rolePermissionViewDashboard),
			DashboardPermissions: &adminDashboardPermissions{
				ManageUsers:   s.userCan(user, rolePermissionManageUsers),
				ManageReports: s.userCan(user, rolePermissionManageReports),
			},
			ModerationNotes:  notes,
			CurrentAccountID: user.AccountID,
		}))
	}
	now := time.Now().UTC()
	note := models.InstanceModerationNote{
		Domain:    row.Instance.Domain,
		AccountID: user.AccountID,
		Content:   sqlNullString(content),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.Create(&note).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(row.Instance.Domain)+"?notice="+url.QueryEscape(adminT(locale, "admin.instances.moderation_notes.created_msg", "Moderation note successfully created"))+"#instance_moderation_note_"+strconv.FormatInt(note.ID, 10))
}

func (s *Server) destroyAdminInstanceModerationNote(c *echo.Context) error {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "delete") {
		return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(normalizeDomain(c.Param("instance_id"))))
	}
	note, err := s.findAdminInstanceModerationNote(c.Param("id"))
	if err != nil {
		return err
	}
	allowed := note.AccountID == user.AccountID
	if !allowed && s.userCan(user, rolePermissionManageFederation) {
		if note.Account.User.ID == 0 {
			allowed = true
		} else {
			allowed, err = s.adminUserOverridesTarget(user, note.Account.User)
			if err != nil {
				return err
			}
		}
	}
	if !allowed {
		locale := s.webLocale(c, user)
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.instances.moderation_notes.title", "Moderation notes"), "", adminT(locale, "admin.instances.moderation_notes.not_permitted", "You are not allowed to delete this note."), "", locale))
	}
	if err := s.db.Delete(&models.InstanceModerationNote{}, note.ID).Error; err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(note.Domain)+"?notice="+url.QueryEscape(adminT(locale, "admin.instances.moderation_notes.destroyed_msg", "Moderation note successfully destroyed"))+"#instance-notes")
}

func adminInstanceModerationNoteContent(c *echo.Context) (string, error) {
	if err := c.Request().ParseForm(); err != nil {
		return "", err
	}
	const root = "instance_moderation_note"
	if !formHasNestedPrefix(c.Request().Form, root) {
		return "", errAdminInstanceModerationNoteParamsMissing
	}
	return lastFormValue(c.Request().Form, root+"[content]"), nil
}

func (s *Server) adminInstanceModerationNotes(domain string) ([]models.InstanceModerationNote, error) {
	if s == nil || s.db == nil {
		return []models.InstanceModerationNote{}, nil
	}
	var notes []models.InstanceModerationNote
	err := s.db.Preload("Account.User.Role").Where("domain = ?", normalizeDomain(domain)).Order("id ASC").Find(&notes).Error
	return notes, err
}

func (s *Server) findAdminInstanceModerationNote(rawID string) (models.InstanceModerationNote, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 || s == nil || s.db == nil {
		return models.InstanceModerationNote{}, echo.NewHTTPError(http.StatusNotFound, "instance moderation note not found")
	}
	var note models.InstanceModerationNote
	if err := s.db.Preload("Account.User.Role").Where("id = ?", id).First(&note).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return note, echo.NewHTTPError(http.StatusNotFound, "instance moderation note not found")
		}
		return note, err
	}
	return note, nil
}
