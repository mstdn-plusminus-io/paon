package api

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestFeaturedCollectionSyncHasConflictSafeStatsAndBoundedWorker(t *testing.T) {
	signatureSource, err := os.ReadFile("activitypub_signature.go")
	if err != nil {
		t.Fatal(err)
	}
	helper := mustFunctionBody(t, string(signatureSource), "createActivityPubAccountStatIfMissing")
	if !strings.Contains(helper, "ON CONFLICT (account_id) DO NOTHING") {
		t.Fatal("ActivityPub account stat creation must be atomic")
	}
	lockTimeout := mustFunctionBody(t, string(signatureSource), "setActivityPubTransactionLockTimeout")
	if !strings.Contains(lockTimeout, `set_config('lock_timeout', '5s', true)`) {
		t.Fatal("ActivityPub database lock waits must fail fast")
	}
	actorUpsert := mustFunctionBody(t, string(signatureSource), "upsertRemoteActivityActorDBForRequest")
	if !strings.Contains(actorUpsert, "tx.Omit(clause.Associations).Create(&account)") {
		t.Fatal("remote actor creation must not auto-save account_stats associations")
	}

	workerSource, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	handler := mustFunctionBody(t, string(workerSource), "handleAsynqFeaturedCollectionSync")
	for _, want := range []string{
		"s.featuredSyncContext(ctx, asynqTaskFeaturedCollectionSync, p.AccountID)",
		"s.syncActivityPubFeaturedCollectionNowWithContext(workerCtx",
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("featured collection handler missing %q", want)
		}
	}

	collectionSource, err := os.ReadFile("activitypub_featured_collection.go")
	if err != nil {
		t.Fatal(err)
	}
	collectionSync := mustFunctionBody(t, string(collectionSource), "syncRemoteStatusPinsFromActivityCollectionWithContext")
	if !strings.Contains(collectionSync, "s.statusFromActivityURIWithContext(ctx, uri)") {
		t.Fatal("featured collection status lookup must use the worker context")
	}
	if !strings.Contains(collectionSync, "s.db.WithContext(ctx).Transaction") {
		t.Fatal("featured collection pin reconciliation must use the worker context")
	}

	inboxSource, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	statusLookup := mustFunctionBody(t, string(inboxSource), "statusFromActivityURIWithContext")
	if !strings.Contains(statusLookup, `Where("statuses.uri = ? AND statuses.deleted_at IS NULL", lookupURI)`) {
		t.Fatal("ActivityPub status lookup must use the indexed URI predicate")
	}
	if strings.Contains(statusLookup, " OR ") || strings.Contains(statusLookup, `"url = ?"`) {
		t.Fatal("ActivityPub status lookup must not combine indexed URI lookup with an unindexed URL scan")
	}
	worker := mustFunctionBody(t, string(workerSource), "featuredSyncContext")
	for _, want := range []string{
		"context.WithTimeout(parent, featuredCollectionWorkerTimeout)",
		`"featured_sync:"+taskType+":"+strconv.FormatInt(accountID, 10)`,
	} {
		if !strings.Contains(worker, want) {
			t.Fatalf("featured sync worker missing %q", want)
		}
	}
}

func TestRemoteStatusPinChangesReconcilesWithoutReinsertingExistingPins(t *testing.T) {
	tests := []struct {
		name       string
		existing   []int64
		desired    []int64
		wantAdd    []int64
		wantRemove []int64
	}{
		{
			name:     "unchanged pins",
			existing: []int64{10, 20},
			desired:  []int64{10, 20},
		},
		{
			name:       "add and remove",
			existing:   []int64{10, 20},
			desired:    []int64{20, 30},
			wantAdd:    []int64{30},
			wantRemove: []int64{10},
		},
		{
			name:    "deduplicate remote collection",
			desired: []int64{30, 30, 40, 30},
			wantAdd: []int64{30, 40},
		},
		{
			name:       "empty collection removes all pins",
			existing:   []int64{10, 20},
			wantRemove: []int64{10, 20},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotAdd, gotRemove := remoteStatusPinChanges(test.existing, test.desired)
			if !reflect.DeepEqual(gotAdd, test.wantAdd) {
				t.Fatalf("toAdd = %#v, want %#v", gotAdd, test.wantAdd)
			}
			if !reflect.DeepEqual(gotRemove, test.wantRemove) {
				t.Fatalf("toRemove = %#v, want %#v", gotRemove, test.wantRemove)
			}
		})
	}
}
