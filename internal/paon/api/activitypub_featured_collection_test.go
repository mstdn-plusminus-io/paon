package api

import (
	"reflect"
	"testing"
)

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
