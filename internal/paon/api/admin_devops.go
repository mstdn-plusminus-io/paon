package api

import (
	"database/sql"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type pgHeroRelationStatus struct {
	Schema     sql.NullString
	Relation   sql.NullString
	Size       sql.NullInt64
	CapturedAt sql.NullTime
}

func (s *Server) requireDevopsWebUser(c *echo.Context) (*models.User, error) {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		_ = redirectToSignIn(c)
		return nil, errWebAuthResponseHandled
	}
	return user, nil
}

func (s *Server) pgHeroPage(c *echo.Context) error {
	user, err := s.requireDevopsWebUser(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionViewDevops) {
		return c.HTML(http.StatusForbidden, authPageHTML("PgHero", "", adminT(locale, "admin.dashboard.not_permitted", "You are not allowed to view the dashboard."), "", locale, theme))
	}
	stats, err := s.pgHeroLatestRelationStats()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, authPageHTML("PgHero", "", "", pgHeroPageHTML(stats, locale), locale, theme))
}

func (s *Server) pgHeroLatestRelationStats() ([]pgHeroRelationStatus, error) {
	db := s.pgHeroStatsDatabase()
	if db == nil {
		return nil, nil
	}
	var stats []pgHeroRelationStatus
	query := db.Model(&models.PgHeroSpaceStat{}).
		Select("schema, relation, size, captured_at").
		Where("captured_at = (?)", db.Model(&models.PgHeroSpaceStat{}).Select("MAX(captured_at)")).
		Order("size DESC").
		Limit(50)
	if err := query.Find(&stats).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

func pgHeroPageHTML(stats []pgHeroRelationStatus, locale string) string {
	var body strings.Builder
	body.WriteString(`<p><a href="/admin/dashboard">` + html.EscapeString(adminT(locale, "admin.dashboard.title", "Admin dashboard")) + `</a></p>`)
	body.WriteString(`<h2>PgHero</h2>`)
	body.WriteString(`<p class="lead">` + html.EscapeString(adminT(locale, "admin.dashboard.space", "Space usage")) + `</p>`)
	body.WriteString(`<div class="table-wrapper"><table class="table"><thead><tr><th>` + html.EscapeString(adminT(locale, "admin.devops.relation", "Relation")) + `</th><th>` + html.EscapeString(adminT(locale, "admin.devops.size", "Size")) + `</th><th>` + html.EscapeString(adminT(locale, "admin.devops.captured_at", "Captured at")) + `</th></tr></thead><tbody>`)
	if len(stats) == 0 {
		body.WriteString(`<tr><td colspan="3">` + html.EscapeString(settingsT(locale, "generic.none", "None")) + `</td></tr>`)
	}
	for _, stat := range stats {
		name := strings.Trim(stat.Schema.String+"."+stat.Relation.String, ".")
		capturedAt := ""
		if stat.CapturedAt.Valid {
			capturedAt = stat.CapturedAt.Time.UTC().Format(time.RFC3339)
		}
		body.WriteString(`<tr><td><code>` + html.EscapeString(name) + `</code></td><td>` + html.EscapeString(formatBytes(stat.Size.Int64)) + `</td><td>` + html.EscapeString(capturedAt) + `</td></tr>`)
	}
	body.WriteString(`</tbody></table></div>`)
	return body.String()
}

func formatBytes(size int64) string {
	if size < 1024 {
		return strconv.FormatInt(size, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(size)
	for _, unit := range units {
		value = value / 1024
		if value < 1024 {
			return strconv.FormatFloat(value, 'f', 1, 64) + " " + unit
		}
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " PB"
}
