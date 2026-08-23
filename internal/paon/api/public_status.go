package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
	"gorm.io/gorm"
)

func (s *Server) publicStatus(c *echo.Context) error {
	if publicAccountPathIsRemote(publicShortAccountParam(c, "username")) {
		return s.webApp(c)
	}
	idParam := publicShortAccountParam(c, "id")
	if id, ok := publicPathWithoutFormat(idParam, "json"); ok {
		return withPathParam(c, "id", id, func() error {
			return s.activityPubStatus(c)
		})
	}
	if publicRequestHasFormat(c, "json") {
		return withPathParam(c, "id", idParam, func() error {
			return s.activityPubStatus(c)
		})
	}
	if format := publicRequestFormat(c); format != "" {
		switch format {
		case "html":
		case "json":
			return withPathParam(c, "id", idParam, func() error {
				return s.activityPubStatus(c)
			})
		default:
			return noContentError(http.StatusNotAcceptable)
		}
	}
	if idParam != "" && idParam != c.Param("id") {
		return withPathParam(c, "id", idParam, func() error {
			return s.publicStatusResolved(c)
		})
	}
	return s.publicStatusResolved(c)
}

func (s *Server) publicStatusResolved(c *echo.Context) error {
	switch publicHTMLActivityPubAcceptFormat(c.Request().Header.Get("Accept")) {
	case publicAcceptUnsupported:
		return noContentError(http.StatusNotAcceptable)
	case publicAcceptActivityPub:
		return s.activityPubStatus(c)
	}
	if err := s.requirePublicAccountAuthenticationIfLimited(c); err != nil {
		return err
	}
	status, err := s.publicStatusHTMLStatus(c)
	if err != nil {
		return err
	}
	s.setPublicStatusLinkHeaderForStatus(c, *status)
	if redirectURL := s.publicStatusOriginalRedirectURL(*status); redirectURL != "" {
		return c.Redirect(http.StatusFound, redirectURL)
	}
	publicHTMLCacheIfUnauthenticated(c, 10, 0)
	return s.webAppWithOptions(c, func(options *web.AppOptions, user *models.User) {
		s.applyPublicStatusHead(options, *status, s.webLocale(c, user))
	})
}

func (s *Server) applyPublicStatusHead(options *web.AppOptions, status models.Status, locale string) {
	if options == nil || !publicStatusPathStatusAllowed(status, status.Account.Username) {
		return
	}
	displayName := strings.TrimSpace(status.Account.DisplayName)
	if displayName == "" {
		displayName = status.Account.Username
	}
	quote := truncateRunes(firstNonEmpty(status.SpoilerText, status.Text), 50)
	options.DocumentTitle = settingsTVars(locale, "statuses.title", `%{name}: "%{quote}"`, map[string]string{"name": displayName, "quote": quote})

	statusURL := activityPubStatusPublicURL(s, status)
	actorURL := localActivityPubActorURL(s.cfg, status.Account)
	activityURL := actorURL + "/statuses/" + strconv.FormatInt(status.ID, 10)
	localDomain := firstNonEmpty(strings.TrimSpace(s.cfg.LocalDomain), strings.TrimSpace(s.cfg.WebDomain))
	acct := "@" + status.Account.Username
	if localDomain != "" {
		acct += "@" + localDomain
	}
	description := publicStatusDescription(status, locale)
	options.HeadMeta = []web.HeadMeta{
		{Property: "og:site_name", Content: s.settingStringValue("site_title", s.cfg.Title)},
		{Property: "og:type", Content: "article"},
		{Property: "og:title", Content: displayName + " (" + acct + ")"},
		{Property: "og:url", Content: statusURL},
		{Property: "og:published_time", Content: status.CreatedAt.UTC().Format(time.RFC3339)},
		{Property: "profile:username", Content: strings.TrimPrefix(acct, "@")},
		{Name: "description", Content: description},
		{Property: "og:description", Content: description},
	}
	if status.Language.Valid && strings.TrimSpace(status.Language.String) != "" {
		options.HeadMeta = append(options.HeadMeta, web.HeadMeta{Property: "og:locale", Content: strings.TrimSpace(status.Language.String)})
	}
	noIndex := status.Account.User.Settings.Valid && rawBool(decodeUserSettings(status.Account.User.Settings.String)["noindex"], false)
	if noIndex {
		options.HeadMeta = append([]web.HeadMeta{{Name: "robots", Content: "noindex, noarchive"}}, options.HeadMeta...)
	} else if schema := s.publicStatusSEOSchema(status); schema != "" {
		options.HeadJSONLD = []string{schema}
	}
	options.HeadLinks = []web.HeadLink{
		{Rel: "alternate", Type: "application/json+oembed", Href: s.cfg.BaseURL() + "/api/oembed?url=" + url.QueryEscape(statusURL)},
		{Rel: "alternate", Type: "application/activity+json", Href: activityURL},
	}
	options.HeadMeta = append(options.HeadMeta, s.publicStatusMediaMeta(status)...)
}

func (s *Server) publicStatusSEOSchema(status models.Status) string {
	displayName := strings.TrimSpace(status.Account.DisplayName)
	if displayName == "" {
		displayName = status.Account.Username
	}
	localDomain := firstNonEmpty(strings.TrimSpace(s.cfg.LocalDomain), strings.TrimSpace(s.cfg.WebDomain))
	identifier := status.Account.Username
	if localDomain != "" {
		identifier += "@" + localDomain
	}
	statusView := serializer.StatusFromModel(s.cfg, status, nil)
	payload := map[string]any{
		"@context":      "https://schema.org",
		"@type":         "SocialMediaPosting",
		"url":           activityPubStatusPublicURL(s, status),
		"datePublished": status.CreatedAt.UTC().Format(time.RFC3339),
		"author": map[string]any{
			"@type":                "Person",
			"name":                 displayName,
			"alternateName":        identifier,
			"identifier":           identifier,
			"url":                  s.cfg.BaseURL() + "/@" + url.PathEscape(status.Account.Username),
			"interactionStatistic": []any{publicStatusSEOInteraction("FollowAction", status.Account.AccountStat.FollowersCount)},
		},
		"text": statusView.Content,
		"interactionStatistic": []any{
			publicStatusSEOInteraction("LikeAction", status.StatusStat.FavouritesCount),
			publicStatusSEOInteraction("ShareAction", status.StatusStat.ReblogsCount),
			publicStatusSEOInteraction("ReplyAction", status.StatusStat.RepliesCount),
		},
	}
	if status.EditedAt.Valid {
		payload["dateModified"] = status.EditedAt.Time.UTC().Format(time.RFC3339)
	}
	images, videos, audio := []any{}, []any{}, []any{}
	for _, attachment := range activityPubOrderedMediaAttachments(status) {
		media := serializer.MediaAttachmentFromModel(s.cfg, attachment)
		item := map[string]any{
			"contentUrl":   media.URL,
			"thumbnailUrl": media.PreviewURL,
		}
		if attachment.Description.Valid {
			item["description"] = attachment.Description.String
		}
		switch attachment.Type {
		case 0:
			item["@type"] = "ImageObject"
			images = append(images, item)
		case 1, 2:
			item["@type"] = "VideoObject"
			item["uploadDate"] = attachment.CreatedAt.UTC().Format(time.RFC3339)
			item["embedUrl"] = s.cfg.BaseURL() + "/media/" + strconv.FormatInt(attachment.ID, 10)
			videos = append(videos, item)
		case 4:
			item["@type"] = "AudioObject"
			item["uploadDate"] = attachment.CreatedAt.UTC().Format(time.RFC3339)
			item["embedUrl"] = s.cfg.BaseURL() + "/media/" + strconv.FormatInt(attachment.ID, 10)
			audio = append(audio, item)
		}
	}
	if len(images) > 0 {
		payload["image"] = images
	}
	if len(videos) > 0 {
		payload["video"] = videos
	}
	if len(audio) > 0 {
		payload["audio"] = audio
	}
	if card, ok := status.FirstPreviewCard(); ok && strings.TrimSpace(card.URL) != "" {
		payload["sharedContent"] = map[string]any{"@type": "WebPage", "url": card.URL}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func publicStatusSEOInteraction(action string, count int64) map[string]any {
	return map[string]any{
		"@type":                "InteractionCounter",
		"interactionType":      "https://schema.org/" + action,
		"userInteractionCount": count,
	}
}

func publicStatusDescription(status models.Status, locale string) string {
	mediaCounts := map[string]int{"image": 0, "video": 0, "audio": 0}
	for _, attachment := range activityPubOrderedMediaAttachments(status) {
		switch attachment.Type {
		case 2:
			mediaCounts["video"]++
		case 4:
			mediaCounts["audio"]++
		default:
			mediaCounts["image"]++
		}
	}
	mediaParts := make([]string, 0, 3)
	for _, kind := range []string{"image", "video", "audio"} {
		count := mediaCounts[kind]
		if count == 0 {
			continue
		}
		key := "statuses.attached." + kind + ".other"
		if count == 1 {
			oneKey := "statuses.attached." + kind + ".one"
			if translated := webT(locale, oneKey, map[string]string{"count": strconv.Itoa(count)}); translated != oneKey {
				key = oneKey
			}
		}
		fallback := map[string]string{"image": "%{count} images", "video": "%{count} videos", "audio": "%{count} audio"}[kind]
		if count == 1 {
			fallback = map[string]string{"image": "%{count} image", "video": "%{count} video", "audio": "%{count} audio"}[kind]
		}
		mediaParts = append(mediaParts, settingsTVars(locale, key, fallback, map[string]string{"count": strconv.Itoa(count)}))
	}
	headerParts := make([]string, 0, 2)
	if len(mediaParts) > 0 {
		headerParts = append(headerParts, settingsTVars(locale, "statuses.attached.description", "Attached: %{attached}", map[string]string{"attached": strings.Join(mediaParts, " · ")}))
	}
	if warning := strings.TrimSpace(status.SpoilerText); warning != "" {
		headerParts = append(headerParts, settingsTVars(locale, "statuses.content_warning", "Content warning: %{warning}", map[string]string{"warning": warning}))
	}
	components := []string{strings.Join(headerParts, " · ")}
	if strings.TrimSpace(status.SpoilerText) == "" {
		components = append(components, strings.TrimSpace(status.Text))
		if status.Poll != nil && len(status.Poll.Options) > 0 {
			poll := make([]string, 0, len(status.Poll.Options))
			for _, option := range status.Poll.Options {
				poll = append(poll, "[ ] "+option)
			}
			components = append(components, strings.Join(poll, "\n"))
		}
	}
	nonBlank := components[:0]
	for _, component := range components {
		if strings.TrimSpace(component) != "" {
			nonBlank = append(nonBlank, component)
		}
	}
	return strings.Join(nonBlank, "\n\n")
}

func (s *Server) publicStatusMediaMeta(status models.Status) []web.HeadMeta {
	if status.Sensitive && !s.settingBoolValue("preview_sensitive_media", false) {
		return []web.HeadMeta{{Property: "twitter:card", Content: "summary"}}
	}
	metaTags := make([]web.HeadMeta, 0)
	playerCard := false
	for _, attachment := range activityPubOrderedMediaAttachments(status) {
		media := serializer.MediaAttachmentFromModel(s.cfg, attachment)
		fileMeta := mediaAttachmentMeta(attachment)
		switch attachment.Type {
		case 0:
			metaTags = appendPublicStatusImageMeta(metaTags, media.URL, attachment.FileContentType.String, fileMeta, "original", attachment.Description.String)
		case 1, 2:
			playerCard = true
			metaTags = appendPublicStatusImageMeta(metaTags, media.PreviewURL, "image/png", fileMeta, "small", "")
			metaTags = append(metaTags,
				web.HeadMeta{Property: "og:video", Content: media.URL},
				web.HeadMeta{Property: "og:video:secure_url", Content: media.URL},
				web.HeadMeta{Property: "og:video:type", Content: attachment.FileContentType.String},
				web.HeadMeta{Property: "twitter:player", Content: s.cfg.BaseURL() + "/media/" + strconv.FormatInt(attachment.ID, 10) + "/player"},
				web.HeadMeta{Property: "twitter:player:stream", Content: media.URL},
				web.HeadMeta{Property: "twitter:player:stream:content_type", Content: attachment.FileContentType.String},
			)
			metaTags = appendPublicStatusDimensions(metaTags, fileMeta, "original", "og:video", "twitter:player")
		case 4:
			playerCard = true
			metaTags = append(metaTags,
				web.HeadMeta{Property: "og:image", Content: statusEmbedAccountAvatarURLWithConfig(s.cfg, status.Account)},
				web.HeadMeta{Property: "og:image:width", Content: "400"},
				web.HeadMeta{Property: "og:image:height", Content: "400"},
				web.HeadMeta{Property: "og:audio", Content: media.URL},
				web.HeadMeta{Property: "og:audio:secure_url", Content: media.URL},
				web.HeadMeta{Property: "og:audio:type", Content: attachment.FileContentType.String},
				web.HeadMeta{Property: "twitter:player", Content: s.cfg.BaseURL() + "/media/" + strconv.FormatInt(attachment.ID, 10) + "/player"},
				web.HeadMeta{Property: "twitter:player:stream", Content: media.URL},
				web.HeadMeta{Property: "twitter:player:stream:content_type", Content: attachment.FileContentType.String},
				web.HeadMeta{Property: "twitter:player:width", Content: "670"},
				web.HeadMeta{Property: "twitter:player:height", Content: "380"},
			)
		}
	}
	card := "summary_large_image"
	if len(status.MediaAttachments) == 0 {
		card = "summary"
	} else if playerCard {
		card = "player"
	}
	return append(metaTags, web.HeadMeta{Property: "twitter:card", Content: card})
}

func appendPublicStatusImageMeta(tags []web.HeadMeta, imageURL string, contentType string, meta map[string]any, size string, description string) []web.HeadMeta {
	if strings.TrimSpace(imageURL) == "" {
		return tags
	}
	tags = append(tags, web.HeadMeta{Property: "og:image", Content: imageURL})
	if strings.TrimSpace(contentType) != "" {
		tags = append(tags, web.HeadMeta{Property: "og:image:type", Content: contentType})
	}
	tags = appendPublicStatusDimensions(tags, meta, size, "og:image")
	if strings.TrimSpace(description) != "" {
		tags = append(tags, web.HeadMeta{Property: "og:image:alt", Content: description})
	}
	return tags
}

func appendPublicStatusDimensions(tags []web.HeadMeta, meta map[string]any, size string, prefixes ...string) []web.HeadMeta {
	for _, dimension := range []string{"width", "height"} {
		value, ok := nestedPresentValue(meta, size, dimension)
		if !ok {
			continue
		}
		for _, prefix := range prefixes {
			tags = append(tags, web.HeadMeta{Property: prefix + ":" + dimension, Content: fmt.Sprint(value)})
		}
	}
	return tags
}

func (s *Server) setPublicStatusLinkHeader(c *echo.Context) {
	status, err := s.findPublicStatusForPath(c)
	if err != nil {
		return
	}
	s.setPublicStatusLinkHeaderForStatus(c, *status)
}

func (s *Server) setPublicStatusLinkHeaderForStatus(c *echo.Context, status models.Status) {
	c.Response().Header().Set("Link", publicStatusLinkHeader(s.cfg, status))
}

func (s *Server) findPublicStatusForPath(c *echo.Context) (*models.Status, error) {
	status, err := s.findStatus(c.Param("id"))
	if err != nil || !publicStatusPathStatusAllowed(*status, c.Param("username")) || status.ReblogOfID.Valid {
		if err != nil {
			return nil, err
		}
		return nil, gorm.ErrRecordNotFound
	}
	if !status.Account.Local() || status.Account.Username != c.Param("username") {
		return nil, errors.New("status does not match path account")
	}
	return status, nil
}

func (s *Server) publicStatusHTMLStatus(c *echo.Context) (*models.Status, error) {
	status, err := s.findStatus(c.Param("id"))
	if err != nil {
		return nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	if !publicStatusPathStatusAllowed(*status, c.Param("username")) {
		return nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.activityPubAccountOwnedGuard(c, &status.Account, false); err != nil {
		return nil, err
	}
	return status, nil
}

func publicStatusPathStatusAllowed(status models.Status, username string) bool {
	return status.Visibility <= 1 && status.Account.Local() && status.Account.Username == username
}

func (s *Server) publicStatusOriginalRedirectURL(status models.Status) string {
	if !status.ReblogOfID.Valid {
		return ""
	}
	if status.Reblog != nil && status.Reblog.ID != 0 {
		return activityPubStatusPublicURL(s, *status.Reblog)
	}
	target, err := s.findStatus(strconv.FormatInt(status.ReblogOfID.Int64, 10))
	if err != nil || target == nil {
		return ""
	}
	return activityPubStatusPublicURL(s, *target)
}

func publicStatusLinkHeader(cfg config.Config, status models.Status) string {
	actorURL := localActivityPubActorURL(cfg, status.Account)
	statusURL := actorURL + "/statuses/" + url.PathEscape(strconv.FormatInt(status.ID, 10))
	return "<" + statusURL + `>; rel="alternate"; type="application/activity+json"`
}
