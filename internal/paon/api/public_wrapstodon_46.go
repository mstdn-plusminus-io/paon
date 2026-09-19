package api

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) publicWrapstodon(c *echo.Context) error {
	appendVaryHeader(c, "Accept")
	appendVaryHeader(c, "Accept-Language")
	appendVaryHeader(c, "Cookie")
	account, err := s.findAccountByUsernameDomainTx(s.db, c.Param("username"), "")
	if err != nil || account == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	year, err := strconv.Atoi(c.Param("year"))
	if err != nil || year <= 0 || strings.TrimSpace(c.Param("share_key")) == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var report models.GeneratedAnnualReport
	err = s.db.Preload("Account").Where("account_id = ? AND year = ? AND share_key = ?", account.ID, year, c.Param("share_key")).First(&report).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	accountIDs, statusIDs := annualReportReferencedIDs([]models.GeneratedAnnualReport{report})
	accounts, err := s.annualReportAccounts(accountIDs)
	if err != nil {
		return err
	}
	statuses, err := s.annualReportStatuses(statusIDs, nil)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"annual_reports": []annualReportEntity{annualReportEntityFromModel(s, report, json.RawMessage(report.Data))},
		"accounts":       accounts,
		"statuses":       statuses,
		"domain":         firstNonEmpty(s.cfg.LocalDomain, s.cfg.WebDomain),
	})
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "public, max-age=600")
	c.Response().Header().Set("X-Robots-Tag", "noindex, noarchive")
	locale := s.webLocale(c, nil)
	name := accountDisplayName(*account)
	title := settingsTVars(locale, "wrapstodon.title", "%{name}'s %{year} recap", map[string]string{"name": name, "year": strconv.Itoa(year)})
	body := `<div id="wrapstodon"><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(settingsTVars(locale, "wrapstodon.description", "A look back at %{name}'s year on the fediverse.", map[string]string{"name": name})) + `</p></div>`
	body = strings.TrimSuffix(body, "</div>") + publicWrapstodonSummaryHTML(report.Data, locale) + `</div>`
	body += `<script type="application/json" id="wrapstodon-data">` + string(payload) + `</script>`
	return c.HTML(http.StatusOK, authShellHTML(title, "", "", body, locale, "default"))
}

func publicWrapstodonSummaryHTML(raw models.JSONValue, locale string) string {
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return ""
	}
	var out strings.Builder
	if archetype, ok := data["archetype"].(string); ok && archetype != "" {
		out.WriteString(`<p><strong>` + html.EscapeString(settingsT(locale, "annual_report.summary.archetype.title", "Your archetype")) + `:</strong> ` + html.EscapeString(archetype) + `</p>`)
	}
	if rows := anySlice(data["time_series"]); len(rows) > 0 {
		if row, ok := rows[0].(map[string]any); ok {
			out.WriteString(`<ul><li>` + html.EscapeString(settingsT(locale, "annual_report.summary.statuses", "Posts")) + `: ` + strconv.FormatInt(anyPositiveInt64(row["statuses"]), 10) + `</li>`)
			out.WriteString(`<li>` + html.EscapeString(settingsT(locale, "annual_report.summary.followers", "New followers")) + `: ` + strconv.FormatInt(anyPositiveInt64(row["followers"]), 10) + `</li></ul>`)
		}
	}
	if rows := anySlice(data["top_hashtags"]); len(rows) > 0 {
		if row, ok := rows[0].(map[string]any); ok {
			if name, ok := row["name"].(string); ok && name != "" {
				out.WriteString(`<p><strong>` + html.EscapeString(settingsT(locale, "annual_report.summary.hashtag", "Most-used hashtag")) + `:</strong> #` + html.EscapeString(name) + `</p>`)
			}
		}
	}
	return out.String()
}
