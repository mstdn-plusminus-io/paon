package api

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type oEmbedResponse struct {
	Type         string `json:"type"`
	Version      string `json:"version"`
	AuthorName   string `json:"author_name"`
	AuthorURL    string `json:"author_url"`
	ProviderName string `json:"provider_name"`
	ProviderURL  string `json:"provider_url"`
	CacheAge     int    `json:"cache_age"`
	HTML         string `json:"html"`
	Width        int    `json:"width"`
	Height       *int   `json:"height"`
}

type remoteOEmbedFormat string

const (
	remoteOEmbedJSON remoteOEmbedFormat = "json"
	remoteOEmbedXML  remoteOEmbedFormat = "xml"
)

const (
	remoteOEmbedEndpointCacheTTL            = 24 * time.Hour
	remoteOEmbedEndpointCacheCommandTimeout = 500 * time.Millisecond
	statusEmbedMaxDomainLength              = 253
)

type remoteOEmbedEndpointCache struct {
	Endpoint string             `json:"endpoint"`
	Format   remoteOEmbedFormat `json:"format"`
}

type statusEmbedLinkContext struct {
	mentions map[string]statusEmbedMentionLink
	tags     map[string]string
}

type statusEmbedMentionLink struct {
	href    string
	display string
}

var (
	oEmbedLinkTagPattern     = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	oEmbedAttrPattern        = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>` + "`" + `=]+))`)
	oEmbedHTMLTagPattern     = regexp.MustCompile(`(?is)<\s*(/?)\s*([a-zA-Z0-9]+)\b([^>]*)>`)
	oEmbedScriptBlockPattern = regexp.MustCompile(`(?is)<\s*(script|style|noscript|template)\b[^>]*>.*?<\s*/\s*(script|style|noscript|template)\s*>`)
	oEmbedEndpointURLPattern = regexp.MustCompile(`(?i)(=(https?(%3A|:)(//|%2F%2F)))([^&]*)`)
	customEmojiPlainPattern  = regexp.MustCompile(`:([A-Za-z0-9_]{2,}):`)
	statusEmbedLinkPattern   = regexp.MustCompile(`(?:(?:https?|dat|dweb|ipfs|ipns|ssb|gopher|gemini)://[^\s<]+|xmpp:[^\s<]+|magnet:\?[^\s<]+)|#[\pL\pN_][\pL\pN_·・]*|@[A-Za-z0-9_]+(?:@[A-Za-z0-9.-]+\.[A-Za-z]{2,})?`)
)

func (s *Server) oEmbed(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	id := statusIDFromLocalURL(s.cfg.BaseURL(), c.QueryParam("url"))
	if id == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	status, err := s.findStatus(id)
	if err != nil || !embeddableStatus(status) || !status.Account.Local() {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	width := oEmbedMaxWidth(c.QueryParam("maxwidth"))
	var height *int
	if strings.TrimSpace(c.QueryParam("maxheight")) != "" {
		value := int(railsToInt64(c.QueryParam("maxheight")))
		height = &value
	}
	return c.JSON(http.StatusOK, s.oEmbedResponse(*status, width, height))
}

func oEmbedMaxWidth(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 400
	}
	return int(railsToInt64(raw))
}

func (s *Server) webEmbed(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	status, err := s.findStatus(c.Param("id"))
	if err != nil || !embeddableStatus(status) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if status.Account.Local() {
		width := 400
		return c.JSON(http.StatusOK, s.oEmbedResponse(*status, width, nil))
	}

	if _, _, err := s.currentAccount(c); err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	statusURL := remoteStatusURL(*status)
	if statusURL == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	embed, err := s.fetchRemoteOEmbed(c.Request().Context(), statusURL)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if htmlValue, ok := embed["html"].(string); ok {
		embed["html"] = sanitizeRemoteOEmbedHTML(htmlValue)
	}
	return c.JSON(http.StatusOK, embed)
}

func (s *Server) statusEmbed(c *echo.Context) error {
	status, err := s.findStatus(c.Param("id"))
	if err != nil || !embeddableStatus(status) || !status.Account.Local() || !strings.EqualFold(status.Account.Username, c.Param("username")) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	s.activityPubAccountVary(c)
	c.Response().Header().Set("Cache-Control", "max-age=180, public")
	c.Response().Header().Del("X-Frame-Options")
	c.Response().Header().Set("Content-Security-Policy", railsContentSecurityPolicyWithoutDirective(s.cfg, "frame-ancestors"))
	s.setPublicStatusLinkHeaderForStatus(c, *status)
	emojis, err := s.statusEmbedCustomEmojis(*status)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, statusEmbedHTMLWithConfig(s.settingStringValue("site_title", s.cfg.Title), s.cfg, *status, emojis, s.cfg.Locale()))
}

func (s *Server) oEmbedResponse(status models.Status, width int, height *int) oEmbedResponse {
	authorName := status.Account.DisplayName
	if strings.TrimSpace(authorName) == "" {
		authorName = status.Account.Username
	}
	statusPath := "/@" + url.PathEscape(status.Account.Username) + "/" + strconv.FormatInt(status.ID, 10)
	embedPath := statusPath + "/embed"
	return oEmbedResponse{
		Type:         "rich",
		Version:      "1.0",
		AuthorName:   authorName,
		AuthorURL:    s.cfg.BaseURL() + statusPath,
		ProviderName: s.cfg.LocalDomain,
		ProviderURL:  s.cfg.BaseURL() + "/",
		CacheAge:     86400,
		HTML:         embedHTML(s.cfg.BaseURL()+embedPath, s.cfg.BaseURL()+"/embed.js", width, height),
		Width:        width,
		Height:       height,
	}
}

func embeddableStatus(status *models.Status) bool {
	return status != nil && !status.ReblogOfID.Valid && status.Visibility <= 1
}

func statusEmbedHTML(title string, baseURL string, status models.Status) string {
	return statusEmbedHTMLWithCustomEmojis(title, baseURL, status, nil)
}

func statusEmbedHTMLWithCustomEmojis(title string, baseURL string, status models.Status, emojis []models.CustomEmoji, locales ...string) string {
	return statusEmbedHTMLWithConfig(title, config.Config{WebDomain: baseURL}, status, emojis, locales...)
}

func statusEmbedHTMLWithConfig(title string, cfg config.Config, status models.Status, emojis []models.CustomEmoji, locales ...string) string {
	baseURL := cfg.BaseURL()
	locale := ""
	if len(locales) > 0 {
		locale = locales[0]
	}
	accountName := statusEmbedAccountNameHTMLWithConfig(cfg, status.Account, emojis)
	avatarURL := statusEmbedAccountAvatarURLWithConfig(cfg, status.Account)
	statusURL := baseURL + "/@" + url.PathEscape(status.Account.Username) + "/" + strconv.FormatInt(status.ID, 10)
	content := statusEmbedContentHTMLWithConfig(cfg, status, emojis, locale)
	meta := statusEmbedMetaHTML(statusURL, status, locale)
	poll := statusEmbedPollHTMLWithConfig(cfg, status, emojis)
	if strings.TrimSpace(status.SpoilerText) != "" {
		poll = ""
	}
	return `<!DOCTYPE html>
<html lang="` + htmlDocumentLang(locale) + `">
<head>
  <meta charset="utf-8">
  <meta name="robots" content="noindex">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + html.EscapeString(title) + `</title>
  <style>
    body{margin:0;background:transparent;color:#282c37;font:14px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
    .status{box-sizing:border-box;border:1px solid #d9e1e8;border-radius:4px;padding:14px;background:#fff}
    .account{display:flex;align-items:center;gap:10px;font-weight:700;color:#191b22;text-decoration:none;min-width:0}
    .avatar{width:40px;height:40px;border-radius:4px;object-fit:cover;background:#d9e1e8;flex:0 0 auto}
    .account-name{display:block;min-width:0;overflow-wrap:anywhere}
    .acct{color:#657786;font-weight:400;margin-left:4px}
    .content{margin:10px 0 12px;line-height:1.45;overflow-wrap:anywhere}
    .content-warning__summary{display:flex;align-items:center;gap:8px;cursor:pointer}
    .content-warning__summary::-webkit-details-marker{display:none}
    .content-warning__summary::marker{content:""}
    .content-warning__text{font-weight:700}
    .content-warning__trigger{color:#6364ff;font-weight:700}
    .content-warning__body{margin-top:10px}
    .emojione{width:16px;height:16px;vertical-align:-3px}
    .media{display:grid;gap:8px;margin:0 0 12px}
    .sensitive-media{position:relative;border-radius:4px;overflow:hidden;background:#191b22}
    .media-spoiler{display:flex;min-height:160px;align-items:center;justify-content:center;text-align:center;cursor:pointer;color:#fff;background:rgba(0,0,0,.55)}
    .media-spoiler::-webkit-details-marker{display:none}
    .media-spoiler::marker{content:""}
    .media-spoiler__warning,.media-spoiler__trigger{display:block}
    .media-spoiler__trigger{font-size:12px;font-weight:700;margin-top:4px}
    .sensitive-media[open] .media-spoiler{min-height:0;padding:8px;background:#191b22;color:#d9e1e8}
    .media img,.media video{display:block;max-width:100%;max-height:380px;border-radius:4px;background:#000;object-fit:contain}
    .media audio{display:block;width:100%}
    .media a{color:#6364ff;overflow-wrap:anywhere}
    .status-card{display:flex;margin:0 0 12px;border:1px solid #d9e1e8;border-radius:4px;overflow:hidden;color:inherit;text-decoration:none;background:#fff}
    .status-card.expanded{display:block}
    .status-card__image{width:120px;min-height:90px;display:flex;align-items:center;justify-content:center;background:#d9e1e8;color:#657786;flex:0 0 auto}
    .status-card.expanded .status-card__image{width:100%;min-height:0}
    .status-card__image-image{display:block;width:100%;height:100%;object-fit:cover;background:#d9e1e8}
    .status-card.expanded .status-card__image-image{height:auto;max-height:280px;object-fit:contain}
    .status-card__content{min-width:0;padding:8px 10px;display:grid;gap:4px}
    .status-card__host{color:#657786;font-size:12px;text-transform:uppercase;overflow-wrap:anywhere}
    .status-card__title{font-weight:700;color:#191b22;overflow-wrap:anywhere}
    .status-card__description,.status-card__author{color:#657786;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
    .poll{margin:0 0 12px;border:1px solid #d9e1e8;border-radius:4px;padding:10px}
    .poll ul{list-style:none;margin:0;padding:0;display:grid;gap:8px}
    .poll li{display:grid;gap:4px}
    .poll-option{display:flex;gap:8px;align-items:center}
    .poll-input{width:12px;height:12px;border:1px solid #8c8dff;border-radius:50%;flex:0 0 auto}
    .poll-input.checkbox{border-radius:3px}
    .poll-percent{font-weight:700;min-width:38px}
    .poll progress{width:100%;height:6px}
    .poll-footer{margin-top:10px;color:#657786;font-size:12px}
    .status__content__read-more-button{display:block;margin:0 0 12px;color:#6364ff;font-weight:700;text-decoration:none}
    .meta,.muted{color:#657786}
    .meta{display:flex;flex-wrap:wrap;gap:6px;align-items:center;font-size:13px}
    .meta a{color:#657786;text-decoration:none}
  </style>
</head>
<body class="embed">
  <article class="entry status detailed-status detailed-status--flex detailed-status-` + statusEmbedVisibilityClass(status.Visibility) + `">
    <span class="p-author h-card"><a class="account u-url" target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(baseURL+"/@"+url.PathEscape(status.Account.Username)) + `"><img class="avatar u-photo" src="` + html.EscapeString(avatarURL) + `" alt=""><span class="account-name"><strong class="p-name">` + accountName + `</strong><span class="acct">@` + html.EscapeString(status.Account.Username) + statusEmbedLockedAccountMarker(status.Account) + `</span></span></a></span>
    ` + content + `
    ` + poll + `
    ` + statusEmbedMediaHTMLWithConfig(cfg, status, locale) + `
    ` + statusEmbedPreviewCardHTMLWithConfig(cfg, status, locale) + `
    ` + statusEmbedThreadLinkHTML(statusURL, status, locale) + `
    ` + meta + `
  </article>
  <script>
    (function () {
      var frameId = null;
      var sendHeight = function () {
        if (frameId === null || !window.parent) {
          return;
        }
        window.parent.postMessage({
          type: 'setHeight',
          id: frameId,
          height: document.documentElement.scrollHeight
        }, '*');
      };
      window.addEventListener('message', function (e) {
        var data = e.data || {};
        if (!window.parent || data.type !== 'setHeight') {
          return;
        }
        frameId = data.id;
        sendHeight();
      });
      window.addEventListener('load', sendHeight);
      window.addEventListener('resize', sendHeight);
      document.addEventListener('toggle', sendHeight, true);
    })();
  </script>
</body>
</html>`
}

func statusEmbedThreadLinkHTML(statusURL string, status models.Status, locale string) string {
	if !status.InReplyToID.Valid || !status.InReplyToAccountID.Valid || status.InReplyToAccountID.Int64 != status.AccountID {
		return ""
	}
	return `<a class="status__content__read-more-button" target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(statusURL) + `">` + html.EscapeString(settingsT(locale, "statuses.show_thread", "Show thread")) + `</a>`
}

func statusEmbedMetaHTML(statusURL string, status models.Status, locales ...string) string {
	locale := ""
	if len(locales) > 0 {
		locale = locales[0]
	}
	var out strings.Builder
	createdISO := status.CreatedAt.UTC().Format(time.RFC3339)
	out.WriteString(`<div class="meta">`)
	out.WriteString(`<data class="dt-published" value="` + html.EscapeString(createdISO) + `"></data>`)
	if status.EditedAt.Valid {
		out.WriteString(`<data class="dt-updated" value="` + html.EscapeString(status.EditedAt.Time.UTC().Format(time.RFC3339)) + `"></data>`)
	}
	out.WriteString(`<a class="u-url u-uid" target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(statusURL) + `"><time class="formatted" datetime="` + html.EscapeString(createdISO) + `">` + html.EscapeString(status.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")) + `</time></a>`)
	if status.EditedAt.Valid {
		editedISO := status.EditedAt.Time.UTC().Format(time.RFC3339)
		editedAt := status.EditedAt.Time.UTC().Format("2006-01-02 15:04 UTC")
		editedTime := `<time class="formatted" datetime="` + html.EscapeString(editedISO) + `">` + html.EscapeString(editedAt) + `</time>`
		out.WriteString(`<span>·</span><span>` + settingsTVars(locale, "statuses.edited_at_html", "Edited %{date}", map[string]string{"date": editedTime}) + `</span>`)
	}
	out.WriteString(`<span>·</span><span title="` + html.EscapeString(settingsT(locale, "admin.statuses.visibility", "Visibility")) + `">` + html.EscapeString(statusEmbedVisibilityLabel(status.Visibility, locale)) + `</span>`)
	if appHTML := statusEmbedApplicationHTML(status); appHTML != "" {
		out.WriteString(`<span>·</span>` + appHTML)
	}
	repliesLabel := settingsT(locale, "statuses.replies", "Replies")
	boostsLabel := settingsT(locale, "admin.statuses.reblogs", "Boosts")
	favouritesLabel := settingsT(locale, "admin.statuses.favourites", "Favourites")
	out.WriteString(`<span>·</span><span title="` + html.EscapeString(repliesLabel) + `">` + html.EscapeString(repliesLabel) + ` ` + strconv.FormatInt(status.StatusStat.RepliesCount, 10) + `</span>`)
	out.WriteString(`<span>·</span><span title="` + html.EscapeString(boostsLabel) + `">` + html.EscapeString(boostsLabel) + ` ` + strconv.FormatInt(status.StatusStat.ReblogsCount, 10) + `</span>`)
	out.WriteString(`<span>·</span><span title="` + html.EscapeString(favouritesLabel) + `">` + html.EscapeString(favouritesLabel) + ` ` + strconv.FormatInt(status.StatusStat.FavouritesCount, 10) + `</span>`)
	out.WriteString(`</div>`)
	return out.String()
}

func statusEmbedVisibilityLabel(value int, locale string) string {
	switch value {
	case 1:
		return settingsT(locale, "statuses.visibilities.unlisted", "Unlisted")
	case 2, 4:
		return settingsT(locale, "statuses.visibilities.private", "Followers-only")
	case 3:
		return settingsT(locale, "statuses.visibilities.direct", "Direct")
	default:
		return settingsT(locale, "statuses.visibilities.public", "Public")
	}
}

func statusEmbedVisibilityName(value int) string {
	switch value {
	case 1:
		return "Unlisted"
	case 2, 4:
		return "Private"
	case 3:
		return "Direct"
	default:
		return "Public"
	}
}

func statusEmbedVisibilityClass(value int) string {
	return strings.ToLower(statusEmbedVisibilityName(value))
}

func statusEmbedApplicationHTML(status models.Status) string {
	if status.Application == nil || strings.TrimSpace(status.Application.Name) == "" || !statusEmbedShowApplication(status) {
		return ""
	}
	name := html.EscapeString(strings.TrimSpace(status.Application.Name))
	website := strings.TrimSpace(string(status.Application.Website))
	if website == "" {
		return `<strong class="application">` + name + `</strong>`
	}
	return `<a class="application" target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(website) + `">` + name + `</a>`
}

func statusEmbedShowApplication(status models.Status) bool {
	if status.Account.User.ID == 0 {
		return false
	}
	if !status.Account.User.Settings.Valid {
		return true
	}
	settings := decodeUserSettings(status.Account.User.Settings.String)
	return rawBool(settings["show_application"], true)
}

func statusEmbedAccountAvatarURL(baseURL string, account models.Account) string {
	return statusEmbedAccountAvatarURLWithConfig(config.Config{WebDomain: baseURL}, account)
}

func statusEmbedAccountAvatarURLWithConfig(cfg config.Config, account models.Account) string {
	baseURL := cfg.BaseURL()
	if account.AvatarRemoteURL.Valid && strings.TrimSpace(account.AvatarRemoteURL.String) != "" {
		return strings.TrimSpace(account.AvatarRemoteURL.String)
	}
	if account.AvatarFileName.Valid && strings.TrimSpace(account.AvatarFileName.String) != "" {
		return cfg.SystemAssetURL("accounts/avatars/" + strings.ReplaceAll(mediaPaperclipIDPartition(account.ID), "\\", "/") + "/original/" + url.PathEscape(account.AvatarFileName.String))
	}
	return baseURL + "/avatars/original/missing.png"
}

func statusEmbedAccountNameHTML(baseURL string, account models.Account, emojis []models.CustomEmoji) string {
	return statusEmbedAccountNameHTMLWithConfig(config.Config{WebDomain: baseURL}, account, emojis)
}

func statusEmbedAccountNameHTMLWithConfig(cfg config.Config, account models.Account, emojis []models.CustomEmoji) string {
	displayName := strings.TrimSpace(account.DisplayName)
	if displayName == "" {
		displayName = account.Username
	}
	return statusEmbedPlainTextHTMLWithConfig(cfg, displayName, emojis)
}

func statusEmbedLockedAccountMarker(account models.Account) string {
	if account.Locked {
		return ` <span title="Locked">[locked]</span>`
	}
	return ""
}

func statusEmbedContentHTML(baseURL string, status models.Status, emojis []models.CustomEmoji) string {
	return statusEmbedContentHTMLWithConfig(config.Config{WebDomain: baseURL}, status, emojis)
}

func statusEmbedContentHTMLWithConfig(cfg config.Config, status models.Status, emojis []models.CustomEmoji, locales ...string) string {
	locale := ""
	if len(locales) > 0 {
		locale = locales[0]
	}
	content := statusEmbedLinkedStatusTextHTMLWithConfig(cfg, status, emojis)
	if content == "" {
		content = `<span class="muted">` + html.EscapeString(settingsT(locale, "statuses.no_text", "No text")) + `</span>`
	}
	langAttr := statusEmbedLangAttr(status)
	spoiler := strings.TrimSpace(status.SpoilerText)
	if spoiler == "" {
		return `<div class="content e-content"` + langAttr + `>` + content + `</div>`
	}
	summary := statusEmbedPlainTextHTMLWithConfig(cfg, spoiler, emojis)
	if summary == "" {
		summary = html.EscapeString(settingsT(locale, "rss.content_warning", "Content warning"))
	}
	return `<details class="content content-warning"><summary class="content-warning__summary"><span class="content-warning__text p-summary">` + summary + `</span><span class="content-warning__trigger">` + html.EscapeString(settingsT(locale, "statuses.show_more", "Show more")) + `</span></summary><div class="content-warning__body e-content"` + langAttr + `>` + content + statusEmbedPollHTMLWithConfig(cfg, status, emojis) + `</div></details>`
}

func statusEmbedLangAttr(status models.Status) string {
	if status.Language.Valid && strings.TrimSpace(status.Language.String) != "" {
		return ` lang="` + html.EscapeString(strings.TrimSpace(status.Language.String)) + `"`
	}
	return ""
}

func (s *Server) statusEmbedCustomEmojis(status models.Status) ([]models.CustomEmoji, error) {
	if s.db == nil {
		return nil, nil
	}
	shortcodes := statusEmbedEmojiShortcodes(statusEmbedEmojiText(status))
	if len(shortcodes) == 0 {
		return nil, nil
	}
	var emojis []models.CustomEmoji
	query := customEmojiDomainQuery(s.db.Where("shortcode IN ? AND disabled = false", shortcodes), status.Account.Domain)
	if err := query.Find(&emojis).Error; err != nil {
		return nil, err
	}
	return emojis, nil
}

func statusEmbedEmojiText(status models.Status) string {
	parts := []string{status.Text, status.SpoilerText, status.Account.DisplayName}
	if status.Poll != nil {
		parts = append(parts, status.Poll.Options...)
	}
	return strings.Join(parts, "\n")
}

func statusEmbedEmojiShortcodes(text string) []string {
	seen := map[string]struct{}{}
	shortcodes := []string{}
	matches := customEmojiPlainPattern.FindAllStringSubmatchIndex(text, -1)
	for _, match := range matches {
		start, end := match[0], match[1]
		if !statusEmbedEmojiBoundaryOK(text, start-1) || !statusEmbedEmojiBoundaryOK(text, end) {
			continue
		}
		shortcode := text[match[2]:match[3]]
		if _, ok := seen[shortcode]; ok {
			continue
		}
		seen[shortcode] = struct{}{}
		shortcodes = append(shortcodes, shortcode)
	}
	return shortcodes
}

func statusEmbedEmojiBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	ch := text[index]
	return !(ch == ':' || ch == '_' || (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z'))
}

func statusEmbedPlainTextHTML(baseURL string, text string, emojis []models.CustomEmoji) string {
	return statusEmbedPlainTextHTMLWithConfig(config.Config{WebDomain: baseURL}, text, emojis)
}

func statusEmbedPlainTextHTMLWithConfig(cfg config.Config, text string, emojis []models.CustomEmoji) string {
	return statusEmbedTextHTMLWithConfig(cfg, text, emojis, false, statusEmbedLinkContext{})
}

func statusEmbedLinkedTextHTML(baseURL string, text string, emojis []models.CustomEmoji) string {
	return statusEmbedTextHTMLWithConfig(config.Config{WebDomain: baseURL}, text, emojis, true, statusEmbedLinkContext{})
}

func statusEmbedLinkedStatusTextHTML(baseURL string, status models.Status, emojis []models.CustomEmoji) string {
	return statusEmbedLinkedStatusTextHTMLWithConfig(config.Config{WebDomain: baseURL}, status, emojis)
}

func statusEmbedTextHTML(baseURL string, text string, emojis []models.CustomEmoji, linkify bool, linkContext statusEmbedLinkContext) string {
	return statusEmbedTextHTMLWithConfig(config.Config{WebDomain: baseURL}, text, emojis, linkify, linkContext)
}

func statusEmbedLinkedStatusTextHTMLWithConfig(cfg config.Config, status models.Status, emojis []models.CustomEmoji) string {
	return statusEmbedTextHTMLWithConfig(cfg, status.Text, emojis, true, statusEmbedLinkContextFromStatus(cfg, status))
}

func statusEmbedTextHTMLWithConfig(cfg config.Config, text string, emojis []models.CustomEmoji, linkify bool, linkContext statusEmbedLinkContext) string {
	baseURL := cfg.BaseURL()
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if len(emojis) == 0 {
		return statusEmbedPlainFragmentHTML(baseURL, text, linkify, linkContext)
	}
	emojiByShortcode := map[string]models.CustomEmoji{}
	for _, emoji := range emojis {
		emojiByShortcode[emoji.Shortcode] = emoji
	}
	var out strings.Builder
	last := 0
	matches := customEmojiPlainPattern.FindAllStringSubmatchIndex(text, -1)
	for _, match := range matches {
		start, end := match[0], match[1]
		shortcode := text[match[2]:match[3]]
		emoji, ok := emojiByShortcode[shortcode]
		if !ok || !statusEmbedEmojiBoundaryOK(text, start-1) || !statusEmbedEmojiBoundaryOK(text, end) {
			continue
		}
		out.WriteString(statusEmbedPlainFragmentHTML(baseURL, text[last:start], linkify, linkContext))
		out.WriteString(statusEmbedCustomEmojiHTMLWithConfig(cfg, emoji))
		last = end
	}
	out.WriteString(statusEmbedPlainFragmentHTML(baseURL, text[last:], linkify, linkContext))
	return out.String()
}

func statusEmbedPlainFragmentHTML(baseURL string, text string, linkify bool, linkContext statusEmbedLinkContext) string {
	if !linkify {
		return strings.ReplaceAll(html.EscapeString(text), "\n", "<br>")
	}
	return strings.ReplaceAll(statusEmbedLinkifyText(baseURL, text, linkContext), "\n", "<br>")
}

func statusEmbedLinkContextFromStatus(cfg config.Config, status models.Status) statusEmbedLinkContext {
	baseURL := cfg.BaseURL()
	ctx := statusEmbedLinkContext{
		mentions: map[string]statusEmbedMentionLink{},
		tags:     map[string]string{},
	}
	preloadedAccounts := []models.Account{status.Account}
	for _, mention := range status.Mentions {
		preloadedAccounts = append(preloadedAccounts, mention.Account)
	}
	for _, mention := range status.Mentions {
		account := mention.Account
		if account.ID == 0 && strings.TrimSpace(account.Username) == "" {
			continue
		}
		accountURL := statusEmbedAccountURL(baseURL, account)
		if accountURL == "" {
			continue
		}
		link := statusEmbedMentionLink{href: accountURL, display: statusEmbedMentionDisplayName(account, preloadedAccounts)}
		if account.Local() {
			ctx.mentions[strings.ToLower(account.Username)] = link
			for _, domain := range statusEmbedLocalMentionDomains(cfg) {
				ctx.mentions[strings.ToLower(account.Username+"@"+domain)] = link
			}
		}
		ctx.mentions[strings.ToLower(account.Acct())] = link
	}
	for _, tag := range status.Tags {
		if strings.TrimSpace(tag.Name) == "" {
			continue
		}
		tagURL := baseURL + "/tags/" + url.PathEscape(tag.Name)
		ctx.tags[strings.ToLower(tag.Name)] = tagURL
		if tag.DisplayName.Valid && strings.TrimSpace(tag.DisplayName.String) != "" {
			ctx.tags[strings.ToLower(strings.TrimSpace(tag.DisplayName.String))] = tagURL
		}
	}
	return ctx
}

func statusEmbedLocalMentionDomains(cfg config.Config) []string {
	seen := map[string]struct{}{}
	domains := []string{}
	for _, raw := range []string{cfg.LocalDomain} {
		domain := strings.Trim(strings.TrimSpace(raw), "/")
		if domain == "" {
			continue
		}
		if parsed, err := url.Parse(domain); err == nil && parsed.Host != "" {
			domain = parsed.Host
		}
		key := strings.ToLower(domain)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		domains = append(domains, domain)
	}
	return domains
}

func statusEmbedMentionDisplayName(account models.Account, preloaded []models.Account) string {
	sameUsernameHits := 0
	for _, other := range preloaded {
		if strings.EqualFold(other.Username, account.Username) && !statusEmbedSameAccountDomain(other, account) {
			sameUsernameHits++
		}
	}
	if sameUsernameHits > 0 {
		return account.Acct()
	}
	return account.Username
}

func statusEmbedSameAccountDomain(a models.Account, b models.Account) bool {
	aDomain := ""
	if a.Domain.Valid {
		aDomain = strings.TrimSpace(a.Domain.String)
	}
	bDomain := ""
	if b.Domain.Valid {
		bDomain = strings.TrimSpace(b.Domain.String)
	}
	return strings.EqualFold(aDomain, bDomain)
}

func statusEmbedAccountURL(baseURL string, account models.Account) string {
	if account.URL.Valid && strings.TrimSpace(account.URL.String) != "" {
		return strings.TrimSpace(account.URL.String)
	}
	if account.Local() {
		return baseURL + "/@" + url.PathEscape(account.Username)
	}
	if strings.TrimSpace(account.URI) != "" {
		return strings.TrimSpace(account.URI)
	}
	return baseURL + "/@" + url.PathEscape(account.Acct())
}

func statusEmbedLinkifyText(baseURL string, text string, linkContext statusEmbedLinkContext) string {
	var out strings.Builder
	last := 0
	matches := statusEmbedLinkPattern.FindAllStringIndex(text, -1)
	for _, match := range matches {
		start, end := match[0], match[1]
		raw := text[start:end]
		if !statusEmbedTokenBoundaryOK(text, start-1, raw) {
			continue
		}
		if strings.HasPrefix(raw, "@") && !statusEmbedMentionEndBoundaryOK(text, end) {
			continue
		}
		if adjustedEnd, ok := statusEmbedHashtagHTTPBoundaryEnd(text, raw, end); ok {
			end = adjustedEnd
			raw = text[start:end]
		}
		token, trailing := statusEmbedTrimTrailingLinkPunctuation(raw)
		if token == "" {
			continue
		}
		out.WriteString(html.EscapeString(text[last:start]))
		out.WriteString(statusEmbedLinkHTML(baseURL, token, linkContext))
		out.WriteString(html.EscapeString(trailing))
		last = end
	}
	out.WriteString(html.EscapeString(text[last:]))
	return out.String()
}

func statusEmbedTokenBoundaryOK(text string, index int, token string) bool {
	if strings.HasPrefix(token, "#") {
		return statusEmbedHashtagBoundaryOK(text, index)
	}
	if strings.HasPrefix(token, "@") {
		return statusEmbedMentionBoundaryOK(text, index)
	}
	return statusEmbedLinkBoundaryOK(text, index)
}

func statusEmbedHashtagBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index+1])
	return !(r == '=' || r == '/' || r == ')' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

func statusEmbedMentionBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index+1])
	return !(r == '=' || r == '/' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

func statusEmbedMentionEndBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[index:])
	return r != '@'
}

func statusEmbedLinkBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	ch := text[index]
	return !(ch == '_' || (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z'))
}

func statusEmbedHashtagHTTPBoundaryEnd(text string, raw string, end int) (int, bool) {
	if !strings.HasPrefix(raw, "#") || !strings.HasPrefix(text[end:], "://") {
		return end, false
	}
	lower := strings.ToLower(raw)
	for _, suffix := range []string{"https", "http"} {
		if strings.HasSuffix(lower, suffix) && len(raw) > len("#")+len(suffix) {
			return end - len(suffix), true
		}
	}
	return end, false
}

func statusEmbedTrimTrailingLinkPunctuation(raw string) (string, string) {
	token := raw
	trailing := ""
	for token != "" {
		last := token[len(token)-1]
		if last == ')' && strings.Count(token, ")") <= strings.Count(token, "(") {
			break
		}
		if !strings.ContainsRune(".,!?:;)]}>\"'「」", rune(last)) {
			break
		}
		token = token[:len(token)-1]
		trailing = string(last) + trailing
	}
	return token, trailing
}

func statusEmbedLinkHTML(baseURL string, token string, linkContext statusEmbedLinkContext) string {
	switch {
	case statusEmbedURLToken(token):
		return statusEmbedURLLinkHTML(token)
	case strings.HasPrefix(token, "#"):
		tag := strings.TrimPrefix(token, "#")
		if !statusEmbedHashtagNameValid(tag) {
			return html.EscapeString(token)
		}
		href := baseURL + "/tags/" + url.PathEscape(strings.ToLower(tag))
		if linkContext.tags != nil {
			if value := linkContext.tags[strings.ToLower(tag)]; value != "" {
				href = value
			}
		}
		return `<a href="` + html.EscapeString(href) + `" class="mention hashtag" rel="tag">#<span>` + html.EscapeString(tag) + `</span></a>`
	case strings.HasPrefix(token, "@"):
		acct := strings.TrimPrefix(token, "@")
		if statusEmbedMentionDomainTooLong(acct) {
			return html.EscapeString(token)
		}
		if linkContext.mentions != nil {
			if value := linkContext.mentions[strings.ToLower(acct)]; value.href != "" {
				return `<span class="h-card" translate="no"><a href="` + html.EscapeString(value.href) + `" class="u-url mention">@<span>` + html.EscapeString(value.display) + `</span></a></span>`
			}
		}
		return html.EscapeString(token)
	default:
		return html.EscapeString(token)
	}
}

func statusEmbedMentionDomainTooLong(acct string) bool {
	parts := strings.SplitN(acct, "@", 2)
	return len(parts) == 2 && len(parts[1]) > statusEmbedMaxDomainLength
}

func statusEmbedHashtagNameValid(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || r == '·' || r == '・' || r == '\u200c' {
			return true
		}
	}
	return false
}

func statusEmbedURLLinkHTML(raw string) string {
	urlValue := strings.TrimSpace(raw)
	if !statusEmbedURLToken(urlValue) {
		return html.EscapeString(raw)
	}
	if strings.Contains(urlValue, "komiflo.com") {
		return `<a href="` + html.EscapeString(urlValue) + `" target="_blank" rel="nofollow noopener noreferrer" translate="no">` + html.EscapeString(urlValue) + `</a>`
	}
	prefix := statusEmbedURLDisplayPrefix(urlValue)
	rest := urlValue[len(prefix):]
	display := rest
	suffix := ""
	class := ""
	if len(rest) > 30 {
		display = rest[:30]
		suffix = rest[30:]
		class = ` class="ellipsis"`
	}
	return `<a href="` + html.EscapeString(urlValue) + `" target="_blank" rel="nofollow noopener noreferrer" translate="no"><span class="invisible">` + html.EscapeString(prefix) + `</span><span` + class + `>` + html.EscapeString(display) + `</span><span class="invisible">` + html.EscapeString(suffix) + `</span></a>`
}

func statusEmbedURLToken(token string) bool {
	lower := strings.ToLower(strings.TrimSpace(token))
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "dat://") ||
		strings.HasPrefix(lower, "dweb://") ||
		strings.HasPrefix(lower, "ipfs://") ||
		strings.HasPrefix(lower, "ipns://") ||
		strings.HasPrefix(lower, "ssb://") ||
		strings.HasPrefix(lower, "gopher://") ||
		strings.HasPrefix(lower, "gemini://") ||
		strings.HasPrefix(lower, "xmpp:") ||
		strings.HasPrefix(lower, "magnet:?")
}

func statusEmbedURLDisplayPrefix(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "http://www."):
		return raw[:len("http://www.")]
	case strings.HasPrefix(lower, "https://www."):
		return raw[:len("https://www.")]
	case strings.HasPrefix(lower, "http://"):
		return raw[:len("http://")]
	case strings.HasPrefix(lower, "https://"):
		return raw[:len("https://")]
	case strings.HasPrefix(lower, "xmpp:"):
		return raw[:len("xmpp:")]
	default:
		return ""
	}
}

func statusEmbedCustomEmojiHTML(baseURL string, emoji models.CustomEmoji) string {
	return statusEmbedCustomEmojiHTMLWithConfig(config.Config{WebDomain: baseURL}, emoji)
}

func statusEmbedCustomEmojiHTMLWithConfig(cfg config.Config, emoji models.CustomEmoji) string {
	src := statusEmbedCustomEmojiURLWithConfig(cfg, emoji, "static")
	original := statusEmbedCustomEmojiURLWithConfig(cfg, emoji, "original")
	if src == "" {
		return html.EscapeString(":" + emoji.Shortcode + ":")
	}
	alt := ":" + emoji.Shortcode + ":"
	return `<img rel="emoji" draggable="false" width="16" height="16" class="emojione custom-emoji" alt="` + html.EscapeString(alt) + `" title="` + html.EscapeString(alt) + `" src="` + html.EscapeString(src) + `" data-original="` + html.EscapeString(original) + `" data-static="` + html.EscapeString(src) + `">`
}

func statusEmbedCustomEmojiURL(baseURL string, emoji models.CustomEmoji, style string) string {
	return statusEmbedCustomEmojiURLWithConfig(config.Config{WebDomain: baseURL}, emoji, style)
}

func statusEmbedCustomEmojiURLWithConfig(cfg config.Config, emoji models.CustomEmoji, style string) string {
	if !emoji.ImageFileName.Valid || strings.TrimSpace(emoji.ImageFileName.String) == "" {
		if emoji.ImageRemoteURL.Valid {
			return strings.TrimSpace(emoji.ImageRemoteURL.String)
		}
		return ""
	}
	filename := emoji.ImageFileName.String
	if style == "static" {
		filename = statusEmbedPaperclipStaticFilename(filename)
	}
	prefix := ""
	if !emoji.Local() && emoji.ImageStorageSchemaVersion.Valid && emoji.ImageStorageSchemaVersion.Int64 >= 1 {
		prefix = "cache/"
	}
	return cfg.SystemAssetURL(prefix + "custom_emojis/images/" + strings.ReplaceAll(mediaPaperclipIDPartition(emoji.ID), "\\", "/") + "/" + style + "/" + url.PathEscape(filename))
}

func statusEmbedPaperclipStaticFilename(filename string) string {
	if index := strings.LastIndex(filename, "."); index > 0 {
		return filename[:index] + ".png"
	}
	return filename + ".png"
}

func statusEmbedMediaHTML(baseURL string, status models.Status) string {
	return statusEmbedMediaHTMLWithConfig(config.Config{WebDomain: baseURL}, status)
}

func statusEmbedMediaHTMLWithConfig(cfg config.Config, status models.Status, locales ...string) string {
	locale := ""
	if len(locales) > 0 {
		locale = locales[0]
	}
	attachments := snapshotMediaAttachments(status)
	if len(attachments) == 0 {
		return ""
	}
	items := statusEmbedMediaItemsHTMLWithConfig(cfg, attachments)
	if items == "" {
		return ""
	}
	if status.Sensitive {
		return `<details class="media sensitive-media"><summary class="media-spoiler"><span><span class="media-spoiler__warning">` + html.EscapeString(settingsT(locale, "stream_entries.sensitive_content", "Sensitive content")) + `</span><span class="media-spoiler__trigger">` + html.EscapeString(settingsT(locale, "stream_entries.click_to_show", "Click to show")) + `</span></span></summary>` + items + `</details>`
	}

	return `<div class="media">` + items + `</div>`
}

func statusEmbedMediaItemsHTML(baseURL string, attachments []models.MediaAttachment) string {
	return statusEmbedMediaItemsHTMLWithConfig(config.Config{WebDomain: baseURL}, attachments)
}

func statusEmbedMediaItemsHTMLWithConfig(cfg config.Config, attachments []models.MediaAttachment) string {
	var out strings.Builder
	for _, attachment := range attachments {
		src := statusEmbedMediaURLWithConfig(cfg, attachment, false)
		if src == "" {
			continue
		}
		alt := strings.TrimSpace(attachment.Description.String)
		switch attachment.Type {
		case 0:
			out.WriteString(`<img src="` + html.EscapeString(src) + `" alt="` + html.EscapeString(alt) + `">`)
		case 1:
			out.WriteString(`<video src="` + html.EscapeString(src) + `" autoplay="autoplay" muted="muted" loop="loop" playsinline="playsinline" title="` + html.EscapeString(alt) + `"></video>`)
		case 2:
			poster := statusEmbedMediaURLWithConfig(cfg, attachment, true)
			out.WriteString(`<video src="` + html.EscapeString(src) + `" controls="controls"`)
			if poster != "" {
				out.WriteString(` poster="` + html.EscapeString(poster) + `"`)
			}
			out.WriteString(` title="` + html.EscapeString(alt) + `"></video>`)
		case 4:
			out.WriteString(`<audio src="` + html.EscapeString(src) + `" controls="controls" title="` + html.EscapeString(alt) + `"></audio>`)
		default:
			out.WriteString(`<a target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(src) + `">` + html.EscapeString(statusEmbedMediaLabel(attachment)) + `</a>`)
		}
	}
	return out.String()
}

func statusEmbedPollHTML(status models.Status) string {
	return statusEmbedPollHTMLWithCustomEmojis("", status, nil)
}

func statusEmbedPollHTMLWithCustomEmojis(baseURL string, status models.Status, emojis []models.CustomEmoji) string {
	return statusEmbedPollHTMLWithConfig(config.Config{WebDomain: baseURL}, status, emojis)
}

func statusEmbedPollHTMLWithConfig(cfg config.Config, status models.Status, emojis []models.CustomEmoji) string {
	if status.Poll == nil || len(status.Poll.Options) == 0 {
		return ""
	}
	poll := status.Poll
	expired := pollExpiredAt(poll.ExpiresAt, time.Now().UTC())
	total := poll.VotesCount
	totalLabel := "votes"
	if poll.VotersCount.Valid {
		total = poll.VotersCount.Int64
		totalLabel = "people"
	}

	var out strings.Builder
	out.WriteString(`<div class="poll"><ul>`)
	for index, option := range poll.Options {
		out.WriteString(`<li><label class="poll-option">`)
		if expired {
			votes := int64(0)
			if index < len(poll.CachedTallies) {
				votes = poll.CachedTallies[index]
			}
			percent := int64(0)
			if total > 0 {
				percent = (100*votes + total/2) / total
			}
			out.WriteString(`<span class="poll-percent">` + strconv.FormatInt(percent, 10) + `%</span>`)
			out.WriteString(`<span>` + statusEmbedPlainTextHTMLWithConfig(cfg, option, emojis) + `</span></label>`)
			out.WriteString(`<progress max="100" value="` + strconv.FormatInt(maxInt64(percent, 1), 10) + `"></progress>`)
		} else {
			inputClass := "poll-input"
			if poll.Multiple {
				inputClass += " checkbox"
			}
			out.WriteString(`<span class="` + inputClass + `"></span><span>` + statusEmbedPlainTextHTMLWithConfig(cfg, option, emojis) + `</span></label>`)
		}
		out.WriteString(`</li>`)
	}
	out.WriteString(`</ul><div class="poll-footer">` + strconv.FormatInt(total, 10) + ` ` + totalLabel)
	if poll.ExpiresAt.Valid {
		out.WriteString(` · ` + html.EscapeString(poll.ExpiresAt.Time.UTC().Format("2006-01-02 15:04 UTC")))
	}
	out.WriteString(`</div></div>`)
	return out.String()
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func statusEmbedPreviewCardHTML(baseURL string, status models.Status) string {
	return statusEmbedPreviewCardHTMLWithConfig(config.Config{WebDomain: baseURL}, status)
}

func statusEmbedPreviewCardHTMLWithConfig(cfg config.Config, status models.Status, locales ...string) string {
	if len(status.MediaAttachments) > 0 || len(status.PreviewCards) == 0 {
		return ""
	}
	locale := ""
	if len(locales) > 0 {
		locale = locales[0]
	}
	card := status.PreviewCards[0]
	if strings.TrimSpace(card.URL) == "" {
		return ""
	}
	title := firstNonEmpty(strings.TrimSpace(card.Title), strings.TrimSpace(card.URL))
	provider := firstNonEmpty(strings.TrimSpace(card.ProviderName), previewCardHost(card.URL))
	cardLanguage := ""
	if card.Language.Valid {
		cardLanguage = strings.TrimSpace(card.Language.String)
	}
	expanded := card.Type == 2 || (card.Width > card.Height && statusEmbedPreviewCardImageURLWithConfig(cfg, card) != "")
	cardClass := "status-card"
	if expanded {
		cardClass += " expanded"
	}
	var out strings.Builder
	out.WriteString(`<a class="` + cardClass + `" target="_blank" rel="noopener noreferrer" href="` + html.EscapeString(card.URL) + `">`)
	out.WriteString(`<div class="status-card__image">`)
	if image := statusEmbedPreviewCardImageURLWithConfig(cfg, card); image != "" {
		langAttr := ""
		if cardLanguage != "" {
			langAttr = ` lang="` + html.EscapeString(cardLanguage) + `"`
		}
		alt := strings.TrimSpace(card.ImageDescription)
		out.WriteString(`<img class="status-card__image-image" src="` + html.EscapeString(image) + `" alt="` + html.EscapeString(alt) + `" title="` + html.EscapeString(alt) + `"` + langAttr + `>`)
	} else {
		out.WriteString(`<span class="status-card__image-icon" aria-hidden="true">file</span>`)
	}
	out.WriteString(`</div><div class="status-card__content">`)
	if provider != "" {
		out.WriteString(`<span class="status-card__host">`)
		if cardLanguage != "" {
			out.WriteString(`<span lang="` + html.EscapeString(cardLanguage) + `">` + html.EscapeString(provider) + `</span>`)
		} else {
			out.WriteString(html.EscapeString(provider))
		}
		out.WriteString(`</span>`)
	}
	titleLang := ""
	if cardLanguage != "" {
		titleLang = ` lang="` + html.EscapeString(cardLanguage) + `"`
	}
	out.WriteString(`<strong class="status-card__title" title="` + html.EscapeString(title) + `"` + titleLang + `>` + html.EscapeString(title) + `</strong>`)
	if strings.TrimSpace(card.AuthorName) != "" {
		authorHTML := `<strong>` + html.EscapeString(strings.TrimSpace(card.AuthorName)) + `</strong>`
		out.WriteString(`<span class="status-card__author">` + settingsTVars(locale, "link_preview.author", "By %{name}", map[string]string{"name": authorHTML}) + `</span>`)
	} else if strings.TrimSpace(card.Description) != "" {
		out.WriteString(`<span class="status-card__description">` + html.EscapeString(card.Description) + `</span>`)
	}
	out.WriteString(`</div></a>`)
	return out.String()
}

func statusEmbedPreviewCardImageURL(baseURL string, card models.PreviewCard) string {
	return statusEmbedPreviewCardImageURLWithConfig(config.Config{WebDomain: baseURL}, card)
}

func statusEmbedPreviewCardImageURLWithConfig(cfg config.Config, card models.PreviewCard) string {
	if !card.ImageFileName.Valid || strings.TrimSpace(card.ImageFileName.String) == "" {
		return ""
	}
	prefix := ""
	if card.ImageStorageSchemaVersion.Valid && card.ImageStorageSchemaVersion.Int64 >= 1 {
		prefix = "cache/"
	}
	return cfg.SystemAssetURL(prefix + "preview_cards/images/" + strings.ReplaceAll(mediaPaperclipIDPartition(card.ID), "\\", "/") + "/original/" + url.PathEscape(card.ImageFileName.String))
}

func statusEmbedMediaURL(baseURL string, attachment models.MediaAttachment, thumbnail bool) string {
	return statusEmbedMediaURLWithConfig(config.Config{WebDomain: baseURL}, attachment, thumbnail)
}

func statusEmbedMediaURLWithConfig(cfg config.Config, attachment models.MediaAttachment, thumbnail bool) string {
	if thumbnail {
		if attachment.ThumbnailRemoteURL.Valid && strings.TrimSpace(attachment.ThumbnailRemoteURL.String) != "" {
			return strings.TrimSpace(attachment.ThumbnailRemoteURL.String)
		}
		if attachment.ThumbnailFileName.Valid && strings.TrimSpace(attachment.ThumbnailFileName.String) != "" {
			return cfg.SystemAssetURL("media_attachments/thumbnails/" + strings.ReplaceAll(mediaPaperclipIDPartition(attachment.ID), "\\", "/") + "/original/" + url.PathEscape(attachment.ThumbnailFileName.String))
		}
	}
	if strings.TrimSpace(attachment.RemoteURL) != "" {
		return strings.TrimSpace(attachment.RemoteURL)
	}
	if attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != "" {
		return cfg.SystemAssetURL("media_attachments/files/" + strings.ReplaceAll(mediaPaperclipIDPartition(attachment.ID), "\\", "/") + "/original/" + url.PathEscape(attachment.FileFileName.String))
	}
	return ""
}

func statusEmbedMediaLabel(attachment models.MediaAttachment) string {
	if attachment.Description.Valid && strings.TrimSpace(attachment.Description.String) != "" {
		return attachment.Description.String
	}
	if attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != "" {
		return attachment.FileFileName.String
	}
	return "Media attachment"
}

func embedHTML(src string, script string, width int, height *int) string {
	heightAttr := ""
	if height != nil {
		heightAttr = ` height="` + strconv.Itoa(*height) + `"`
	}
	return `<iframe src="` + html.EscapeString(src) + `" class="mastodon-embed" style="max-width: 100%; border: 0" width="` +
		strconv.Itoa(width) + `"` + heightAttr + ` allowfullscreen="allowfullscreen"></iframe>` +
		`<script src="` + html.EscapeString(script) + `" async="async"></script>`
}

func statusIDFromLocalURL(baseURL string, rawURL string) string {
	if strings.TrimSpace(rawURL) == "" {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Host, base.Host) {
		return ""
	}
	parts := strings.Split(strings.Trim(path.Clean(u.Path), "/"), "/")
	id := ""
	switch {
	case len(parts) == 2 && strings.HasPrefix(parts[0], "@"):
		id = parts[1]
	case len(parts) == 4 && parts[0] == "users" && parts[2] == "statuses":
		id = parts[3]
	default:
		return ""
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return ""
	}
	return id
}

func intParam(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	return rubyToI(raw)
}

func remoteStatusURL(status models.Status) string {
	if status.URL.Valid && strings.TrimSpace(status.URL.String) != "" {
		return strings.TrimSpace(status.URL.String)
	}
	if status.URI.Valid {
		return strings.TrimSpace(status.URI.String)
	}
	return ""
}

func fetchRemoteOEmbed(rawURL string) (map[string]any, error) {
	endpoint, format, err := discoverRemoteOEmbedEndpoint(rawURL)
	if err != nil {
		return nil, err
	}
	body, err := fetchRemoteOEmbedBody(endpoint, "")
	if err != nil {
		return nil, err
	}
	embed, err := parseRemoteOEmbed(body, format)
	if err != nil {
		return nil, err
	}
	if !validRemoteOEmbed(embed) {
		return nil, fmt.Errorf("invalid oembed payload")
	}
	return embed, nil
}

func (s *Server) fetchRemoteOEmbed(ctx context.Context, rawURL string) (map[string]any, error) {
	endpoint, format, err := s.discoverRemoteOEmbedEndpoint(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	body, err := fetchRemoteOEmbedBody(endpoint, "")
	if err != nil {
		return nil, err
	}
	embed, err := parseRemoteOEmbed(body, format)
	if err != nil {
		return nil, err
	}
	if !validRemoteOEmbed(embed) {
		return nil, fmt.Errorf("invalid oembed payload")
	}
	return embed, nil
}

func discoverRemoteOEmbedEndpoint(rawURL string) (string, remoteOEmbedFormat, error) {
	body, err := fetchRemoteOEmbedBody(rawURL, "text/html")
	if err != nil {
		return "", "", err
	}
	endpoint, format := discoverRemoteOEmbedEndpointFromHTML(rawURL, string(body))
	if endpoint == "" {
		return "", "", fmt.Errorf("oembed endpoint not found")
	}
	return endpoint, format, nil
}

func (s *Server) discoverRemoteOEmbedEndpoint(ctx context.Context, rawURL string) (string, remoteOEmbedFormat, error) {
	if endpoint, format, ok := s.cachedRemoteOEmbedEndpoint(ctx, rawURL); ok {
		return endpoint, format, nil
	}
	body, err := fetchRemoteOEmbedBody(rawURL, "text/html")
	if err != nil {
		return "", "", err
	}
	endpoint, format := s.discoverRemoteOEmbedEndpointFromHTML(ctx, rawURL, string(body))
	if endpoint == "" {
		return "", "", fmt.Errorf("oembed endpoint not found")
	}
	return endpoint, format, nil
}

func discoverRemoteOEmbedEndpointFromHTML(baseURL string, body string) (string, remoteOEmbedFormat) {
	if endpoint := discoverRemoteOEmbedEndpointByTypes(baseURL, body, map[string]bool{"application/json+oembed": true, "text/json+oembed": true}); endpoint != "" {
		return endpoint, remoteOEmbedJSON
	}
	if endpoint := discoverRemoteOEmbedEndpointByTypes(baseURL, body, map[string]bool{"text/xml+oembed": true}); endpoint != "" {
		return endpoint, remoteOEmbedXML
	}
	return "", ""
}

func (s *Server) discoverRemoteOEmbedEndpointFromHTML(ctx context.Context, baseURL string, body string) (string, remoteOEmbedFormat) {
	endpoint, format := discoverRemoteOEmbedEndpointFromHTML(baseURL, body)
	if endpoint != "" {
		s.cacheRemoteOEmbedEndpoint(ctx, baseURL, endpoint, format)
	}
	return endpoint, format
}

func discoverRemoteOEmbedEndpointByTypes(baseURL string, body string, types map[string]bool) string {
	for _, link := range oEmbedLinkTagPattern.FindAllString(body, -1) {
		attrs := htmlTagAttrs(link)
		if !types[strings.ToLower(strings.TrimSpace(attrs["type"]))] {
			continue
		}
		if endpoint := resolveRemoteOEmbedEndpoint(baseURL, attrs["href"]); endpoint != "" {
			return endpoint
		}
	}
	return ""
}

func htmlTagAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	for _, match := range oEmbedAttrPattern.FindAllStringSubmatch(tag, -1) {
		if len(match) < 5 {
			continue
		}
		value := firstNonEmpty(match[2], match[3], match[4])
		attrs[strings.ToLower(match[1])] = html.UnescapeString(value)
	}
	return attrs
}

func resolveRemoteOEmbedEndpoint(baseRaw string, hrefRaw string) string {
	base, err := url.Parse(strings.TrimSpace(baseRaw))
	if err != nil || base.Host == "" {
		return ""
	}
	href, err := url.Parse(strings.TrimSpace(hrefRaw))
	if err != nil || hrefRaw == "" {
		return ""
	}
	resolved := base.ResolveReference(href)
	if base.Scheme == "https" {
		resolved.Scheme = "https"
	}
	if !remoteFetchURLAllowed(resolved.String()) {
		return ""
	}
	return resolved.String()
}

func remoteOEmbedEndpointCacheKey(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	return "oembed_endpoint:" + host
}

func remoteOEmbedEndpointTemplate(endpoint string) string {
	return oEmbedEndpointURLPattern.ReplaceAllString(endpoint, "={url}")
}

func remoteOEmbedEndpointTemplateReusable(endpoint string) bool {
	return remoteOEmbedEndpointTemplate(endpoint) != endpoint
}

func remoteOEmbedEndpointFromTemplate(template string, rawURL string) string {
	if !strings.Contains(template, "{url}") {
		return template
	}
	return strings.ReplaceAll(template, "{url}", url.QueryEscape(rawURL))
}

func (s *Server) cachedRemoteOEmbedEndpoint(ctx context.Context, rawURL string) (string, remoteOEmbedFormat, bool) {
	if s == nil {
		return "", "", false
	}
	key := remoteOEmbedEndpointCacheKey(rawURL)
	if key == "" {
		return "", "", false
	}
	redisCtx, cancel := remoteOEmbedEndpointCacheContext(ctx)
	defer cancel()
	raw := ""
	for _, candidate := range railsCacheRedisKeyCandidates(s.cfg, key) {
		value, err := s.cacheRedisCommand(redisCtx, "GET", candidate)
		if err != nil {
			continue
		}
		if valueString, ok := value.(string); ok && strings.TrimSpace(valueString) != "" {
			raw = valueString
			break
		}
	}
	if raw == "" {
		return "", "", false
	}
	var cached remoteOEmbedEndpointCache
	if err := json.Unmarshal([]byte(raw), &cached); err != nil || cached.Endpoint == "" || cached.Format == "" {
		return "", "", false
	}
	endpoint := remoteOEmbedEndpointFromTemplate(cached.Endpoint, rawURL)
	if !remoteFetchURLAllowed(endpoint) {
		return "", "", false
	}
	return endpoint, cached.Format, true
}

func (s *Server) cacheRemoteOEmbedEndpoint(ctx context.Context, rawURL string, endpoint string, format remoteOEmbedFormat) {
	if s == nil || endpoint == "" || format == "" || !remoteOEmbedEndpointTemplateReusable(endpoint) {
		return
	}
	key := remoteOEmbedEndpointCacheKey(rawURL)
	if key == "" {
		return
	}
	cached := remoteOEmbedEndpointCache{Endpoint: remoteOEmbedEndpointTemplate(endpoint), Format: format}
	encoded, err := json.Marshal(cached)
	if err != nil {
		return
	}
	redisCtx, cancel := remoteOEmbedEndpointCacheContext(ctx)
	defer cancel()
	_, _ = s.cacheRedisCommand(redisCtx, "SETEX", railsCacheRedisWriteKey(s.cfg, key), strconv.FormatInt(int64(remoteOEmbedEndpointCacheTTL/time.Second), 10), string(encoded))
}

func remoteOEmbedEndpointCacheContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, remoteOEmbedEndpointCacheCommandTimeout)
}

func fetchRemoteOEmbedBody(rawURL string, accept string) ([]byte, error) {
	if !remoteFetchURLAllowed(rawURL) {
		return nil, fmt.Errorf("remote oembed host is not allowed")
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := activityHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote oembed fetch failed")
	}
	if accept == "text/html" {
		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		if contentType != "" && !strings.Contains(contentType, "text/html") {
			return nil, fmt.Errorf("remote oembed discovery did not return html")
		}
	}
	return readActivityResponseBodyWithRailsLimit(resp, "remote-oembed")
}

func remoteFetchURLAllowed(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return activityFetchHostAllowed(parsed.Hostname())
}

func parseRemoteOEmbed(body []byte, format remoteOEmbedFormat) (map[string]any, error) {
	switch format {
	case remoteOEmbedJSON:
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		return out, nil
	case remoteOEmbedXML:
		return parseRemoteOEmbedXML(body)
	default:
		return nil, fmt.Errorf("unknown oembed format")
	}
}

func parseRemoteOEmbedXML(body []byte) (map[string]any, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	out := map[string]any{}
	var current string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local != "oembed" {
				current = typed.Name.Local
			}
		case xml.CharData:
			if current != "" {
				value := strings.TrimSpace(string(typed))
				if value != "" {
					out[current] = oEmbedXMLValue(current, value)
				}
			}
		case xml.EndElement:
			if typed.Name.Local == current {
				current = ""
			}
		}
	}
	return out, nil
}

func oEmbedXMLValue(key string, value string) any {
	switch key {
	case "width", "height", "cache_age":
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return value
}

func validRemoteOEmbed(embed map[string]any) bool {
	if len(embed) == 0 {
		return false
	}
	if fmt.Sprint(embed["version"]) != "1.0" {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(embed["type"])) != ""
}

func sanitizeRemoteOEmbedHTML(value string) string {
	value = oEmbedScriptBlockPattern.ReplaceAllString(value, "")
	var out strings.Builder
	last := 0
	for _, match := range oEmbedHTMLTagPattern.FindAllStringSubmatchIndex(value, -1) {
		if match[0] > last {
			out.WriteString(html.EscapeString(value[last:match[0]]))
		}
		closing := value[match[2]:match[3]] != ""
		name := strings.ToLower(value[match[4]:match[5]])
		attrText := ""
		if match[6] >= 0 {
			attrText = value[match[6]:match[7]]
		}
		if remoteOEmbedAllowedElement(name) {
			if closing {
				if name != "source" {
					out.WriteString("</" + name + ">")
				}
			} else {
				out.WriteString("<" + name + sanitizeRemoteOEmbedAttrs(name, attrText) + ">")
			}
		}
		last = match[1]
	}
	if last < len(value) {
		out.WriteString(html.EscapeString(value[last:]))
	}
	return strings.TrimSpace(out.String())
}

func remoteOEmbedAllowedElement(name string) bool {
	switch name {
	case "audio", "iframe", "source", "video":
		return true
	default:
		return false
	}
}

func sanitizeRemoteOEmbedAttrs(name string, attrText string) string {
	attrs := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, match := range oEmbedAttrPattern.FindAllStringSubmatch(attrText, -1) {
		if len(match) < 5 {
			continue
		}
		key := strings.ToLower(match[1])
		if !remoteOEmbedAllowedAttr(name, key) {
			continue
		}
		value := strings.TrimSpace(firstNonEmpty(match[2], match[3], match[4]))
		if (name == "iframe" || name == "source") && key == "src" && !remoteOEmbedHTTPURLAllowed(value) {
			continue
		}
		attrs = append(attrs, key+`="`+html.EscapeString(value)+`"`)
		seen[key] = struct{}{}
	}
	for _, token := range strings.Fields(attrText) {
		key := strings.ToLower(strings.Trim(token, "/"))
		if key == "" || strings.Contains(key, "=") || !remoteOEmbedAllowedAttr(name, key) || !remoteOEmbedBooleanAttr(key) {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		attrs = append(attrs, key+`=""`)
		seen[key] = struct{}{}
	}
	if name == "iframe" {
		attrs = append(attrs, `sandbox="allow-scripts allow-same-origin allow-popups allow-popups-to-escape-sandbox allow-forms"`)
	}
	if len(attrs) == 0 {
		return ""
	}
	return " " + strings.Join(attrs, " ")
}

func remoteOEmbedAllowedAttr(name string, key string) bool {
	switch name {
	case "audio":
		return key == "controls"
	case "iframe":
		switch key {
		case "allowfullscreen", "frameborder", "height", "scrolling", "src", "width":
			return true
		}
	case "source":
		return key == "src" || key == "type"
	case "video":
		switch key {
		case "controls", "height", "loop", "width":
			return true
		}
	}
	return false
}

func remoteOEmbedBooleanAttr(key string) bool {
	switch key {
	case "allowfullscreen", "controls", "loop":
		return true
	default:
		return false
	}
}

func remoteOEmbedHTTPURLAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
