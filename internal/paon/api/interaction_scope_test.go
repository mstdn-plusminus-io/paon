package api

import (
	"os"
	"testing"
)

func TestNotificationConversationMarkerAPIsCheckRailsScopes(t *testing.T) {
	type check struct {
		fn   string
		want string
	}
	checks := map[string][]check{
		"notifications.go": {
			{"notifications", `s.requireAccountScope(c, "read", "read:notifications")`},
			{"showNotification", `s.requireAccountScope(c, "read", "read:notifications")`},
			{"clearNotifications", `s.requireAccountScope(c, "write", "write:notifications")`},
			{"dismissNotification", `s.requireAccountScope(c, "write", "write:notifications")`},
		},
		"conversations.go": {
			{"conversations", `s.requireAccountScope(c, "read", "read:statuses")`},
			{"setConversationUnread", `s.requireAccountScope(c, "write", "write:conversations")`},
			{"deleteConversation", `s.requireAccountScope(c, "write", "write:conversations")`},
		},
		"markers.go": {
			{"markers", `s.requireUserScope(c, "read", "read:statuses")`},
			{"updateMarkers", `s.requireUserScope(c, "write", "write:statuses")`},
		},
		"status_extras.go": {
			{"requireUserScope", `return user, token, nil`},
			{"requireUserTokenScope", `if !tokenHasAnyScope(accessToken.Scopes, scopes...)`},
		},
	}
	for file, fileChecks := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range fileChecks {
			if !functionBodyContains(t, src, check.fn, check.want) {
				t.Fatalf("%s:%s does not contain %q", file, check.fn, check.want)
			}
		}
	}
}
