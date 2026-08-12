package api

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseActivityObjectReadsMastodon44UntrustedCounts(t *testing.T) {
	object := parseActivityObject(map[string]any{
		"id":     "https://remote.example/statuses/1",
		"type":   "Note",
		"likes":  map[string]any{"type": "Collection", "totalItems": float64(50)},
		"shares": map[string]any{"type": "Collection", "totalItems": "75"},
	})
	if !object.LikesTotalItems.Valid || object.LikesTotalItems.Int64 != 50 {
		t.Fatalf("likes.totalItems = %#v", object.LikesTotalItems)
	}
	if !object.SharesTotalItems.Valid || object.SharesTotalItems.Int64 != 75 {
		t.Fatalf("shares.totalItems = %#v", object.SharesTotalItems)
	}

	missing := parseActivityObject(map[string]any{"type": "Note", "likes": "https://remote.example/likes"})
	if missing.LikesTotalItems.Valid || missing.SharesTotalItems.Valid {
		t.Fatalf("non-collection counts should stay absent: likes=%#v shares=%#v", missing.LikesTotalItems, missing.SharesTotalItems)
	}
}

func TestMastodon44UntrustedCountsAreClampedAndPartialUpdatesArePreserved(t *testing.T) {
	if got := clampUntrustedStatusCount(sql.NullInt64{Int64: -1, Valid: true}); !got.Valid || got.Int64 != 0 {
		t.Fatalf("negative count = %#v", got)
	}
	if got := clampUntrustedStatusCount(sql.NullInt64{Int64: maxUntrustedStatusCount + 1, Valid: true}); !got.Valid || got.Int64 != maxUntrustedStatusCount {
		t.Fatalf("oversized count = %#v", got)
	}
	if got := clampUntrustedStatusCount(sql.NullInt64{}); got.Valid {
		t.Fatalf("missing count became present: %#v", got)
	}

	now := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	updates := activityPubUntrustedStatusCountUpdates(activityObject{
		LikesTotalItems: sql.NullInt64{Int64: 12, Valid: true},
	}, now)
	if got := updates["untrusted_favourites_count"]; got != int64(12) {
		t.Fatalf("favourites update = %#v", got)
	}
	if _, ok := updates["untrusted_reblogs_count"]; ok {
		t.Fatalf("missing shares count must not clear an existing value: %#v", updates)
	}
	if got := updates["updated_at"]; got != now {
		t.Fatalf("updated_at = %#v", got)
	}
}

func TestLoadedRemoteUntrustedCountsTrackLocalInteractions(t *testing.T) {
	remote := models.Status{
		URI:   sql.NullString{String: "https://remote.example/statuses/1", Valid: true},
		Local: sql.NullBool{Bool: false, Valid: true},
		StatusStat: models.StatusStat{
			ReblogsCount:             2,
			FavouritesCount:          3,
			UntrustedReblogsCount:    sql.NullInt64{Int64: 20, Valid: true},
			UntrustedFavouritesCount: sql.NullInt64{Int64: 30, Valid: true},
		},
	}
	decrementLoadedStatusStatCounter(&remote, statusStatCounterReblogs, 1)
	decrementLoadedStatusStatCounter(&remote, statusStatCounterFavourites, 1)
	if remote.StatusStat.ReblogsCount != 1 || remote.StatusStat.FavouritesCount != 2 || remote.StatusStat.UntrustedReblogsCount.Int64 != 19 || remote.StatusStat.UntrustedFavouritesCount.Int64 != 29 {
		t.Fatalf("remote counters = %#v", remote.StatusStat)
	}

	local := remote
	local.Local = sql.NullBool{Bool: true, Valid: true}
	decrementLoadedStatusStatCounter(&local, statusStatCounterFavourites, 1)
	if local.StatusStat.FavouritesCount != 1 || local.StatusStat.UntrustedFavouritesCount.Int64 != 29 {
		t.Fatalf("local counters = %#v", local.StatusStat)
	}
}

func TestStatusCounterSQLTracksRemoteUntrustedCounts(t *testing.T) {
	src, err := os.ReadFile("status_counters.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		`return "untrusted_reblogs_count"`,
		`return "untrusted_favourites_count"`,
		`ELSE LEAST(?, GREATEST(status_stats.`,
		`ELSE GREATEST(LEAST(status_stats.`,
		`statuses.local IS TRUE OR statuses.uri IS NULL`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("status counter implementation missing %q", want)
		}
	}
}

func TestActivityPubStatusUpdatesPersistUntrustedCounts(t *testing.T) {
	src, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, functionName := range []string{"processActivityPubUpdate", "processActivityPubImplicitStatusUpdate"} {
		if !functionBodyContains(t, src, functionName, `updateActivityPubStatusUntrustedCounts(tx, status.ID, object, now)`) {
			t.Fatalf("%s does not persist likes/shares totals", functionName)
		}
	}
}
