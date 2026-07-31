package api

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIndexingConstantsMatchRailsScheduler(t *testing.T) {
	if indexingWorkerInterval != time.Minute {
		t.Fatalf("indexingWorkerInterval = %s", indexingWorkerInterval)
	}
	if indexingImportBatchSize != 1000 {
		t.Fatalf("indexingImportBatchSize = %d", indexingImportBatchSize)
	}
	if indexingScanBatchSize != 10*indexingImportBatchSize {
		t.Fatalf("indexingScanBatchSize = %d", indexingScanBatchSize)
	}
}

func TestIndexingWorkerUsesRailsQueueShape(t *testing.T) {
	src, err := os.ReadFile("indexing_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runIndexingWorker", `s.processQueuedMeiliIndexes(ctx)`},
		{"processQueuedMeiliIndexes", `!s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == ""`},
		{"queuedMeiliIndexes", `IndexName: "AccountsIndex"`},
		{"queuedMeiliIndexes", `IndexName: "TagsIndex"`},
		{"queuedMeiliIndexes", `IndexName: "PublicStatusesIndex"`},
		{"queuedMeiliIndexes", `IndexName: "StatusesIndex"`},
		{"processQueuedMeiliIndex", `"SSCAN", key, cursor, "COUNT", strconv.Itoa(indexingScanBatchSize)`},
		{"processQueuedMeiliIndex", `stringBatches(members, indexingImportBatchSize)`},
		{"processQueuedMeiliIndex", `append([]string{"SREM", key}, batch...)`},
		{"indexQueuedMeiliAccounts", `s.meiliIndexAccountBestEffort(ctx, id)`},
		{"indexQueuedMeiliTags", `s.meiliIndexTagsBestEffort(ctx, ids)`},
		{"indexQueuedMeiliStatuses", `s.meiliIndexStatusBestEffort(ctx, id)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runIndexingWorker)") {
		t.Fatal("StartBackgroundWorkers does not start indexing worker")
	}
}

func TestIndexingWorkerDrainsRailsQueueOnStartup(t *testing.T) {
	src, err := os.ReadFile("indexing_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "runIndexingWorker")
	startupDrain := strings.Index(body, `s.processQueuedMeiliIndexes(ctx)`)
	ticker := strings.Index(body, `time.NewTicker(indexingWorkerInterval)`)
	if startupDrain < 0 || ticker < 0 || startupDrain > ticker {
		t.Fatal("indexing worker must drain Rails Chewy queues before waiting for the first tick")
	}
}

func TestRedisScanResultParsesSSCANResponse(t *testing.T) {
	cursor, members, ok := redisScanResult([]any{"12", []any{"1", "2"}})
	if !ok || cursor != "12" || !reflect.DeepEqual(members, []string{"1", "2"}) {
		t.Fatalf("scan result = cursor %q members %#v ok %v", cursor, members, ok)
	}
	if _, _, ok := redisScanResult([]any{"12", []any{int64(1)}}); ok {
		t.Fatal("non-string member should not parse")
	}
}

func TestIndexingHelpersBatchAndParseIDs(t *testing.T) {
	batches := stringBatches([]string{"1", "2", "3"}, 2)
	if !reflect.DeepEqual(batches, [][]string{{"1", "2"}, {"3"}}) {
		t.Fatalf("batches = %#v", batches)
	}
	ids := parsePositiveInt64s([]string{"1", "bad", "0", "-2", "3"})
	if !reflect.DeepEqual(ids, []int64{1, 3}) {
		t.Fatalf("ids = %#v", ids)
	}
}
