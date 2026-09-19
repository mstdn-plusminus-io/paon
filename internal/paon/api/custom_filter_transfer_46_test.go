package api

import (
	"strings"
	"testing"
)

func TestMastodon46CustomFilterTransferParsesJSONEnvelope(t *testing.T) {
	rows, err := parseCustomFilterImport([]byte(`{"custom_filters":[{"title":"Media","context":["home"],"action":"blur","keywords_attributes":[],"statuses":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["title"] != "Media" || rows[0]["action"] != "blur" {
		t.Fatalf("custom filter rows = %#v", rows)
	}
	if _, err := parseCustomFilterImport([]byte(`{"bookmarks":[]}`)); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("missing custom_filters error = %v", err)
	}
}

func TestMastodon46CustomFilterTransferPreservesBlurAction(t *testing.T) {
	for input, want := range map[int]string{0: "warn", 1: "hide", 2: "blur", 99: "warn"} {
		if got := customFilterTransferAction(input); got != want {
			t.Fatalf("action %d = %q, want %q", input, got, want)
		}
	}
}
