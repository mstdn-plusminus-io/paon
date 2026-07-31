package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestNotificationTypeSQLIncludesLegacyMapping(t *testing.T) {
	sql := notificationTypeSQL()
	for _, want := range []string{"Mention", "Favourite", "FollowRequest", "COALESCE"} {
		if !contains(sql, want) {
			t.Fatalf("notificationTypeSQL() missing %q: %s", want, sql)
		}
	}
}

func TestNotificationPaginationLinkUsesRailsMinIDPrevParamAndAllowedParams(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/notifications?limit=5&account_id=7&types[]=mention&exclude_types[]=follow&types=follow&exclude_types=mention&min_id=1&max_id=9&since_id=3&local=true&extra=1", nil)
	req.Host = "social.example"
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	got := notificationPaginationLink(c, 110, 100)
	wantNext := `<http://social.example/api/v1/notifications?account_id=7&exclude_types%5B%5D=follow&limit=5&max_id=100&types%5B%5D=mention>; rel="next"`
	wantPrev := `<http://social.example/api/v1/notifications?account_id=7&exclude_types%5B%5D=follow&limit=5&min_id=110&types%5B%5D=mention>; rel="prev"`
	if !strings.Contains(got, wantNext) || !strings.Contains(got, wantPrev) || strings.Contains(got, "since_id=") || strings.Contains(got, "local=") || strings.Contains(got, "extra=") || strings.Contains(got, "types=follow") || strings.Contains(got, "exclude_types=mention") {
		t.Fatalf("Link = %q", got)
	}
}

func TestNotificationAccountIDFilterMatchesRailsInvalidAccountParam(t *testing.T) {
	if got, ok := notificationAccountIDFilter("123"); got != 123 || !ok {
		t.Fatalf("notificationAccountIDFilter valid = %d, %v", got, ok)
	}
	for _, raw := range []string{"foo", "0", "-1"} {
		if got, ok := notificationAccountIDFilter(raw); ok {
			t.Fatalf("notificationAccountIDFilter(%q) = %d, true; want invalid", raw, got)
		}
	}
}

func TestApplyNotificationFiltersTurnsInvalidAccountIDIntoEmptyResultLikeRails(t *testing.T) {
	src, err := os.ReadFile("notifications.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`accountID, ok := notificationAccountIDFilter(rawAccountID)`,
		`return query.Where("1 = 0")`,
		`query = query.Where("notifications.from_account_id = ?", accountID)`,
	} {
		if !functionBodyContains(t, src, "applyNotificationFilters", want) {
			t.Fatalf("applyNotificationFilters missing %q", want)
		}
	}
}

func TestApplyNotificationFiltersTurnsInvalidRequestedTypesIntoEmptyResultLikeRails(t *testing.T) {
	src, err := os.ReadFile("notifications.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`types, typesRequested := notificationFilterValues(c, "types[]")`,
		`if typesRequested && len(types) == 0`,
		`query = query.Where("1 = 0")`,
	} {
		if !functionBodyContains(t, src, "applyNotificationFilters", want) {
			t.Fatalf("applyNotificationFilters missing invalid type empty-result behavior %q", want)
		}
	}
}

func TestNotificationAccountPayloadsUseAccountSerializerPreloadsAndEmojiHydration(t *testing.T) {
	src, err := os.ReadFile("notifications.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"notifications": {
			`if err := s.hydrateNotificationAccounts(notifications); err != nil`,
		},
		"showNotification": {
			`if err := s.hydrateNotificationAccounts(notifications); err != nil`,
		},
		"notificationQuery": {
			`accountRelationSerializerPreloads(s.db.Model(&models.Notification{}), "FromAccount")`,
		},
		"hydrateNotificationReports": {
			`accountRelationSerializerPreloads(s.db, "TargetAccount")`,
		},
		"hydrateNotificationAccounts": {
			`s.hydrateAccountCustomEmojis(&notifications[i].FromAccount)`,
			`s.hydrateAccountCustomEmojis(&notifications[i].Report.TargetAccount)`,
		},
	}
	for fn, wants := range checks {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
}

func TestNotificationStreamPayloadAndChannel(t *testing.T) {
	notification := models.Notification{
		ID:        42,
		Type:      "follow",
		CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		FromAccount: models.Account{
			ID:        7,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		},
	}
	payload := notificationStreamPayload(config.Config{LocalDomain: "example.test"}, notification, &models.Account{ID: 9})
	for _, want := range []string{`"event":"notification"`, `"id":"42"`, `"type":"follow"`, `"account"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload missing %q: %s", want, payload)
		}
	}
	if got, want := notificationStreamingChannel("mastodon:", 9), "mastodon:timeline:9:notifications"; got != want {
		t.Fatalf("channel = %q, want %q", got, want)
	}
}

func TestNotificationStreamPayloadIncludesNotificationContextFilters(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	notification := notificationFilterFixture(now)
	filters := notificationFilterFixtureFilters()

	item := notificationWithStatusFilters(config.Config{LocalDomain: "example.test"}, notification, &models.Account{ID: 9}, filters)
	if item.Status == nil || len(item.Status.Filtered) != 1 {
		t.Fatalf("filtered = %#v", item.Status)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(data)
	for _, want := range []string{`"filtered"`, `"id":"9"`, `"context":["notifications"]`, `"keyword_matches":["spoiler"]`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload missing %q: %s", want, payload)
		}
	}
	if strings.Contains(payload, `"id":"10"`) {
		t.Fatalf("home-only filter leaked into notification payload: %s", payload)
	}
}

func TestSerializeNotificationsIncludesNotificationContextFilters(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	items := serializeNotificationsWithFilters(
		config.Config{LocalDomain: "example.test"},
		[]models.Notification{notificationFilterFixture(now)},
		&models.Account{ID: 9},
		notificationFilterFixtureFilters(),
	)
	if len(items) != 1 || items[0].Status == nil || len(items[0].Status.Filtered) != 1 {
		t.Fatalf("items = %#v", items)
	}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(data)
	if !strings.Contains(payload, `"context":["notifications"]`) || strings.Contains(payload, `"id":"10"`) {
		t.Fatalf("payload = %s", payload)
	}
}

func notificationFilterFixture(now time.Time) models.Notification {
	return models.Notification{
		ID:        42,
		Type:      "mention",
		CreatedAt: now,
		FromAccount: models.Account{
			ID:        7,
			Username:  "alice",
			CreatedAt: now,
		},
		TargetStatus: &models.Status{
			ID:        100,
			AccountID: 7,
			Text:      "a spoiler appears",
			CreatedAt: now,
			Account: models.Account{
				ID:        7,
				Username:  "alice",
				CreatedAt: now,
			},
		},
	}
}

func notificationFilterFixtureFilters() []streamingFilter {
	return []streamingFilter{
		{
			ID:           "9",
			Title:        "Notify",
			Context:      []string{"notifications"},
			FilterAction: "hide",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
		{
			ID:           "10",
			Title:        "Home only",
			Context:      []string{"home"},
			FilterAction: "hide",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
	}
}

func TestNotificationStreamingSurfacesPublishCreatedNotifications(t *testing.T) {
	checks := map[string]map[string][]string{
		"server.go": {
			"toggleStatusJoin": []string{`s.publishNotificationIDs(notificationIDs)`},
			"createStatus": []string{
				`s.saveStatusMentionsFromTextAndCollectAccounts(tx, status.ID, account.ID, text, now)`,
			},
			"updateStatus": []string{
				`s.updateStatusMentionsFromTextAndCollectAccounts(tx, status.ID, account.ID, nextText, now)`,
				`s.enqueueOrCreateLocalNotifications(c.Request().Context(), notificationPayloads)`,
			},
			"saveStatusMentionsFromTextAndCollectAccounts":   []string{`ActivityType:      "Mention"`},
			"updateStatusMentionsFromTextAndCollectAccounts": []string{`ActivityType:      "Mention"`},
		},
		"local_status_postcommit.go": {
			"runLocalStatusCreatePostCommit": []string{
				`s.enqueueOrCreateLocalNotifications(ctx, effects.NotificationPayloads)`,
				`s.publishNotificationIDs(notificationIDs)`,
			},
		},
		"relationships.go": {
			"followAccount": []string{
				`s.enqueueOrCreateLocalNotifications(c.Request().Context(), notificationPayloads)`,
				`s.publishNotificationIDs(notificationIDs)`,
			},
		},
		"follow_requests.go": {
			"authorizeFollowRequest": []string{
				`s.enqueueOrCreateLocalNotifications(c.Request().Context(), notificationPayloads)`,
				`s.publishNotificationIDs(notificationIDs)`,
			},
			"authorizeFollowRequestPairNow": []string{`s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)`},
		},
		"activitypub_inbox.go": {
			"processActivityPubCreateNote": []string{
				`s.enqueueOrCreateLocalNotifications(context.Background(), notificationPayloads)`,
				`s.publishNotificationIDs(notificationIDs)`,
			},
			"processActivityPubUpdate": []string{
				`s.enqueueOrCreateLocalNotifications(context.Background(), notificationPayloads)`,
				`s.publishNotificationIDs(notificationIDs)`,
			},
			"processActivityPubFollow":          []string{`s.publishNotificationIDs(notificationIDs)`},
			"processActivityPubLike":            []string{`s.publishNotificationIDs(notificationIDs)`},
			"processActivityPubAnnounce":        []string{`s.publishNotificationIDs(notificationIDs)`},
			"saveActivityPubMentionsAndCollect": []string{`ActivityType:      "Mention"`},
		},
		"scheduled_status_publish.go": {
			"publishScheduledStatus": []string{
				`s.saveStatusMentionsFromTextAndCollectAccounts(tx, status.ID, account.ID, text, now)`,
				`s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)`,
			},
		},
	}
	for file, bodyChecks := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for fn, wants := range bodyChecks {
			for _, want := range wants {
				if !functionBodyContains(t, src, fn, want) {
					t.Fatalf("%s:%s does not contain %q", file, fn, want)
				}
			}
		}
	}
}

func contains(value string, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
