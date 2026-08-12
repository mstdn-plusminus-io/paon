package api

import (
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

func (s *Server) instanceTermsOfService(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	terms, err := s.currentTermsOfService(time.Now().UTC())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	return s.renderTermsOfService(c, *terms)
}

func (s *Server) instanceTermsOfServiceVersion(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	effectiveDate, err := time.Parse("2006-01-02", strings.TrimSpace(c.Param("date")))
	if err != nil || s.db == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var terms models.TermsOfService
	if err := s.db.Where("published_at IS NOT NULL AND effective_date = ?", effectiveDate).First(&terms).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	return s.renderTermsOfService(c, terms)
}

func (s *Server) renderTermsOfService(c *echo.Context, terms models.TermsOfService) error {
	s.publicRESTCacheEvenIfAuthenticated(c, 15)
	successor, err := s.termsOfServiceSuccessor(terms)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.TermsOfServiceFromModel(s.cfg, terms, successor, time.Now().UTC()))
}

func (s *Server) currentTermsOfService(now time.Time) (*models.TermsOfService, error) {
	if s == nil || s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var terms models.TermsOfService
	err := s.db.
		Where("published_at IS NOT NULL AND (effective_date IS NULL OR effective_date < ?)", now).
		Order("COALESCE(effective_date::timestamp, published_at) DESC").
		First(&terms).Error
	if err == nil {
		return &terms, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	err = s.db.
		Where("published_at IS NOT NULL AND effective_date IS NOT NULL AND effective_date > ?", now).
		Order("effective_date ASC").
		First(&terms).Error
	return &terms, err
}

func (s *Server) termsOfServiceSuccessor(terms models.TermsOfService) (*models.TermsOfService, error) {
	if s == nil || s.db == nil || !terms.EffectiveDate.Valid {
		return nil, nil
	}
	var successor models.TermsOfService
	err := s.db.
		Where("published_at IS NOT NULL AND effective_date >= ? AND id <> ?", terms.EffectiveDate.Time, terms.ID).
		Order("COALESCE(effective_date::timestamp, published_at) DESC").
		First(&successor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &successor, nil
}

func (s *Server) termsOfServicePage(c *echo.Context) error {
	if user, _, err := s.currentUserIncludingDisabled(c); err == nil && user.RequireTOSInterstitial && s.db != nil {
		if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Update("require_tos_interstitial", false).Error; err != nil {
			return err
		}
	}
	return s.webApp(c)
}

func termsOfServiceInterstitialRequired(path string, user *models.User) bool {
	if user == nil || !user.RequireTOSInterstitial {
		return false
	}
	return path != "/terms-of-service" && !strings.HasPrefix(path, "/terms-of-service/")
}

func (s *Server) renderTermsOfServiceInterstitialIfRequired(c *echo.Context, user *models.User) (bool, error) {
	if !termsOfServiceInterstitialRequired(c.Request().URL.Path, user) || s.db == nil {
		return false, nil
	}
	var terms models.TermsOfService
	if err := s.db.Where("published_at IS NOT NULL").Order("COALESCE(effective_date::timestamp, published_at) DESC").First(&terms).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Update("require_tos_interstitial", false).Error; err != nil {
				return false, err
			}
			return false, nil
		}
		return false, err
	}
	locale := s.webLocale(c, user)
	title := webT(locale, "terms_of_service_interstitial.title", map[string]string{"domain": s.cfg.LocalDomain})
	preambleKey := "terms_of_service_interstitial.past_preamble_html"
	vars := map[string]string{}
	if terms.EffectiveDate.Valid && terms.EffectiveDate.Time.After(time.Now().UTC()) {
		preambleKey = "terms_of_service_interstitial.future_preamble_html"
		vars["date"] = terms.EffectiveDate.Time.Format("2006-01-02")
	}
	body := `<h1 class="title">` + html.EscapeString(title) + `</h1>` +
		`<p class="lead">` + webT(locale, preambleKey, vars) + `</p>` +
		`<p class="lead">` + html.EscapeString(webT(locale, "terms_of_service_interstitial.agreement", map[string]string{"domain": s.cfg.LocalDomain})) + `</p>` +
		`<div class="stacked-actions"><a class="button" href="/terms-of-service">` + html.EscapeString(webT(locale, "terms_of_service_interstitial.review_link")) + `</a></div>`
	return true, c.HTML(http.StatusOK, authShellHTML(title, "", "", body, locale))
}
