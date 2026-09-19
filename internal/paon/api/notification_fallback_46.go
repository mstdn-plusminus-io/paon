package api

import (
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

var baselineNotificationTypes = map[string]struct{}{
	"mention": {}, "status": {}, "reblog": {}, "follow": {}, "follow_request": {},
	"favourite": {}, "poll": {}, "update": {}, "annual_report": {}, "quote": {}, "quoted_update": {},
}

func notificationSupportedTypes(c *echo.Context) (map[string]struct{}, bool) {
	if c == nil {
		return nil, false
	}
	params := c.QueryParams()
	values, arrayPresent := params["supported_types[]"]
	plain, plainPresent := params["supported_types"]
	values = append(values, plain...)
	if !arrayPresent && !plainPresent {
		return nil, false
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out[value] = struct{}{}
		}
	}
	return out, true
}

func (s *Server) applyNotificationFallbacks(c *echo.Context, notifications []models.Notification, items []serializer.Notification) {
	for i := range items {
		if i < len(notifications) {
			s.applyNotificationFallback(c, notifications[i], &items[i], nil)
		}
	}
}

func (s *Server) applyNotificationFallback(c *echo.Context, notification models.Notification, item *serializer.Notification, sampleAccounts []models.Account) {
	if item == nil {
		return
	}
	item.Fallback = s.notificationFallback(c, notification, sampleAccounts)
}

func (s *Server) notificationFallback(c *echo.Context, notification models.Notification, sampleAccounts []models.Account) *serializer.NotificationFallback {
	supported, requested := notificationSupportedTypes(c)
	if !requested {
		return nil
	}
	kind := notification.ResolvedType()
	if _, baseline := baselineNotificationTypes[kind]; baseline {
		return nil
	}
	if _, clientSupports := supported[kind]; clientSupports {
		return nil
	}
	if !notificationFallbackHasActivity(kind, notification) {
		return nil
	}

	accounts := sampleAccounts
	if len(accounts) == 0 && notification.FromAccount.ID != 0 {
		accounts = []models.Account{notification.FromAccount}
	}
	var from models.Account
	if len(accounts) > 0 {
		from = accounts[0]
	}
	locale := s.webLocale(c, nil)
	signIn := settingsT(locale, "notification_fallbacks.generic.sign_in", "Sign in to the Mastodon web app")
	rootLink := `<a href="` + html.EscapeString(strings.TrimRight(s.cfg.BaseURL(), "/")+"/") + `">` + html.EscapeString(signIn) + `</a>`
	title := ""
	summary := settingsTVars(locale, "notification_fallbacks.generic.summary_html", "You're on an app that does not support the most recent version of Mastodon. %{link} for full functionality.", map[string]string{"link": rootLink})

	switch kind {
	case "severed_relationships":
		target := notification.SeveranceEvent.RelationshipSeveranceEvent.TargetName
		title = settingsTVars(locale, "notification_fallbacks.severed_relationships.title", "Lost connections with %{name}", map[string]string{"name": target})
		link := `<a href="` + html.EscapeString(strings.TrimRight(s.cfg.BaseURL(), "/")+"/severed_relationships") + `">` + html.EscapeString(signIn) + `</a>`
		summary = settingsTVars(locale, "notification_fallbacks.severed_relationships.summary_html", "An admin from %{from} has suspended %{target}, which means you can no longer receive updates from them or interact with them. %{link} to retrieve a list of the lost relationships.", map[string]string{"from": s.cfg.LocalDomain, "target": target, "link": link})
	case "moderation_warning":
		title = settingsT(locale, "notification_fallbacks.moderation_warning.title", "You have received a moderation warning.")
		link := rootLink
		if notification.AccountWarning != nil {
			link = `<a href="` + html.EscapeString(strings.TrimRight(s.cfg.BaseURL(), "/")+"/disputes/strikes/"+strconv.FormatInt(notification.AccountWarning.ID, 10)) + `">Sign in to the Mastodon web app</a>`
		}
		summary = settingsTVars(locale, "notification_fallbacks.moderation_warning.summary_html", "You're on an app that does not support the most recent version of Mastodon. %{link}.", map[string]string{"link": link})
	case "admin.sign_up":
		name := s.notificationFallbackMention(from)
		if len(accounts) > 1 {
			title = name + " and " + strconv.Itoa(len(accounts)-1) + " others signed up"
			if len(accounts) == 2 {
				title = name + " and one other signed up"
			}
		} else {
			title = name + " signed up"
		}
	case "admin.report":
		name := s.notificationFallbackMention(from)
		if from.Domain.Valid && strings.TrimSpace(from.Domain.String) != "" {
			name = html.EscapeString(from.Domain.String)
		}
		target := ""
		if notification.Report != nil {
			target = s.notificationFallbackMention(notification.Report.TargetAccount)
		}
		title = name + " reported " + target
	case "added_to_collection":
		title = s.notificationFallbackMention(from) + " added you to a collection"
	case "collection_update":
		title = html.EscapeString(from.Acct()) + " updated a collection you are in"
	default:
		return nil
	}

	return &serializer.NotificationFallback{Title: title, Summary: summary, Description: nil}
}

func notificationFallbackHasActivity(kind string, notification models.Notification) bool {
	switch kind {
	case "severed_relationships":
		return notification.SeveranceEvent != nil
	case "admin.report":
		return notification.Report != nil
	case "added_to_collection", "collection_update":
		return notification.TargetCollection != nil
	default:
		return true
	}
}

func (s *Server) notificationFallbackMention(account models.Account) string {
	display := account.Username
	if display == "" {
		display = account.Acct()
	}
	href := strings.TrimRight(s.cfg.BaseURL(), "/") + "/@" + url.PathEscape(account.Acct())
	if !account.Local() {
		href = firstNonEmpty(strings.TrimSpace(account.URL.String), strings.TrimSpace(account.URI), href)
	}
	return `<span class="h-card" translate="no"><a href="` + html.EscapeString(href) + `" class="u-url mention">@<span>` + html.EscapeString(display) + `</span></a></span>`
}
