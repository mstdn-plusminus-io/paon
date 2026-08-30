package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalStatusDeleteQueuesBeforeResponseWithoutSynchronousRemoval(t *testing.T) {
	if localStatusDeleteEnqueueTimeout != time.Second {
		t.Fatalf("local delete enqueue timeout = %s, want 1s", localStatusDeleteEnqueueTimeout)
	}
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "deleteStatus")
	for _, want := range []string{
		`redraft := !formBoolValue(c.QueryParam("delete_media"))`,
		`asynqRemovalPayload{StatusID: status.ID, Redraft: redraft}`,
		`context.WithTimeout(c.Request().Context(), localStatusDeleteEnqueueTimeout)`,
		`s.enqueueRemovalTaskContext(enqueueCtx, removal, asynq.TaskID(removalTaskID(status.ID)))`,
		`http.StatusServiceUnavailable`,
		`status.DeletedAt = sql.NullTime`,
		`return c.JSON(http.StatusOK`,
	} {
		if !sourceContains([]byte(body), want) {
			t.Fatalf("deleteStatus missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"acquireStatusDistributionRedisLock",
		"discardStatusRowsForRemoval",
		"applyDiscardedStatusRowSideEffects",
		"applyDeletedStatusRemovalSideEffects",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("deleteStatus must not synchronously call %s", forbidden)
		}
	}
	if enqueueIndex, responseIndex := strings.Index(body, "s.enqueueRemovalTaskContext"), strings.Index(body, "return c.JSON"); enqueueIndex < 0 || responseIndex < 0 || enqueueIndex > responseIndex {
		t.Fatal("deleteStatus must accept the removal task before returning the response")
	}
}

func TestRemovalTaskUsesStableStatusIDAndAcceptsDuplicates(t *testing.T) {
	if got := removalTaskID(42); got != "removal-42" {
		t.Fatalf("removal task ID = %q, want removal-42", got)
	}
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "enqueueRemovalTaskContext")
	for _, want := range []string{`append(taskOptions, options...)...`, `if asynqEnqueueAccepted(err)`} {
		if !sourceContains([]byte(body), want) {
			t.Fatalf("enqueueRemovalTaskContext missing %q", want)
		}
	}
	deleteSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, deleteSrc, "deleteStatus", `asynq.TaskID(removalTaskID(status.ID))`) {
		t.Fatal("local delete must deduplicate concurrent requests by status ID")
	}
}

func TestRemovalWorkerOwnsDeferredLocalStatusDeletion(t *testing.T) {
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "handleAsynqRemoval")
	for _, want := range []string{
		`s.acquireStatusDistributionRedisLock(ctx, p.StatusID)`,
		`s.deleteStatusRecord(ctx, p.StatusID, now)`,
		`s.publishStatusAndReblogDeletesForIDs(ctx, s.db, []int64{p.StatusID})`,
		`s.deliverActivityPubRemoval(status)`,
	} {
		if !sourceContains([]byte(body), want) {
			t.Fatalf("handleAsynqRemoval missing deferred deletion step %q", want)
		}
	}
}
