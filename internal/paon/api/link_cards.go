package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	nethtml "golang.org/x/net/html"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const previewCardURLByteLimit = 2692

type fetchedPreviewCard struct {
	card     models.PreviewCard
	imageURL string
}

var previewCardURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)
var previewCardHTMLTitlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var previewCardHTMLMetaPattern = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
var previewCardHTMLAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:.-]+)\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'>]+))`)
var previewCardHTMLTagPattern = regexp.MustCompile(`(?is)<[^>]+>`)

func (s *Server) fetchLinkCardForStatusAsync(statusID int64) {
	s.fetchLinkCardForStatusWithDelay(statusID, 0)
}

func (s *Server) fetchLinkCardForStatusDelayed(statusID int64) {
	s.fetchLinkCardForStatusWithDelay(statusID, linkCrawlDelay())
}

func (s *Server) fetchLinkCardForStatusWithDelay(statusID int64, delay time.Duration) {
	if s == nil || s.db == nil || statusID == 0 {
		return
	}
	if s.enqueueLinkCrawlTaskWithDelay(statusID, delay) {
		return
	}
	go func() {
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			<-timer.C
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.fetchLinkCardForStatus(ctx, statusID)
	}()
}

func (s *Server) fetchLinkCardForStatus(ctx context.Context, statusID int64) error {
	if s == nil || s.db == nil || statusID == 0 {
		return nil
	}
	var status models.Status
	if err := s.db.WithContext(ctx).Preload("PreviewCards").Preload("Mentions.Account").Preload("Tags").Where("id = ? AND deleted_at IS NULL", statusID).First(&status).Error; err != nil {
		return nil
	}
	if len(status.PreviewCards) > 0 {
		visibleStatus := statusWithoutHashtagPreviewCards(status)
		if len(visibleStatus.PreviewCards) == len(status.PreviewCards) {
			return nil
		}
		visibleCardIDs := make(map[int64]struct{}, len(visibleStatus.PreviewCards))
		for _, card := range visibleStatus.PreviewCards {
			visibleCardIDs[card.ID] = struct{}{}
		}
		skippedCardIDs := make([]int64, 0, len(status.PreviewCards)-len(visibleStatus.PreviewCards))
		for _, card := range status.PreviewCards {
			if _, visible := visibleCardIDs[card.ID]; !visible {
				skippedCardIDs = append(skippedCardIDs, card.ID)
			}
		}
		if err := s.db.WithContext(ctx).Exec("DELETE FROM preview_cards_statuses WHERE status_id = ? AND preview_card_id IN ?", statusID, skippedCardIDs).Error; err != nil {
			return err
		}
		s.invalidateStatusCache(ctx, statusID)
		status.PreviewCards = visibleStatus.PreviewCards
		if len(status.PreviewCards) > 0 {
			return nil
		}
	}
	rawURL := s.previewCardURLFromStatus(status)
	if rawURL == "" {
		return nil
	}
	acquiredFetch, releaseFetchLock, err := s.acquireActivityPubRedisLock(ctx, "fetch:"+rawURL, 15*time.Minute)
	if err != nil || !acquiredFetch {
		return err
	}
	defer releaseFetchLock()
	card, err := s.findOrFetchPreviewCard(ctx, rawURL)
	if err != nil || card == nil || card.ID == 0 {
		return nil
	}
	if err := s.attachPreviewCardToStatus(ctx, statusID, card.ID); err != nil {
		return err
	}
	s.invalidateStatusCache(ctx, statusID)
	s.recordPreviewCardTrendUseForStatus(ctx, status.AccountID, status.ID, status.Visibility, time.Now().UTC())
	return nil
}

func (s *Server) previewCardURLFromStatus(status models.Status) string {
	if status.Local.Valid && !status.Local.Bool {
		return s.previewCardURLFromRemoteStatus(status)
	}
	for _, candidate := range previewCardURLPattern.FindAllString(status.Text, -1) {
		cleaned := cleanPreviewCardURL(candidate)
		if s.previewCardURLAllowed(cleaned) {
			return cleaned
		}
	}
	return ""
}

func (s *Server) previewCardURLFromRemoteStatus(status models.Status) string {
	mentionURLs := previewCardMentionURLs(s, status.Mentions)
	hashtagNames := previewCardHashtagNames(status.Tags)
	for _, anchor := range previewCardRemoteAnchors(status.Text) {
		if previewCardSkipRemoteAnchor(anchor, mentionURLs, hashtagNames) {
			continue
		}
		cleaned := cleanPreviewCardURL(anchor.attrs["href"])
		if s.previewCardURLAllowed(cleaned) {
			return cleaned
		}
	}
	return ""
}

func statusWithoutHashtagPreviewCards(status models.Status) models.Status {
	if status.Local.Valid && !status.Local.Bool && len(status.PreviewCards) > 0 {
		mentionURLs := previewCardMentionURLs(nil, status.Mentions)
		hashtagNames := previewCardHashtagNames(status.Tags)
		anchors := previewCardRemoteAnchors(status.Text)
		hasOrdinaryLink := false
		for _, anchor := range anchors {
			if !previewCardSkipRemoteAnchor(anchor, mentionURLs, hashtagNames) {
				hasOrdinaryLink = true
				break
			}
		}
		if !hasOrdinaryLink {
			status.PreviewCards = nil
		} else {
			cards := make([]models.PreviewCard, 0, len(status.PreviewCards))
			for _, card := range status.PreviewCards {
				if !previewCardURLMatchesSkippedAnchor(anchors, card.URL, mentionURLs, hashtagNames) {
					cards = append(cards, card)
				}
			}
			status.PreviewCards = cards
		}
	}
	if status.Reblog != nil {
		reblog := statusWithoutHashtagPreviewCards(*status.Reblog)
		status.Reblog = &reblog
	}
	return status
}

func previewCardURLMatchesSkippedAnchor(anchors []previewCardRemoteAnchor, cardURL string, mentionURLs map[string]struct{}, hashtagNames map[string]struct{}) bool {
	cardURL = cleanPreviewCardURL(cardURL)
	if cardURL == "" {
		return false
	}
	for _, anchor := range anchors {
		if cleanPreviewCardURL(anchor.attrs["href"]) == cardURL && previewCardSkipRemoteAnchor(anchor, mentionURLs, hashtagNames) {
			return true
		}
	}
	return false
}

func previewCardMentionURLs(s *Server, mentions []models.Mention) map[string]struct{} {
	out := map[string]struct{}{}
	for _, mention := range mentions {
		if mention.Account.ID == 0 {
			continue
		}
		if s != nil {
			if uri := strings.TrimSpace(activityPubAccountURI(s, mention.Account)); uri != "" {
				out[strings.ToLower(uri)] = struct{}{}
			}
		} else if uri := strings.TrimSpace(mention.Account.URI); uri != "" {
			out[strings.ToLower(uri)] = struct{}{}
		}
		if mention.Account.URL.Valid {
			if uri := strings.TrimSpace(mention.Account.URL.String); uri != "" {
				out[strings.ToLower(uri)] = struct{}{}
			}
		}
	}
	return out
}

type previewCardRemoteAnchor struct {
	attrs map[string]string
	text  string
}

func previewCardRemoteAnchors(content string) []previewCardRemoteAnchor {
	nodes, err := parseHTMLFragment(content)
	if err != nil {
		return nil
	}
	out := make([]previewCardRemoteAnchor, 0)
	var walk func(*nethtml.Node)
	walk = func(node *nethtml.Node) {
		if node.Type == nethtml.ElementNode && node.Data == "a" {
			attrs := make(map[string]string, len(node.Attr))
			for _, attr := range node.Attr {
				attrs[strings.ToLower(attr.Key)] = attr.Val
			}
			out = append(out, previewCardRemoteAnchor{attrs: attrs, text: textContent(node)})
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return out
}

func previewCardHashtagNames(tags []models.Tag) map[string]struct{} {
	out := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if normalized, _, ok := normalizeTagName(tag.Name); ok {
			out[strings.ToLower(normalized)] = struct{}{}
		}
	}
	return out
}

func previewCardSkipRemoteAnchor(anchor previewCardRemoteAnchor, mentionURLs map[string]struct{}, hashtagNames map[string]struct{}) bool {
	href := strings.TrimSpace(anchor.attrs["href"])
	if href == "" {
		return true
	}
	if activityRelContains(anchor.attrs["rel"], "tag") {
		return true
	}
	classNames := strings.Fields(strings.ToLower(anchor.attrs["class"]))
	if stringSliceContains(classNames, "u-url") || stringSliceContains(classNames, "h-card") || stringSliceContains(classNames, "hashtag") {
		return true
	}
	if previewCardAnchorMatchesHashtag(anchor, hashtagNames) {
		return true
	}
	_, mentioned := mentionURLs[strings.ToLower(href)]
	return mentioned
}

func previewCardAnchorMatchesHashtag(anchor previewCardRemoteAnchor, hashtagNames map[string]struct{}) bool {
	text := strings.TrimSpace(anchor.text)
	if !strings.HasPrefix(text, "#") {
		return false
	}
	normalized, _, ok := normalizeTagName(text)
	if !ok {
		return false
	}
	normalized = strings.ToLower(normalized)
	if _, ok := hashtagNames[normalized]; ok {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(anchor.attrs["href"]))
	if err != nil {
		return false
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	for i := 0; i+1 < len(segments); i++ {
		switch strings.ToLower(segments[i]) {
		case "tag", "tags", "hashtag", "hashtags":
			candidate, err := url.PathUnescape(segments[i+1])
			if err != nil {
				continue
			}
			if candidateNormalized, _, ok := normalizeTagName(candidate); ok && strings.EqualFold(candidateNormalized, normalized) {
				return true
			}
		}
	}
	return false
}

func cleanPreviewCardURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ".,;:!?)］】」』〉》")
	raw = strings.TrimLeft(raw, "(［【「『〈《")
	return raw
}

func (s *Server) previewCardURLAllowed(raw string) bool {
	if raw == "" || len([]byte(raw)) > previewCardURLByteLimit {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host != "" && !strings.EqualFold(host, s.cfg.LocalDomain) && !strings.EqualFold(host, s.cfg.WebDomain) && activityFetchHostAllowed(host)
}

func (s *Server) findOrFetchPreviewCard(ctx context.Context, rawURL string) (*models.PreviewCard, error) {
	var existing models.PreviewCard
	err := s.db.WithContext(ctx).Where("url = ?", rawURL).First(&existing).Error
	if err == nil {
		if !previewCardNeedsRefresh(existing, time.Now().UTC()) {
			return &existing, nil
		}
		fetched, ok := s.fetchPreviewCard(ctx, rawURL)
		if !ok {
			return &existing, nil
		}
		return s.storeFetchedPreviewCard(ctx, fetched, &existing)
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	fetched, ok := s.fetchPreviewCard(ctx, rawURL)
	if !ok {
		return nil, nil
	}
	return s.storeFetchedPreviewCard(ctx, fetched, nil)
}

func previewCardNeedsRefresh(card models.PreviewCard, now time.Time) bool {
	if !card.UpdatedAt.IsZero() && !card.UpdatedAt.After(now.Add(-14*24*time.Hour)) {
		return true
	}
	return previewCardMissingImage(card)
}

func previewCardMissingImage(card models.PreviewCard) bool {
	return card.Width > 0 && card.Height > 0 && (!card.ImageFileName.Valid || strings.TrimSpace(card.ImageFileName.String) == "")
}

func (s *Server) storeFetchedPreviewCard(ctx context.Context, fetched fetchedPreviewCard, preferred *models.PreviewCard) (*models.PreviewCard, error) {
	card := fetched.card
	if preferred != nil && preferred.ID != 0 && preferred.URL == card.URL {
		if err := s.db.WithContext(ctx).Model(&models.PreviewCard{}).Where("id = ?", preferred.ID).Updates(previewCardFetchedUpdates(card)).Error; err != nil {
			return nil, err
		}
		if err := s.db.WithContext(ctx).Where("id = ?", preferred.ID).First(preferred).Error; err != nil {
			return nil, err
		}
		s.cachePreviewCardImage(ctx, preferred, fetched.imageURL)
		return preferred, nil
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&card).Error; err != nil {
		return nil, err
	}
	if card.ID != 0 {
		s.cachePreviewCardImage(ctx, &card, fetched.imageURL)
		return &card, nil
	}
	var existing models.PreviewCard
	if err := s.db.WithContext(ctx).Where("url = ?", card.URL).First(&existing).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&models.PreviewCard{}).Where("id = ?", existing.ID).Updates(previewCardFetchedUpdates(card)).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Where("id = ?", existing.ID).First(&existing).Error; err != nil {
		return nil, err
	}
	s.cachePreviewCardImage(ctx, &existing, fetched.imageURL)
	return &existing, nil
}

func previewCardFetchedUpdates(card models.PreviewCard) map[string]any {
	return map[string]any{
		"title":             card.Title,
		"description":       card.Description,
		"type":              card.Type,
		"html":              card.HTML,
		"author_name":       card.AuthorName,
		"author_url":        card.AuthorURL,
		"provider_name":     card.ProviderName,
		"provider_url":      card.ProviderURL,
		"width":             card.Width,
		"height":            card.Height,
		"embed_url":         card.EmbedURL,
		"language":          card.Language,
		"link_type":         card.LinkType,
		"published_at":      card.PublishedAt,
		"image_description": card.ImageDescription,
		"updated_at":        card.UpdatedAt,
	}
}

func (s *Server) fetchPreviewCard(ctx context.Context, rawURL string) (fetchedPreviewCard, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchedPreviewCard{}, false
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", paonUserAgent(s.cfg)+" Bot")
	resp, err := activityHTTPClient.Do(req)
	if err != nil {
		return fetchedPreviewCard{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return fetchedPreviewCard{}, false
	}
	body, err := readActivityResponseBodyWithRailsLimit(resp, "preview-card")
	if err != nil {
		return fetchedPreviewCard{}, false
	}
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if fetched, ok := s.previewCardFromOEmbed(ctx, finalURL, string(body), time.Now().UTC()); ok {
		return fetched, true
	}
	return previewCardFromHTML(finalURL, string(body), time.Now().UTC())
}

func (s *Server) previewCardFromOEmbed(ctx context.Context, rawURL string, body string, now time.Time) (fetchedPreviewCard, bool) {
	endpoint, format := s.discoverRemoteOEmbedEndpointFromHTML(ctx, rawURL, body)
	return previewCardFromOEmbedEndpoint(rawURL, endpoint, format, now)
}

func previewCardFromOEmbed(rawURL string, body string, now time.Time) (fetchedPreviewCard, bool) {
	endpoint, format := discoverRemoteOEmbedEndpointFromHTML(rawURL, body)
	return previewCardFromOEmbedEndpoint(rawURL, endpoint, format, now)
}

func previewCardFromOEmbedEndpoint(rawURL string, endpoint string, format remoteOEmbedFormat, now time.Time) (fetchedPreviewCard, bool) {
	if endpoint == "" {
		return fetchedPreviewCard{}, false
	}
	embedBody, err := fetchRemoteOEmbedBody(endpoint, "")
	if err != nil {
		return fetchedPreviewCard{}, false
	}
	embed, err := parseRemoteOEmbed(embedBody, format)
	if err != nil || !validRemoteOEmbed(embed) {
		return fetchedPreviewCard{}, false
	}
	return previewCardFromOEmbedPayload(rawURL, endpoint, embed, now)
}

func previewCardFromOEmbedPayload(rawURL string, endpoint string, embed map[string]any, now time.Time) (fetchedPreviewCard, bool) {
	embedType := strings.ToLower(strings.TrimSpace(fmt.Sprint(embed["type"])))
	cardType, ok := previewCardTypeFromOEmbed(embedType)
	if !ok {
		return fetchedPreviewCard{}, false
	}
	card := models.PreviewCard{
		URL:          rawURL,
		Title:        truncateString(previewCardOEmbedString(embed["title"]), 512),
		Type:         cardType,
		AuthorName:   truncateString(previewCardOEmbedString(embed["author_name"]), 128),
		AuthorURL:    truncateString(resolvePreviewCardOEmbedURL(endpoint, previewCardOEmbedString(embed["author_url"])), 512),
		ProviderName: truncateString(previewCardOEmbedString(embed["provider_name"]), 128),
		ProviderURL:  truncateString(resolvePreviewCardOEmbedURL(endpoint, previewCardOEmbedString(embed["provider_url"])), 512),
		Width:        previewCardOEmbedInt(embed["width"]),
		Height:       previewCardOEmbedInt(embed["height"]),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	switch embedType {
	case "link":
		if strings.TrimSpace(card.Title) == "" {
			return fetchedPreviewCard{}, false
		}
		card.LinkType = sql.NullInt64{Int64: 1, Valid: true}
	case "photo":
		photoURL := resolvePreviewCardOEmbedURL(endpoint, previewCardOEmbedString(embed["url"]))
		if photoURL == "" {
			return fetchedPreviewCard{}, false
		}
		card.EmbedURL = photoURL
		return fetchedPreviewCard{card: card, imageURL: photoURL}, true
	case "video":
		card.HTML = sanitizeRemoteOEmbedHTML(previewCardOEmbedString(embed["html"]))
		if card.HTML == "" {
			return fetchedPreviewCard{}, false
		}
	}
	imageURL := resolvePreviewCardOEmbedURL(endpoint, previewCardOEmbedString(embed["thumbnail_url"]))
	return fetchedPreviewCard{card: card, imageURL: imageURL}, true
}

func previewCardTypeFromOEmbed(embedType string) (int, bool) {
	switch embedType {
	case "link":
		return 0, true
	case "photo":
		return 1, true
	case "video":
		return 2, true
	default:
		return 0, false
	}
}

func previewCardOEmbedString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func previewCardOEmbedInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func resolvePreviewCardOEmbedURL(baseRaw string, valueRaw string) string {
	if strings.TrimSpace(valueRaw) == "" {
		return ""
	}
	base, err := url.Parse(strings.TrimSpace(baseRaw))
	if err != nil || base.Host == "" {
		return ""
	}
	value, err := url.Parse(strings.TrimSpace(valueRaw))
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(value)
	if base.Scheme == "https" {
		resolved.Scheme = "https"
	}
	if !remoteFetchURLAllowed(resolved.String()) {
		return ""
	}
	return resolved.String()
}

func previewCardFromHTML(rawURL string, body string, now time.Time) (fetchedPreviewCard, bool) {
	meta := previewCardMetaValues(body)
	title := firstNonBlankRaw(meta["og:title"], meta["twitter:title"], previewCardHTMLTitle(body))
	description := firstNonBlankRaw(meta["og:description"], meta["twitter:description"], meta["description"])
	if strings.TrimSpace(title) == "" {
		return fetchedPreviewCard{}, false
	}
	parsed, _ := url.Parse(rawURL)
	providerName := firstNonBlankRaw(meta["og:site_name"], previewCardProviderName(parsed.Hostname()))
	card := models.PreviewCard{
		URL:          rawURL,
		Title:        truncateString(title, 512),
		Description:  truncateString(description, 512),
		Type:         0,
		ProviderName: truncateString(providerName, 128),
		ProviderURL:  previewCardProviderURL(parsed),
		LinkType:     sql.NullInt64{Int64: 1, Valid: true},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if lang := strings.TrimSpace(meta["og:locale"]); lang != "" {
		card.Language = sql.NullString{String: truncateString(lang, 16), Valid: true}
	}
	imageURL := resolvePreviewCardOEmbedURL(rawURL, firstNonEmpty(meta["og:image:secure_url"], meta["og:image"], meta["twitter:image"]))
	if imageURL != "" {
		card.ImageDescription = truncateString(firstNonBlankRaw(meta["og:image:alt"], meta["twitter:image:alt"]), 1500)
	}
	return fetchedPreviewCard{card: card, imageURL: imageURL}, true
}

func previewCardHTMLTitle(body string) string {
	match := previewCardHTMLTitlePattern.FindStringSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return html.UnescapeString(previewCardHTMLTagPattern.ReplaceAllString(match[1], ""))
}

func previewCardMetaValues(body string) map[string]string {
	out := map[string]string{}
	for _, tag := range previewCardHTMLMetaPattern.FindAllString(body, -1) {
		attrs := previewCardTagAttrs(tag)
		key := strings.ToLower(firstNonBlankRaw(attrs["property"], attrs["name"]))
		value := html.UnescapeString(attrs["content"])
		if key != "" && strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

func previewCardTagAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	for _, match := range previewCardHTMLAttrPattern.FindAllStringSubmatch(tag, -1) {
		if len(match) < 6 {
			continue
		}
		value := firstCapturedPreviewCardAttrValue(match)
		attrs[strings.ToLower(match[1])] = value
	}
	return attrs
}

func firstCapturedPreviewCardAttrValue(match []string) string {
	for _, index := range []int{3, 4, 5} {
		if index < len(match) && match[index] != "" {
			return match[index]
		}
	}
	return ""
}

func previewCardProviderName(host string) string {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	return host
}

func previewCardProviderURL(parsed *url.URL) string {
	if parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func (s *Server) attachPreviewCardToStatus(ctx context.Context, statusID int64, cardID int64) error {
	acquired, releaseLock, err := s.acquireActivityPubRedisLock(ctx, "attach_card:"+strconv.FormatInt(statusID, 10), 15*time.Minute)
	if err != nil || !acquired {
		return err
	}
	defer releaseLock()
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.PreviewCardStatus{}).Where("status_id = ?", statusID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return s.db.WithContext(ctx).Exec("INSERT INTO preview_cards_statuses (status_id, preview_card_id) VALUES (?, ?) ON CONFLICT DO NOTHING", statusID, cardID).Error
}

func (s *Server) cachePreviewCardImage(ctx context.Context, card *models.PreviewCard, rawURL string) {
	if s == nil || s.db == nil || card == nil || card.ID == 0 || s.cfg.PublicDir == "" || strings.TrimSpace(rawURL) == "" {
		return
	}
	download, err := fetchRemoteImageMedia(ctx, rawURL, 2*1024*1024)
	if err != nil {
		return
	}
	if _, ok := imageConfigFromReader(bytes.NewReader(download.body)); !ok {
		return
	}
	path := s.previewCardImagePath(card.ID, download.filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, download.body, 0o644); err != nil {
		return
	}
	if err := s.uploadPaperclipObject(ctx, previewCardImageObjectKey(card.ID, download.filename), path, download.contentType); err != nil {
		return
	}
	now := time.Now().UTC()
	card.ImageFileName = sql.NullString{String: download.filename, Valid: true}
	card.ImageContentType = sql.NullString{String: download.contentType, Valid: true}
	card.ImageFileSize = sql.NullInt64{Int64: int64(len(download.body)), Valid: true}
	card.ImageUpdatedAt = sql.NullTime{Time: now, Valid: true}
	card.ImageStorageSchemaVersion = sql.NullInt64{Int64: 1, Valid: true}
	if card.Type == 0 {
		if meta := mediaMetaForStoredFile(path, 0); len(meta) > 0 {
			card.Width = mediaMetaDimension(meta, "width")
			card.Height = mediaMetaDimension(meta, "height")
		}
	}
	if blurhash := blurhashForStoredImage(path); blurhash != "" {
		card.Blurhash = sql.NullString{String: blurhash, Valid: true}
	}
	updates := map[string]any{
		"image_file_name":              card.ImageFileName,
		"image_content_type":           card.ImageContentType,
		"image_file_size":              card.ImageFileSize,
		"image_updated_at":             card.ImageUpdatedAt,
		"image_storage_schema_version": card.ImageStorageSchemaVersion,
		"width":                        card.Width,
		"height":                       card.Height,
		"blurhash":                     card.Blurhash,
		"image_description":            card.ImageDescription,
		"updated_at":                   now,
	}
	_ = s.db.WithContext(ctx).Model(&models.PreviewCard{}).Where("id = ?", card.ID).Updates(updates).Error
}

func (s *Server) previewCardImagePath(id int64, filename string) string {
	return s.cfg.SystemAssetPath("cache", "preview_cards", "images", mediaPaperclipIDPartition(id), "original", filename)
}

func previewCardImageObjectKey(id int64, filename string) string {
	return "cache/preview_cards/images/" + strings.ReplaceAll(mediaPaperclipIDPartition(id), string(filepath.Separator), "/") + "/original/" + filename
}

func mediaMetaDimension(raw []byte, key string) int {
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0
	}
	original, _ := parsed["original"].(map[string]any)
	switch value := original[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func truncateString(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
