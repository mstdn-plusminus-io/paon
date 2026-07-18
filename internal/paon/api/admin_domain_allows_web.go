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
)

type adminDomainAllowForm struct {
	Domain string
}

var errAdminDomainAllowParamsMissing = errors.New("admin domain allow root parameter is missing")

func (s *Server) newAdminDomainAllowPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminDomainAllowFormHTML(adminDomainAllowForm{Domain: c.QueryParam("_domain")}, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminDomainAllowWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminDomainAllowForm(c)
	if err != nil {
		if errors.Is(err, errAdminDomainAllowParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminDomainAllowFormHTML(form, adminDomainAllowMessage(locale, "errors.invalid", "Domain allow is invalid"), locale))
	}
	domain := normalizeDomain(form.Domain)
	if domain == "" {
		return c.HTML(http.StatusOK, adminDomainAllowFormHTML(form, adminDomainAllowMessage(locale, "errors.domain_invalid", "Domain is invalid"), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminDomainAllowFormHTML(form, adminDomainAllowMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	now := time.Now().UTC()
	row := models.DomainAllow{Domain: domain, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return c.HTML(http.StatusOK, adminDomainAllowFormHTML(form, adminDomainAllowMessage(locale, "errors.taken", "Domain has already been taken"), locale))
		}
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "create", domainAllowAuditLogTarget(row), row.CreatedAt); err != nil {
		return err
	}
	if err := s.materializeDomainControlMutation(c.Request().Context(), row.Domain); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances?notice="+url.QueryEscape(adminDomainAllowMessage(locale, "created_msg", "Domain allow created")))
}

func (s *Server) destroyAdminDomainAllowWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		return echo.NewHTTPError(http.StatusNotFound, "domain allow not found")
	}
	locale := s.webLocale(c, user)
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/instances?error="+url.QueryEscape(adminDomainAllowMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	var row models.DomainAllow
	if err := s.db.First(&row, id).Error; err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "domain allow not found")
	}
	if err := s.applyAdminDomainUnallowEffects(s.db, row.Domain); err != nil {
		return err
	}
	if err := s.db.Delete(&models.DomainAllow{}, row.ID).Error; err != nil {
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "destroy", domainAllowAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	if err := s.refreshDomainControlMutation(c.Request().Context(), row.Domain); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances?notice="+url.QueryEscape(adminDomainAllowMessage(locale, "destroyed_msg", "Domain allow removed")))
}

func adminDomainAllowMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.domain_allows."+key, fallback)
}

func (s *Server) adminDomainAllowMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminDomainAllowWeb(c)
	}
	return c.Redirect(http.StatusFound, "/admin/instances")
}

func parseAdminDomainAllowForm(c *echo.Context) (adminDomainAllowForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminDomainAllowForm{}, err
	}
	prefix := "domain_allow"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminDomainAllowForm{}, errAdminDomainAllowParamsMissing
	}
	return adminDomainAllowForm{Domain: strings.TrimSpace(lastFormValue(req.Form, "domain_allow[domain]"))}, nil
}

func adminDomainAllowFormHTML(form adminDomainAllowForm, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	body := simpleFormOpen("/admin/domain_allows", "post") +
		simpleTextInput(adminT(loc, "simple_form.labels.domain_allow.domain", "Domain"), "domain_allow[domain]", form.Domain, "text", `required`) +
		simpleSubmit(adminT(loc, "admin.domain_allows.add_new", "Add domain allow")) +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.domain_allows.add_new", "Add domain allow"), "", errorText, body, loc)
}
