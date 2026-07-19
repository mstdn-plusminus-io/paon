package api

import (
	"os"
	"testing"
)

func TestRenderEmptyMatchesRailsJSONResponse(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "renderEmpty", `return c.JSON(http.StatusOK, map[string]any{})`) {
		t.Fatal("renderEmpty must return Rails-compatible JSON {} with status 200")
	}
}

func TestRailsRenderEmptyEndpointsReturnJSONBody(t *testing.T) {
	checks := []struct {
		file string
		fns  []string
	}{
		{file: "admin.go", fns: []string{"destroyAdminAccount", "adminAccountAction", "rejectAdminAccount"}},
		{file: "admin_blocks.go", fns: []string{"deleteAdminDomainAllow", "deleteAdminDomainBlock", "deleteAdminEmailDomainBlock", "deleteAdminCanonicalEmailBlock", "deleteAdminIPBlock"}},
		{file: "domain_blocks.go", fns: []string{"createDomainBlock", "deleteDomainBlock"}},
		{file: "lists.go", fns: []string{"deleteList", "addListAccounts", "removeListAccounts"}},
		{file: "featured_tags.go", fns: []string{"deleteFeaturedTag"}},
		{file: "notifications.go", fns: []string{"clearNotifications", "dismissNotification"}},
		{file: "conversations.go", fns: []string{"deleteConversation"}},
		{file: "crypto.go", fns: []string{"cryptoClearEncryptedMessages", "cryptoDeliveries"}},
		{file: "registrations.go", fns: []string{"createEmailConfirmation"}},
		{file: "filters.go", fns: []string{"deleteFilter", "deleteFilterKeyword", "deleteFilterStatus"}},
		{file: "suggestions.go", fns: []string{"deleteSuggestion"}},
		{file: "web_push_subscriptions.go", fns: []string{"deletePushSubscription"}},
		{file: "scheduled_statuses.go", fns: []string{"deleteScheduledStatus"}},
		{file: "web_settings.go", fns: []string{"updateWebSettings"}},
		{file: "announcements.go", fns: []string{"dismissAnnouncement", "addAnnouncementReaction", "removeAnnouncementReaction"}},
	}

	for _, check := range checks {
		src, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range check.fns {
			if !functionBodyContains(t, src, fn, `return renderEmpty(c)`) {
				t.Fatalf("%s:%s must return Rails render_empty-compatible JSON", check.file, fn)
			}
			if functionBodyContains(t, src, fn, `return c.NoContent(http.StatusOK)`) {
				t.Fatalf("%s:%s must not return an empty 200 body for Rails render_empty endpoints", check.file, fn)
			}
		}
	}
}
