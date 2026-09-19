//go:build integration

package dbparity

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// This test only executes SELECT statements and can safely use the shared
// integration PostgreSQL instance without creating or modifying any rows.
func TestPostgreSQLDigestNormalizesOnlyWindowAuditValues(t *testing.T) {
	dsn := os.Getenv("PAON_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	capture := func(normalize bool, clock time.Time, old, value string) (string, []int64) {
		t.Helper()
		var columns []string
		if normalize {
			columns = []string{"created_at", "updated_at"}
		}
		query := `WITH parameters AS (SELECT $1::timestamptz, $2::timestamptz), test_rows(id, created_at, updated_at, value) AS (VALUES
			(1, $5::timestamp, NULL::timestamp, 'preserved'),
			(2, $3::timestamp, $4::timestamp, $6::text),
			(2, $3::timestamp, $4::timestamp, $6::text),
			(3, timestamp '2028-01-01', timestamp '2028-01-02', 'future')) ` +
			strings.Replace(tableDigestQuery("fixture", columns), `FROM public."fixture" AS row_data`, `FROM test_rows AS row_data`, 1)
		rows, err := db.Query(query, start, start.Add(10*time.Second), clock, clock.Add(time.Second), old, value)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		hash := sha256.New()
		counts := make([]int64, len(columns))
		rowCount := 0
		previous := ""
		for rows.Next() {
			var digest string
			flags := make([]bool, len(columns))
			destinations := []any{&digest}
			for index := range flags {
				destinations = append(destinations, &flags[index])
			}
			if err := rows.Scan(destinations...); err != nil {
				t.Fatal(err)
			}
			if len(digest) != 64 || digest < previous {
				t.Fatalf("server did not return sorted SHA256 digests: %q after %q", digest, previous)
			}
			previous = digest
			writeRow(hash, digest)
			for index, changed := range flags {
				if changed {
					counts[index]++
				}
			}
			rowCount++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if rowCount != 4 {
			t.Fatalf("duplicate or NULL-containing row lost: got %d rows", rowCount)
		}
		return fmt.Sprintf("%x", hash.Sum(nil)), counts
	}
	want, counts := capture(true, start.Add(time.Second), "2020-01-01", "private payload")
	if !reflect.DeepEqual(counts, []int64{2, 2}) {
		t.Fatalf("normalized value counts = %v, want [2 2]", counts)
	}
	if got, _ := capture(true, start.Add(3*time.Second), "2020-01-01", "private payload"); got != want {
		t.Fatal("different migration clock values inside the window changed the normalized digest")
	}
	if got, _ := capture(true, start.Add(time.Second), "2020-01-02", "private payload"); got == want {
		t.Fatal("changed timestamp outside the migration window was hidden")
	}
	if got, _ := capture(true, start.Add(time.Second), "2020-01-01", "changed payload"); got == want {
		t.Fatal("changed non-timestamp data was hidden")
	}
	first, _ := capture(false, start.Add(time.Second), "2020-01-01", "private payload")
	second, _ := capture(false, start.Add(3*time.Second), "2020-01-01", "private payload")
	if first == second {
		t.Fatal("raw no-op comparison hid changed audit timestamps")
	}
}
