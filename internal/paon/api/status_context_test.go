package api

import (
	"database/sql"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestSortStatusesByIDsUsesTreeOrder(t *testing.T) {
	statuses := []models.Status{{ID: 3}, {ID: 1}, {ID: 2}}
	sortStatusesByIDs(statuses, []int64{1, 2, 3})

	if statuses[0].ID != 1 || statuses[1].ID != 2 || statuses[2].ID != 3 {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestPromoteSelfReplyStatusesMovesSelfRepliesBeforeOthers(t *testing.T) {
	statuses := []models.Status{
		{ID: 1, AccountID: 10, InReplyToAccountID: sql.NullInt64{Int64: 10, Valid: true}},
		{ID: 2, AccountID: 20, InReplyToAccountID: sql.NullInt64{Int64: 10, Valid: true}},
		{ID: 3, AccountID: 10, InReplyToAccountID: sql.NullInt64{Int64: 10, Valid: true}},
		{ID: 4, AccountID: 30, InReplyToAccountID: sql.NullInt64{Int64: 10, Valid: true}},
	}

	promoteSelfReplyStatuses(statuses)
	got := []int64{statuses[0].ID, statuses[1].ID, statuses[2].ID, statuses[3].ID}
	want := []int64{1, 3, 2, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", got, want)
		}
	}
}

func TestTreeRowIDs(t *testing.T) {
	got := treeRowIDs([]treeIDRow{{ID: 9}, {ID: 10}})
	if len(got) != 2 || got[0] != 9 || got[1] != 10 {
		t.Fatalf("ids = %#v", got)
	}
}
