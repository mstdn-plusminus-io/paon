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

func (s *Server) createAdminAccountModerationNoteWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	targetID, content, err := adminAccountModerationNoteParams(c)
	if errors.Is(err, errAdminAccountModerationNoteParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil || targetID <= 0 {
		return c.Redirect(http.StatusFound, "/admin/accounts?error="+url.QueryEscape(adminAccountModerationNoteMessage(locale, "errors.invalid", "Account moderation note is invalid")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/accounts/"+strconv.FormatInt(targetID, 10)+"?error="+url.QueryEscape(adminAccountModerationNoteMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	account, err := s.loadAdminAccount(strconv.FormatInt(targetID, 10))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	if strings.TrimSpace(content) == "" || len([]rune(content)) > 500 {
		notes, _ := s.adminAccountModerationNotes(targetID)
		return c.HTML(http.StatusOK, adminAccountHTMLWithIPHistory(*account, "", adminT(locale, "admin.account_moderation_notes.invalid_msg", "Moderation note could not be saved"), nil, locale, s.cfg, notes...))
	}
	now := time.Now().UTC()
	note := models.AccountModerationNote{AccountID: user.AccountID, TargetAccountID: targetID, Content: content, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&note).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "create", accountModerationNoteAuditLogTarget(note), now)
	}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+strconv.FormatInt(targetID, 10)+"?notice="+url.QueryEscape(adminT(locale, "admin.account_moderation_notes.created_msg", "Moderation note successfully created!")))
}

func (s *Server) destroyAdminAccountModerationNoteWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "delete") {
		return c.Redirect(http.StatusFound, "/admin/accounts")
	}
	note, err := s.findAdminAccountModerationNote(c.Param("id"))
	if err != nil {
		return err
	}
	if note.AccountID != user.AccountID {
		ok, err := s.adminAccountModerationNoteDestroyAllowed(user, note)
		if err != nil {
			return err
		}
		if !ok {
			locale := s.webLocale(c, user)
			return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.reports.notes.title", "Account moderation notes"), "", adminT(locale, "admin.reports.notes.not_permitted", "You are not allowed to delete this note."), "", locale))
		}
	}
	now := time.Now().UTC()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&models.AccountModerationNote{}, note.ID).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "destroy", accountModerationNoteAuditLogTarget(note), now)
	}); err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/admin/accounts/"+strconv.FormatInt(note.TargetAccountID, 10)+"?notice="+url.QueryEscape(adminT(locale, "admin.account_moderation_notes.destroyed_msg", "Moderation note successfully destroyed!")))
}

func (s *Server) adminAccountModerationNotes(accountID int64) ([]models.AccountModerationNote, error) {
	if s.db == nil {
		return []models.AccountModerationNote{}, nil
	}
	var notes []models.AccountModerationNote
	err := s.db.Preload("Account").Where("target_account_id = ?", accountID).Order("created_at DESC").Find(&notes).Error
	return notes, err
}

func (s *Server) findAdminAccountModerationNote(rawID string) (models.AccountModerationNote, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil {
		return models.AccountModerationNote{}, echo.NewHTTPError(http.StatusNotFound, "account moderation note not found")
	}
	var note models.AccountModerationNote
	if s.db == nil {
		return note, echo.NewHTTPError(http.StatusNotFound, "account moderation note not found")
	}
	if err := s.db.Preload("Account.User.Role").Where("id = ?", id).First(&note).Error; err != nil {
		return note, echo.NewHTTPError(http.StatusNotFound, "account moderation note not found")
	}
	return note, nil
}

func (s *Server) adminAccountModerationNoteDestroyAllowed(user *models.User, note models.AccountModerationNote) (bool, error) {
	if user == nil {
		return false, nil
	}
	if !s.userCan(user, rolePermissionManageReports) {
		return false, nil
	}
	if note.Account.User.ID == 0 {
		return true, nil
	}
	return s.adminUserOverridesTarget(user, note.Account.User)
}

func adminAccountModerationNoteMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.account_moderation_notes."+key, fallback)
}

var errAdminAccountModerationNoteParamsMissing = errors.New("admin account moderation note root parameter is missing")

func adminAccountModerationNoteParams(c *echo.Context) (int64, string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return 0, "", err
	}
	const prefix = "account_moderation_note"
	if !formHasNestedPrefix(req.Form, prefix) {
		return 0, "", errAdminAccountModerationNoteParamsMissing
	}
	targetID, err := strconv.ParseInt(strings.TrimSpace(lastFormValue(req.Form, prefix+"[target_account_id]")), 10, 64)
	return targetID, lastFormValue(req.Form, prefix+"[content]"), err
}
