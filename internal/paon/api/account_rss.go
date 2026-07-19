package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type accountRSSOptions struct {
	WithReplies bool
	OnlyMedia   bool
	Tag         string
}

func (s *Server) publicAccount(c *echo.Context) error {
	if publicShortAccountFormatPathIsRemote(c) {
		return s.webApp(c)
	}
	return s.publicAccountMaybeRSS(c, accountRSSOptions{})
}

func (s *Server) publicAccountWithReplies(c *echo.Context) error {
	if publicShortAccountFormatPathIsRemote(c) {
		return s.webApp(c)
	}
	return s.publicAccountMaybeRSS(c, accountRSSOptions{WithReplies: true})
}

func (s *Server) publicAccountMedia(c *echo.Context) error {
	if publicShortAccountFormatPathIsRemote(c) {
		return s.webApp(c)
	}
	return s.publicAccountMaybeRSS(c, accountRSSOptions{OnlyMedia: true})
}

func (s *Server) publicAccountFollowers(c *echo.Context) error {
	return s.publicAccountFollowCollection(c, true)
}

func (s *Server) publicAccountFollowing(c *echo.Context) error {
	return s.publicAccountFollowCollection(c, false)
}

func (s *Server) publicAccountFollowersJSON(c *echo.Context) error {
	if publicAccountPathIsRemote(publicShortAccountParam(c, "username")) {
		return s.webApp(c)
	}
	return s.publicAccountFollowCollectionActivityPub(c, true)
}

func (s *Server) publicAccountFollowingJSON(c *echo.Context) error {
	if publicAccountPathIsRemote(publicShortAccountParam(c, "username")) {
		return s.webApp(c)
	}
	return s.publicAccountFollowCollectionActivityPub(c, false)
}

func (s *Server) publicAccountFollowCollection(c *echo.Context, followers bool) error {
	username := publicShortAccountParam(c, "username")
	if publicAccountPathIsRemote(username) {
		return s.webApp(c)
	}
	if publicRequestHasFormat(c, "json") {
		return s.publicAccountFollowCollectionActivityPub(c, followers)
	}
	if format := strings.ToLower(c.Param("format")); format != "" {
		switch format {
		case "html":
		case "json":
			return s.publicAccountFollowCollectionActivityPub(c, followers)
		default:
			return noContentError(http.StatusNotAcceptable)
		}
	}
	switch publicRSSActivityPubAcceptFormat(c.Request().Header.Get("Accept")) {
	case publicAcceptRSS:
		return noContentError(http.StatusNotAcceptable)
	case publicAcceptActivityPub:
		return s.publicAccountFollowCollectionActivityPub(c, followers)
	}
	if err := s.requirePublicAccountAuthenticationIfLimited(c); err != nil {
		return err
	}
	if err := s.requirePublicAccountVisible(c, username, false); err != nil {
		return err
	}
	s.setPublicAccountLinkHeader(c, username)
	publicHTMLCacheIfUnauthenticated(c, 15, 3600)
	return s.webApp(c)
}

func (s *Server) publicAccountFollowCollectionActivityPub(c *echo.Context, followers bool) error {
	s.activityPubAccountVary(c)
	if err := s.requireActivityPubSignatureIfAuthorized(c); err != nil {
		return err
	}
	return s.activityPubFollowCollection(c, followers)
}

func (s *Server) publicAccountTagged(c *echo.Context) error {
	username, usernameRSS := accountNameFromPublicPath(publicShortAccountParam(c, "username"))
	tag, rss := tagNameFromPublicPath(publicShortAccountParam(c, "tag"))
	if publicAccountPathIsRemote(username) {
		return s.webApp(c)
	}
	tagJSON, jsonFormat := publicPathWithoutFormat(tag, "json")
	if jsonFormat {
		tag = tagJSON
	}
	if jsonFormat || publicRequestHasFormat(c, "json") {
		return s.publicAccountActivityPub(c, username)
	}
	if usernameRSS || rss || publicRequestHasFormat(c, "rss") {
		return s.publicAccountRSS(c, username, accountRSSOptions{Tag: tag})
	}
	switch publicRSSActivityPubAcceptFormat(c.Request().Header.Get("Accept")) {
	case publicAcceptRSS:
		return s.publicAccountRSS(c, username, accountRSSOptions{Tag: tag})
	case publicAcceptActivityPub:
		return s.publicAccountActivityPub(c, username)
	}
	if err := s.requirePublicAccountAuthenticationIfLimited(c); err != nil {
		return err
	}
	if err := s.requirePublicAccountVisible(c, username, false); err != nil {
		return err
	}
	s.setPublicAccountLinkHeader(c, username)
	publicHTMLCacheIfUnauthenticated(c, 15, 3600)
	return s.webApp(c)
}

func (s *Server) publicAccountMaybeRSS(c *echo.Context, opts accountRSSOptions) error {
	username, rss := accountNameFromPublicPath(publicShortAccountParam(c, "username"))
	if rss {
		if publicAccountPathIsRemote(username) {
			return s.webApp(c)
		}
		return s.publicAccountRSS(c, username, opts)
	}
	if usernameJSON, ok := publicPathWithoutFormat(username, "json"); ok {
		if publicAccountPathIsRemote(usernameJSON) {
			return s.webApp(c)
		}
		return s.publicAccountActivityPub(c, usernameJSON)
	}
	if publicAccountPathIsRemote(username) {
		return s.webApp(c)
	}
	if publicRequestHasFormat(c, "json") {
		return s.publicAccountActivityPub(c, username)
	}
	acceptFormat := publicRSSActivityPubAcceptFormat(c.Request().Header.Get("Accept"))
	if acceptFormat == publicAcceptActivityPub {
		return s.publicAccountActivityPub(c, username)
	}
	if publicRequestHasFormat(c, "rss") || acceptFormat == publicAcceptRSS {
		return s.publicAccountRSS(c, username, opts)
	}
	if err := s.requirePublicAccountAuthenticationIfLimited(c); err != nil {
		return err
	}
	if err := s.requirePublicAccountVisible(c, username, false); err != nil {
		return err
	}
	s.setPublicAccountLinkHeader(c, username)
	publicHTMLCacheIfUnauthenticated(c, 15, 3600)
	return s.webApp(c)
}

func accountNameFromPublicPath(username string) (string, bool) {
	if strings.HasSuffix(strings.ToLower(username), ".rss") {
		return strings.TrimSuffix(username[:len(username)-4], "."), true
	}
	return username, false
}

func publicAccountPathIsRemote(username string) bool {
	return strings.ContainsAny(username, "@.")
}

func publicShortAccountFormatPathIsRemote(c *echo.Context) bool {
	if c.Param("format") == "" {
		return false
	}
	segment := strings.TrimPrefix(c.Request().URL.EscapedPath(), "/@")
	if index := strings.IndexByte(segment, '/'); index >= 0 {
		segment = segment[:index]
	}
	segment, _ = url.PathUnescape(segment)
	username, _ := accountNameFromPublicPath(segment)
	if value, ok := publicPathWithoutFormat(username, "json"); ok {
		username = value
	}
	return publicAccountPathIsRemote(username)
}

func (s *Server) publicAccountRSS(c *echo.Context, username string, opts accountRSSOptions) error {
	if err := s.requirePublicAccountAuthenticationIfLimited(c); err != nil {
		return err
	}
	account, err := s.findAccountByAcct(username)
	if err != nil || s.publicAccountHidden(account) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	statuses, err := s.publicAccountRSSStatuses(*account, opts, publicRSSLimit(c))
	if err != nil {
		return err
	}
	if err := s.hydrateStatusesCustomEmojis(statuses); err != nil {
		return err
	}
	body, err := s.renderAccountRSS(*account, opts, statuses)
	if err != nil {
		return err
	}
	setPublicRSSCache(c, 60)
	return c.Blob(http.StatusOK, "application/rss+xml; charset=utf-8", body)
}

func (s *Server) requirePublicAccountAuthenticationIfLimited(c *echo.Context) error {
	if s == nil || !s.cfg.LimitedFederationMode {
		return nil
	}
	if _, _, err := s.currentUser(c); err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	return nil
}

func setPublicRSSCache(c *echo.Context, maxAgeSeconds int) {
	c.Response().Header().Set("Cache-Control", "max-age="+strconv.Itoa(maxAgeSeconds)+", public")
}

func publicRSSLimit(c *echo.Context) int {
	values, ok := c.Request().URL.Query()["limit"]
	if !ok {
		return 20
	}
	raw := ""
	if len(values) > 0 {
		raw = values[0]
	}
	if strings.TrimSpace(raw) == "" {
		return 20
	}
	value := rubyToI(raw)
	if value > 200 {
		return 200
	}
	if value < 0 {
		return 0
	}
	return value
}

func (s *Server) publicAccountActivityPub(c *echo.Context, username string) error {
	s.activityPubAccountVary(c)
	signedAccount, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	if err := s.requirePublicAccountVisible(c, username, true); err != nil {
		return err
	}
	account, err := s.findAccountByAcct(username)
	if err != nil || !account.Local() {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.hydrateAccountCustomEmojis(account); err != nil {
		return err
	}
	if err := s.hydrateActivityPubActorTags(account); err != nil {
		return err
	}
	return activityJSONWithCachePrivacy(c, activityPubActorObject(s, *account), 180, activityPubActorPublicCache(s, signedAccount))
}

func (s *Server) setPublicAccountLinkHeader(c *echo.Context, username string) {
	account, err := s.findAccountByAcct(username)
	if err != nil || !account.Local() || s.publicAccountHidden(account) {
		return
	}
	c.Response().Header().Set("Link", publicAccountLinkHeader(s.cfg, *account))
}

func (s *Server) requirePublicAccountVisible(c *echo.Context, username string, skipTemporarySuspension bool) error {
	if s.db == nil {
		return nil
	}
	account, err := s.findAccountByAcct(username)
	if err != nil || !account.Local() || accountHiddenFromAccountsShow(account) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !account.SuspendedAt.Valid {
		return nil
	}
	permanentlySuspended, err := s.accountSuspendedPermanently(account)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "max-age=180, public")
	if permanentlySuspended {
		return c.NoContent(http.StatusGone)
	}
	if skipTemporarySuspension {
		return nil
	}
	return c.NoContent(http.StatusForbidden)
}

func (s *Server) publicAccountHidden(account *models.Account) bool {
	if account == nil {
		return true
	}
	if accountHiddenFromAccountsShow(account) {
		return true
	}
	return account.SuspendedAt.Valid
}

func publicAccountLinkHeader(cfg config.Config, account models.Account) string {
	resource := "acct:" + account.Username + "@" + cfg.LocalDomain
	webfingerURL := cfg.BaseURL() + "/.well-known/webfinger?resource=" + url.QueryEscape(resource)
	actorURL := cfg.BaseURL() + "/users/" + url.PathEscape(account.Username)
	return "<" + webfingerURL + `>; rel="lrdd"; type="application/jrd+json", <` + actorURL + `>; rel="alternate"; type="application/activity+json"`
}

func (s *Server) publicAccountRSSStatuses(account models.Account, opts accountRSSOptions, limitValue int) ([]models.Status, error) {
	if s.db == nil {
		return []models.Status{}, nil
	}
	query := s.statusQuery().
		Where("statuses.account_id = ? AND statuses.deleted_at IS NULL", account.ID).
		Where("statuses.visibility IN ?", []int{0, 1}).
		Where("statuses.reblog_of_id IS NULL")
	if !opts.WithReplies {
		query = query.Where("(statuses.reply = false OR statuses.in_reply_to_account_id = statuses.account_id)")
	}
	if opts.OnlyMedia {
		query = query.Where(`EXISTS (
			SELECT 1 FROM media_attachments account_rss_media
			WHERE account_rss_media.status_id = statuses.id
			  AND account_rss_media.account_id = ?
		)`, account.ID)
	}
	if tag, ok := accountStatusTagQueryValue(opts.Tag); ok {
		query = query.Where(statusHasTagSQL(), tag)
	}
	var statuses []models.Status
	err := query.Order("statuses.id DESC").Limit(limitValue).Find(&statuses).Error
	return statuses, err
}

func (s *Server) renderAccountRSS(account models.Account, opts accountRSSOptions, statuses []models.Status) ([]byte, error) {
	title := accountRSSDisplayName(account)
	link := s.cfg.BaseURL() + accountWebPath(account)
	if opts.Tag != "" {
		link += "/tagged/" + url.PathEscape(strings.TrimPrefix(opts.Tag, "#"))
	}
	acct := accountRSSLocalUsernameAndDomain(s.cfg, account)
	channel := rssChannel{
		Title:       title,
		Description: webT(s.cfg.Locale(), "rss.descriptions.account", map[string]string{"acct": acct}),
		Link:        link,
		Generator:   rssGenerator(s.cfg),
		Items:       make([]rssItem, 0, len(statuses)),
	}
	if avatar := accountRSSAvatarURL(s.cfg, account); avatar != "" {
		channel.Image = &rssImage{URL: avatar, Title: title, Link: link}
		channel.Icon = avatar
	}
	if len(statuses) > 0 {
		channel.LastBuildDate = statuses[0].CreatedAt.Format(rssTimeFormat)
	}
	for _, status := range statuses {
		statusURL := statusRSSURL(s.cfg, status)
		channel.Items = append(channel.Items, rssItem{
			Link:          statusURL,
			GUID:          rssGUID{IsPermaLink: "true", Value: statusURL},
			PubDate:       status.CreatedAt.Format(rssTimeFormat),
			Description:   statusRSSDescriptionWithConfig(s.cfg, status),
			Enclosure:     rssAudioEnclosure(s.cfg, status),
			MediaContents: rssMediaContents(s.cfg, status),
			Categories:    statusRSSTagNames(status.Tags),
		})
	}
	return renderRSS(channel)
}

func accountRSSDisplayName(account models.Account) string {
	if strings.TrimSpace(account.DisplayName) != "" {
		return account.DisplayName
	}
	return "@" + account.Acct()
}

func accountRSSLocalUsernameAndDomain(cfg config.Config, account models.Account) string {
	if account.Local() {
		return account.Username + "@" + firstNonEmpty(cfg.LocalDomain, cfg.WebDomain)
	}
	return account.Acct()
}

func accountRSSAvatarURL(cfg config.Config, account models.Account) string {
	if account.AvatarFileName.Valid && account.AvatarFileName.String != "" {
		return cfg.SystemAssetURL("accounts/avatars/" + strings.ReplaceAll(mediaPaperclipIDPartition(account.ID), "\\", "/") + "/original/" + url.PathEscape(account.AvatarFileName.String))
	}
	return cfg.BaseURL() + "/avatars/original/missing.png"
}
