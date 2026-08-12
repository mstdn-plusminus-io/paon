package api

import (
	"strings"
	"testing"
)

func TestAsyncRefreshIDIsPurposeBoundAndTamperEvident(t *testing.T) {
	const secret = "async-refresh-test-secret"
	id, ok := asyncRefreshID("fasp:account_search:cats", secret)
	if !ok {
		t.Fatal("async refresh ID was not generated")
	}
	key, ok := asyncRefreshKeyFromID(id, secret)
	if !ok || key != "fasp:account_search:cats" {
		t.Fatalf("decoded async refresh = %q, %v", key, ok)
	}
	if _, ok := asyncRefreshKeyFromID(id, "wrong-secret"); ok {
		t.Fatal("async refresh ID verified with another secret")
	}
	tampered := id[:len(id)-1] + strings.ToUpper(id[len(id)-1:])
	if tampered == id {
		tampered = id[:len(id)-1] + "0"
	}
	if _, ok := asyncRefreshKeyFromID(tampered, secret); ok {
		t.Fatal("tampered async refresh ID verified")
	}
}

func TestAsyncRefreshWireShapeKeepsNilResultCount(t *testing.T) {
	entity := asyncRefreshEntity{}
	entity.AsyncRefresh.ID = "id"
	entity.AsyncRefresh.Status = "running"
	if entity.AsyncRefresh.ResultCount != nil {
		t.Fatalf("result_count = %#v", entity.AsyncRefresh.ResultCount)
	}
}
