package api

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestShouldFetchAllRepliesMatchesMastodonCooldownAndInitialWait(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	s := &Server{cfg: config.Config{
		FetchRepliesEnabled:     true,
		FetchRepliesCooldown:    15 * time.Minute,
		FetchRepliesInitialWait: 5 * time.Minute,
		FetchRepliesMaxGlobal:   1000,
		FetchRepliesMaxSingle:   500,
		FetchRepliesMaxPages:    500,
	}}
	remote := models.Account{Domain: sql.NullString{String: "remote.example", Valid: true}}
	eligible := models.Status{
		ID:        1,
		URI:       sql.NullString{String: "https://remote.example/statuses/1", Valid: true},
		CreatedAt: now.Add(-5 * time.Minute),
		Account:   remote,
	}
	if !s.shouldFetchAllReplies(eligible, now) {
		t.Fatal("remote status at the initial-wait boundary should be eligible")
	}
	recent := eligible
	recent.CreatedAt = now.Add(-5*time.Minute + time.Second)
	if s.shouldFetchAllReplies(recent, now) {
		t.Fatal("recent remote status should not be eligible")
	}
	fetched := eligible
	fetched.FetchedRepliesAt = sql.NullTime{Time: now.Add(-15*time.Minute + time.Second), Valid: true}
	if s.shouldFetchAllReplies(fetched, now) {
		t.Fatal("status inside the cooldown should not be eligible")
	}
	fetched.FetchedRepliesAt.Time = now.Add(-15 * time.Minute)
	if !s.shouldFetchAllReplies(fetched, now) {
		t.Fatal("status at the cooldown boundary should be eligible")
	}
	local := eligible
	local.Account.Domain = sql.NullString{}
	if s.shouldFetchAllReplies(local, now) {
		t.Fatal("local status should never be fetched recursively")
	}
}

func TestWalkActivityPubRepliesBoundsDuplicatesAndCycles(t *testing.T) {
	const (
		root   = "https://remote.example/statuses/root"
		child1 = "https://remote.example/statuses/1"
		child2 = "https://other.example/statuses/2"
		child3 = "https://third.example/statuses/3"
	)
	resources := map[string]string{
		child1: activityReplyTestNote(child1, fmt.Sprintf(`{"type":"OrderedCollection","orderedItems":[%q,%q,%q]}`, child2, child3, child1)),
		child2: activityReplyTestNote(child2, "null"),
		child3: activityReplyTestNote(child3, "null"),
	}
	fetcher := func(uri string, _ string) (fetchedActivityResource, error) {
		body, ok := resources[uri]
		if !ok {
			return fetchedActivityResource{}, fmt.Errorf("unexpected fetch %s", uri)
		}
		return fetchedActivityResource{body: []byte(body), contentType: "application/activity+json"}, nil
	}
	filter := func(_ context.Context, _ string, candidates []string, limit int) ([]string, error) {
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		return candidates, nil
	}
	var enqueued []string
	count, pages, err := walkActivityPubRepliesWithFetcher(
		context.Background(),
		root,
		activityObject{Replies: activityCollection{Type: "OrderedCollection", OrderedItems: []string{child1, child2, child1}}},
		fetchRepliesLimits{MaxGlobal: 3, MaxSingle: 10, MaxPages: 10},
		"test",
		fetcher,
		filter,
		func(uri string) { enqueued = append(enqueued, uri) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("reply count = %d, want 3", count)
	}
	if pages != 2 {
		t.Fatalf("pages = %d, want root and child collection pages", pages)
	}
	if want := []string{child1, child2, child3}; !reflect.DeepEqual(enqueued, want) {
		t.Fatalf("enqueued = %#v, want %#v", enqueued, want)
	}
}

func TestCollectActivityPubReplyCollectionEnforcesPageOriginCycleAndLimits(t *testing.T) {
	const (
		status     = "https://remote.example/statuses/1"
		collection = "https://remote.example/statuses/1/replies"
		page1      = "https://remote.example/statuses/1/replies?page=1"
		page2      = "https://remote.example/statuses/1/replies?page=2"
	)
	resources := map[string]string{
		collection: fmt.Sprintf(`{"type":"OrderedCollection","first":%q}`, page1),
		page1:      fmt.Sprintf(`{"id":%q,"type":"OrderedCollectionPage","orderedItems":["https://a.example/1","https://a.example/2"],"next":%q}`, page1, page2),
		page2:      fmt.Sprintf(`{"id":%q,"type":"OrderedCollectionPage","orderedItems":["https://a.example/3"],"next":%q}`, page2, page1),
	}
	fetches := 0
	fetcher := func(uri string, _ string) (fetchedActivityResource, error) {
		fetches++
		body, ok := resources[uri]
		if !ok {
			return fetchedActivityResource{}, fmt.Errorf("unexpected fetch %s", uri)
		}
		return fetchedActivityResource{body: []byte(body), contentType: "application/activity+json"}, nil
	}
	items, pages, err := collectActivityPubReplyCollection(status, activityCollection{ID: collection}, 20, 20, "test", fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://a.example/1", "https://a.example/2", "https://a.example/3"}; !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %#v, want %#v", items, want)
	}
	if pages != 2 || fetches != 3 {
		t.Fatalf("pages/fetches = %d/%d, want 2/3", pages, fetches)
	}

	offOrigin := activityCollection{Type: "OrderedCollection", Next: "https://evil.example/page", NextPresent: true, OrderedItems: []string{"https://a.example/1"}}
	items, pages, err = collectActivityPubReplyCollection(status, offOrigin, 20, 20, "test", func(string, string) (fetchedActivityResource, error) {
		t.Fatal("off-origin collection page was fetched")
		return fetchedActivityResource{}, nil
	})
	if err != nil || pages != 1 || !reflect.DeepEqual(items, []string{"https://a.example/1"}) {
		t.Fatalf("off-origin next result = %#v pages=%d err=%v", items, pages, err)
	}

	items, pages, err = collectActivityPubReplyCollection(status, activityCollection{Type: "OrderedCollection", OrderedItems: []string{"https://a.example/1", "https://a.example/2"}}, 10, 1, "test", fetcher)
	if err != nil || pages != 1 || !reflect.DeepEqual(items, []string{"https://a.example/1"}) {
		t.Fatalf("single limit result = %#v pages=%d err=%v", items, pages, err)
	}
}

func activityReplyTestNote(uri string, replies string) string {
	return fmt.Sprintf(`{"@context":"https://www.w3.org/ns/activitystreams","id":%q,"type":"Note","attributedTo":"https://remote.example/users/alice","content":"reply","replies":%s}`, uri, replies)
}
