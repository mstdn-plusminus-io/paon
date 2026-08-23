package api

import (
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestSeveredRelationshipPurgedEventSpansBothDownloadColumns(t *testing.T) {
	event := models.AccountRelationshipSeveranceEvent{
		ID: 7,
		RelationshipSeveranceEvent: models.RelationshipSeveranceEvent{
			TargetName: "remote.example",
			Purged:     true,
		},
		FollowingCount: 2,
		FollowersCount: 3,
	}
	body := severedRelationshipEventRowHTML(event, "en")
	if !strings.Contains(body, `colspan="2"`) || !strings.Contains(body, "Information about this server has been purged") {
		t.Fatalf("purged severance row must use one message cell across both download columns: %s", body)
	}
	if strings.Contains(body, ".csv") || strings.Contains(body, `>2</td>`) || strings.Contains(body, `>3</td>`) {
		t.Fatalf("purged severance row must not expose stale counts or download links: %s", body)
	}
}

func TestSeveredRelationshipAvailableEventKeepsBothDownloads(t *testing.T) {
	event := models.AccountRelationshipSeveranceEvent{
		ID:                         7,
		RelationshipSeveranceEvent: models.RelationshipSeveranceEvent{TargetName: "remote.example"},
		FollowingCount:             2,
		FollowersCount:             3,
	}
	body := severedRelationshipEventRowHTML(event, "en")
	for _, want := range []string{"/severed_relationships/7/following.csv", "/severed_relationships/7/followers.csv"} {
		if !strings.Contains(body, want) {
			t.Fatalf("available severance row missing %q: %s", want, body)
		}
	}
}
