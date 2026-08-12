package api

import (
	"os"
	"testing"
)

func TestUnfollowPathsCaptureListsBeforeFollowCascade(t *testing.T) {
	checks := []struct {
		path string
		fn   string
		want string
	}{
		{"relationships.go", "deleteFollowWithAffectedListIDs", `Joins("JOIN list_accounts ON list_accounts.list_id = lists.id")`},
		{"relationships.go", "deleteFollowWithAffectedListIDs", `deleteFollow(tx, follow)`},
		{"relationships.go", "unfollowAccount", `deleteFollowWithAffectedListIDs(tx, follow)`},
		{"relationships.go", "unfollowAccount", `s.unmergeListFeedsAfterUnfollowBestEffort(c.Request().Context(), target.ID, affectedListIDs)`},
		{"web_relationships.go", "deleteRelationshipFollowForBatch", `deleteFollowWithAffectedListIDs(tx, follow)`},
		{"activitypub_inbox.go", "processActivityPubReject", `deleteFollowWithAffectedListIDs(tx, *match.Follow)`},
		{"activitypub_inbox.go", "cleanupActivityPubBlockRelationships", `deleteFollowEdgeReturningFollow(tx, targetID, accountID)`},
		{"activitypub_followers_synchronization.go", "removeUnexpectedActivityPubFollowers", `deleteFollowWithAffectedListIDs(tx, follow)`},
		{"imports.go", "applyRelationshipImportOverwrite", `deleteFollowWithAffectedListIDs(tx, follow)`},
		{"domain_blocks.go", "cleanupDomainBlockRecords", `deleteFollowWithAffectedListIDs(tx, row)`},
	}
	cache := map[string][]byte{}
	for _, check := range checks {
		src := cache[check.path]
		if src == nil {
			var err error
			src, err = os.ReadFile(check.path)
			if err != nil {
				t.Fatal(err)
			}
			cache[check.path] = src
		}
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s:%s missing %q", check.path, check.fn, check.want)
		}
	}
}
