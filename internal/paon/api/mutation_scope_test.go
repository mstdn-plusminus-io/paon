package api

import (
	"os"
	"testing"
)

func TestStatusMutationAPIsCheckRailsScopes(t *testing.T) {
	type check struct {
		file string
		fn   string
		want string
	}
	checks := []check{
		{"server.go", "createStatus", `s.requireAccountScope(c, "write", "write:statuses")`},
		{"server.go", "updateStatus", `s.requireAccountScope(c, "write", "write:statuses")`},
		{"server.go", "deleteStatus", `s.requireAccountScope(c, "write", "write:statuses")`},
		{"server.go", "reblogStatus", `s.requireAccountScope(c, "write", "write:statuses")`},
		{"server.go", "unreblogStatus", `s.requireAccountScope(c, "write", "write:statuses")`},
		{"polls.go", "votePoll", `s.requireAccountScope(c, "write", "write:statuses")`},
		{"status_extras.go", "translateStatus", `s.requireUserScope(c, "read", "read:statuses")`},
		{"status_extras.go", "toggleStatusPin", `s.requireAccountScope(c, "write", "write:accounts")`},
		{"status_mutes.go", "toggleStatusMute", `s.requireAccountScope(c, "write", "write:mutes")`},
	}
	for _, check := range checks {
		src, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s:%s does not contain %q", check.file, check.fn, check.want)
		}
	}
}

func TestMediaProfileScheduledAPIsCheckRailsScopes(t *testing.T) {
	type check struct {
		file string
		fn   string
		want string
	}
	checks := []check{
		{"media.go", "createMediaWithOptions", `s.requireAccountScope(c, "write", "write:media")`},
		{"media.go", "findOwnedPendingMedia", `s.requireAccountScope(c, "write", "write:media")`},
		{"profile_credentials.go", "updateCredentials", `s.requireUserScope(c, "write", "write:accounts")`},
		{"profile_credentials.go", "deleteProfileImage", `s.requireAccountScope(c, "write", "write:accounts")`},
		{"scheduled_statuses.go", "scheduledStatuses", `s.requireAccountScope(c, "read", "read:statuses")`},
		{"scheduled_statuses.go", "showScheduledStatus", `s.requireAccountScope(c, "read", "read:statuses")`},
		{"scheduled_statuses.go", "updateScheduledStatus", `s.requireAccountScope(c, "write", "write:statuses")`},
		{"scheduled_statuses.go", "deleteScheduledStatus", `s.requireAccountScope(c, "write", "write:statuses")`},
	}
	for _, check := range checks {
		src, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s:%s does not contain %q", check.file, check.fn, check.want)
		}
	}
}
