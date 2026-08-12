package api

import (
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAccountURIOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://remote.example/users/alice":  "https://remote.example",
		"http://remote.example":               "http://remote.example",
		"https://remote.example/":             "https://remote.example",
		" HTTPS://remote.example/users/alice": "",
		"HTTPS://remote.example/users/alice":  "",
		"Http://remote.example/users/alice":   "",
		"https://remote.example ":             "https://remote.example ",
		"https://remote.example /users/alice": "https://remote.example ",
		"acct:alice@remote.example":           "",
		"":                                    "",
	} {
		if got := accountURIOrigin(raw); got != want {
			t.Fatalf("accountURIOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseActivityPubCollectionSynchronizationHeader(t *testing.T) {
	params, ok := parseActivityPubCollectionSynchronizationHeader(`collectionId="https://remote.example/followers", digest="abc", url="https://remote.example/sync"`)
	if !ok {
		t.Fatal("header did not parse")
	}
	if params["collectionId"] != "https://remote.example/followers" || params["digest"] != "abc" || params["url"] != "https://remote.example/sync" {
		t.Fatalf("params = %#v", params)
	}
	if _, ok := parseActivityPubCollectionSynchronizationHeader(`collectionId="https://remote.example/followers"`); ok {
		t.Fatal("incomplete header parsed successfully")
	}
}

func TestActivityPubInboxProcessesCollectionSynchronizationHeader(t *testing.T) {
	src, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `s.processActivityPubCollectionSynchronization(c, actor)`) {
		t.Fatal("activityPubInbox does not process Collection-Synchronization header after signature verification")
	}
}

func TestEscapeSQLLikeMatchesRailsSanitizeSQLLikeContract(t *testing.T) {
	got := escapeSQLLike(`https://remote_ex%ample/users\alice`)
	want := `https://remote\_ex\%ample/users\\alice`
	if got != want {
		t.Fatalf("escapeSQLLike = %q, want %q", got, want)
	}
}

func TestActivityPubFollowersSynchronizationRequiresStoredFollowersURLLikeRails(t *testing.T) {
	account := models.Account{
		ID:           42,
		URI:          "https://remote.example/users/alice",
		FollowersURL: "https://remote.example/users/alice/followers",
	}
	params := map[string]string{
		"collectionId": "https://remote.example/users/alice/followers",
		"url":          "https://remote.example/users/alice/followers_synchronization",
		"digest":       "different",
	}
	if !(&Server{}).shouldSynchronizeActivityPubFollowers(account, params) {
		t.Fatal("matching stored followers_url should be accepted")
	}
	account.FollowersURL = ""
	if (&Server{}).shouldSynchronizeActivityPubFollowers(account, params) {
		t.Fatal("blank followers_url must not fall back to the actor followers collection")
	}
	account.FollowersURL = "https://remote.example/users/alice/followers"
	params["collectionId"] = "https://remote.example/users/alice/following"
	if (&Server{}).shouldSynchronizeActivityPubFollowers(account, params) {
		t.Fatal("non-followers collectionId should still be rejected")
	}
	account.FollowersURL = " https://remote.example/users/alice/followers "
	params["collectionId"] = "https://remote.example/users/alice/followers"
	if (&Server{}).shouldSynchronizeActivityPubFollowers(account, params) {
		t.Fatal("stored padded followers_url must not be trimmed for collectionId comparison")
	}
	account.FollowersURL = "https://remote.example/users/alice/followers"
	params["collectionId"] = " https://remote.example/users/alice/followers "
	if (&Server{}).shouldSynchronizeActivityPubFollowers(account, params) {
		t.Fatal("padded collectionId must not be trimmed for Rails raw equality")
	}
	params["collectionId"] = "https://remote.example/users/alice/followers"
	params["url"] = " https://remote.example/users/alice/followers_synchronization "
	if (&Server{}).shouldSynchronizeActivityPubFollowers(account, params) {
		t.Fatal("padded synchronization URL must be rejected like Rails non_matching_uri_hosts?")
	}
}

func TestActivityPubFollowersSynchronizationCollectionPageShapesMatchMastodon43(t *testing.T) {
	tests := []struct {
		name       string
		collection map[string]any
		want       []string
	}{
		{
			name: "ordered collection array",
			collection: map[string]any{
				"type":         "OrderedCollection",
				"orderedItems": []any{"https://local.example/users/alice", map[string]any{"id": "https://local.example/users/bob"}},
			},
			want: []string{"https://local.example/users/alice", "https://local.example/users/bob"},
		},
		{
			name: "collection compacted scalar",
			collection: map[string]any{
				"type":  "CollectionPage",
				"items": "https://local.example/users/alice",
			},
			want: []string{"https://local.example/users/alice"},
		},
		{name: "missing items is an empty complete page", collection: map[string]any{"type": "CollectionPage"}},
		{name: "unsupported page type has no items", collection: map[string]any{"type": "Note", "items": []any{"https://local.example/users/alice"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activityPubCollectionItems(tt.collection)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("items = %#v, want %#v", got, tt.want)
			}
		})
	}

	inline := map[string]any{
		"type":  "Collection",
		"first": map[string]any{"type": "CollectionPage", "items": []any{"https://local.example/users/alice"}},
	}
	page := activityPubCollectionInlineMap(activityJSONLDValue(inline, "first"))
	if page == nil || strings.Join(activityPubCollectionItems(page), "\n") != "https://local.example/users/alice" {
		t.Fatalf("inline first page was not preserved: %#v", page)
	}
}
