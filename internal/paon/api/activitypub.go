package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
	"gorm.io/gorm"
)

const instanceActorUsername = "mastodon.internal"
const activityPubFollowCollectionPageLimit = 12

func (s *Server) nodeInfoDiscovery(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "max-age=259200, public")
	return c.JSON(http.StatusOK, map[string]any{
		"links": []map[string]string{
			{"rel": "http://nodeinfo.diaspora.software/ns/schema/2.0", "href": s.cfg.BaseURL() + "/nodeinfo/2.0"},
		},
	})
}

func (s *Server) nodeInfo(c *echo.Context) error {
	return s.nodeInfoVersion(c, "2.0")
}

func (s *Server) nodeInfoVersion(c *echo.Context, version string) error {
	c.Response().Header().Set("Cache-Control", "max-age=1800, public")
	stats := s.instanceStatCounts()
	now := time.Now().UTC()
	activeMonth, err := s.instanceActiveUsers(c.Request().Context(), now, 4)
	if err != nil {
		return err
	}
	activeHalfYear, err := s.instanceActiveUsers(c.Request().Context(), now, 24)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"version": version,
		"software": map[string]string{
			"name":    "mastodon",
			"version": firstNonEmptyString(strings.TrimSpace(s.cfg.MastodonVersion), config.DefaultMastodonVersion),
		},
		"protocols": []string{"activitypub"},
		"services": map[string]any{
			"inbound":  []string{},
			"outbound": []string{},
		},
		"openRegistrations": s.registrationsOpen(),
		"usage": map[string]any{
			"users": map[string]any{
				"total":          stats.UserCount,
				"activeMonth":    activeMonth,
				"activeHalfyear": activeHalfYear,
			},
			"localPosts": stats.StatusCount,
		},
		"metadata": map[string]any{
			"nodeName":        s.settingStringValue("site_title", s.cfg.Title),
			"nodeDescription": s.settingStringValue("site_short_description", ""),
		},
	})
}

func (s *Server) hostMeta(c *echo.Context) error {
	if hostMetaWantsJSON(c) {
		return s.hostMetaJSON(c)
	}
	appendVaryHeader(c, "Accept")
	appendVaryHeader(c, "Origin")
	var escapedTemplate bytes.Buffer
	if err := xml.EscapeText(&escapedTemplate, []byte(s.cfg.BaseURL()+"/.well-known/webfinger?resource={uri}")); err != nil {
		return err
	}
	body := xml.Header + `<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">` + "\n" +
		`  <Link rel="lrdd" template="` + escapedTemplate.String() + `"/>` + "\n" +
		"</XRD>\n"
	c.Response().Header().Set("Cache-Control", "max-age=259200, public")
	return c.Blob(http.StatusOK, "application/xrd+xml; charset=utf-8", []byte(body))
}

func (s *Server) webfinger(c *echo.Context) error {
	resourceParam := c.QueryParam("resource")
	resource := strings.TrimPrefix(resourceParam, "acct:")
	if instanceActorResource(resource, s.cfg.LocalDomain) {
		return s.webfingerInstanceActor(c)
	}
	username, status := s.webfingerLocalUsername(resourceParam)
	if status != 0 {
		return webfingerError(c, status)
	}
	if strings.EqualFold(username, instanceActorUsername) {
		return s.webfingerInstanceActor(c)
	}
	account, err := s.findAccountByAcct(username + "@" + s.cfg.LocalDomain)
	if err != nil {
		return webfingerError(c, http.StatusNotFound)
	}
	permanentlySuspended, err := s.accountSuspendedPermanently(account)
	if err != nil {
		return err
	}
	if permanentlySuspended {
		return webfingerError(c, http.StatusGone)
	}
	subject := "acct:" + account.Acct()
	if account.Local() {
		subject = "acct:" + account.Username + "@" + s.cfg.LocalDomain
	}
	actorURL := activityPubActorID(s, *account)
	aliases := []string{s.cfg.BaseURL() + "/@" + account.Username, actorURL}
	profilePage := s.cfg.BaseURL() + "/@" + account.Username
	if account.ID == -99 {
		aliases = []string{actorURL}
		profilePage = s.cfg.BaseURL() + "/about/more?instance_actor=true"
	}
	return jrdJSON(c, map[string]any{
		"subject": subject,
		"aliases": aliases,
		"links":   s.webfingerLinks(profilePage, actorURL, account),
	})
}

func (s *Server) webfingerInstanceActor(c *echo.Context) error {
	account := s.instanceActorAccount()
	actorURL := activityPubActorID(s, account)
	return jrdJSON(c, map[string]any{
		"subject": "acct:" + instanceActorUsername + "@" + s.cfg.LocalDomain,
		"aliases": []string{
			actorURL,
		},
		"links": s.webfingerLinks(s.cfg.BaseURL()+"/about/more?instance_actor=true", actorURL, &account),
	})
}

func (s *Server) webfingerLinks(profilePage string, actorURL string, account *models.Account) []map[string]string {
	links := []map[string]string{
		{"rel": "http://webfinger.net/rel/profile-page", "type": "text/html", "href": profilePage},
		{"rel": "self", "type": "application/activity+json", "href": actorURL},
		{"rel": "http://ostatus.org/schema/1.0/subscribe", "template": s.cfg.BaseURL() + "/authorize_interaction?uri={uri}"},
	}
	if avatar := s.webfingerAvatarLink(account); avatar != nil {
		links = append(links, avatar)
	}
	return links
}

func (s *Server) webfingerAvatarLink(account *models.Account) map[string]string {
	if account == nil || s.disallowUnauthenticatedAPIAccess() {
		return nil
	}
	if !account.AvatarFileName.Valid || strings.TrimSpace(account.AvatarFileName.String) == "" || !account.AvatarContentType.Valid || strings.TrimSpace(account.AvatarContentType.String) == "" {
		return nil
	}
	return map[string]string{
		"rel":  "http://webfinger.net/rel/avatar",
		"type": account.AvatarContentType.String,
		"href": activityPubAccountAvatarURL(s, *account),
	}
}

func (s *Server) webfingerLocalUsername(resource string) (string, int) {
	if strings.TrimSpace(resource) == "" {
		return "", http.StatusBadRequest
	}
	if webfingerLocalHostResource(resource, s.cfg.LocalDomain, s.cfg.WebDomain, s.cfg.AlternateDomains) {
		return instanceActorUsername, 0
	}
	if strings.HasPrefix(strings.ToLower(resource), "http://") || strings.HasPrefix(strings.ToLower(resource), "https://") {
		parsed, err := url.Parse(resource)
		if err != nil || parsed.Host == "" || !webfingerLocalHost(parsed.Host, s.cfg.LocalDomain, s.cfg.WebDomain, s.cfg.AlternateDomains) {
			return "", http.StatusNotFound
		}
		path := strings.TrimSuffix(parsed.EscapedPath(), "/")
		switch {
		case path == "" || path == "/actor" || activityPubWebfingerOptionalFormatBase(path) == "/actor":
			return instanceActorUsername, 0
		case strings.HasPrefix(path, "/@"):
			username, err := activityPubWebfingerShortProfileUsername(strings.TrimPrefix(path, "/@"))
			if err != nil || username == "" || strings.Contains(username, "/") {
				return "", http.StatusNotFound
			}
			return username, 0
		case strings.HasPrefix(path, "/users/"):
			username, err := activityPubWebfingerAccountResourceUsername(strings.TrimPrefix(path, "/users/"))
			if err != nil || username == "" || strings.Contains(username, "/") {
				return "", http.StatusNotFound
			}
			return username, 0
		default:
			return "", http.StatusNotFound
		}
	}
	if !strings.Contains(resource, "@") {
		return "", http.StatusBadRequest
	}
	acct := strings.TrimPrefix(resource, "acct:")
	parts := strings.Split(acct, "@")
	username := parts[0]
	domain := parts[len(parts)-1]
	if username == "" || !webfingerLocalHostRaw(domain, s.cfg.LocalDomain, s.cfg.WebDomain, s.cfg.AlternateDomains) {
		return "", http.StatusNotFound
	}
	return username, 0
}

func activityPubWebfingerAccountResourceUsername(raw string) (string, error) {
	if strings.Contains(raw, "/") {
		return "", nil
	}
	segment := activityPubWebfingerOptionalFormatBase(raw)
	return url.PathUnescape(segment)
}

func activityPubWebfingerShortProfileUsername(raw string) (string, error) {
	segment, _, _ := strings.Cut(raw, "/")
	suffix := strings.TrimPrefix(raw, segment)
	switch {
	case suffix == "":
	case activityPubWebfingerOptionalFormatBase(suffix) == "/with_replies", activityPubWebfingerOptionalFormatBase(suffix) == "/media":
	case strings.HasPrefix(suffix, "/tagged/") && strings.TrimPrefix(suffix, "/tagged/") != "":
	default:
		return "", nil
	}
	segment = activityPubWebfingerOptionalFormatBase(segment)
	return url.PathUnescape(segment)
}

func activityPubWebfingerOptionalFormatBase(path string) string {
	base, suffix, ok := strings.Cut(path, ".")
	if !ok || base == "" || strings.Contains(suffix, "/") {
		return path
	}
	return base
}

func webfingerLocalHostResource(resource string, localDomain string, webDomain string, alternateDomains []string) bool {
	if resource != strings.TrimSpace(resource) {
		return false
	}
	if strings.HasPrefix(strings.ToLower(resource), "acct:") {
		return false
	}
	return webfingerLocalHostRaw(resource, localDomain, webDomain, alternateDomains)
}

func webfingerLocalHost(host string, localDomain string, webDomain string, alternateDomains []string) bool {
	host = strings.TrimSpace(host)
	return webfingerLocalHostRaw(host, localDomain, webDomain, alternateDomains)
}

func webfingerLocalHostRaw(host string, localDomain string, webDomain string, alternateDomains []string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, localDomain) || strings.EqualFold(host, webDomain) {
		return true
	}
	for _, domain := range alternateDomains {
		if strings.EqualFold(host, domain) {
			return true
		}
	}
	return false
}

func webfingerError(c *echo.Context, status int) error {
	c.Response().Header().Set("Cache-Control", "max-age=180, public")
	return c.NoContent(status)
}

func jrdJSON(c *echo.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "max-age=259200, public")
	return c.Blob(http.StatusOK, "application/jrd+json; charset=UTF-8", body)
}

func (s *Server) accountSuspendedPermanently(account *models.Account) (bool, error) {
	if account == nil || !account.SuspendedAt.Valid || account.ID == -99 {
		return false, nil
	}
	if s.db == nil {
		return true, nil
	}
	var deletionRequests int64
	if err := s.db.Model(&models.AccountDeletionRequest{}).Where("account_id = ?", account.ID).Count(&deletionRequests).Error; err != nil {
		return false, err
	}
	return deletionRequests == 0, nil
}

func (s *Server) activityPubInstanceActor(c *echo.Context) error {
	account := s.instanceActorAccount()
	return activityJSONWithCache(c, activityPubInstanceActorObject(s, account), 600)
}

func (s *Server) activityPubInstanceOutbox(c *echo.Context) error {
	pageRequested := truthy(c.QueryParam("page"))
	if s.authorizedFetchMode() || pageRequested {
		appendVaryHeader(c, "Signature")
	}
	signedAccount, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	account := s.instanceActorAccount()
	base := s.cfg.BaseURL() + "/actor/outbox"
	if !pageRequested {
		return activityJSONWithCachePrivacy(c, map[string]any{
			"@context":   activityPubActivityStreamsContext(),
			"id":         base,
			"type":       "OrderedCollection",
			"totalItems": account.AccountStat.StatusesCount,
			"first":      base + "?page=true",
			"last":       base + "?page=true&min_id=0",
		}, 180, activityPubPublicFetchCache(s))
	}
	if s.db == nil {
		return activityJSONWithCachePrivacy(c, map[string]any{"@context": activityPubActivityStreamsContext(), "id": activityPubOutboxPageURL(c, base), "type": "OrderedCollectionPage", "partOf": base, "orderedItems": []any{}}, 60, activityPubOutboxPagePublicCache(s, signedAccount))
	}
	statuses, err := s.activityPubStatuses(account, signedAccount, c.QueryParam("max_id"), c.QueryParam("min_id"), c.QueryParam("since_id"))
	if err != nil {
		return err
	}
	page, err := activityPubOutboxPageObjectWithError(s, base, activityPubOutboxPageURL(c, base), statuses)
	if err != nil {
		return err
	}
	return activityJSONWithCachePrivacy(c, page, 60, activityPubOutboxPagePublicCache(s, signedAccount))
}

func (s *Server) activityPubActor(c *echo.Context) error {
	if shouldRedirectActivityPubHTML(c) {
		return activityPubHTMLRedirect(c, "/@"+url.PathEscape(c.Param("username")))
	}
	s.activityPubAccountVary(c)
	signedAccount, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}

	account, err := s.findAccountByAcct(activityPubFormatParam(c, "username"))
	if err != nil || !account.Local() {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.activityPubAccountOwnedGuard(c, account, true); err != nil {
		return err
	}
	if err := s.hydrateAccountCustomEmojis(account); err != nil {
		return err
	}
	if err := s.hydrateActivityPubActorTags(account); err != nil {
		return err
	}

	return activityJSONWithCachePrivacy(c, activityPubActorObject(s, *account), 180, activityPubActorPublicCache(s, signedAccount))
}

func (s *Server) activityPubOutbox(c *echo.Context) error {
	pageRequested := truthy(c.QueryParam("page"))
	if s.authorizedFetchMode() || pageRequested {
		appendVaryHeader(c, "Signature")
	}
	signedAccount, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	base := activityPubActorURL(s, *account) + "/outbox"
	if !pageRequested {
		return activityJSONWithCachePrivacy(c, map[string]any{
			"@context":   activityPubActivityStreamsContext(),
			"id":         base,
			"type":       "OrderedCollection",
			"totalItems": account.AccountStat.StatusesCount,
			"first":      base + "?page=true",
			"last":       base + "?page=true&min_id=0",
		}, 180, activityPubPublicFetchCache(s))
	}

	statuses, err := s.activityPubStatuses(*account, signedAccount, c.QueryParam("max_id"), c.QueryParam("min_id"), c.QueryParam("since_id"))
	if err != nil {
		return err
	}
	page, err := activityPubOutboxPageObjectWithError(s, base, activityPubOutboxPageURL(c, base), statuses)
	if err != nil {
		return err
	}
	return activityJSONWithCachePrivacy(c, page, 60, activityPubOutboxPagePublicCache(s, signedAccount))
}

func activityPubOutboxPagePublicCache(s *Server, signedAccount *models.Account) bool {
	return (s == nil || !s.authorizedFetchMode()) && signedAccount == nil
}

func activityPubOutboxPageObject(s *Server, base string, pageID string, statuses []models.Status) map[string]any {
	page, _ := activityPubOutboxPageObjectWithError(s, base, pageID, statuses)
	return page
}

func activityPubOutboxPageObjectWithError(s *Server, base string, pageID string, statuses []models.Status) (map[string]any, error) {
	items := make([]any, 0, len(statuses))
	for _, status := range statuses {
		item, err := activityPubOutboxActivityWithError(s, status)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	page := map[string]any{
		"@context":     activityPubContextForEmbeddedItems(items),
		"id":           pageID,
		"type":         "OrderedCollectionPage",
		"partOf":       base,
		"orderedItems": items,
	}
	if len(statuses) > 0 {
		if len(statuses) == 20 {
			page["next"] = base + "?page=true&max_id=" + strconv.FormatInt(statuses[len(statuses)-1].ID, 10)
		}
		page["prev"] = base + "?page=true&min_id=" + strconv.FormatInt(statuses[0].ID, 10)
	}
	return page, nil
}

func (s *Server) activityPubFollowers(c *echo.Context) error {
	if shouldRedirectActivityPubHTML(c) {
		return activityPubHTMLRedirect(c, "/@"+url.PathEscape(c.Param("username"))+"/followers")
	}
	s.activityPubAccountVary(c)
	if err := s.requireActivityPubSignatureIfAuthorized(c); err != nil {
		return err
	}
	return s.activityPubFollowCollection(c, true)
}

func (s *Server) activityPubFollowing(c *echo.Context) error {
	if shouldRedirectActivityPubHTML(c) {
		return activityPubHTMLRedirect(c, "/@"+url.PathEscape(c.Param("username"))+"/following")
	}
	s.activityPubAccountVary(c)
	if err := s.requireActivityPubSignatureIfAuthorized(c); err != nil {
		return err
	}
	return s.activityPubFollowCollection(c, false)
}

func (s *Server) activityPubCollection(c *echo.Context) error {
	if s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
	signedAccount, err := s.activityPubSignatureAccountIfAuthorized(c)
	if err != nil {
		return err
	}
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	collectionID := activityPubFormatParam(c, "id")
	base := activityPubActorURL(s, *account) + "/collections/" + url.PathEscape(collectionID)
	hiddenForSignedAccount := func() (bool, error) {
		return s.activityPubCollectionHiddenForSignedAccount(*account, signedAccount)
	}
	publicCache := activityPubPublicFetchCache(s)
	switch collectionID {
	case "featured":
		hidden, err := hiddenForSignedAccount()
		if err != nil {
			return err
		}
		if hidden {
			return activityJSONWithCachePrivacy(c, map[string]any{"@context": activityPubActivityStreamsContext(), "id": base, "type": "OrderedCollection", "totalItems": 0, "orderedItems": []any{}}, 180, publicCache)
		}
		var statuses []models.Status
		if err := s.statusQuery().
			Joins("JOIN status_pins ON status_pins.status_id = statuses.id").
			Where("status_pins.account_id = ?", account.ID).
			Where("statuses.deleted_at IS NULL").
			Order("status_pins.created_at DESC").
			Find(&statuses).Error; err != nil {
			return err
		}
		if err := s.hydrateStatusesCustomEmojis(statuses); err != nil {
			return err
		}
		items := make([]any, 0, len(statuses))
		for _, status := range statuses {
			item, err := activityPubFeaturedStatusItemWithError(s, status)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		context := any(activityPubActivityStreamsContext())
		if activityPubItemsNeedActivityContext(items) {
			context = activityContext()
		}
		return activityJSONWithCachePrivacy(c, map[string]any{"@context": context, "id": base, "type": "OrderedCollection", "totalItems": len(items), "orderedItems": items}, 180, publicCache)
	case "tags":
		hidden, err := hiddenForSignedAccount()
		if err != nil {
			return err
		}
		if hidden {
			return activityJSONWithCachePrivacy(c, map[string]any{"@context": activityPubActivityStreamsContext(), "id": base, "type": "Collection", "totalItems": 0, "items": []any{}}, 180, publicCache)
		}
		var tags []models.FeaturedTag
		if err := s.db.Preload("Tag").Where("account_id = ?", account.ID).Find(&tags).Error; err != nil {
			return err
		}
		items := make([]any, 0, len(tags))
		for _, tag := range tags {
			items = append(items, activityPubFeaturedTagObject(s, tag))
		}
		context := any(activityPubActivityStreamsContext())
		if len(items) > 0 {
			context = activityPubHashtagContext()
		}
		return activityJSONWithCachePrivacy(c, map[string]any{"@context": context, "id": base, "type": "Collection", "totalItems": len(items), "items": items}, 180, publicCache)
	default:
		return apiError(c, http.StatusNotFound, "Record not found")
	}
}

func (s *Server) activityPubCollectionHiddenForSignedAccount(owner models.Account, signed *models.Account) (bool, error) {
	if s == nil || s.db == nil || signed == nil {
		return false, nil
	}
	var count int64
	if err := s.db.Model(&models.Block{}).Where("account_id = ? AND target_account_id = ?", owner.ID, signed.ID).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if signed.Domain.Valid {
		return s.accountDomainBlocking(owner.ID, signed.Domain.String)
	}
	return false, nil
}

func (s *Server) activityPubStatus(c *echo.Context) error {
	if shouldRedirectActivityPubHTML(c) {
		return activityPubHTMLRedirect(c, "/@"+url.PathEscape(c.Param("username"))+"/"+url.PathEscape(c.Param("id")))
	}
	s.activityPubAccountVary(c)
	signedAccount, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	status, err := s.findActivityPubStatus(c, signedAccount)
	if err != nil {
		return err
	}
	s.setActivityPubStatusLinkHeader(c, *status)
	if status.ReblogOfID.Valid && status.Reblog != nil && status.Reblog.ID != 0 {
		if targetURL := activityPubStatusPublicURL(s, *status.Reblog); targetURL != "" {
			return c.Redirect(http.StatusFound, targetURL)
		}
	}
	note, err := activityPubNoteWithError(s, *status)
	if err != nil {
		return err
	}
	return activityJSONWithCachePrivacy(c, note, 180, activityPubStatusPublicCache(s, status))
}

func (s *Server) activityPubStatusLikes(c *echo.Context) error {
	return s.activityPubStatusEngagementCollection(c, "likes")
}

func (s *Server) activityPubStatusShares(c *echo.Context) error {
	return s.activityPubStatusEngagementCollection(c, "shares")
}

func (s *Server) activityPubStatusEngagementCollection(c *echo.Context, kind string) error {
	s.activityPubAccountVary(c)
	signed, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	status, err := s.findActivityPubStatus(c, signed)
	if err != nil {
		return err
	}
	total := status.StatusStat.FavouritesCount
	if kind == "shares" {
		total = status.StatusStat.ReblogsCount
	}
	id := activityPubStatusURL(s, status.Account, status.ID) + "/" + kind
	return activityJSONWithCachePrivacy(c, map[string]any{
		"@context":   activityPubActivityStreamsContext(),
		"id":         id,
		"type":       "Collection",
		"totalItems": total,
	}, 0, activityPubStatusPublicCache(s, status))
}

func (s *Server) activityPubStatusActivity(c *echo.Context) error {
	s.activityPubAccountVary(c)
	signedAccount, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	status, err := s.findActivityPubStatus(c, signedAccount)
	if err != nil {
		return err
	}
	s.setActivityPubStatusLinkHeader(c, *status)
	activity, err := activityPubOutboxActivityWithError(s, *status)
	if err != nil {
		return err
	}
	return activityJSONWithCachePrivacy(c, activity, 180, activityPubStatusPublicCache(s, status))
}

func (s *Server) setActivityPubStatusLinkHeader(c *echo.Context, status models.Status) {
	c.Response().Header().Set("Link", publicStatusLinkHeader(s.cfg, status))
}

func (s *Server) activityPubReplies(c *echo.Context) error {
	if s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
	signedAccount, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	status, err := s.findActivityPubStatus(c, signedAccount)
	if err != nil {
		return err
	}
	base := activityPubStatusURL(s, status.Account, status.ID) + "/replies"
	if truthy(c.QueryParam("page")) {
		page, err := s.activityPubRepliesPage(*status, base, activityPubRepliesPageURL(c, base), c.QueryParam("min_id"), truthy(c.QueryParam("only_other_accounts")), 60)
		if err != nil {
			return err
		}
		page["@context"] = activityPubContextForCollectionPage(page)
		return activityJSONWithCachePrivacy(c, page, 0, activityPubStatusPublicCache(s, status))
	}
	first, err := s.activityPubRepliesPage(*status, base, activityPubRepliesPageURL(c, base), c.QueryParam("min_id"), truthy(c.QueryParam("only_other_accounts")), 60)
	if err != nil {
		return err
	}
	return activityJSONWithCachePrivacy(c, map[string]any{
		"@context": activityPubContextForNestedCollection(first),
		"id":       base,
		"type":     "Collection",
		"first":    first,
	}, 0, activityPubStatusPublicCache(s, status))
}

func activityPubPublicFetchCache(s *Server) bool {
	return s == nil || !s.authorizedFetchMode()
}

func activityPubActorPublicCache(s *Server, signedAccount *models.Account) bool {
	return !(s != nil && s.authorizedFetchMode() && signedAccount != nil)
}

func activityPubStatusPublicCache(s *Server, status *models.Status) bool {
	return activityPubPublicFetchCache(s) && status != nil && (status.Visibility == 0 || status.Visibility == 1)
}

func (s *Server) activityPubInbox(c *echo.Context) (err error) {
	ctx := c.Request().Context()
	finishTelemetry := func(int, error) {}
	if s != nil && s.cfg.OpenTelemetryEnabled {
		ctx, finishTelemetry = telemetry.StartFederation(ctx, "inbox")
	}
	defer func() { finishTelemetry(0, err) }()
	c.SetRequest(c.Request().WithContext(ctx))
	c.Response().Header().Set("Vary", "Authorization")
	setPrivateNoStoreCacheHeaders(c)
	if c.Request().ContentLength > activityPubInboxMaxJSONBytes {
		return c.NoContent(http.StatusRequestEntityTooLarge)
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, activityPubInboxMaxJSONBytes+1))
	if err != nil {
		return err
	}
	if len(body) > activityPubInboxMaxJSONBytes {
		return c.NoContent(http.StatusRequestEntityTooLarge)
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	var target *models.Account
	if strings.TrimSpace(c.Param("username")) != "" {
		target, err = s.localActivityPubInboxAccount(c)
		if err != nil {
			return err
		}
	}
	if s.unknownAffectedAccountActivity(body) {
		return c.NoContent(http.StatusAccepted)
	}
	actor, err := s.verifyActivityPubSignature(c, body)
	if err != nil {
		logActivityPubIngressIssue(c, "rejected", "signature_verification_failed", body, err)
		return apiError(c, http.StatusUnauthorized, err.Error())
	}
	s.upgradeActivityPubInboxAccount(actor)
	s.trackActivityPubDeliverySuccess(actor.InboxURL)
	s.processActivityPubCollectionSynchronization(c, actor)
	// Rails InboxesController enqueues ActivityPub::ProcessingWorker before returning 202.
	// Mirror that ordering so a failed enqueue is not acknowledged as accepted.
	deliveredTo := int64(0)
	if target != nil {
		deliveredTo = target.ID
	}
	if err := s.enqueueActivityPubInboxProcessingJobWithContext(c.Request().Context(), actor.ID, deliveredTo, "Account", body); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)).Wrap(
			activityPubProcessingError(body, actor.ID, deliveredTo, fmt.Errorf("enqueue ingress job: %w", err)),
		)
	}
	return c.NoContent(http.StatusAccepted)
}

func (s *Server) upgradeActivityPubInboxAccount(actor *models.Account) {
	if s == nil || s.db == nil || actor == nil || actor.Local() || actor.Protocol != 0 {
		return
	}
	if err := s.db.Model(&models.Account{}).Where("id = ?", actor.ID).Update("last_webfingered_at", nil).Error; err != nil {
		return
	}
	if strings.TrimSpace(actor.Username) == "" || !actor.Domain.Valid || strings.TrimSpace(actor.Domain.String) == "" {
		return
	}
	acct := actor.Username + "@" + actor.Domain.String
	s.enqueueResolveAccountTask(acct)
}

func (s *Server) unknownAffectedAccountActivity(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	activityType, _ := payload["type"].(string)
	if activityType != "Delete" && activityType != "Update" {
		return false
	}
	actor, _ := payload["actor"].(string)
	if strings.TrimSpace(actor) == "" || actor != activityRawValueOrID(payload["object"]) {
		return false
	}
	if s.db == nil {
		return true
	}
	var count int64
	if err := s.db.Model(&models.Account{}).Where("uri = ?", actor).Count(&count).Error; err != nil {
		return false
	}
	return count == 0
}

func activityRawValueOrID(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		id, _ := typed["id"].(string)
		return id
	default:
		return ""
	}
}

func (s *Server) activityPubFollowCollection(c *echo.Context, followers bool) error {
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	base := activityPubActorURL(s, *account)
	path := "/following"
	count := account.AccountStat.FollowingCount
	if followers {
		path = "/followers"
		count = account.AccountStat.FollowersCount
	}
	id := base + path
	hidden := account.HideCollections.Valid && account.HideCollections.Bool
	publicCache := activityPubPublicFetchCache(s)
	if !activityPubPageRequested(c) {
		return activityJSONWithCachePrivacy(c, activityPubFollowCollectionObject(id, count, hidden), 180, publicCache)
	}
	if hidden {
		return apiError(c, http.StatusForbidden, "Forbidden")
	}
	current, _, _ := s.currentAccount(c)
	var rows []models.Follow
	page := activityPubPageNumber(c)
	query := s.db.Preload("Account").Preload("TargetAccount").Order("follows.id DESC").Offset((page - 1) * activityPubFollowCollectionPageLimit).Limit(activityPubFollowCollectionPageLimit + 1)
	if followers {
		query = query.Where("target_account_id = ?", account.ID)
		if current != nil {
			query = applyFollowCollectionExclusionsByIDExpression(query, current, "follows.account_id")
		}
	} else {
		query = query.Where("account_id = ?", account.ID)
		if current != nil {
			query = applyFollowCollectionExclusionsByIDExpression(query, current, "follows.target_account_id")
		}
	}
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	hasNext := len(rows) > activityPubFollowCollectionPageLimit
	if hasNext {
		rows = rows[:activityPubFollowCollectionPageLimit]
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		if followers {
			items = append(items, activityPubAccountTagManagerURI(s, row.Account))
		} else {
			items = append(items, activityPubAccountTagManagerURI(s, row.TargetAccount))
		}
	}
	out := map[string]any{"@context": activityPubActivityStreamsContext(), "id": activityPubFollowCollectionCurrentPageURL(id, c.QueryParam("page")), "type": "OrderedCollectionPage", "totalItems": count, "partOf": id, "orderedItems": items}
	if hasNext {
		out["next"] = activityPubFollowCollectionPageURL(id, page+1)
	}
	if page > 1 {
		out["prev"] = activityPubFollowCollectionPageURL(id, page-1)
	}
	return activityJSONWithCachePrivacy(c, out, 0, publicCache)
}

func activityPubFollowCollectionObject(id string, count int64, hidden bool) map[string]any {
	out := map[string]any{"@context": activityPubActivityStreamsContext(), "id": id, "type": "OrderedCollection", "totalItems": count}
	if !hidden {
		out["first"] = id + "?page=1"
	}
	return out
}

func activityPubPageRequested(c *echo.Context) bool {
	return strings.TrimSpace(c.QueryParam("page")) != ""
}

func activityPubPageNumber(c *echo.Context) int {
	page, err := strconv.Atoi(strings.TrimSpace(c.QueryParam("page")))
	if err != nil || page < 1 {
		return 1
	}
	return page
}

func activityPubFollowCollectionPageURL(base string, page int) string {
	if page < 1 {
		page = 1
	}
	return base + "?page=" + strconv.Itoa(page)
}

func activityPubFollowCollectionCurrentPageURL(base string, rawPage string) string {
	if strings.TrimSpace(rawPage) == "" {
		return activityPubFollowCollectionPageURL(base, 1)
	}
	values := url.Values{}
	values.Set("page", rawPage)
	return base + "?" + values.Encode()
}

func (s *Server) localActivityPubAccount(c *echo.Context) (*models.Account, error) {
	return s.localActivityPubAccountWithSuspensionMode(c, false)
}

func (s *Server) localActivityPubInboxAccount(c *echo.Context) (*models.Account, error) {
	return s.localActivityPubAccountWithSuspensionMode(c, true)
}

func (s *Server) localActivityPubAccountWithSuspensionMode(c *echo.Context, skipTemporarySuspension bool) (*models.Account, error) {
	account, err := s.findAccountByAcct(activityPubFormatParam(c, "username"))
	if err != nil {
		return nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.activityPubAccountOwnedGuard(c, account, skipTemporarySuspension); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Server) activityPubAccountOwnedGuard(c *echo.Context, account *models.Account, skipTemporarySuspension bool) error {
	if account == nil || !account.Local() || accountHiddenFromAccountsShow(account) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !account.SuspendedAt.Valid {
		return nil
	}
	permanentlySuspended, err := s.accountSuspendedPermanently(account)
	if err != nil {
		return err
	}
	if !permanentlySuspended && skipTemporarySuspension {
		return nil
	}
	c.Response().Header().Set("Cache-Control", "max-age=180, public")
	if permanentlySuspended {
		return noContentError(http.StatusGone)
	}
	return noContentError(http.StatusForbidden)
}

func (s *Server) findActivityPubStatus(c *echo.Context, signed *models.Account) (*models.Status, error) {
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return nil, err
	}
	status, err := s.findStatus(activityPubFormatParam(c, "id"))
	if err != nil || status.AccountID != account.ID {
		return nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	visible, err := s.activityPubStatusVisible(*status, *account, signed)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	return status, nil
}

func (s *Server) activityPubStatusVisible(status models.Status, author models.Account, signed *models.Account) (bool, error) {
	if author.SuspendedAt.Valid {
		return false, nil
	}
	if signed == nil {
		return status.Visibility == 0 || status.Visibility == 1, nil
	}
	if signed.ID == author.ID {
		return true, nil
	}
	hidden, err := s.activityPubCollectionHiddenForSignedAccount(author, signed)
	if err != nil || hidden {
		return false, err
	}
	mentioned, err := s.activityPubStatusMentionsAccount(status.ID, signed.ID)
	if err != nil {
		return false, err
	}
	switch status.Visibility {
	case 0, 1:
		return true, nil
	case 2:
		if mentioned {
			return true, nil
		}
		return s.activityPubAccountFollows(signed.ID, author.ID)
	case 3, 4:
		return mentioned, nil
	default:
		return false, nil
	}
}

func (s *Server) activityPubStatusMentionsAccount(statusID int64, accountID int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	var count int64
	err := s.db.Model(&models.Mention{}).Where("status_id = ? AND account_id = ?", statusID, accountID).Count(&count).Error
	return count > 0, err
}

func (s *Server) activityPubAccountFollows(accountID int64, targetAccountID int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	var count int64
	err := s.db.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error
	return count > 0, err
}

func (s *Server) activityPubStatuses(account models.Account, signed *models.Account, maxID string, minID string, sinceID string) ([]models.Status, error) {
	query := s.statusQuery().Where("statuses.account_id = ? AND statuses.deleted_at IS NULL", account.ID)
	query, err := s.applyActivityPubOutboxVisibility(query, account, signed)
	if err != nil {
		return nil, err
	}
	maxIDPresent := strings.TrimSpace(maxID) != ""
	minIDPresent := strings.TrimSpace(minID) != ""
	sinceIDPresent := strings.TrimSpace(sinceID) != ""
	if maxIDPresent {
		query = query.Where("statuses.id < ?", maxID)
	}
	if minIDPresent {
		query = query.Where("statuses.id > ?", minID).Order("statuses.id ASC")
	} else if sinceIDPresent {
		query = query.Where("statuses.id > ?", sinceID)
	}
	if !minIDPresent {
		query = query.Order("statuses.id DESC")
	}
	var statuses []models.Status
	err = query.Limit(20).Find(&statuses).Error
	if err != nil {
		return nil, err
	}
	if minIDPresent {
		reverseRows(statuses)
	}
	if err := s.hydrateStatusesCustomEmojis(statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (s *Server) applyActivityPubOutboxVisibility(query *gorm.DB, account models.Account, signed *models.Account) (*gorm.DB, error) {
	if signed == nil {
		return query.Where("statuses.visibility IN ?", []int{0, 1}), nil
	}
	if signed.ID == account.ID {
		return query, nil
	}
	hidden, err := s.activityPubCollectionHiddenForSignedAccount(account, signed)
	if err != nil {
		return nil, err
	}
	if hidden {
		return query.Where("1 = 0"), nil
	}
	visible := []int{0, 1}
	var followCount int64
	if err := s.db.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", signed.ID, account.ID).Count(&followCount).Error; err != nil {
		return nil, err
	}
	if followCount > 0 {
		visible = []int{0, 1, 2}
	}
	query = query.
		Joins("LEFT JOIN mentions AS activitypub_outbox_mentions ON activitypub_outbox_mentions.status_id = statuses.id AND activitypub_outbox_mentions.account_id = ?", signed.ID).
		Where("(statuses.visibility IN ? OR activitypub_outbox_mentions.id IS NOT NULL)", visible).
		Group("statuses.id")
	query = s.applyActivityPubOutboxReblogExclusions(query, signed)
	return query, nil
}

func (s *Server) applyActivityPubOutboxReblogExclusions(query *gorm.DB, signed *models.Account) *gorm.DB {
	if signed == nil {
		return query
	}
	query = query.
		Joins("LEFT JOIN statuses AS activitypub_outbox_reblogs ON activitypub_outbox_reblogs.id = statuses.reblog_of_id").
		Joins("LEFT JOIN accounts AS activitypub_outbox_reblog_accounts ON activitypub_outbox_reblog_accounts.id = activitypub_outbox_reblogs.account_id").
		Where(`statuses.reblog_of_id IS NULL
			OR activitypub_outbox_reblog_accounts.domain IS NULL
			OR NOT EXISTS (
				SELECT 1 FROM account_domain_blocks
				WHERE account_domain_blocks.account_id = ?
				AND account_domain_blocks.domain = activitypub_outbox_reblog_accounts.domain
			)`, signed.ID)
	return applyFollowCollectionExclusionsByIDExpression(query, signed, "activitypub_outbox_reblog_accounts.id")
}

func (s *Server) activityPubRepliesPage(status models.Status, base string, pageID string, minID string, onlyOtherAccounts bool, limit int) (map[string]any, error) {
	if s.db == nil {
		return activityPubRepliesPageObject(base, pageID, []any{}, ""), nil
	}
	query := s.statusQuery().
		Where("statuses.in_reply_to_id = ? AND statuses.deleted_at IS NULL AND statuses.visibility IN ?", status.ID, []int{0, 1})
	if strings.TrimSpace(minID) != "" {
		query = query.Where("statuses.id > ?", minID)
	}
	if onlyOtherAccounts {
		query = query.Joins("JOIN accounts AS reply_accounts ON reply_accounts.id = statuses.account_id").
			Where("statuses.account_id <> ? AND reply_accounts.suspended_at IS NULL", status.AccountID)
	} else {
		query = query.Where("statuses.account_id = ?", status.AccountID)
	}

	var replies []models.Status
	if err := query.Order("statuses.id ASC").Limit(limit).Find(&replies).Error; err != nil {
		return nil, err
	}
	if err := s.hydrateStatusesCustomEmojis(replies); err != nil {
		return nil, err
	}
	items := make([]any, 0, len(replies))
	for _, reply := range replies {
		item, err := activityPubRepliesItemWithError(s, reply)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return activityPubRepliesPageObject(base, pageID, items, activityPubRepliesNext(base, replies, status.AccountID, limit, onlyOtherAccounts)), nil
}

func activityJSON(c *echo.Context, value any) error {
	c.Response().Header().Set("Content-Type", "application/activity+json")
	return c.JSON(http.StatusOK, value)
}

func activityJSONWithCache(c *echo.Context, value any, maxAgeSeconds int) error {
	return activityJSONWithCachePrivacy(c, value, maxAgeSeconds, true)
}

func activityJSONWithCachePrivacy(c *echo.Context, value any, maxAgeSeconds int, public bool) error {
	scope := "private"
	if public {
		scope = "public"
	}
	c.Response().Header().Set("Cache-Control", "max-age="+strconv.Itoa(maxAgeSeconds)+", "+scope)
	enforceRailsHighEntropyVaryCacheControl(c)
	return activityJSON(c, value)
}

func enforceRailsHighEntropyVaryCacheControl(c *echo.Context) {
	for _, vary := range strings.Split(c.Response().Header().Get("Vary"), ",") {
		vary = strings.TrimSpace(vary)
		switch strings.ToLower(vary) {
		case "cookie", "authorization", "signature":
			if c.Request().Header.Get(vary) != "" {
				c.Response().Header().Set("Cache-Control", "private, no-store")
				return
			}
		}
	}
}

func (s *Server) activityPubAccountVary(c *echo.Context) {
	appendVaryHeader(c, "Accept")
	appendVaryHeader(c, "Accept-Language")
	appendVaryHeader(c, "Cookie")
	if s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
}

func activityContext() []any {
	return []any{
		"https://www.w3.org/ns/activitystreams",
		map[string]any{
			"ostatus":          "http://ostatus.org#",
			"atomUri":          "ostatus:atomUri",
			"inReplyToAtomUri": "ostatus:inReplyToAtomUri",
			"conversation":     "ostatus:conversation",
			"sensitive":        "as:sensitive",
			"Hashtag":          "as:Hashtag",
			"toot":             "http://joinmastodon.org/ns#",
			"Emoji":            "toot:Emoji",
			"votersCount":      "toot:votersCount",
			"blurhash":         "toot:blurhash",
			"focalPoint":       map[string]any{"@container": "@list", "@id": "toot:focalPoint"},
			"misskey":          "https://misskey-hub.net/ns#",
			"_misskey_quote":   "misskey:_misskey_quote",
		},
	}
}

func activityPubActivityStreamsContext() string {
	return "https://www.w3.org/ns/activitystreams"
}

func activityPubContextForEmbeddedItems(items []any) any {
	if activityPubItemsNeedActivityContext(items) {
		return activityContext()
	}
	return activityPubActivityStreamsContext()
}

func activityPubContextForCollectionPage(page map[string]any) any {
	items, _ := page["items"].([]any)
	return activityPubContextForEmbeddedItems(items)
}

func activityPubContextForNestedCollection(page map[string]any) any {
	return activityPubContextForCollectionPage(page)
}

func activityPubItemsNeedActivityContext(items []any) bool {
	for _, item := range items {
		if _, ok := item.(map[string]any); ok {
			return true
		}
	}
	return false
}

func activityPubHashtagContext() []any {
	return []any{
		activityPubActivityStreamsContext(),
		map[string]any{"Hashtag": "as:Hashtag"},
	}
}

func activityPubAtomURIContext() []any {
	return []any{
		activityPubActivityStreamsContext(),
		map[string]any{
			"ostatus": "http://ostatus.org#",
			"atomUri": "ostatus:atomUri",
		},
	}
}

func activityPubEmojiContext() []any {
	return []any{
		activityPubActivityStreamsContext(),
		map[string]any{
			"toot":       "http://joinmastodon.org/ns#",
			"Emoji":      "toot:Emoji",
			"focalPoint": map[string]any{"@container": "@list", "@id": "toot:focalPoint"},
		},
	}
}

func activityPubActorContext() []any {
	extension := map[string]any{
		"toot":                      "http://joinmastodon.org/ns#",
		"manuallyApprovesFollowers": "as:manuallyApprovesFollowers",
		"movedTo":                   map[string]any{"@id": "as:movedTo", "@type": "@id"},
		"alsoKnownAs":               map[string]any{"@id": "as:alsoKnownAs", "@type": "@id"},
		"featured":                  map[string]any{"@id": "toot:featured", "@type": "@id"},
		"featuredTags":              map[string]any{"@id": "toot:featuredTags", "@type": "@id"},
		"schema":                    "http://schema.org#",
		"PropertyValue":             "schema:PropertyValue",
		"value":                     "schema:value",
		"discoverable":              "toot:discoverable",
		"indexable":                 "toot:indexable",
		"memorial":                  "toot:memorial",
		"suspended":                 "toot:suspended",
		"attributionDomains":        map[string]any{"@id": "toot:attributionDomains", "@type": "@id"},
	}
	return []any{
		activityPubActivityStreamsContext(),
		"https://w3id.org/security/v1",
		extension,
	}
}

func activityPubActorContextForTags(tags []any) []any {
	return activityPubActorContextForNestedSerializers(tags, nil, nil)
}

func activityPubActorContextForNestedSerializers(tags []any, icon map[string]any, image map[string]any) []any {
	context := activityPubActorContext()
	if len(tags) == 0 && icon == nil && image == nil {
		return context
	}
	extension, _ := context[len(context)-1].(map[string]any)
	if icon != nil || image != nil {
		extension["focalPoint"] = map[string]any{"@container": "@list", "@id": "toot:focalPoint"}
	}
	for _, item := range tags {
		tag, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch tag["type"] {
		case "Hashtag":
			extension["Hashtag"] = "as:Hashtag"
		case "Emoji":
			extension["Emoji"] = "toot:Emoji"
		}
	}
	return context
}

func shouldRedirectActivityPubHTML(c *echo.Context) bool {
	return c.Request().Method == http.MethodGet && !publicRequestHasFormat(c, "json") && !acceptsActivityPub(c.Request().Header.Get("Accept"))
}

func acceptsActivityPub(accept string) bool {
	bestActivityPubQ := -1.0
	bestActivityPubOrder := len(accept)
	bestHTMLQ := -1.0
	bestHTMLOrder := len(accept)
	for order, part := range strings.Split(accept, ",") {
		mediaType, q, params := parseAcceptEntry(part)
		if q <= 0 {
			continue
		}
		if activityPubAcceptMediaType(mediaType, params) {
			if q > bestActivityPubQ || (q == bestActivityPubQ && order < bestActivityPubOrder) {
				bestActivityPubQ = q
				bestActivityPubOrder = order
			}
		} else if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
			if q > bestHTMLQ || (q == bestHTMLQ && order < bestHTMLOrder) {
				bestHTMLQ = q
				bestHTMLOrder = order
			}
		}
	}
	return bestActivityPubQ > bestHTMLQ || (bestActivityPubQ == bestHTMLQ && bestActivityPubOrder < bestHTMLOrder)
}

func activityPubAcceptMediaType(mediaType string, params map[string]string) bool {
	switch mediaType {
	case "application/activity+json", "application/json", "application/ld+json", "application/jrd+json", "application/jsonrequest", "text/x-json":
		return true
	default:
		return false
	}
}

func activityPubProfileParamIncludes(profile string, target string) bool {
	for _, item := range strings.Fields(profile) {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

func parseAcceptEntry(entry string) (string, float64, map[string]string) {
	mediaType := ""
	q := 1.0
	params := map[string]string{}
	for i, part := range strings.Split(entry, ";") {
		part = strings.TrimSpace(part)
		if i == 0 {
			mediaType = strings.ToLower(part)
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if key != "" {
			params[key] = value
		}
		if key != "q" {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err == nil {
			q = parsed
		}
	}
	return mediaType, q, params
}

func (s *Server) instanceActorAccount() models.Account {
	if s.db != nil {
		account, err := s.representativeActivityPubAccount()
		if err == nil && account != nil {
			return *account
		}
	}
	now := time.Now().UTC()
	return models.Account{
		ID:        -99,
		Username:  instanceActorUsername,
		CreatedAt: now,
		UpdatedAt: now,
		Locked:    true,
		ActorType: sql.NullString{String: "Application", Valid: true},
	}
}

func instanceActorResource(resource string, localDomain string) bool {
	username, domain, _ := strings.Cut(strings.TrimPrefix(resource, "@"), "@")
	return strings.EqualFold(username, instanceActorUsername) && (domain == "" || strings.EqualFold(domain, localDomain))
}

func activityPubActorID(s *Server, account models.Account) string {
	if account.ID == -99 {
		return s.cfg.BaseURL() + "/actor"
	}
	return activityPubActorURL(s, account)
}

func activityPubActorObject(s *Server, account models.Account) map[string]any {
	actorID := activityPubActorID(s, account)
	actorType := activityPubActorType(account)
	suspended := activityPubActorSuspended(account)
	name := account.DisplayName
	if strings.TrimSpace(name) == "" {
		name = account.Username
	}
	if suspended {
		name = account.Username
	}
	restAccount := serializer.AccountFromModel(s.cfg, account)
	summary := restAccount.Note
	attachments := activityPubActorAttachments(s, account)
	if suspended {
		summary = ""
		attachments = []any{}
	}
	icon := activityPubAccountIcon(s, account)
	image := activityPubAccountImage(s, account)
	profileURL := s.cfg.BaseURL() + "/@" + account.Username
	inboxURL := actorID + "/inbox"
	outboxURL := actorID + "/outbox"
	if account.ID == -99 {
		profileURL = s.cfg.BaseURL() + "/about/more?instance_actor=true"
	}
	tags := activityPubActorTags(s, account)
	if suspended {
		tags = []any{}
	}
	object := map[string]any{
		"@context":                  activityPubActorContextForNestedSerializers(tags, icon, image),
		"id":                        actorID,
		"type":                      actorType,
		"preferredUsername":         account.Username,
		"name":                      name,
		"summary":                   summary,
		"url":                       profileURL,
		"inbox":                     inboxURL,
		"outbox":                    outboxURL,
		"featured":                  activityPubActorURL(s, account) + "/collections/featured",
		"featuredTags":              activityPubActorURL(s, account) + "/collections/tags",
		"followers":                 activityPubActorURL(s, account) + "/followers",
		"following":                 activityPubActorURL(s, account) + "/following",
		"manuallyApprovesFollowers": !suspended && account.Locked,
		"discoverable":              !suspended && account.Discoverable.Valid && account.Discoverable.Bool,
		"indexable":                 !suspended && account.Indexable,
		"memorial":                  account.Memorial,
		"tag":                       tags,
		"attachment":                attachments,
		"endpoints":                 map[string]any{"sharedInbox": s.cfg.BaseURL() + "/inbox"},
		"publicKey":                 map[string]any{"id": actorID + "#main-key", "owner": actorID, "publicKeyPem": account.PublicKey},
	}
	if icon != nil {
		object["icon"] = icon
	}
	if image != nil {
		object["image"] = image
	}
	if !account.CreatedAt.IsZero() {
		object["published"] = account.CreatedAt.UTC().Truncate(24 * time.Hour).Format("2006-01-02T15:04:05Z")
	}
	if suspended {
		object["suspended"] = true
	}
	if len(account.AlsoKnownAs) > 0 && !suspended {
		object["alsoKnownAs"] = append([]string{}, account.AlsoKnownAs...)
	}
	if len(account.AttributionDomains) > 0 {
		object["attributionDomains"] = append([]string{}, account.AttributionDomains...)
	}
	if !suspended {
		if moved := s.movedToAccountFor(&account); moved != nil {
			object["movedTo"] = activityPubActorMovedToURI(s, *moved)
		}
	}
	return object
}

func activityPubInstanceActorObject(s *Server, account models.Account) map[string]any {
	object := activityPubActorObject(s, account)
	allowed := map[string]bool{
		"@context":                  true,
		"id":                        true,
		"type":                      true,
		"preferredUsername":         true,
		"inbox":                     true,
		"outbox":                    true,
		"publicKey":                 true,
		"endpoints":                 true,
		"url":                       true,
		"manuallyApprovesFollowers": true,
	}
	for key := range object {
		if !allowed[key] {
			delete(object, key)
		}
	}
	return object
}

func activityPubActorType(account models.Account) string {
	if account.ID == -99 {
		return "Application"
	}
	switch account.ActorType.String {
	case "Application", "Service":
		return "Service"
	case "Group":
		return "Group"
	default:
		return "Person"
	}
}

func activityPubActorSuspended(account models.Account) bool {
	return account.ID != -99 && account.SuspendedAt.Valid
}

func activityPubActorURL(s *Server, account models.Account) string {
	return s.cfg.BaseURL() + "/users/" + url.PathEscape(account.Username)
}

func activityPubActorAttachments(s *Server, account models.Account) []any {
	fields := activityPubActorFields(account)
	items := make([]any, 0, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.Name) == "" && strings.TrimSpace(field.Value) == "" {
			continue
		}
		items = append(items, map[string]any{
			"type":  "PropertyValue",
			"name":  field.Name,
			"value": serializer.AccountFieldValueHTML(s.cfg, account, field),
		})
	}
	return items
}

func activityPubActorFields(account models.Account) []serializer.Field {
	if len(account.Fields) == 0 {
		return nil
	}
	var decoded []struct {
		Name       string  `json:"name"`
		Value      string  `json:"value"`
		VerifiedAt *string `json:"verified_at"`
	}
	if err := json.Unmarshal(account.Fields, &decoded); err != nil {
		return nil
	}
	limit := 2047
	if account.Local() {
		limit = 255
	}
	fields := make([]serializer.Field, 0, len(decoded))
	for _, field := range decoded {
		fields = append(fields, serializer.Field{
			Name:       trimAccountFieldForActivityPub(field.Name, limit),
			Value:      trimAccountFieldForActivityPub(field.Value, limit),
			VerifiedAt: field.VerifiedAt,
		})
	}
	return fields
}

func trimAccountFieldForActivityPub(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func activityPubAccountIcon(s *Server, account models.Account) map[string]any {
	if account.SuspendedAt.Valid || !account.AvatarFileName.Valid || strings.TrimSpace(account.AvatarFileName.String) == "" {
		return nil
	}
	return activityPubAccountImageObject(account.AvatarContentType.String, activityPubAccountAvatarURL(s, account))
}

func activityPubAccountImage(s *Server, account models.Account) map[string]any {
	if account.SuspendedAt.Valid || !account.HeaderFileName.Valid || strings.TrimSpace(account.HeaderFileName.String) == "" {
		return nil
	}
	return activityPubAccountImageObject(account.HeaderContentType.String, activityPubAccountHeaderURL(s, account))
}

func activityPubAccountAvatarURL(s *Server, account models.Account) string {
	return s.cfg.SystemAssetURL(accountImageAssetPath(account, "avatar", "original", account.AvatarFileName.String))
}

func activityPubAccountHeaderURL(s *Server, account models.Account) string {
	return s.cfg.SystemAssetURL(accountImageAssetPath(account, "header", "original", account.HeaderFileName.String))
}

func activityPubAccountImageObject(contentType string, value string) map[string]any {
	if value == "" {
		return nil
	}
	return map[string]any{
		"type":      "Image",
		"mediaType": emptyNil(contentType),
		"url":       value,
	}
}

func (s *Server) hydrateActivityPubActorTags(account *models.Account) error {
	if s == nil || s.db == nil || account == nil || account.ID == 0 {
		return nil
	}
	var rows []models.AccountTag
	if err := s.db.Preload("Tag").Where("account_id = ?", account.ID).Find(&rows).Error; err != nil {
		return err
	}
	account.Tags = make([]models.Tag, 0, len(rows))
	for _, row := range rows {
		if row.Tag.ID != 0 {
			account.Tags = append(account.Tags, row.Tag)
		}
	}
	return nil
}

func activityPubActorTags(s *Server, account models.Account) []any {
	if account.SuspendedAt.Valid {
		return []any{}
	}
	items := make([]any, 0, len(account.CustomEmojis)+len(account.Tags))
	for _, emoji := range account.CustomEmojis {
		if emoji.Shortcode == "" {
			continue
		}
		item := map[string]any{
			"id":      activityPubCustomEmojiID(s, emoji),
			"type":    "Emoji",
			"name":    ":" + emoji.Shortcode + ":",
			"updated": emoji.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if icon := activityPubCustomEmojiIcon(s, emoji); icon != nil {
			item["icon"] = icon
		}
		items = append(items, item)
	}
	for _, tag := range account.Tags {
		if strings.TrimSpace(tag.Name) == "" {
			continue
		}
		items = append(items, map[string]any{
			"type": "Hashtag",
			"href": s.cfg.BaseURL() + "/tags/" + url.PathEscape(tag.Name),
			"name": "#" + tag.Name,
		})
	}
	return items
}

func activityPubCustomEmojiID(s *Server, emoji models.CustomEmoji) any {
	if !emoji.Local() {
		if emoji.URI.Valid {
			return emoji.URI.String
		}
		return nil
	}
	return s.cfg.BaseURL() + "/emojis/" + strconv.FormatInt(emoji.ID, 10)
}

func activityPubCustomEmojiIcon(s *Server, emoji models.CustomEmoji) map[string]any {
	value := activityPubCustomEmojiURL(s, emoji)
	if value == "" {
		return nil
	}
	return map[string]any{
		"type":      "Image",
		"mediaType": emptyNil(emoji.ImageContentType.String),
		"url":       value,
	}
}

func activityPubCustomEmojiURL(s *Server, emoji models.CustomEmoji) string {
	if !emoji.ImageFileName.Valid || strings.TrimSpace(emoji.ImageFileName.String) == "" {
		if emoji.ImageRemoteURL.Valid {
			return emoji.ImageRemoteURL.String
		}
		return ""
	}
	prefix := ""
	if !emoji.Local() && emoji.ImageStorageSchemaVersion.Valid && emoji.ImageStorageSchemaVersion.Int64 >= 1 {
		prefix = "cache/"
	}
	return s.cfg.SystemAssetURL(prefix + "custom_emojis/images/" + adminPaperclipIDPartition(emoji.ID) + "/original/" + url.PathEscape(emoji.ImageFileName.String))
}

func activityPubAccountURI(s *Server, account models.Account) string {
	if account.Local() {
		return activityPubActorURL(s, account)
	}
	if account.URI != "" {
		return account.URI
	}
	if account.URL.Valid && account.URL.String != "" {
		return account.URL.String
	}
	return "https://" + account.Domain.String + "/users/" + url.PathEscape(account.Username)
}

func activityPubActorMovedToURI(s *Server, account models.Account) string {
	if account.Local() {
		return activityPubActorURL(s, account)
	}
	return account.URI
}

func activityPubAccountTagManagerURI(s *Server, account models.Account) string {
	if account.Local() {
		return activityPubActorURL(s, account)
	}
	return account.URI
}

func activityPubStatusURL(s *Server, account models.Account, statusID int64) string {
	return activityPubActorURL(s, account) + "/statuses/" + strconv.FormatInt(statusID, 10)
}

func activityPubStatusActivityURL(s *Server, account models.Account, statusID int64) string {
	return activityPubStatusURL(s, account, statusID) + "/activity"
}

func activityPubStatusURI(s *Server, status models.Status) string {
	if status.Account.Local() {
		return activityPubStatusURL(s, status.Account, status.ID)
	}
	if status.URI.Valid && status.URI.String != "" {
		return status.URI.String
	}
	if status.URL.Valid && status.URL.String != "" {
		return status.URL.String
	}
	return activityPubStatusURL(s, status.Account, status.ID)
}

func activityPubStatusTagManagerURI(s *Server, status models.Status) any {
	if status.Account.Local() {
		return activityPubStatusURL(s, status.Account, status.ID)
	}
	if status.URI.Valid {
		return status.URI.String
	}
	return nil
}

func activityPubStatusPublicURL(s *Server, status models.Status) string {
	if !status.Account.Local() {
		if status.URL.Valid && activityPubHTTPURL(status.URL.String) {
			return strings.TrimSpace(status.URL.String)
		}
		if status.URI.Valid && activityPubHTTPURL(status.URI.String) {
			return strings.TrimSpace(status.URI.String)
		}
		return ""
	}
	return s.cfg.BaseURL() + "/@" + url.PathEscape(status.Account.Username) + "/" + strconv.FormatInt(status.ID, 10)
}

func activityPubHTTPURL(value string) bool {
	return activityNormalizedHTTPURIRaw(value) != ""
}

func activityPubNoteID(s *Server, status models.Status) any {
	return activityPubStatusTagManagerURI(s, status)
}

func activityPubNoteURL(s *Server, status models.Status) any {
	if !status.Account.Local() {
		if status.URL.Valid && activityPubRawHTTPURL(status.URL.String) {
			return status.URL.String
		}
		return nil
	}
	return s.cfg.BaseURL() + "/@" + url.PathEscape(status.Account.Username) + "/" + strconv.FormatInt(status.ID, 10)
}

func activityPubRawHTTPURL(value string) bool {
	return activityNormalizedHTTPURIRaw(value) != ""
}

func activityPubOStatusTag(s *Server, createdAt time.Time, id int64, objectType string) string {
	return "tag:" + firstNonEmpty(s.cfg.LocalDomain, s.cfg.WebDomain, "localhost") + "," + createdAt.UTC().Format("2006-01-02") + ":objectId=" + strconv.FormatInt(id, 10) + ":objectType=" + objectType
}

func activityPubOStatusStatusURI(s *Server, status models.Status) any {
	if status.URI.Valid && status.URI.String != "" {
		return status.URI.String
	}
	if !status.Account.Local() {
		return nil
	}
	return activityPubOStatusTag(s, status.CreatedAt, status.ID, "Status")
}

func activityPubStatusAtomURI(s *Server, status models.Status) any {
	if !status.Account.Local() {
		return nil
	}
	return activityPubOStatusStatusURI(s, status)
}

func activityPubConversationURI(s *Server, conversation models.Conversation) any {
	if conversation.URI.Valid && conversation.URI.String != "" {
		return conversation.URI.String
	}
	return activityPubOStatusTag(s, conversation.CreatedAt, conversation.ID, "Conversation")
}

func activityPubNote(s *Server, status models.Status) map[string]any {
	note, _ := activityPubNoteWithError(s, status)
	return note
}

func activityPubNoteWithError(s *Server, status models.Status) (map[string]any, error) {
	account := status.Account
	id := activityPubNoteID(s, status)
	to, cc := activityPubAudience(s, status)
	content := serializer.StatusContentHTML(s.cfg, status)
	note := map[string]any{
		"@context":         activityContext(),
		"id":               id,
		"type":             activityPubStatusObjectType(status),
		"summary":          activityPubPresenceString(status.SpoilerText),
		"inReplyTo":        activityPubReplyTarget(s, status),
		"published":        status.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"url":              activityPubNoteURL(s, status),
		"attributedTo":     activityPubAccountTagManagerURI(s, account),
		"to":               to,
		"cc":               cc,
		"sensitive":        status.Sensitive || account.SensitizedAt.Valid,
		"content":          content,
		"atomUri":          activityPubStatusAtomURI(s, status),
		"inReplyToAtomUri": activityPubReplyAtomURI(s, status),
		"conversation":     activityPubConversation(s, status),
		"tag":              activityPubTags(s, status),
		"attachment":       activityPubMediaAttachments(s, status),
	}
	if status.Account.Local() {
		localID := activityPubStatusURL(s, status.Account, status.ID)
		replies, err := activityPubRepliesPreview(s, status)
		if err != nil {
			return nil, err
		}
		note["replies"] = replies
		note["likes"] = map[string]any{
			"id":         localID + "/likes",
			"type":       "Collection",
			"totalItems": status.StatusStat.FavouritesCount,
		}
		note["shares"] = map[string]any{
			"id":         localID + "/shares",
			"type":       "Collection",
			"totalItems": status.StatusStat.ReblogsCount,
		}
	}
	if status.Language.Valid && strings.TrimSpace(status.Language.String) != "" {
		note["contentMap"] = map[string]string{status.Language.String: content}
	}
	if status.EditedAt.Valid {
		note["updated"] = status.EditedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	if activityPubQuoteInContent(s, status) {
		var quoteURL any
		if status.QuoteOriginalURL.Valid {
			quoteURL = status.QuoteOriginalURL.String
		}
		note["quoteUrl"] = quoteURL
		note["_misskey_quote"] = quoteURL
	}
	if status.Poll != nil && status.Poll.ID != 0 {
		key := "oneOf"
		if status.Poll.Multiple {
			key = "anyOf"
		}
		note[key] = activityPubPollOptions(*status.Poll)
		if status.Poll.ExpiresAt.Valid {
			expiresAt := status.Poll.ExpiresAt.Time.UTC().Format("2006-01-02T15:04:05Z")
			note["endTime"] = expiresAt
			if pollExpiredAt(status.Poll.ExpiresAt, time.Now().UTC()) {
				note["closed"] = expiresAt
			}
		}
		if status.Poll.VotersCount.Valid {
			note["votersCount"] = status.Poll.VotersCount.Int64
		}
	}
	return note, nil
}

func activityPubPresenceString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func activityPubQuoteInContent(s *Server, status models.Status) bool {
	if s == nil || !s.cfg.DynamoDBEnabled {
		return false
	}
	return strings.Contains(html.EscapeString(status.Text), "RE:")
}

func activityPubStatusObjectType(status models.Status) string {
	if status.Poll != nil && status.Poll.ID != 0 {
		return "Question"
	}
	return "Note"
}

func activityPubPollOptions(poll models.Poll) []any {
	out := make([]any, 0, len(poll.Options))
	for index, title := range poll.Options {
		totalItems := int64(0)
		if index < len(poll.CachedTallies) {
			totalItems = poll.CachedTallies[index]
		}
		out = append(out, map[string]any{
			"type": "Note",
			"name": title,
			"replies": map[string]any{
				"type":       "Collection",
				"totalItems": totalItems,
			},
		})
	}
	return out
}

func activityPubCreate(s *Server, status models.Status) map[string]any {
	activity, _ := activityPubCreateWithError(s, status)
	return activity
}

func activityPubCreateWithError(s *Server, status models.Status) (map[string]any, error) {
	note, err := activityPubNoteWithError(s, status)
	if err != nil {
		return nil, err
	}
	delete(note, "@context")
	return map[string]any{
		"@context":  activityContext(),
		"id":        activityPubStatusActivityURL(s, status.Account, status.ID),
		"type":      "Create",
		"actor":     activityPubActorURL(s, status.Account),
		"published": status.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"to":        note["to"],
		"cc":        note["cc"],
		"object":    note,
	}, nil
}

func activityPubOutboxActivity(s *Server, status models.Status) map[string]any {
	activity, _ := activityPubOutboxActivityWithError(s, status)
	return activity
}

func activityPubOutboxActivityWithError(s *Server, status models.Status) (map[string]any, error) {
	if status.ReblogOfID.Valid {
		return activityPubAnnounceWithError(s, status)
	}
	return activityPubCreateWithError(s, status)
}

func activityPubUpdate(s *Server, status models.Status) map[string]any {
	activity, _ := activityPubUpdateWithError(s, status)
	return activity
}

func activityPubUpdateWithError(s *Server, status models.Status) (map[string]any, error) {
	note, err := activityPubNoteWithError(s, status)
	if err != nil {
		return nil, err
	}
	delete(note, "@context")
	updatedAt := status.UpdatedAt.UTC()
	if status.EditedAt.Valid {
		updatedAt = status.EditedAt.Time.UTC()
	}
	idSuffix := strconv.FormatInt(updatedAt.Unix(), 10)
	return map[string]any{
		"@context":  activityContext(),
		"id":        note["id"].(string) + "#updates/" + idSuffix,
		"type":      "Update",
		"actor":     activityPubActorURL(s, status.Account),
		"published": updatedAt.Format("2006-01-02T15:04:05Z"),
		"to":        note["to"],
		"cc":        note["cc"],
		"object":    note,
	}, nil
}

func activityPubActorUpdate(s *Server, account models.Account) map[string]any {
	actor := activityPubActorID(s, account)
	object := activityPubActorObject(s, account)
	context := object["@context"]
	delete(object, "@context")
	return map[string]any{
		"@context": context,
		"id":       actor + "#updates/" + strconv.FormatInt(account.UpdatedAt.UTC().Unix(), 10),
		"type":     "Update",
		"actor":    actor,
		"to":       []string{"https://www.w3.org/ns/activitystreams#Public"},
		"object":   object,
	}
}

func activityPubPollUpdate(s *Server, status models.Status) map[string]any {
	activity, _ := activityPubPollUpdateWithError(s, status)
	return activity
}

func activityPubPollUpdateWithError(s *Server, status models.Status) (map[string]any, error) {
	note, err := activityPubNoteWithError(s, status)
	if err != nil {
		return nil, err
	}
	delete(note, "@context")
	idSuffix := strconv.FormatInt(status.UpdatedAt.UTC().Unix(), 10)
	if status.Poll != nil && !status.Poll.UpdatedAt.IsZero() {
		idSuffix = strconv.FormatInt(status.Poll.UpdatedAt.UTC().Unix(), 10)
	}
	return map[string]any{
		"@context": activityContext(),
		"id":       note["id"].(string) + "#updates/" + idSuffix,
		"type":     "Update",
		"actor":    activityPubActorURL(s, status.Account),
		"to":       note["to"],
		"object":   note,
	}, nil
}

func activityPubDelete(s *Server, status models.Status) map[string]any {
	activity, _ := activityPubDeleteWithError(s, status)
	return activity
}

func activityPubDeleteWithError(s *Server, status models.Status) (map[string]any, error) {
	note, err := activityPubNoteWithError(s, status)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"@context": activityPubAtomURIContext(),
		"id":       note["id"].(string) + "#delete",
		"type":     "Delete",
		"actor":    activityPubActorURL(s, status.Account),
		"to":       []string{"https://www.w3.org/ns/activitystreams#Public"},
		"object": map[string]any{
			"id":      note["id"],
			"type":    "Tombstone",
			"atomUri": note["atomUri"],
		},
	}, nil
}

func activityPubDeleteActor(s *Server, account models.Account) map[string]any {
	actor := activityPubActorURL(s, account)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       actor + "#delete",
		"type":     "Delete",
		"actor":    actor,
		"to":       []string{"https://www.w3.org/ns/activitystreams#Public"},
		"object":   actor,
	}
}

func activityPubAddPinnedStatus(s *Server, status models.Status) map[string]any {
	statusURL := activityPubStatusURL(s, status.Account, status.ID)
	actorURL := activityPubActorURL(s, status.Account)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"type":     "Add",
		"actor":    actorURL,
		"object":   statusURL,
		"target":   actorURL + "/collections/featured",
	}
}

func activityPubRemovePinnedStatus(s *Server, status models.Status) map[string]any {
	statusURL := activityPubStatusURL(s, status.Account, status.ID)
	actorURL := activityPubActorURL(s, status.Account)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"type":     "Remove",
		"actor":    actorURL,
		"object":   statusURL,
		"target":   actorURL + "/collections/featured",
	}
}

func activityPubAddFeaturedTag(s *Server, featured models.FeaturedTag) map[string]any {
	return activityPubFeaturedTagCollectionChange(s, "Add", featured)
}

func activityPubRemoveFeaturedTag(s *Server, featured models.FeaturedTag) map[string]any {
	return activityPubFeaturedTagCollectionChange(s, "Remove", featured)
}

func activityPubFeaturedTagCollectionChange(s *Server, kind string, featured models.FeaturedTag) map[string]any {
	actorURL := activityPubActorURL(s, featured.Account)
	return map[string]any{
		"@context": activityPubHashtagContext(),
		"type":     kind,
		"actor":    actorURL,
		"object":   activityPubFeaturedTagObject(s, featured),
		"target":   actorURL + "/collections/featured",
	}
}

func activityPubFeaturedTagObject(s *Server, featured models.FeaturedTag) map[string]any {
	name := featuredActivityTagName(featured)
	return map[string]any{
		"type": "Hashtag",
		"name": "#" + name,
		"href": s.cfg.BaseURL() + "/@" + url.PathEscape(featured.Account.Username) + "/tagged/" + url.PathEscape(featured.Tag.Name),
	}
}

func featuredActivityTagName(featured models.FeaturedTag) string {
	return featured.DisplayNameValue()
}

func activityPubAnnounce(s *Server, status models.Status) map[string]any {
	activity, _ := activityPubAnnounceWithError(s, status)
	return activity
}

func activityPubAnnounceWithError(s *Server, status models.Status) (map[string]any, error) {
	return activityPubAnnounceWithInliningWithError(s, status, true)
}

func activityPubAnnounceWithInlining(s *Server, status models.Status, allowInlining bool) map[string]any {
	activity, _ := activityPubAnnounceWithInliningWithError(s, status, allowInlining)
	return activity
}

func activityPubAnnounceWithInliningWithError(s *Server, status models.Status, allowInlining bool) (map[string]any, error) {
	object, err := activityPubAnnounceObjectWithError(s, status, allowInlining)
	if err != nil {
		return nil, err
	}
	to, cc := activityPubAudience(s, status)
	return map[string]any{
		"@context":  activityPubActivityStreamsContext(),
		"id":        activityPubStatusActivityURL(s, status.Account, status.ID),
		"type":      "Announce",
		"actor":     activityPubActorURL(s, status.Account),
		"published": status.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"to":        to,
		"cc":        cc,
		"object":    object,
	}, nil
}

func activityPubUndoAnnounce(s *Server, status models.Status) map[string]any {
	announce := activityPubNestedSerializerObject(activityPubAnnounceWithInlining(s, status, false))
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       activityPubAnnounceURIForSerializer(s, status) + "/undo",
		"type":     "Undo",
		"actor":    activityPubActorURL(s, status.Account),
		"to":       []string{"https://www.w3.org/ns/activitystreams#Public"},
		"object":   announce,
	}
}

func activityPubAnnounceURIForSerializer(s *Server, status models.Status) string {
	if status.ID <= 0 {
		return activityPubActorURL(s, status.Account) + "#announces/"
	}
	return activityPubActorURL(s, status.Account) + "#announces/" + strconv.FormatInt(status.ID, 10)
}

func activityPubAnnounceObject(s *Server, status models.Status, allowInlining bool) any {
	object, _ := activityPubAnnounceObjectWithError(s, status, allowInlining)
	return object
}

func activityPubAnnounceObjectWithError(s *Server, status models.Status, allowInlining bool) (any, error) {
	if status.Reblog != nil && status.Reblog.ID != 0 {
		if allowInlining && activityPubAnnounceShouldInlineReblog(status) {
			note, err := activityPubNoteWithError(s, *status.Reblog)
			if err != nil {
				return nil, err
			}
			delete(note, "@context")
			return note, nil
		}
		return activityPubStatusTagManagerURI(s, *status.Reblog), nil
	}
	return activityPubStatusURL(s, status.Account, status.ID), nil
}

func activityPubAnnounceShouldInlineReblog(status models.Status) bool {
	return status.Reblog != nil &&
		status.AccountID == status.Reblog.AccountID &&
		status.Reblog.Visibility == 2 &&
		statusLocalLikeRails(status)
}

func statusLocalLikeRails(status models.Status) bool {
	return (status.Local.Valid && status.Local.Bool) || !status.URI.Valid
}

func activityPubLike(s *Server, account models.Account, status models.Status, favouriteID int64) map[string]any {
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       activityPubLikeURIForSerializer(s, account, favouriteID),
		"type":     "Like",
		"actor":    activityPubActorURL(s, account),
		"object":   activityPubStatusTagManagerURI(s, status),
	}
}

func activityPubUndoLike(s *Server, account models.Account, status models.Status, favouriteID int64) map[string]any {
	like := activityPubNestedSerializerObject(activityPubLike(s, account, status, favouriteID))
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       activityPubLikeURIForSerializer(s, account, favouriteID) + "/undo",
		"type":     "Undo",
		"actor":    activityPubActorURL(s, account),
		"object":   like,
	}
}

func activityPubNestedSerializerObject(payload map[string]any) map[string]any {
	object := make(map[string]any, len(payload))
	for key, value := range payload {
		if key == "@context" {
			continue
		}
		object[key] = value
	}
	return object
}

func activityPubLikeURI(s *Server, account models.Account, favouriteID int64) string {
	return activityPubActorURL(s, account) + "#likes/" + strconv.FormatInt(favouriteID, 10)
}

func activityPubLikeURIForSerializer(s *Server, account models.Account, favouriteID int64) string {
	if favouriteID <= 0 {
		return activityPubActorURL(s, account) + "#likes/"
	}
	return activityPubLikeURI(s, account, favouriteID)
}

func activityPubVote(s *Server, account models.Account, poll models.Poll, owner models.Account, status models.Status, vote models.PollVote) map[string]any {
	voteURI := activityPubVoteURIForSerializer(s, account, vote.ID)
	ownerURI := activityPubAccountTagManagerURI(s, owner)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       voteURI + "/activity",
		"type":     "Create",
		"actor":    activityPubActorURL(s, account),
		"to":       ownerURI,
		"object": map[string]any{
			"id":           voteURI,
			"type":         "Note",
			"name":         pollVoteOptionName(poll, vote.Choice),
			"attributedTo": activityPubActorURL(s, account),
			"inReplyTo":    activityPubStatusTagManagerURI(s, status),
			"to":           ownerURI,
		},
	}
}

func activityPubVoteURI(s *Server, account models.Account, voteID int64) string {
	return activityPubActorURL(s, account) + "#votes/" + strconv.FormatInt(voteID, 10)
}

func activityPubVoteURIForSerializer(s *Server, account models.Account, voteID int64) string {
	if voteID <= 0 {
		return activityPubActorURL(s, account) + "#votes/"
	}
	return activityPubVoteURI(s, account, voteID)
}

func pollVoteOptionName(poll models.Poll, choice int) string {
	if choice >= 0 && choice < len(poll.Options) {
		return poll.Options[choice]
	}
	return ""
}

func activityPubAudience(s *Server, status models.Status) ([]string, []string) {
	account := status.Account
	public := "https://www.w3.org/ns/activitystreams#Public"
	followers := activityPubActorURL(s, account) + "/followers"
	mentions := activityPubAudienceMentions(s, status)
	reblogCC := activityPubReblogAudience(s, status)
	switch status.Visibility {
	case 1:
		return []string{followers}, compactActivityPubAudience(append(append(reblogCC, public), mentions...))
	case 2:
		return []string{followers}, compactActivityPubAudience(append(reblogCC, mentions...))
	case 3, 4:
		return compactActivityPubAudience(mentions), compactActivityPubAudience(reblogCC)
	default:
		return []string{public}, compactActivityPubAudience(append(append(reblogCC, followers), mentions...))
	}
}

func activityPubAudienceMentions(s *Server, status models.Status) []string {
	if status.Account.SilencedAt.Valid {
		if mentions, ok := s.activityPubSilencedMentionAudience(status); ok {
			return mentions
		}
	}
	return activityPubMentionAudience(s, status)
}

func (s *Server) activityPubSilencedMentionAudience(status models.Status) ([]string, bool) {
	if s == nil || s.db == nil || status.AccountID == 0 {
		return nil, false
	}
	mentionIDs := make([]int64, 0, len(status.Mentions))
	seen := map[int64]struct{}{}
	for _, mention := range status.Mentions {
		if mention.Account.ID == 0 {
			continue
		}
		if _, ok := seen[mention.Account.ID]; ok {
			continue
		}
		seen[mention.Account.ID] = struct{}{}
		mentionIDs = append(mentionIDs, mention.Account.ID)
	}
	if len(mentionIDs) == 0 {
		return nil, true
	}
	allowedIDs := []int64{}
	if err := s.db.Model(&models.Follow{}).
		Where("target_account_id = ? AND account_id IN ?", status.AccountID, mentionIDs).
		Pluck("account_id", &allowedIDs).Error; err != nil {
		return nil, true
	}
	requestedIDs := []int64{}
	if err := s.db.Model(&models.FollowRequest{}).
		Where("target_account_id = ? AND account_id IN ?", status.AccountID, mentionIDs).
		Pluck("account_id", &requestedIDs).Error; err != nil {
		return nil, true
	}
	allowed := make(map[int64]struct{}, len(allowedIDs)+len(requestedIDs))
	for _, id := range allowedIDs {
		allowed[id] = struct{}{}
	}
	for _, id := range requestedIDs {
		allowed[id] = struct{}{}
	}
	out := []string{}
	for _, mention := range status.Mentions {
		if _, ok := allowed[mention.Account.ID]; !ok {
			continue
		}
		out = append(out, activityPubAccountTagManagerURI(s, mention.Account))
		if activityPubAccountIsGroup(mention.Account) {
			out = append(out, activityPubFollowersURI(s, mention.Account))
		}
	}
	return compactActivityPubAudience(out), true
}

func activityPubMentionAudience(s *Server, status models.Status) []string {
	out := make([]string, 0, len(status.Mentions))
	for _, mention := range status.Mentions {
		if mention.Account.ID == 0 {
			continue
		}
		out = append(out, activityPubAccountTagManagerURI(s, mention.Account))
		if activityPubAccountIsGroup(mention.Account) {
			out = append(out, activityPubFollowersURI(s, mention.Account))
		}
	}
	return out
}

func activityPubReblogAudience(s *Server, status models.Status) []string {
	if status.Reblog == nil || status.Reblog.Account.ID == 0 {
		return nil
	}
	return []string{activityPubAccountTagManagerURI(s, status.Reblog.Account)}
}

func activityPubAccountIsGroup(account models.Account) bool {
	return account.ActorType.Valid && account.ActorType.String == "Group"
}

func activityPubFollowersURI(s *Server, account models.Account) string {
	if account.Local() {
		return activityPubActorURL(s, account) + "/followers"
	}
	if strings.TrimSpace(account.FollowersURL) == "" {
		return ""
	}
	return account.FollowersURL
}

func compactActivityPubAudience(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func activityPubTags(s *Server, status models.Status) []any {
	tags := make([]any, 0, len(status.Mentions)+len(status.Tags)+len(status.CustomEmojis))
	mentions := append([]models.Mention(nil), status.Mentions...)
	sort.SliceStable(mentions, func(i, j int) bool {
		return mentions[i].ID < mentions[j].ID
	})
	for _, mention := range mentions {
		if mention.Silent {
			continue
		}
		tags = append(tags, map[string]any{"type": "Mention", "href": activityPubAccountTagManagerURI(s, mention.Account), "name": "@" + mention.Account.Acct()})
	}
	for _, tag := range status.Tags {
		tags = append(tags, map[string]any{"type": "Hashtag", "href": s.cfg.BaseURL() + "/tags/" + url.PathEscape(tag.Name), "name": "#" + tag.Name})
	}
	for _, emoji := range status.CustomEmojis {
		if emoji.Shortcode == "" {
			continue
		}
		item := map[string]any{
			"id":      activityPubCustomEmojiID(s, emoji),
			"type":    "Emoji",
			"name":    ":" + emoji.Shortcode + ":",
			"updated": emoji.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if icon := activityPubCustomEmojiIcon(s, emoji); icon != nil {
			item["icon"] = icon
		}
		tags = append(tags, item)
	}
	return tags
}

func activityPubMediaAttachments(s *Server, status models.Status) []any {
	attachments := activityPubOrderedMediaAttachments(status)
	items := make([]any, 0, len(attachments))
	for _, attachment := range attachments {
		fileURL := activityPubMediaURL(s, attachment)
		if fileURL == "" {
			continue
		}
		item := map[string]any{
			"type":      "Document",
			"mediaType": emptyNil(attachment.FileContentType.String),
			"url":       fileURL,
			"name":      emptyNil(attachment.Description.String),
			"blurhash":  emptyNil(attachment.Blurhash.String),
		}
		meta := mediaAttachmentMeta(attachment)
		if width, ok := nestedPresentValue(meta, "original", "width"); ok {
			item["width"] = width
		}
		if height, ok := nestedPresentValue(meta, "original", "height"); ok {
			item["height"] = height
		}
		if focus, ok := nestedMap(meta, "focus"); ok {
			item["focalPoint"] = []any{focus["x"], focus["y"]}
		}
		if iconURL := activityPubMediaIconURL(s, attachment); iconURL != "" {
			item["icon"] = map[string]any{
				"type":      "Image",
				"mediaType": emptyNil(activityPubMediaIconContentType(attachment)),
				"url":       iconURL,
			}
		}
		items = append(items, item)
	}
	return items
}

func activityPubOrderedMediaAttachments(status models.Status) []models.MediaAttachment {
	if status.OrderedMediaAttachmentIDs == nil {
		return mediaAttachmentsSortedByID(status.MediaAttachments)
	}
	byID := make(map[int64]models.MediaAttachment, len(status.MediaAttachments))
	for _, attachment := range status.MediaAttachments {
		byID[attachment.ID] = attachment
	}
	ordered := make([]models.MediaAttachment, 0, len(status.OrderedMediaAttachmentIDs))
	for _, id := range status.OrderedMediaAttachmentIDs {
		if attachment, ok := byID[id]; ok {
			ordered = append(ordered, attachment)
		}
	}
	return ordered
}

func activityPubMediaURL(s *Server, attachment models.MediaAttachment) string {
	if strings.TrimSpace(attachment.RemoteURL) != "" {
		return attachment.RemoteURL
	}
	if attachment.FileFileName.Valid && attachment.FileFileName.String != "" {
		return s.mediaAttachmentURL(attachment.ID, "files", "original", attachment.FileFileName.String)
	}
	return ""
}

func activityPubMediaIconURL(s *Server, attachment models.MediaAttachment) string {
	if attachment.ThumbnailRemoteURL.Valid && strings.TrimSpace(attachment.ThumbnailRemoteURL.String) != "" {
		return attachment.ThumbnailRemoteURL.String
	}
	if attachment.ThumbnailFileName.Valid && attachment.ThumbnailFileName.String != "" {
		return s.mediaAttachmentURL(attachment.ID, "thumbnails", "original", attachment.ThumbnailFileName.String)
	}
	return ""
}

func activityPubMediaIconContentType(attachment models.MediaAttachment) string {
	if attachment.ThumbnailContentType.Valid && strings.TrimSpace(attachment.ThumbnailContentType.String) != "" {
		return attachment.ThumbnailContentType.String
	}
	return attachment.FileContentType.String
}

func mediaAttachmentMeta(attachment models.MediaAttachment) map[string]any {
	if len(attachment.FileMeta) == 0 {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(attachment.FileMeta, &meta); err != nil {
		return nil
	}
	return meta
}

func nestedPresentValue(meta map[string]any, key string, nested string) (any, bool) {
	if meta == nil {
		return nil, false
	}
	parent, ok := meta[key].(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := parent[nested]
	if !ok || value == nil {
		return nil, false
	}
	if raw, ok := value.(string); ok && strings.TrimSpace(raw) == "" {
		return nil, false
	}
	return value, true
}

func nestedMap(meta map[string]any, key string) (map[string]any, bool) {
	if meta == nil {
		return nil, false
	}
	value, ok := meta[key].(map[string]any)
	return value, ok
}

func nestedNumber(meta map[string]any, key string, nested string) (any, bool) {
	if meta == nil {
		return nil, false
	}
	parent, ok := meta[key].(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := parent[nested]
	if !ok {
		return nil, false
	}
	switch value.(type) {
	case float64, int, int64, json.Number:
		return value, true
	default:
		return nil, false
	}
}

func activityPubRepliesPreview(s *Server, status models.Status) (map[string]any, error) {
	base := activityPubStatusURL(s, status.Account, status.ID) + "/replies"
	first, err := s.activityPubRepliesPreviewPage(status, base)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":    base,
		"type":  "Collection",
		"first": first,
	}, nil
}

func (s *Server) activityPubRepliesPreviewPage(status models.Status, base string) (map[string]any, error) {
	if s.db == nil {
		return activityPubRepliesPageObject(base, base+"?page=true", []any{}, activityPubRepliesURL(base, 0, true)), nil
	}
	var replies []models.Status
	if err := s.statusQuery().
		Where("statuses.account_id = ? AND statuses.in_reply_to_id = ? AND statuses.deleted_at IS NULL AND statuses.visibility IN ?", status.AccountID, status.ID, []int{0, 1}).
		Order("statuses.id ASC").
		Limit(5).
		Find(&replies).Error; err != nil {
		return nil, err
	}
	items := activityPubRepliesPreviewItems(replies)
	return activityPubRepliesPageObject(base, base+"?page=true", items, activityPubRepliesPreviewNext(base, replies)), nil
}

func activityPubRepliesPageObject(base string, pageID string, items []any, next string) map[string]any {
	page := map[string]any{
		"id":     pageID,
		"type":   "CollectionPage",
		"partOf": base,
		"items":  items,
	}
	if next != "" {
		page["next"] = next
	}
	return page
}

func activityPubRepliesPreviewItems(replies []models.Status) []any {
	items := make([]any, 0, len(replies))
	for _, reply := range replies {
		if reply.URI.Valid {
			items = append(items, reply.URI.String)
		} else {
			items = append(items, nil)
		}
	}
	return items
}

func activityPubFeaturedStatusItem(s *Server, status models.Status) any {
	item, _ := activityPubFeaturedStatusItemWithError(s, status)
	return item
}

func activityPubFeaturedStatusItemWithError(s *Server, status models.Status) (any, error) {
	if !activityPubStatusDistributable(status) {
		return activityPubStatusURI(s, status), nil
	}
	note, err := activityPubNoteWithError(s, status)
	if err != nil {
		return nil, err
	}
	delete(note, "@context")
	return note, nil
}

func activityPubRepliesItem(s *Server, reply models.Status) any {
	item, _ := activityPubRepliesItemWithError(s, reply)
	return item
}

func activityPubRepliesItemWithError(s *Server, reply models.Status) (any, error) {
	if !reply.Account.Local() {
		return activityPubStatusURI(s, reply), nil
	}
	note, err := activityPubNoteWithError(s, reply)
	if err != nil {
		return nil, err
	}
	delete(note, "@context")
	return note, nil
}

func activityPubRepliesPreviewNext(base string, replies []models.Status) string {
	if len(replies) == 0 {
		return activityPubRepliesURL(base, 0, true)
	}
	return activityPubRepliesURL(base, replies[len(replies)-1].ID, false)
}

func activityPubStatusDistributable(status models.Status) bool {
	return status.Visibility == 0 || status.Visibility == 1
}

func activityPubRepliesNext(base string, replies []models.Status, ownerAccountID int64, limit int, onlyOtherAccounts bool) string {
	if onlyOtherAccounts {
		if len(replies) < limit {
			return ""
		}
		return activityPubRepliesURL(base, replies[len(replies)-1].ID, true)
	}
	if len(replies) == 0 {
		return activityPubRepliesURL(base, 0, true)
	}
	if len(replies) < limit {
		return activityPubRepliesURL(base, 0, true)
	}
	return activityPubRepliesURL(base, replies[len(replies)-1].ID, false)
}

func activityPubRepliesURL(base string, minID int64, onlyOtherAccounts bool) string {
	values := url.Values{}
	values.Set("page", "true")
	if minID > 0 {
		values.Set("min_id", strconv.FormatInt(minID, 10))
	}
	if onlyOtherAccounts {
		values.Set("only_other_accounts", "true")
	}
	return base + "?" + values.Encode()
}

func activityPubReplyTarget(s *Server, status models.Status) any {
	if !status.InReplyToID.Valid || s.db == nil {
		return nil
	}
	var reply models.Status
	if err := s.statusQuery().First(&reply, status.InReplyToID.Int64).Error; err != nil {
		return nil
	}
	return activityPubReplyTargetURI(s, reply)
}

func activityPubReplyTargetURI(s *Server, reply models.Status) any {
	if !reply.Account.Local() && reply.URI.Valid && reply.URI.String != "" && !strings.HasPrefix(reply.URI.String, "http") {
		if reply.URL.Valid && reply.URL.String != "" {
			return reply.URL.String
		}
		return nil
	}
	return activityPubStatusURI(s, reply)
}

func activityPubReplyAtomURI(s *Server, status models.Status) any {
	if !status.InReplyToID.Valid || s.db == nil {
		return nil
	}
	var reply models.Status
	if err := s.statusQuery().First(&reply, status.InReplyToID.Int64).Error; err != nil {
		return nil
	}
	return activityPubOStatusStatusURI(s, reply)
}

func activityPubConversation(s *Server, status models.Status) any {
	if !status.ConversationID.Valid || s.db == nil {
		return nil
	}
	var conversation models.Conversation
	if err := s.db.First(&conversation, status.ConversationID.Int64).Error; err != nil {
		return nil
	}
	return activityPubConversationURI(s, conversation)
}

func activityPubOutboxPageURL(c *echo.Context, base string) string {
	values := url.Values{}
	values.Set("page", "true")
	query := c.Request().URL.Query()
	if raw, ok := query["max_id"]; ok && len(raw) > 0 {
		values.Set("max_id", raw[0])
	}
	if raw, ok := query["min_id"]; ok && len(raw) > 0 {
		values.Set("min_id", raw[0])
	}
	return base + "?" + values.Encode()
}

func activityPubRepliesPageURL(c *echo.Context, base string) string {
	values := url.Values{}
	values.Set("page", "true")
	query := c.Request().URL.Query()
	if raw, ok := query["min_id"]; ok && len(raw) > 0 {
		values.Set("min_id", raw[0])
	}
	if raw, ok := query["only_other_accounts"]; ok && len(raw) > 0 {
		values.Set("only_other_accounts", raw[0])
	}
	return base + "?" + values.Encode()
}
