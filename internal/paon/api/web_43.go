package api

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) redirectRemoteAccount(c *echo.Context) error {
	if s == nil || s.db == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var account models.Account
	if err := s.db.Select("id", "domain", "url", "uri").Where("id = ?", c.Param("id")).First(&account).Error; err != nil || account.Local() {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return s.renderRemoteRedirectConfirmation(c, permalinkRemoteAccountURL(account))
}

func (s *Server) redirectRemoteStatus(c *echo.Context) error {
	if s == nil || s.db == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var status models.Status
	if err := s.db.Select("id", "account_id", "url", "uri", "visibility", "deleted_at", "reblog_of_id").Preload("Account", func(db *gorm.DB) *gorm.DB {
		return db.Select("id", "domain")
	}).Where("id = ?", c.Param("id")).First(&status).Error; err != nil || status.DeletedAt.Valid || status.Account.Local() || !embeddableStatus(&status) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return s.renderRemoteRedirectConfirmation(c, permalinkRemoteStatusURL(status))
}

func (s *Server) renderRemoteRedirectConfirmation(c *echo.Context, target string) error {
	if target == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	locale := s.webLocale(c, nil)
	title := settingsTVars(locale, "redirects.title", "Leaving %{instance}", map[string]string{
		"instance": firstNonEmpty(s.cfg.WebDomain, s.cfg.LocalDomain),
	})
	prompt := settingsT(locale, "redirects.prompt", "The content you requested is hosted on another server.")
	c.Response().Header().Set("Vary", "Accept-Language")
	c.Response().Header().Set("Cache-Control", "max-age=15, public")
	body := `<!doctype html><html lang="` + htmlDocumentLang(locale) + `"><head><meta charset="utf-8">` +
		`<meta name="robots" content="noindex, noarchive"><meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<link rel="canonical" href="` + html.EscapeString(target) + `"><title>` + html.EscapeString(title) + `</title></head>` +
		`<body class="app-body"><main class="redirect"><div class="redirect__logo"><a href="/">Paon</a></div>` +
		`<div class="redirect__message"><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(prompt) + `</p>` +
		`<p><a href="` + html.EscapeString(target) + `" rel="noreferrer noopener">` + html.EscapeString(target) + `</a></p></div></main></body></html>`
	return c.HTML(http.StatusOK, body)
}

func (s *Server) permalinkRedirectConfirmationPath(path string) string {
	segments := permalinkPathSegments(path)
	first, second := "", ""
	if len(segments) > 0 {
		first = firstPathSegmentWithoutQuery(segments[0])
	}
	if len(segments) > 1 {
		second = firstPathSegmentWithoutQuery(segments[1])
	}
	if (strings.HasPrefix(first, "@") || first == "statuses") && permalinkRecordIDCandidate(second) {
		if id, ok := s.remotePermalinkStatusID(second); ok {
			return "/redirect/statuses/" + strconv.FormatInt(id, 10)
		}
	}
	if strings.HasPrefix(first, "@") {
		if id, ok := s.remotePermalinkAccountIDByName(first); ok {
			return "/redirect/accounts/" + strconv.FormatInt(id, 10)
		}
	}
	if first == "accounts" && permalinkRecordIDCandidate(second) {
		if id, ok := s.remotePermalinkAccountID(second); ok {
			return "/redirect/accounts/" + strconv.FormatInt(id, 10)
		}
	}
	if strings.HasPrefix(path, "/deck") {
		return strings.TrimPrefix(path, "/deck")
	}
	return ""
}

func firstPathSegmentWithoutQuery(value string) string {
	return strings.SplitN(value, "?", 2)[0]
}

func (s *Server) remotePermalinkStatusID(rawID string) (int64, bool) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 || s == nil || s.db == nil {
		return 0, false
	}
	var status models.Status
	if err := s.db.Select("id", "account_id", "url", "uri", "visibility", "deleted_at", "reblog_of_id").Preload("Account", func(db *gorm.DB) *gorm.DB {
		return db.Select("id", "domain")
	}).Where("id = ?", id).First(&status).Error; err != nil || status.DeletedAt.Valid || status.Account.Local() || !embeddableStatus(&status) || permalinkRemoteStatusURL(status) == "" {
		return 0, false
	}
	return status.ID, true
}

func (s *Server) remotePermalinkAccountID(rawID string) (int64, bool) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 || s == nil || s.db == nil {
		return 0, false
	}
	var account models.Account
	if err := s.db.Select("id", "domain", "url", "uri").Where("id = ?", id).First(&account).Error; err != nil || account.Local() || permalinkRemoteAccountURL(account) == "" {
		return 0, false
	}
	return account.ID, true
}

func (s *Server) remotePermalinkAccountIDByName(name string) (int64, bool) {
	if s == nil || s.db == nil {
		return 0, false
	}
	username, domain, _ := strings.Cut(strings.TrimPrefix(name, "@"), "@")
	query := s.db.Select("id", "domain", "url", "uri").Where("lower(username) = ?", strings.ToLower(username))
	if domain == "" || webfingerLocalHostRaw(domain, s.cfg.LocalDomain, s.cfg.WebDomain, s.cfg.AlternateDomains) {
		query = query.Where("domain IS NULL")
	} else {
		query = query.Where("lower(domain) = ?", strings.ToLower(domain))
	}
	var account models.Account
	if err := query.First(&account).Error; err != nil || account.Local() || permalinkRemoteAccountURL(account) == "" {
		return 0, false
	}
	return account.ID, true
}

func safeExternalHTTPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" && strings.ContainsAny(parsed.Fragment, "\r\n") {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	return parsed.String()
}
