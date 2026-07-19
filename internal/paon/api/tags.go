package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var validTagName = regexp.MustCompile(`^[\pL\pN_·・‌]+$`)

const rssTimeFormat = time.RFC1123Z

type trendTagRow struct {
	ID          int64
	Name        string
	DisplayName sql.NullString
	Uses        int64
	Accounts    int64
}

type publicTagRSSOptions struct {
	Local bool
}

func (s *Server) showTag(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	tag, err := s.findOrBuildTag(c.Param("name"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	following := s.tagFollowing(c, tag.ID)
	return c.JSON(http.StatusOK, serializer.TagDetailFromModel(s.cfg, *tag, following))
}

func (s *Server) publicTag(c *echo.Context) error {
	s.publicTagVary(c)
	name := c.Param("name")
	unsupportedFormat := false
	routeJSON := false
	routeRSS := false
	if format := strings.ToLower(c.Param("format")); format != "" {
		switch format {
		case "json":
			routeJSON = true
		case "rss":
			routeRSS = true
		case "html":
		default:
			unsupportedFormat = true
		}
	} else if base, format, ok := publicPathFormat(name); ok {
		name = base
		switch format {
		case "json":
			routeJSON = true
		case "rss":
			routeRSS = true
		case "html":
		default:
			unsupportedFormat = true
		}
	}
	name, rss := tagNameFromPublicPath(name)
	nameJSON, jsonFormat := publicPathWithoutFormat(name, "json")
	jsonFormat = jsonFormat || routeJSON
	if jsonFormat && !routeJSON {
		name = nameJSON
	}
	rss = rss || routeRSS || publicRequestHasFormat(c, "rss")
	acceptFormat := publicRSSActivityPubAcceptFormat(c.Request().Header.Get("Accept"))
	activityPubRequest := jsonFormat || acceptFormat == publicAcceptActivityPub
	if activityPubRequest {
		if err := s.requireActivityPubSignatureIfAuthorized(c); err != nil {
			return err
		}
	}
	if err := s.requirePublicTagAuthenticationIfLimited(c, activityPubRequest); err != nil {
		return err
	}
	tag, err := s.findPublicTag(name)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if unsupportedFormat {
		return noContentError(http.StatusNotAcceptable)
	}
	if rss || (!jsonFormat && acceptFormat == publicAcceptRSS) {
		return s.publicTagRSS(c, *tag, publicTagRSSOptionsFromRequest(c))
	}
	if activityPubRequest {
		return activityJSONWithCachePrivacy(c, activityPubTagCollection(s.cfg, *tag), 180, activityPubPublicFetchCache(s))
	}
	publicHTMLCacheIfUnauthenticated(c, 15, 3600)
	return s.webApp(c)
}

func (s *Server) publicTagVary(c *echo.Context) {
	appendVaryHeader(c, "Accept")
	appendVaryHeader(c, "Accept-Language")
	appendVaryHeader(c, "Cookie")
	if s != nil && s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
}

func (s *Server) requirePublicTagAuthenticationIfLimited(c *echo.Context, activityPubRequest bool) error {
	if s == nil || !s.cfg.LimitedFederationMode {
		return nil
	}
	if _, _, err := s.currentUser(c); err != nil {
		if activityPubRequest {
			return apiError(c, http.StatusUnauthorized, "You need to login or sign up before continuing.")
		}
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	return nil
}

func tagNameFromPublicPath(name string) (string, bool) {
	if strings.HasSuffix(strings.ToLower(name), ".rss") {
		return strings.TrimSuffix(name[:len(name)-4], "."), true
	}
	return name, false
}

func acceptsRSS(header string) bool {
	for _, part := range strings.Split(header, ",") {
		mediaType, q, _ := parseAcceptEntry(part)
		if q > 0 && mediaType == "application/rss+xml" {
			return true
		}
	}
	return false
}

type publicAcceptFormat int

const (
	publicAcceptNone publicAcceptFormat = iota
	publicAcceptHTML
	publicAcceptRSS
	publicAcceptActivityPub
	publicAcceptUnsupported
)

func publicRSSActivityPubAcceptFormat(header string) publicAcceptFormat {
	best := publicAcceptNone
	bestQ := -1.0
	bestOrder := len(header)
	for order, part := range strings.Split(header, ",") {
		mediaType, q, params := parseAcceptEntry(part)
		if q <= 0 {
			continue
		}
		candidate := publicAcceptNone
		switch {
		case mediaType == "application/rss+xml":
			candidate = publicAcceptRSS
		case activityPubAcceptMediaType(mediaType, params):
			candidate = publicAcceptActivityPub
		case mediaType == "text/html" || mediaType == "application/xhtml+xml":
			candidate = publicAcceptHTML
		}
		if candidate == publicAcceptNone {
			continue
		}
		if q > bestQ || (q == bestQ && order < bestOrder) {
			best = candidate
			bestQ = q
			bestOrder = order
		}
	}
	return best
}

func publicHTMLActivityPubAcceptFormat(header string) publicAcceptFormat {
	best := publicAcceptNone
	bestQ := -1.0
	bestOrder := len(header)
	for order, part := range strings.Split(header, ",") {
		mediaType, q, params := parseAcceptEntry(part)
		if q <= 0 {
			continue
		}
		candidate := publicAcceptNone
		switch {
		case activityPubAcceptMediaType(mediaType, params):
			candidate = publicAcceptActivityPub
		case mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == "*/*":
			candidate = publicAcceptHTML
		case mediaType == "application/rss+xml" || mediaType == "application/xml" || mediaType == "text/xml" || mediaType == "application/atom+xml" || mediaType == "application/xrd+xml":
			candidate = publicAcceptUnsupported
		}
		if candidate == publicAcceptNone {
			continue
		}
		if q > bestQ || (q == bestQ && order < bestOrder) {
			best = candidate
			bestQ = q
			bestOrder = order
		}
	}
	return best
}

func publicTagRSSOptionsFromRequest(c *echo.Context) publicTagRSSOptions {
	return publicTagRSSOptions{Local: truthy(c.QueryParam("local"))}
}

func publicTagRSSLimit(c *echo.Context) int {
	return publicRSSLimit(c)
}

func (s *Server) publicTagRSS(c *echo.Context, tag models.Tag, opts publicTagRSSOptions) error {
	statuses, err := s.publicTagRSSStatuses(tag, publicTagRSSLimit(c), opts)
	if err != nil {
		return err
	}
	if err := s.hydrateStatusesCustomEmojis(statuses); err != nil {
		return err
	}
	body, err := s.renderTagRSS(tag, statuses)
	if err != nil {
		return err
	}
	setPublicRSSCache(c, 0)
	return c.Blob(http.StatusOK, "application/rss+xml; charset=utf-8", body)
}

func (s *Server) publicTagRSSStatuses(tag models.Tag, limitValue int, opts publicTagRSSOptions) ([]models.Status, error) {
	if s.db == nil || tag.ID == 0 {
		return []models.Status{}, nil
	}
	var statuses []models.Status
	query := s.statusQuery().
		Joins("JOIN statuses_tags tag_rss_statuses_tags ON tag_rss_statuses_tags.status_id = statuses.id").
		Joins("JOIN accounts tag_rss_accounts ON tag_rss_accounts.id = statuses.account_id").
		Where("tag_rss_statuses_tags.tag_id = ?", tag.ID).
		Where("statuses.deleted_at IS NULL AND statuses.visibility = 0").
		Where("tag_rss_accounts.suspended_at IS NULL AND tag_rss_accounts.silenced_at IS NULL")
	if opts.Local {
		query = query.Where("(statuses.local = TRUE OR statuses.uri IS NULL)")
	}
	err := query.
		Order("statuses.id DESC").
		Limit(limitValue).
		Find(&statuses).Error
	return statuses, err
}

func (s *Server) renderTagRSS(tag models.Tag, statuses []models.Status) ([]byte, error) {
	title := "#" + tagDisplayName(tag)
	channel := rssChannel{
		Title:       title,
		Description: webT(s.cfg.Locale(), "rss.descriptions.tag", map[string]string{"hashtag": tagDisplayName(tag)}),
		Link:        s.cfg.BaseURL() + "/tags/" + url.PathEscape(tag.Name),
		Generator:   rssGenerator(s.cfg),
		Items:       make([]rssItem, 0, len(statuses)),
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
			Categories:    statusRSSTagNames(status.Tags),
			Enclosure:     rssAudioEnclosure(s.cfg, status),
			MediaContents: rssMediaContents(s.cfg, status),
		})
	}
	return renderRSS(channel)
}

func renderRSS(channel rssChannel) ([]byte, error) {
	feed := rssFeed{
		Version:       "2.0",
		XMLNSWebfeeds: "http://webfeeds.org/rss/1.0",
		XMLNSMedia:    "http://search.yahoo.com/mrss/",
		Channel:       channel,
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	encoder := xml.NewEncoder(&buf)
	encoder.Indent("", "  ")
	if err := encoder.Encode(feed); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func rssGenerator(cfg config.Config) string {
	return "Mastodon v" + firstNonEmpty(cfg.MastodonVersion, config.DefaultMastodonVersion)
}

type rssFeed struct {
	XMLName       xml.Name   `xml:"rss"`
	Version       string     `xml:"version,attr"`
	XMLNSWebfeeds string     `xml:"xmlns:webfeeds,attr,omitempty"`
	XMLNSMedia    string     `xml:"xmlns:media,attr,omitempty"`
	Channel       rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Description   string    `xml:"description"`
	Link          string    `xml:"link"`
	LastBuildDate string    `xml:"lastBuildDate,omitempty"`
	Generator     string    `xml:"generator"`
	Image         *rssImage `xml:"image,omitempty"`
	Icon          string    `xml:"webfeeds:icon,omitempty"`
	Items         []rssItem `xml:"item"`
}

type rssImage struct {
	URL   string `xml:"url"`
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type rssItem struct {
	GUID          rssGUID           `xml:"guid"`
	Link          string            `xml:"link"`
	PubDate       string            `xml:"pubDate"`
	Description   string            `xml:"description"`
	Enclosure     *rssEnclosure     `xml:"enclosure,omitempty"`
	MediaContents []rssMediaContent `xml:"media:content,omitempty"`
	Categories    []string          `xml:"category,omitempty"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr,omitempty"`
	Type   string `xml:"type,attr,omitempty"`
}

type rssMediaContent struct {
	URL         string               `xml:"url,attr"`
	Type        string               `xml:"type,attr,omitempty"`
	FileSize    int64                `xml:"fileSize,attr,omitempty"`
	Medium      string               `xml:"medium,attr,omitempty"`
	Rating      *rssMediaRating      `xml:"media:rating,omitempty"`
	Description *rssMediaDescription `xml:"media:description,omitempty"`
	Thumbnail   *rssMediaThumbnail   `xml:"media:thumbnail,omitempty"`
}

type rssMediaRating struct {
	Scheme string `xml:"scheme,attr,omitempty"`
	Value  string `xml:",chardata"`
}

type rssMediaDescription struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

type rssMediaThumbnail struct {
	URL string `xml:"url,attr"`
}

func statusRSSURL(cfg config.Config, status models.Status) string {
	if status.URL.Valid && status.URL.String != "" {
		return status.URL.String
	}
	return cfg.BaseURL() + statusWebPath(status)
}

func statusRSSDescription(status models.Status) string {
	return statusRSSDescriptionWithConfig(config.Config{}, status)
}

const rssCustomEmojiStyle = "width: 1.1em; height: 1.1em; object-fit: contain; vertical-align: middle; margin: -.2ex .15em .2ex"

func statusRSSDescriptionWithConfig(cfg config.Config, status models.Status) string {
	var parts []string
	if strings.TrimSpace(status.SpoilerText) != "" {
		parts = append(parts, `<p><strong>`+html.EscapeString(rssContentWarningLabel(status.Language.String))+`</strong> `+html.EscapeString(status.SpoilerText)+`</p><hr>`)
	}
	parts = append(parts, serializer.StatusContentHTML(cfg, status))
	if poll := statusRSSPollOptions(status.Poll); poll != "" {
		parts = append(parts, poll)
	}
	description := strings.Join(nonEmptyRSSDescriptionParts(parts), "")
	return applyRSSCustomEmojis(cfg, description, status.CustomEmojis)
}

func nonEmptyRSSDescriptionParts(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func applyRSSCustomEmojis(cfg config.Config, content string, emojis []models.CustomEmoji) string {
	if content == "" || len(emojis) == 0 {
		return content
	}
	emojiMap := make(map[string]models.CustomEmoji, len(emojis))
	for _, emoji := range emojis {
		if emoji.Shortcode != "" {
			emojiMap[emoji.Shortcode] = emoji
		}
	}
	if len(emojiMap) == 0 {
		return content
	}
	return applyCustomEmojisToContentWithTag(content, emojiMap, func(emoji models.CustomEmoji) string {
		return statusEmbedCustomEmojiURLWithConfig(cfg, emoji, "static")
	}, rssCustomEmojiImgTag)
}

func applyCustomEmojisToContentWithTag(content string, emojis map[string]models.CustomEmoji, emojiImgSrc func(models.CustomEmoji) string, emojiImgTag func(models.CustomEmoji, string, func(models.CustomEmoji) string) string) string {
	if content == "" || len(emojis) == 0 || emojiImgSrc == nil || emojiImgTag == nil {
		return content
	}
	return applyCustomEmojisToContentWithReplacer(content, emojis, func(text string) string {
		return emojiReplacementHTMLWithTagBuilder(text, emojis, emojiImgSrc, emojiImgTag)
	})
}

func rssCustomEmojiImgTag(emoji models.CustomEmoji, shortcode string, imgSrc func(models.CustomEmoji) string) string {
	src := imgSrc(emoji)
	if src == "" {
		return ":" + shortcode + ":"
	}
	return `<img draggable="false" class="emojione" alt=":` + html.EscapeString(shortcode) + `:" title=":` + html.EscapeString(shortcode) + `:" src="` + html.EscapeString(src) + `" style="` + html.EscapeString(rssCustomEmojiStyle) + `" />`
}

func rssContentWarningLabel(language string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "ja") {
		return "閲覧注意:"
	}
	return "Content warning:"
}

func statusRSSPollOptions(poll *models.Poll) string {
	if poll == nil || len(poll.Options) == 0 {
		return ""
	}
	tagName := "radio"
	if poll.Multiple {
		tagName = "checkbox"
	}
	options := make([]string, 0, len(poll.Options))
	for _, option := range poll.Options {
		options = append(options, "<"+tagName+` disabled="disabled">`+html.EscapeString(option)+"</"+tagName+">")
	}
	return "<p>" + strings.Join(options, "<br>") + "</p>"
}

func statusRSSTagNames(tags []models.Tag) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		out = append(out, tagDisplayName(tag))
	}
	return out
}

func rssAudioEnclosure(cfg config.Config, status models.Status) *rssEnclosure {
	attachments := snapshotMediaAttachments(status)
	if len(attachments) == 0 || attachments[0].Type != 4 {
		return nil
	}
	src := rssMediaURL(cfg, attachments[0])
	if src == "" {
		return nil
	}
	return &rssEnclosure{
		URL:    src,
		Length: attachments[0].FileFileSize.Int64,
		Type:   attachments[0].FileContentType.String,
	}
}

func rssMediaContents(cfg config.Config, status models.Status) []rssMediaContent {
	attachments := snapshotMediaAttachments(status)
	out := make([]rssMediaContent, 0, len(attachments))
	rating := "nonadult"
	if status.Sensitive {
		rating = "adult"
	}
	for _, attachment := range attachments {
		src := rssMediaURL(cfg, attachment)
		if src == "" {
			continue
		}
		item := rssMediaContent{
			URL:      src,
			Type:     attachment.FileContentType.String,
			FileSize: attachment.FileFileSize.Int64,
			Medium:   rssMediaMedium(attachment),
			Rating:   &rssMediaRating{Scheme: "urn:simple", Value: rating},
		}
		if description := strings.TrimSpace(attachment.Description.String); description != "" {
			item.Description = &rssMediaDescription{Type: "plain", Value: description}
		}
		if thumbnail := rssMediaThumbnailURL(cfg, attachment); thumbnail != "" {
			item.Thumbnail = &rssMediaThumbnail{URL: thumbnail}
		}
		out = append(out, item)
	}
	return out
}

func rssMediaURL(cfg config.Config, attachment models.MediaAttachment) string {
	if strings.TrimSpace(attachment.RemoteURL) != "" {
		return attachment.RemoteURL
	}
	if attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != "" {
		return cfg.SystemAssetURL("media_attachments/files/" + strings.ReplaceAll(mediaPaperclipIDPartition(attachment.ID), string(filepath.Separator), "/") + "/original/" + url.PathEscape(attachment.FileFileName.String))
	}
	return ""
}

func rssMediaThumbnailURL(cfg config.Config, attachment models.MediaAttachment) string {
	if attachment.ThumbnailRemoteURL.Valid && strings.TrimSpace(attachment.ThumbnailRemoteURL.String) != "" {
		return attachment.ThumbnailRemoteURL.String
	}
	if attachment.ThumbnailFileName.Valid && strings.TrimSpace(attachment.ThumbnailFileName.String) != "" {
		return rssPaperclipURL(cfg, "media_attachments/thumbnails/"+strings.ReplaceAll(mediaPaperclipIDPartition(attachment.ID), string(filepath.Separator), "/")+"/original/"+url.PathEscape(attachment.ThumbnailFileName.String))
	}
	return ""
}

func rssPaperclipURL(cfg config.Config, assetPath string) string {
	assetPath = strings.TrimLeft(strings.TrimSpace(assetPath), "/")
	if strings.TrimSpace(cfg.StorageHost) != "" || strings.HasPrefix(strings.TrimSpace(cfg.PaperclipRootURL), "http://") || strings.HasPrefix(strings.TrimSpace(cfg.PaperclipRootURL), "https://") {
		return cfg.SystemAssetURL(assetPath)
	}
	root := strings.TrimRight(strings.TrimSpace(cfg.PaperclipRootURL), "/")
	if root == "" && !cfg.PaperclipRootURLSet {
		root = "/system"
	}
	root = strings.Trim(root, "/")
	if root == "" {
		return "/" + assetPath
	}
	return "/" + root + "/" + assetPath
}

func rssMediaMedium(attachment models.MediaAttachment) string {
	if attachment.Type == 1 {
		return "image"
	}
	switch attachment.Type {
	case 0:
		return "image"
	case 2:
		return "video"
	case 4:
		return "audio"
	default:
		return "document"
	}
}

func (s *Server) followTag(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:follows")
	if err != nil {
		return err
	}
	setFollowsFamilyRateLimitHeaders(c, railsFollowsFamilyLimit-1)
	tag, err := s.findOrCreateTag(c.Param("name"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	now := time.Now().UTC()
	rateLimitRecorded, err := s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyFollows, now)
	if err != nil {
		return err
	}
	follow := models.TagFollow{TagID: tag.ID, AccountID: account.ID, CreatedAt: now, UpdatedAt: now}
	res := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
	if res.Error != nil {
		if rateLimitRecorded {
			s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyFollows, now)
		}
		return res.Error
	}
	if rateLimitRecorded && res.RowsAffected == 0 {
		s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyFollows, now)
		setFollowsFamilyRateLimitHeaders(c, railsFollowsFamilyLimit)
	}
	following := true
	return c.JSON(http.StatusOK, serializer.TagDetailFromModel(s.cfg, *tag, &following))
}

func (s *Server) unfollowTag(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:follows")
	if err != nil {
		return err
	}
	tag, err := s.findOrBuildTag(c.Param("name"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if tag.ID != 0 {
		if err := s.db.Where("account_id = ? AND tag_id = ?", account.ID, tag.ID).Delete(&models.TagFollow{}).Error; err != nil {
			return err
		}
		s.unmergeTagFromHomeBestEffort(c.Request().Context(), tag.ID, account.ID)
	}
	following := false
	return c.JSON(http.StatusOK, serializer.TagDetailFromModel(s.cfg, *tag, &following))
}

func (s *Server) unmergeTagFromHomeBestEffort(ctx context.Context, tagID int64, accountID int64) {
	if s == nil || s.db == nil || tagID == 0 || accountID == 0 {
		return
	}
	if s.enqueueTagUnmergeTask(tagID, accountID) {
		return
	}
	_ = ctx
	go func() {
		workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.unmergeTagFromHome(workerCtx, tagID, accountID)
	}()
}

func (s *Server) unmergeTagFromHome(ctx context.Context, tagID int64, accountID int64) error {
	value, err := s.redisCommand(ctx, "ZRANGE", homeFeedRedisKey(redisConfig(s.cfg).prefix, accountID), "0", "-1")
	if err != nil {
		return nil
	}
	timelineIDs, ok := redisStringArray(value)
	if !ok || len(timelineIDs) == 0 {
		return nil
	}
	var statuses []models.Status
	err = s.db.WithContext(ctx).
		Select("statuses.id, statuses.reblog_of_id, statuses.account_id").
		Joins("JOIN statuses_tags removed_tag ON removed_tag.status_id = statuses.id AND removed_tag.tag_id = ?", tagID).
		Where("statuses.id IN ?", timelineIDs).
		Where("statuses.account_id <> ?", accountID).
		Where(`NOT EXISTS (
			SELECT 1 FROM follows
			WHERE follows.account_id = ?
			  AND follows.target_account_id = statuses.account_id
		)`, accountID).
		Where(`NOT EXISTS (
			SELECT 1 FROM statuses_tags remaining_statuses_tags
			JOIN tag_follows remaining_tag_follows
			  ON remaining_tag_follows.tag_id = remaining_statuses_tags.tag_id
			 AND remaining_tag_follows.account_id = ?
			WHERE remaining_statuses_tags.status_id = statuses.id
		)`, accountID).
		Find(&statuses).Error
	if err != nil {
		return err
	}
	aggregateReblogs := s.accountAggregatesReblogs(ctx, accountID)
	for _, status := range statuses {
		if _, err := s.removeStatusFromFeedContext(ctx, "home", accountID, status, aggregateReblogs); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) accountAggregatesReblogs(ctx context.Context, accountID int64) bool {
	if s == nil || s.db == nil || accountID == 0 {
		return true
	}
	var row feedTargetSettingsRow
	if err := s.db.WithContext(ctx).Table("users").Select("users.account_id AS id, users.settings").Where("users.account_id = ?", accountID).Limit(1).Scan(&row).Error; err != nil || row.ID == 0 {
		return true
	}
	return aggregateReblogsFromSettings(row.Settings)
}

func (s *Server) followedTags(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "read", "read:follows")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	query := s.db.Preload("Tag").Where("account_id = ?", account.ID)
	if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id") {
		query = query.Where("tag_follows.id > ?", minID).Order("tag_follows.id ASC")
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("tag_follows.id < ?", maxID)
		}
	} else {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("tag_follows.id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where("tag_follows.id > ?", sinceID)
		}
		query = query.Order("tag_follows.id DESC")
	}
	limitValue := limit(c, 100, 200)
	var follows []models.TagFollow
	if err := query.Limit(limitValue).Find(&follows).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseTagFollows(follows)
	}
	if len(follows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, follows[0].ID, follows[len(follows)-1].ID, "since_id", len(follows) == limitValue))
	}
	following := true
	out := make([]serializer.TagDetail, 0, len(follows))
	for _, follow := range follows {
		out = append(out, serializer.TagDetailFromModel(s.cfg, follow.Tag, &following))
	}
	return c.JSON(http.StatusOK, out)
}

func reverseTagFollows(rows []models.TagFollow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func (s *Server) trendingTags(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if s.db == nil || !s.trendsEnabled() {
		return c.JSON(http.StatusOK, []any{})
	}
	now := time.Now().UTC()
	offsetValue := offset(c)
	limitValue := limit(c, 10, 20)
	rows, err := s.trendingTagRows(c.Request().Context(), limitValue, offsetValue, now)
	if err != nil {
		return err
	}
	out := make([]serializer.TagDetail, 0, len(rows))
	for _, row := range rows {
		following := s.tagFollowing(c, row.ID)
		out = append(out, serializerTrendingTag(s.cfg, row, following, now))
	}
	if len(out) > 0 {
		setPaginationLinkHeader(c, offsetPaginationLinkWithAllowedParams(c, offsetValue, limitValue, len(out), []string{"limit"}))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) trendingTagRows(ctx context.Context, limitValue int, offsetValue int, now time.Time) ([]trendTagRow, error) {
	if rows, ok, err := s.trendingTagRowsFromRedis(ctx, limitValue, offsetValue, now, true); ok || err != nil {
		return rows, err
	}
	var rows []trendTagRow
	since := now.AddDate(0, 0, -7)
	err := s.db.Model(&models.Tag{}).
		Select("tags.id, tags.name, tags.display_name, COUNT(statuses.id) AS uses, COUNT(DISTINCT statuses.account_id) AS accounts").
		Joins("JOIN statuses_tags ON statuses_tags.tag_id = tags.id").
		Joins("JOIN statuses ON statuses.id = statuses_tags.status_id").
		Where("statuses.deleted_at IS NULL").
		Where("statuses.visibility IN ?", []int{0, 1}).
		Where("statuses.created_at >= ?", since).
		Where("(tags.trendable IS NULL OR tags.trendable = true)").
		Group("tags.id, tags.name, tags.display_name").
		Order("uses DESC").
		Order("accounts DESC").
		Order("tags.id ASC").
		Offset(offsetValue).
		Limit(limitValue).
		Scan(&rows).Error
	return rows, err
}

func (s *Server) trendingTagRowsFromRedis(ctx context.Context, limitValue int, offsetValue int, now time.Time, allowed bool) ([]trendTagRow, bool, error) {
	if limitValue <= 0 {
		return []trendTagRow{}, true, nil
	}
	stop := offsetValue + limitValue - 1
	key := s.cfg.RedisNamespace + "trending_tags:all"
	if allowed {
		key = s.cfg.RedisNamespace + "trending_tags:allowed"
	}
	value, err := s.redisCommand(ctx, "ZREVRANGE", key, strconv.Itoa(offsetValue), strconv.Itoa(stop))
	if err != nil {
		return nil, false, nil
	}
	members, ok := redisStringArray(value)
	if !ok || len(members) == 0 {
		return nil, false, nil
	}
	ids := make([]int64, 0, len(members))
	order := map[int64]int{}
	for _, member := range members {
		id, err := strconv.ParseInt(strings.TrimSpace(member), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, exists := order[id]; exists {
			continue
		}
		order[id] = len(ids)
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return []trendTagRow{}, true, nil
	}
	var tags []models.Tag
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&tags).Error; err != nil {
		return nil, true, err
	}
	rows := make([]trendTagRow, len(ids))
	present := make([]bool, len(ids))
	day := truncateMetricTime(now, "day")
	for _, tag := range tags {
		pos, ok := order[tag.ID]
		if !ok {
			continue
		}
		uses, _ := s.adminTagUsesRedisDay(tag.ID, day)
		accounts, _ := s.adminTagAccountsRedisDay(tag.ID, day)
		rows[pos] = trendTagRow{
			ID:          tag.ID,
			Name:        tag.Name,
			DisplayName: tag.DisplayName,
			Uses:        uses,
			Accounts:    accounts,
		}
		present[pos] = true
	}
	out := make([]trendTagRow, 0, len(rows))
	for i, row := range rows {
		if present[i] {
			out = append(out, row)
		}
	}
	return out, true, nil
}

func serializerTrendingTag(cfg config.Config, row trendTagRow, following *bool, now time.Time) serializer.TagDetail {
	tag := models.Tag{ID: row.ID, Name: row.Name, DisplayName: row.DisplayName}
	out := serializer.TagDetailFromModel(cfg, tag, following)
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).Unix()
	out.History = []any{
		map[string]string{
			"day":      strconv.FormatInt(day, 10),
			"uses":     strconv.FormatInt(row.Uses, 10),
			"accounts": strconv.FormatInt(row.Accounts, 10),
		},
	}
	return out
}

func (s *Server) adminTagFromModel(c *echo.Context, tag models.Tag) serializer.AdminTag {
	return serializer.AdminTagFromModelWithHistoryAndTrendableDefault(s.cfg, tag, s.tagHistory(c.Request().Context(), tag.ID, time.Now().UTC()), s.settingBoolValue("trendable_by_default", false))
}

func (s *Server) tagHistory(ctx context.Context, tagID int64, now time.Time) []any {
	out := make([]any, 0, 7)
	for i := 0; i < 7; i++ {
		day := dayStart(now.AddDate(0, 0, -i))
		uses, accounts := s.tagHistoryDay(ctx, tagID, day)
		out = append(out, map[string]string{
			"day":      strconv.FormatInt(day.Unix(), 10),
			"uses":     strconv.FormatInt(uses, 10),
			"accounts": strconv.FormatInt(accounts, 10),
		})
	}
	return out
}

func (s *Server) tagHistoryDay(ctx context.Context, tagID int64, day time.Time) (int64, int64) {
	usesCtx, cancelUses := context.WithTimeout(ctx, 150*time.Millisecond)
	usesValue, usesErr := s.redisCommand(usesCtx, "GET", tagHistoryRedisKey(s.cfg.RedisNamespace, tagID, day, false))
	cancelUses()
	accountsCtx, cancelAccounts := context.WithTimeout(ctx, 150*time.Millisecond)
	accountsValue, accountsErr := s.redisCommand(accountsCtx, "PFCOUNT", tagHistoryRedisKey(s.cfg.RedisNamespace, tagID, day, true))
	cancelAccounts()
	var uses int64
	if usesErr == nil {
		uses = redisInt(usesValue)
	}
	var accounts int64
	if accountsErr == nil {
		accounts = redisInt(accountsValue)
	}
	return uses, accounts
}

func (s *Server) findOrBuildTag(name string) (*models.Tag, error) {
	normalized, display, ok := normalizeTagName(name)
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	if s.db == nil {
		return &models.Tag{Name: normalized, DisplayName: sql.NullString{String: display, Valid: display != "" && display != normalized}}, nil
	}
	var tag models.Tag
	err := s.db.Where("lower(name) = ?", strings.ToLower(normalized)).First(&tag).Error
	if err == nil {
		return &tag, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return &models.Tag{Name: normalized, DisplayName: sql.NullString{String: display, Valid: display != "" && display != normalized}}, nil
}

func (s *Server) findPublicTag(name string) (*models.Tag, error) {
	normalized, display, ok := normalizeTagName(name)
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	if s.db == nil {
		return &models.Tag{Name: normalized, DisplayName: sql.NullString{String: display, Valid: display != "" && display != normalized}}, nil
	}
	var tag models.Tag
	err := s.db.
		Where("lower(name) = ?", strings.ToLower(normalized)).
		Where("(usable IS NULL OR usable = true)").
		First(&tag).Error
	return &tag, err
}

func (s *Server) findOrCreateTag(name string) (*models.Tag, error) {
	tag, err := s.findOrBuildTag(name)
	if err != nil {
		return nil, err
	}
	if tag.ID != 0 {
		return tag, nil
	}
	now := time.Now().UTC()
	tag.CreatedAt = now
	tag.UpdatedAt = now
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(tag).Error; err != nil {
		return nil, err
	}
	if tag.ID == 0 {
		if err := s.db.Where("lower(name) = ?", strings.ToLower(tag.Name)).First(tag).Error; err != nil {
			return nil, err
		}
	}
	return tag, nil
}

func (s *Server) tagFollowing(c *echo.Context, tagID int64) *bool {
	account, _, err := s.currentAccount(c)
	if err != nil || tagID == 0 || s.db == nil {
		return nil
	}
	var count int64
	_ = s.db.Model(&models.TagFollow{}).Where("account_id = ? AND tag_id = ?", account.ID, tagID).Count(&count).Error
	value := count > 0
	return &value
}

func normalizeTagName(raw string) (string, string, bool) {
	decoded := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	decoded = strings.TrimSpace(decoded)
	if !railsValidTagName(decoded) {
		return "", "", false
	}
	normalized := railsNormalizeHashtagName(decoded)
	if !railsValidTagName(normalized) {
		return "", "", false
	}
	display := decoded
	return normalized, display, true
}

func railsValidTagName(value string) bool {
	if value == "" || !validTagName.MatchString(value) {
		return false
	}
	runes := []rune(value)
	if railsTagLastSequence(runes) {
		return true
	}
	return railsTagFirstSequence(runes)
}

func railsTagLastSequence(runes []rune) bool {
	for _, r := range runes {
		if railsTagSeparator(r) && r != '_' {
			return false
		}
	}
	for _, r := range runes {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func railsTagFirstSequence(runes []rune) bool {
	if len(runes) < 3 || !railsTagWord(runes[0]) || !railsTagWord(runes[len(runes)-1]) {
		return false
	}
	for _, r := range runes[1 : len(runes)-1] {
		if unicode.IsLetter(r) || railsTagSeparator(r) {
			return true
		}
	}
	return false
}

func railsTagWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
}

func railsTagSeparator(r rune) bool {
	return r == '_' || r == '·' || r == '・' || r == '\u200c'
}

func railsNormalizeHashtagName(value string) string {
	folded := norm.NFKD.String(norm.NFKC.String(strings.ToLower(value)))
	var out strings.Builder
	for _, r := range folded {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		out.WriteRune(r)
	}
	return norm.NFC.String(out.String())
}

func activityPubTagCollection(cfg config.Config, tag models.Tag) map[string]any {
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       cfg.BaseURL() + "/tags/" + url.PathEscape(tag.Name),
		"type":     "OrderedCollection",
	}
}
