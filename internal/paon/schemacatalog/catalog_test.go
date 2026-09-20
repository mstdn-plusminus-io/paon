package schemacatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeTimestampIDFunctionOnlyReplacesSingleSalt(t *testing.T) {
	functions := []Function{{
		Name:       "timestamp_id",
		Definition: "md5(table_name || '0123456789abcdef0123456789abcdef' || time_part::text)",
	}}
	if err := normalizeTimestampIDFunction(functions); err != nil {
		t.Fatal(err)
	}
	if got := functions[0].Definition; got != "md5(table_name || '"+TimestampIDSaltPlaceholder+"' || time_part::text)" {
		t.Fatalf("normalized definition = %q", got)
	}

	for _, definition := range []string{
		"md5(table_name || 'too-short' || time_part::text)",
		"md5('0123456789abcdef0123456789abcdef' || 'fedcba9876543210fedcba9876543210')",
	} {
		if err := normalizeTimestampIDFunction([]Function{{Name: "timestamp_id", Definition: definition}}); err == nil {
			t.Fatalf("normalizeTimestampIDFunction accepted %q", definition)
		}
	}
	if err := normalizeTimestampIDFunction([]Function{{Name: "other"}}); err == nil {
		t.Fatal("normalizeTimestampIDFunction accepted a catalog without timestamp_id")
	}
}

func TestDiffDetectsEveryCompatibilitySection(t *testing.T) {
	base := representativeCatalog()
	manifest, err := BuildManifest(base)
	if err != nil {
		t.Fatal(err)
	}
	golden := Golden{Catalog: manifest}
	tests := []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{"schema", func(value *Catalog) { value.PostgreSQLMajor++ }, "schema differs"},
		{"extensions", func(value *Catalog) { value.Extensions[0].Version = "2.0" }, "extensions differs"},
		{"relations", func(value *Catalog) { value.Relations[0].Kind = "v" }, "relations differs"},
		{"physical column order", func(value *Catalog) { value.Columns[0].Position++ }, "columns differs"},
		{"column type", func(value *Catalog) { value.Columns[0].Type = "integer" }, "columns differs"},
		{"column nullability", func(value *Catalog) { value.Columns[0].NotNull = false }, "columns differs"},
		{"column default", func(value *Catalog) { changed := "1"; value.Columns[0].Default = &changed }, "columns differs"},
		{"dropped columns", func(value *Catalog) { value.DroppedColumns[0].Position++ }, "dropped_columns differs"},
		{"indexes", func(value *Catalog) { value.Indexes[0].Definition += " WHERE false" }, "indexes differs"},
		{"foreign key name", func(value *Catalog) { value.Constraints[0].Name = "wrong_fk" }, "constraints differs"},
		{"foreign key action", func(value *Catalog) { value.Constraints[0].DeleteAction = "a" }, "constraints differs"},
		{"primary key details", func(value *Catalog) { value.Constraints[1].Columns = []string{"other"} }, "constraints differs"},
		{"views", func(value *Catalog) { value.Views[0].Definition = " SELECT 2;" }, "views differs"},
		{"functions", func(value *Catalog) { value.Functions[0].Definition += " -- changed" }, "functions differs"},
		{"sequences", func(value *Catalog) { value.Sequences[0].Increment++ }, "sequences differs"},
		{"triggers", func(value *Catalog) { value.Triggers[0].Enabled = "D" }, "triggers differs"},
		{"rules", func(value *Catalog) { value.Rules[0].Definition += " " }, "rules differs"},
		{"policies", func(value *Catalog) { value.Policies[0].Command = "r" }, "policies differs"},
		{"types", func(value *Catalog) { value.Types[0].EnumLabels = append(value.Types[0].EnumLabels, "c") }, "types differs"},
		{"migration markers", func(value *Catalog) { value.MigrationVersions = value.MigrationVersions[:1] }, "migration_versions differs"},
		{"Active Record metadata", func(value *Catalog) { changed := "wrong"; value.ActiveRecordMetadata[0].Value = &changed }, "active_record_metadata differs"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := cloneCatalog(t, base)
			test.mutate(&got)
			difference := Diff(base, got)
			if !strings.Contains(difference, test.want) {
				t.Fatalf("Diff() = %q, want substring %q", difference, test.want)
			}
			goldenDifference, err := DiffGolden(golden, got)
			if err != nil {
				t.Fatal(err)
			}
			if goldenDifference == "" {
				t.Fatal("DiffGolden did not detect the mutation")
			}
		})
	}
	if difference := Diff(base, cloneCatalog(t, base)); difference != "" {
		t.Fatalf("Diff(equal) = %q", difference)
	}
}

func TestGoldenJSONIsStrictAndDeterministic(t *testing.T) {
	golden := Golden{
		Source: Source{
			Name:                    "test",
			Path:                    "fresh",
			Tag:                     "v4.3.23",
			Commit:                  strings.Repeat("a", 40),
			SchemaVersion:           "20241007071624",
			SchemaSHA256:            strings.Repeat("b", 64),
			PostgreSQLVersionNumber: 150018,
		},
	}
	manifest, err := BuildManifest(representativeCatalog())
	if err != nil {
		t.Fatal(err)
	}
	golden.Catalog = manifest
	first, err := MarshalGolden(golden)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseGolden(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalGolden(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("golden JSON did not marshal deterministically")
	}
	unknown := strings.Replace(string(first), `"format_version": 1`, `"format_version": 1, "unknown": true`, 1)
	if _, err := ParseGolden([]byte(unknown)); err == nil {
		t.Fatal("ParseGolden accepted an unknown field")
	}
}

func TestCommittedMastodonGoldensAreStrictAndVersioned(t *testing.T) {
	tests := []struct {
		file          string
		tag           string
		commit        string
		version       string
		schemaSHA256  string
		snowflakeSHA  string
		postgresMajor int
	}{
		{"mastodon_v4_2_19_fresh_catalog_pg14.json", "v4.2.19", "a58a2b5fafb33e6ced71884419f0da5f1b398ae7", "20230907150100", "9a7802fb98a941c4a02099abfb7396ea37f95b2dbe6926b858101c8b2c2a6b13", "9a83a0107673ab6af58ebb5aef203a5717fd0132f8612f2bb5fd4cc2871f693c", 14},
		{"mastodon_v4_3_23_fresh_catalog_pg14.json", "v4.3.23", "efb25b6aa2014201b04f25c390ad1f516a3ff52d", "20241007071624", "72d9974a13a17dbe17eef6b6d3f4617ca7e69620ae95aacf877e38910a3dd855", "6aa3884d6ec5f6b8a489a1dcf9989586f5049c383b2ece694865baefbe1fe032", 14},
		{"mastodon_v4_2_19_to_v4_3_23_catalog_pg14.json", "v4.3.23", "efb25b6aa2014201b04f25c390ad1f516a3ff52d", "20241007071624", "72d9974a13a17dbe17eef6b6d3f4617ca7e69620ae95aacf877e38910a3dd855", "6aa3884d6ec5f6b8a489a1dcf9989586f5049c383b2ece694865baefbe1fe032", 14},
		{"mastodon_v4_2_19_fresh_catalog.json", "v4.2.19", "a58a2b5fafb33e6ced71884419f0da5f1b398ae7", "20230907150100", "9a7802fb98a941c4a02099abfb7396ea37f95b2dbe6926b858101c8b2c2a6b13", "9a83a0107673ab6af58ebb5aef203a5717fd0132f8612f2bb5fd4cc2871f693c", 15},
		{"mastodon_v4_3_23_fresh_catalog.json", "v4.3.23", "efb25b6aa2014201b04f25c390ad1f516a3ff52d", "20241007071624", "72d9974a13a17dbe17eef6b6d3f4617ca7e69620ae95aacf877e38910a3dd855", "6aa3884d6ec5f6b8a489a1dcf9989586f5049c383b2ece694865baefbe1fe032", 15},
		{"mastodon_v4_2_19_to_v4_3_23_catalog.json", "v4.3.23", "efb25b6aa2014201b04f25c390ad1f516a3ff52d", "20241007071624", "72d9974a13a17dbe17eef6b6d3f4617ca7e69620ae95aacf877e38910a3dd855", "6aa3884d6ec5f6b8a489a1dcf9989586f5049c383b2ece694865baefbe1fe032", 15},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "migrate", "testdata", test.file))
			if err != nil {
				t.Fatal(err)
			}
			golden, err := ParseGolden(data)
			if err != nil {
				t.Fatal(err)
			}
			if golden.Source.Tag != test.tag ||
				golden.Source.Commit != test.commit ||
				golden.Source.SchemaVersion != test.version ||
				golden.Source.SchemaSHA256 != test.schemaSHA256 ||
				golden.Source.SnowflakeSHA256 != test.snowflakeSHA {
				t.Fatalf("source provenance = %#v", golden.Source)
			}
			if golden.Catalog.PostgreSQLMajor != test.postgresMajor {
				t.Fatalf("PostgreSQL major = %d", golden.Catalog.PostgreSQLMajor)
			}
			canonical, err := MarshalGolden(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != string(data) {
				t.Fatal("committed golden is not in canonical generated form")
			}
		})
	}
}

func cloneCatalog(t *testing.T, value Catalog) Catalog {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned Catalog
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func representativeCatalog() Catalog {
	zero := "0"
	comment := "comment"
	metadataValue := "production"
	return Catalog{
		Schema:          "public",
		PostgreSQLMajor: 15,
		Extensions:      []Extension{{Name: "plpgsql", Schema: "pg_catalog", Version: "1.0"}},
		Relations:       []Relation{{Name: "children", Kind: "r", Persistence: "p", Options: []string{}}},
		Columns: []Column{{
			Relation: "children", Position: 1, Name: "id", Type: "bigint", NotNull: true,
			Default: &zero, Options: []string{}, Comment: &comment,
		}},
		DroppedColumns: []DroppedColumn{{Relation: "children", Position: 2, Name: "........pg.dropped.2........"}},
		Indexes:        []Index{{Table: "children", Name: "children_pkey", Primary: true, Unique: true, Valid: true, Ready: true, Immediate: true, Keys: []string{"id"}, Options: []string{}, Definition: "CREATE UNIQUE INDEX children_pkey ON public.children USING btree (id)"}},
		Constraints: []Constraint{
			{Table: "children", Name: "fk_rails_123", Type: "f", Columns: []string{"parent_id"}, ReferencedTable: "parents", ReferencedColumns: []string{"id"}, DeleteAction: "c", UpdateAction: "a", MatchType: "s", Validated: true, Definition: "FOREIGN KEY (parent_id) REFERENCES parents(id) ON DELETE CASCADE"},
			{Table: "children", Name: "children_pkey", Type: "p", Columns: []string{"id"}, ReferencedColumns: []string{}, BackingIndex: "children_pkey", Validated: true, Definition: "PRIMARY KEY (id)"},
		},
		Views:                []View{{Name: "child_view", Kind: "v", Populated: true, Definition: " SELECT 1;"}},
		Functions:            []Function{{Name: "timestamp_id", IdentityArguments: "table_name text", Definition: "function '" + TimestampIDSaltPlaceholder + "'"}},
		Sequences:            []Sequence{{Name: "children_id_seq", Type: "bigint", Start: 1, Minimum: 1, Maximum: 100, Increment: 1, Cache: 1, OwnedByTable: "children", OwnedByColumn: "id"}},
		Triggers:             []Trigger{{Table: "children", Name: "trigger", Enabled: "O", Function: "public.trigger()"}},
		Rules:                []Rule{{Table: "children", Name: "rule", Enabled: "O", Definition: "CREATE RULE"}},
		Policies:             []Policy{{Table: "children", Name: "policy", Permissive: true, Command: "*", Roles: []string{"public"}}},
		Types:                []Type{{Name: "kind", Kind: "e", Category: "E", EnumLabels: []string{"a", "b"}, DomainConstraints: []string{}}},
		MigrationVersions:    []string{"20230907150100", "20241007071624"},
		ActiveRecordMetadata: []ActiveRecordEntry{{Key: "environment", Value: &metadataValue}},
	}
}
