package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

// createAuthAcceptance preserves the small hand-off endpoint used by the
// registration interstitial. Mastodon only carries these two non-blank values
// into the sign-up form.
func (s *Server) createAuthAcceptance(c *echo.Context) error {
	values := url.Values{}
	for _, key := range []string{"accept", "invite_code"} {
		if value := strings.TrimSpace(requestRawParamValue(c, key)); value != "" {
			values.Set(key, value)
		}
	}
	target := "/auth/sign_up"
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	return c.Redirect(http.StatusFound, target)
}

func (s *Server) redirectRemoteCollection(c *echo.Context) error {
	if s == nil || s.db == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var collection models.Collection
	err := s.db.Preload("Account", func(db *gorm.DB) *gorm.DB {
		return db.Select("id", "domain", "suspended_at")
	}).Where("id = ?", c.Param("id")).First(&collection).Error
	if err != nil || collection.Local || collection.Account.Local() || collection.Account.SuspendedAt.Valid {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return s.renderRemoteRedirectConfirmation(c, activityPubCollectionURL(s, collection))
}
