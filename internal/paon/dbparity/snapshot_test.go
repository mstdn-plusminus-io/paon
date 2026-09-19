package dbparity

import (
	"crypto/sha256"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/schemacatalog"
)

func TestDiffDetectsDataAndSequenceChangesWithoutRowContents(t *testing.T) {
	want := Snapshot{FormatVersion: 1, Tables: []Table{{Name: "statuses", Rows: 2, SHA256: "expected"}}, Sequences: []Sequence{{Name: "statuses_id_seq", LastValue: 3, IsCalled: true}}}
	got := want
	if difference := Diff(want, got); difference != "" {
		t.Fatal(difference)
	}
	got.Tables = []Table{{Name: "statuses", Rows: 2, SHA256: "changed"}}
	got.Sequences = []Sequence{{Name: "statuses_id_seq", LastValue: 4, IsCalled: true}}
	difference := Diff(want, got)
	for _, required := range []string{"data statuses:", "rows=2", "sha256=changed", "sequence statuses_id_seq:"} {
		if !strings.Contains(difference, required) {
			t.Fatalf("difference missing %q: %s", required, difference)
		}
	}
	got.Tables = nil
	got.Sequences = nil
	if difference := Diff(want, got); !strings.Contains(difference, "data table missing: statuses") || !strings.Contains(difference, "sequence missing: statuses_id_seq") {
		t.Fatal(difference)
	}
}

func TestDiffDetectsRawFunctionSaltAndTimestampWriteCounts(t *testing.T) {
	want := Snapshot{FormatVersion: 1, TimestampIDDefinitionSHA256: "original", NormalizedTimestamps: []NormalizedTimestamp{{Table: "settings", Column: "updated_at", Values: 1}}}
	got := want
	got.TimestampIDDefinitionSHA256 = "changed-salt"
	got.NormalizedTimestamps = []NormalizedTimestamp{{Table: "settings", Column: "updated_at", Values: 2}}
	difference := Diff(want, got)
	for _, required := range []string{"timestamp_id definition:", "normalized audit timestamp counts:"} {
		if !strings.Contains(difference, required) {
			t.Fatalf("difference missing %q: %s", required, difference)
		}
	}
}

func TestNormalizationIncludesOnlyKnownAuditColumns(t *testing.T) {
	catalog := schemacatalog.Catalog{Columns: []schemacatalog.Column{
		{Relation: "users", Name: "created_at"}, {Relation: "users", Name: "updated_at"},
		{Relation: "statuses", Name: "updated_at"}, {Relation: "settings", Name: "updated_at"},
	}}
	for _, test := range []struct {
		table   string
		enabled bool
		want    []string
	}{
		{"users", true, nil},
		{"users", false, nil},
		{"statuses", true, nil},
		{"settings", true, []string{"updated_at"}},
		{"username_blocks", true, nil},
	} {
		if got := normalizationColumns(catalog, test.table, test.enabled); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("normalizationColumns(%s, %t) = %v, want %v", test.table, test.enabled, got, test.want)
		}
	}
}

func TestMigrationWindowRejectsMissingAndReversedBoundaries(t *testing.T) {
	start := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	for _, window := range []MigrationWindow{{}, {StartedAt: start}, {FinishedAt: start}, {StartedAt: start, FinishedAt: start}, {StartedAt: start, FinishedAt: start.Add(-time.Second)}} {
		if window.Validate() == nil {
			t.Fatalf("invalid window accepted: %+v", window)
		}
	}
	if err := (MigrationWindow{StartedAt: start, FinishedAt: start.Add(time.Second)}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRowDigestPreservesBoundariesAndDuplicates(t *testing.T) {
	digest := func(rows ...string) string {
		hash := sha256.New()
		for _, row := range rows {
			writeRow(hash, row)
		}
		return string(hash.Sum(nil))
	}
	if digest("ab", "c") == digest("a", "bc") {
		t.Fatal("row boundaries were lost")
	}
	if digest("a", "a") == digest("a") {
		t.Fatal("duplicate rows were lost")
	}
}
