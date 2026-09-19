// Package dbparity captures database migration evidence without exporting row data.
// Callers must quiesce writers while capturing schema, data, and sequence state.
package dbparity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/schemacatalog"
)

type Snapshot struct {
	FormatVersion               int                   `json:"format_version"`
	Catalog                     schemacatalog.Catalog `json:"catalog"`
	Tables                      []Table               `json:"tables"`
	Sequences                   []Sequence            `json:"sequence_values"`
	TimestampIDDefinitionSHA256 string                `json:"timestamp_id_definition_sha256"`
	NormalizedTimestamps        []NormalizedTimestamp `json:"normalized_timestamps"`
}

// MigrationWindow permits comparison of audit timestamps written by two
// independent migration executions. It never changes database rows. Callers
// must omit it when checking that rerunning a migration changes nothing.
type MigrationWindow struct {
	StartedAt  time.Time
	FinishedAt time.Time
}

func (window MigrationWindow) Validate() error {
	if window.StartedAt.IsZero() || window.FinishedAt.IsZero() || !window.FinishedAt.After(window.StartedAt) {
		return fmt.Errorf("migration timestamp window must have a nonzero start and a later finish")
	}
	return nil
}

type Options struct {
	MigrationWindow *MigrationWindow
}

type NormalizedTimestamp struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Values int64  `json:"values"`
}

type Table struct {
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}

type Sequence struct {
	Name      string `json:"name"`
	LastValue int64  `json:"last_value"`
	IsCalled  bool   `json:"is_called"`
}

func Capture(ctx context.Context, db *sql.DB) (Snapshot, error) {
	return CaptureWithOptions(ctx, db, Options{})
}

func CaptureWithOptions(ctx context.Context, db *sql.DB, options Options) (Snapshot, error) {
	if options.MigrationWindow != nil {
		if err := options.MigrationWindow.Validate(); err != nil {
			return Snapshot{}, err
		}
	}
	catalog, err := schemacatalog.Capture(ctx, db, "public")
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{FormatVersion: 1, Catalog: catalog, Tables: []Table{}, Sequences: []Sequence{}, NormalizedTimestamps: []NormalizedTimestamp{}}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return Snapshot{}, err
	}
	// The catalog normalizes per-instance snowflake salts for fresh-schema
	// comparisons. Restored copies share a salt, so parity must also compare
	// the complete, unnormalized function definition.
	var timestampIDDefinition string
	if err := tx.QueryRowContext(ctx, `SELECT pg_get_functiondef('public.timestamp_id(text)'::regprocedure)`).Scan(&timestampIDDefinition); err != nil {
		return Snapshot{}, fmt.Errorf("read exact timestamp_id definition: %w", err)
	}
	snapshot.TimestampIDDefinitionSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(timestampIDDefinition)))
	for _, relation := range catalog.Relations {
		// Ordinary views may expose live pg_stat_statements and are already
		// compared by definition. Stored materialized views are data contracts.
		if relation.Kind != "r" && relation.Kind != "p" && relation.Kind != "m" {
			continue
		}
		if !relation.Populated {
			continue
		}
		columns := normalizationColumns(catalog, relation.Name, options.MigrationWindow != nil)
		query := tableDigestQuery(relation.Name, columns)
		var arguments []any
		if len(columns) != 0 {
			arguments = []any{options.MigrationWindow.StartedAt.UTC(), options.MigrationWindow.FinishedAt.UTC()}
		}
		rows, err := tx.QueryContext(ctx, query, arguments...)
		if err != nil {
			return Snapshot{}, fmt.Errorf("read table %s: %w", relation.Name, err)
		}
		digest := sha256.New()
		table := Table{Name: relation.Name}
		counts := make([]int64, len(columns))
		for rows.Next() {
			var row string
			changed := make([]bool, len(columns))
			destinations := []any{&row}
			for index := range changed {
				destinations = append(destinations, &changed[index])
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				return Snapshot{}, fmt.Errorf("read table %s row: %w", relation.Name, err)
			}
			writeRow(digest, row)
			table.Rows++
			for index, normalized := range changed {
				if normalized {
					counts[index]++
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Snapshot{}, fmt.Errorf("read table %s rows: %w", relation.Name, err)
		}
		table.SHA256 = hex.EncodeToString(digest.Sum(nil))
		snapshot.Tables = append(snapshot.Tables, table)
		for index, column := range columns {
			snapshot.NormalizedTimestamps = append(snapshot.NormalizedTimestamps, NormalizedTimestamp{Table: relation.Name, Column: column, Values: counts[index]})
		}
	}
	for _, sequence := range catalog.Sequences {
		value := Sequence{Name: sequence.Name}
		if err := tx.QueryRowContext(ctx, `SELECT last_value, is_called FROM public.`+quoteIdentifier(sequence.Name)).Scan(&value.LastValue, &value.IsCalled); err != nil {
			return Snapshot{}, fmt.Errorf("read sequence %s: %w", sequence.Name, err)
		}
		snapshot.Sequences = append(snapshot.Sequences, value)
	}
	return snapshot, tx.Commit()
}

// Diff reports names/counts/digests only; staging row contents never enter logs.
func Diff(want, got Snapshot) string {
	var differences []string
	if want.FormatVersion != 1 || got.FormatVersion != 1 {
		return "unsupported database parity snapshot format"
	}
	if difference := schemacatalog.Diff(want.Catalog, got.Catalog); difference != "" {
		differences = append(differences, difference)
	}
	if want.TimestampIDDefinitionSHA256 != got.TimestampIDDefinitionSHA256 {
		differences = append(differences, fmt.Sprintf("timestamp_id definition: expected sha256=%s; actual sha256=%s", want.TimestampIDDefinitionSHA256, got.TimestampIDDefinitionSHA256))
	}
	if !reflect.DeepEqual(want.NormalizedTimestamps, got.NormalizedTimestamps) {
		differences = append(differences, fmt.Sprintf("normalized audit timestamp counts: expected %v; actual %v", want.NormalizedTimestamps, got.NormalizedTimestamps))
	}
	if !reflect.DeepEqual(want.Tables, got.Tables) {
		wanted := make(map[string]Table, len(want.Tables))
		for _, table := range want.Tables {
			wanted[table.Name] = table
		}
		for _, table := range got.Tables {
			previous, found := wanted[table.Name]
			if !found || previous != table {
				differences = append(differences, fmt.Sprintf("data %s: expected rows=%d sha256=%s; actual rows=%d sha256=%s", table.Name, previous.Rows, previous.SHA256, table.Rows, table.SHA256))
			}
			delete(wanted, table.Name)
		}
		for _, table := range want.Tables {
			if _, found := wanted[table.Name]; found {
				differences = append(differences, "data table missing: "+table.Name)
			}
		}
	}
	if !reflect.DeepEqual(want.Sequences, got.Sequences) {
		wanted := make(map[string]Sequence, len(want.Sequences))
		for _, sequence := range want.Sequences {
			wanted[sequence.Name] = sequence
		}
		for _, sequence := range got.Sequences {
			if previous, found := wanted[sequence.Name]; !found || previous != sequence {
				differences = append(differences, fmt.Sprintf("sequence %s: expected (%d,%t); actual (%d,%t)", sequence.Name, previous.LastValue, previous.IsCalled, sequence.LastValue, sequence.IsCalled))
			}
			delete(wanted, sequence.Name)
		}
		for _, sequence := range want.Sequences {
			if _, found := wanted[sequence.Name]; found {
				differences = append(differences, "sequence missing: "+sequence.Name)
			}
		}
	}
	return strings.Join(differences, "\n")
}

func quoteIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Only these audit columns are written from the migration execution clock in
// the pinned staging fixture (which has no OTP migration candidates).
// Every other field, including all timestamps outside the recorded window,
// remains byte-for-byte part of the JSON row digest.
func normalizationColumns(catalog schemacatalog.Catalog, table string, enabled bool) []string {
	if !enabled {
		return nil
	}
	allowed := map[string][]string{
		"notification_policies": {"created_at", "updated_at"},
		"username_blocks":       {"created_at", "updated_at"},
		"settings":              {"created_at", "updated_at"},
	}
	var columns []string
	for _, name := range allowed[table] {
		for _, column := range catalog.Columns {
			if column.Relation == table && column.Name == name {
				columns = append(columns, name)
				break
			}
		}
	}
	return columns
}

func normalizedRowExpression(columns []string) (string, []string) {
	expression := "to_jsonb(row_data)"
	var predicates []string
	for _, column := range columns {
		predicate := "row_data." + quoteIdentifier(column) + " >= ($1::timestamptz AT TIME ZONE 'UTC') AND row_data." + quoteIdentifier(column) + " <= ($2::timestamptz AT TIME ZONE 'UTC')"
		predicates = append(predicates, predicate)
		expression = "jsonb_set(" + expression + ", '{" + column + "}', CASE WHEN " + predicate + " THEN '\"__PAON_MIGRATION_TIMESTAMP__\"'::jsonb ELSE to_jsonb(row_data)->'" + column + "' END)"
	}
	return expression, predicates
}

// The table digest is SHA256 over sorted, length-framed lowercase hex SHA256
// row digests. Each row digest covers the UTF-8 PostgreSQL JSONB representation.
// Sorting compact digests avoids transferring or sorting large private rows;
// duplicate rows remain separate inputs and continue to affect the digest.
func tableDigestQuery(table string, columns []string) string {
	expression, predicates := normalizedRowExpression(columns)
	query := `SELECT * FROM (SELECT encode(sha256(convert_to(` + expression + `::text, 'UTF8')), 'hex') AS row_sha256`
	for index, predicate := range predicates {
		query += fmt.Sprintf(", COALESCE(%s, false) AS normalized_%d", predicate, index)
	}
	return query + ` FROM public.` + quoteIdentifier(table) + ` AS row_data) AS row_digests ORDER BY row_sha256 COLLATE "C"`
}

func writeRow(writer io.Writer, row string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(row)))
	writer.Write(length[:])
	io.WriteString(writer, row)
}
