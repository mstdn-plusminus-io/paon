package api

import (
	"os"
	"testing"
)

func TestReblogStatusUsesSingleActivityPubDistributionPath(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("ReadFile(server.go) error = %v", err)
	}
	if !functionBodyContains(t, src, "reblogStatus", `_ = s.enqueueOrDeliverActivityPubDistribution(*createdStatus)`) {
		t.Fatal("reblogStatus must enqueue the StatusReachFinder-equivalent distribution")
	}
	if functionBodyContains(t, src, "reblogStatus", `deliverActivityPubReblogToOriginalAuthor`) {
		t.Fatal("reblogStatus must not separately deliver Announce to the original author")
	}
}

func TestReblogRemovalUsesSingleActivityPubDistributionPath(t *testing.T) {
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatalf("ReadFile(asynq_workers.go) error = %v", err)
	}
	if !functionBodyContains(t, src, "deliverActivityPubRemoval", `return s.deliverActivityPubStatusToFollowers(status, undo)`) {
		t.Fatal("reblog removal must distribute Undo through the status reach set")
	}
	if functionBodyContains(t, src, "deliverActivityPubRemoval", `deliverActivityPubReblogToOriginalAuthor`) {
		t.Fatal("reblog removal must not separately deliver Undo to the original author")
	}
}
