package api

import (
	"os"
	"testing"
)

func TestRejectFollowRequestDeliversRailsRejectActivityID(t *testing.T) {
	src, err := os.ReadFile("follow_requests.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "rejectFollowRequest", `s.deliverActivityPubFollowResponse("Reject", *account, *requester, req.ID, string(req.URI))`) {
		t.Fatal("rejectFollowRequest should use the deleted follow_request id for Rails RejectFollowSerializer parity")
	}
}

func TestFollowRequestActionsUseRailsAccountFindTargetVisibility(t *testing.T) {
	src, err := os.ReadFile("follow_requests.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "followRequests", "accounts.suspended_at IS NULL") {
		t.Fatal("follow request index must keep Rails Account.without_suspended filtering")
	}
	if functionBodyContains(t, src, "followRequestAccounts", "requester.SuspendedAt.Valid") {
		t.Fatal("follow request authorize/reject target lookup must match Rails Account.find and not hide suspended requesters")
	}
}
