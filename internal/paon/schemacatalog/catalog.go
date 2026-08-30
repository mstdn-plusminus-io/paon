package schemacatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
)

const FormatVersion = 1

const TimestampIDSaltPlaceholder = "__MASTODON_TIMESTAMP_ID_SALT__"

var timestampIDSaltPattern = regexp.MustCompile(`'[0-9a-f]{32}'`)

type Golden struct {
	FormatVersion int             `json:"format_version"`
	Source        Source          `json:"source"`
	Catalog       CatalogManifest `json:"catalog"`
}

type Source struct {
	Name                    string `json:"name"`
	Path                    string `json:"path"`
	Tag                     string `json:"tag"`
	Commit                  string `json:"commit"`
	SchemaVersion           string `json:"schema_version"`
	SchemaSHA256            string `json:"schema_sha256"`
	SnowflakeSHA256         string `json:"snowflake_sha256,omitempty"`
	PostgreSQLVersionNumber int    `json:"postgresql_version_number"`
}

type CatalogManifest struct {
	Schema          string          `json:"schema"`
	PostgreSQLMajor int             `json:"postgresql_major"`
	SHA256          string          `json:"sha256"`
	Sections        []SectionDigest `json:"sections"`
}

type SectionDigest struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	SHA256 string `json:"sha256"`
}

// Catalog contains deterministic, migration-relevant public-schema state.
// It deliberately omits instance-specific OIDs, owners/ACLs, physical storage
// statistics, current sequence values, and PostgreSQL-generated internal
// referential-integrity triggers. Function source is otherwise exact; the
// per-database timestamp_id salt is the only normalized definition value.
type Catalog struct {
	Schema               string              `json:"schema"`
	PostgreSQLMajor      int                 `json:"postgresql_major"`
	Extensions           []Extension         `json:"extensions"`
	Relations            []Relation          `json:"relations"`
	Columns              []Column            `json:"columns"`
	DroppedColumns       []DroppedColumn     `json:"dropped_columns"`
	Indexes              []Index             `json:"indexes"`
	Constraints          []Constraint        `json:"constraints"`
	Views                []View              `json:"views"`
	Functions            []Function          `json:"functions"`
	Sequences            []Sequence          `json:"sequences"`
	Triggers             []Trigger           `json:"triggers"`
	Rules                []Rule              `json:"rules"`
	Policies             []Policy            `json:"policies"`
	Types                []Type              `json:"types"`
	MigrationVersions    []string            `json:"migration_versions"`
	ActiveRecordMetadata []ActiveRecordEntry `json:"active_record_metadata"`
}

type Extension struct {
	Name        string `json:"name"`
	Schema      string `json:"schema"`
	Version     string `json:"version"`
	Relocatable bool   `json:"relocatable"`
}

type Relation struct {
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Persistence      string   `json:"persistence"`
	AccessMethod     string   `json:"access_method"`
	ReplicaIdentity  string   `json:"replica_identity"`
	RowSecurity      bool     `json:"row_security"`
	ForceRowSecurity bool     `json:"force_row_security"`
	Populated        bool     `json:"populated"`
	Options          []string `json:"options"`
	Comment          *string  `json:"comment"`
}

type Column struct {
	Relation         string   `json:"relation"`
	Position         int      `json:"position"`
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	NotNull          bool     `json:"not_null"`
	Default          *string  `json:"default"`
	Identity         string   `json:"identity"`
	Generated        string   `json:"generated"`
	Collation        string   `json:"collation"`
	Storage          string   `json:"storage"`
	Compression      string   `json:"compression"`
	StatisticsTarget int      `json:"statistics_target"`
	HasMissing       bool     `json:"has_missing"`
	MissingValue     *string  `json:"missing_value"`
	Options          []string `json:"options"`
	Comment          *string  `json:"comment"`
}

type DroppedColumn struct {
	Relation string `json:"relation"`
	Position int    `json:"position"`
	Name     string `json:"name"`
}

type Index struct {
	Table            string   `json:"table"`
	Name             string   `json:"name"`
	AccessMethod     string   `json:"access_method"`
	Tablespace       string   `json:"tablespace"`
	Unique           bool     `json:"unique"`
	Primary          bool     `json:"primary"`
	Exclusion        bool     `json:"exclusion"`
	Immediate        bool     `json:"immediate"`
	Valid            bool     `json:"valid"`
	Ready            bool     `json:"ready"`
	Clustered        bool     `json:"clustered"`
	ReplicaIdentity  bool     `json:"replica_identity"`
	NullsNotDistinct bool     `json:"nulls_not_distinct"`
	KeyCount         int      `json:"key_count"`
	AttributeCount   int      `json:"attribute_count"`
	Keys             []string `json:"keys"`
	Predicate        *string  `json:"predicate"`
	Expressions      *string  `json:"expressions"`
	Definition       string   `json:"definition"`
	Constraint       string   `json:"constraint"`
	Options          []string `json:"options"`
	Comment          *string  `json:"comment"`
}

type Constraint struct {
	Table             string   `json:"table"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Columns           []string `json:"columns"`
	ReferencedTable   string   `json:"referenced_table"`
	ReferencedColumns []string `json:"referenced_columns"`
	UpdateAction      string   `json:"update_action"`
	DeleteAction      string   `json:"delete_action"`
	MatchType         string   `json:"match_type"`
	Deferrable        bool     `json:"deferrable"`
	Deferred          bool     `json:"deferred"`
	Validated         bool     `json:"validated"`
	NoInherit         bool     `json:"no_inherit"`
	Local             bool     `json:"local"`
	InheritanceCount  int      `json:"inheritance_count"`
	BackingIndex      string   `json:"backing_index"`
	ParentConstraint  string   `json:"parent_constraint"`
	Definition        string   `json:"definition"`
	Comment           *string  `json:"comment"`
}

type View struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Populated  bool    `json:"populated"`
	Definition string  `json:"definition"`
	Comment    *string `json:"comment"`
}

type Function struct {
	Name              string   `json:"name"`
	IdentityArguments string   `json:"identity_arguments"`
	ResultType        string   `json:"result_type"`
	Kind              string   `json:"kind"`
	Language          string   `json:"language"`
	Volatility        string   `json:"volatility"`
	Parallel          string   `json:"parallel"`
	Strict            bool     `json:"strict"`
	SecurityDefiner   bool     `json:"security_definer"`
	Leakproof         bool     `json:"leakproof"`
	ReturnsSet        bool     `json:"returns_set"`
	Cost              float64  `json:"cost"`
	Rows              float64  `json:"rows"`
	Support           string   `json:"support"`
	Binary            string   `json:"binary"`
	Configuration     []string `json:"configuration"`
	Definition        string   `json:"definition"`
	Comment           *string  `json:"comment"`
}

type Sequence struct {
	Name          string  `json:"name"`
	Persistence   string  `json:"persistence"`
	Type          string  `json:"type"`
	Start         int64   `json:"start"`
	Minimum       int64   `json:"minimum"`
	Maximum       int64   `json:"maximum"`
	Increment     int64   `json:"increment"`
	Cache         int64   `json:"cache"`
	Cycle         bool    `json:"cycle"`
	OwnedByTable  string  `json:"owned_by_table"`
	OwnedByColumn string  `json:"owned_by_column"`
	Comment       *string `json:"comment"`
}

type Trigger struct {
	Table      string  `json:"table"`
	Name       string  `json:"name"`
	Enabled    string  `json:"enabled"`
	Type       int     `json:"type"`
	Constraint string  `json:"constraint"`
	Function   string  `json:"function"`
	Definition string  `json:"definition"`
	Comment    *string `json:"comment"`
}

type Rule struct {
	Table      string  `json:"table"`
	Name       string  `json:"name"`
	Enabled    string  `json:"enabled"`
	Event      string  `json:"event"`
	Instead    bool    `json:"instead"`
	Definition string  `json:"definition"`
	Comment    *string `json:"comment"`
}

type Policy struct {
	Table      string   `json:"table"`
	Name       string   `json:"name"`
	Permissive bool     `json:"permissive"`
	Command    string   `json:"command"`
	Roles      []string `json:"roles"`
	Using      *string  `json:"using"`
	WithCheck  *string  `json:"with_check"`
}

type Type struct {
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	Category          string   `json:"category"`
	BaseType          string   `json:"base_type"`
	NotNull           bool     `json:"not_null"`
	Default           *string  `json:"default"`
	Collation         string   `json:"collation"`
	EnumLabels        []string `json:"enum_labels"`
	DomainConstraints []string `json:"domain_constraints"`
	RangeSubtype      string   `json:"range_subtype"`
	Comment           *string  `json:"comment"`
}

type ActiveRecordEntry struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

func ParseGolden(data []byte) (Golden, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var golden Golden
	if err := decoder.Decode(&golden); err != nil {
		return Golden{}, fmt.Errorf("decode schema catalog golden: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Golden{}, errors.New("decode schema catalog golden: trailing JSON value")
		}
		return Golden{}, fmt.Errorf("decode schema catalog golden trailer: %w", err)
	}
	if golden.FormatVersion != FormatVersion {
		return Golden{}, fmt.Errorf("schema catalog format version = %d, want %d", golden.FormatVersion, FormatVersion)
	}
	if strings.TrimSpace(golden.Source.Name) == "" || strings.TrimSpace(golden.Source.Path) == "" || strings.TrimSpace(golden.Source.Tag) == "" || strings.TrimSpace(golden.Source.SchemaVersion) == "" {
		return Golden{}, errors.New("schema catalog golden is missing source identity")
	}
	if !isLowerHex(golden.Source.Commit, 40) || !isSHA256(golden.Source.SchemaSHA256) || (golden.Source.SnowflakeSHA256 != "" && !isSHA256(golden.Source.SnowflakeSHA256)) {
		return Golden{}, errors.New("schema catalog golden has invalid source commit or digest")
	}
	if golden.Catalog.Schema == "" || golden.Catalog.PostgreSQLMajor == 0 || !isSHA256(golden.Catalog.SHA256) {
		return Golden{}, errors.New("schema catalog golden has invalid catalog identity or digest")
	}
	if golden.Source.PostgreSQLVersionNumber/10000 != golden.Catalog.PostgreSQLMajor {
		return Golden{}, fmt.Errorf("schema catalog source PostgreSQL version %d does not match catalog major %d", golden.Source.PostgreSQLVersionNumber, golden.Catalog.PostgreSQLMajor)
	}
	expectedSections := catalogSectionNames()
	if len(golden.Catalog.Sections) != len(expectedSections) {
		return Golden{}, fmt.Errorf("schema catalog golden has %d sections, want %d", len(golden.Catalog.Sections), len(expectedSections))
	}
	for index, section := range golden.Catalog.Sections {
		if section.Name != expectedSections[index] {
			return Golden{}, fmt.Errorf("schema catalog golden section %d = %q, want %q", index, section.Name, expectedSections[index])
		}
		if section.Count < 0 || !isSHA256(section.SHA256) {
			return Golden{}, fmt.Errorf("schema catalog golden section %q has invalid count or digest", section.Name)
		}
	}
	return golden, nil
}

func MarshalGolden(golden Golden) ([]byte, error) {
	golden.FormatVersion = FormatVersion
	encoded, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode schema catalog golden: %w", err)
	}
	return append(encoded, '\n'), nil
}

func BuildManifest(catalog Catalog) (CatalogManifest, error) {
	sections := catalogSections(catalog)
	manifest := CatalogManifest{
		Schema:          catalog.Schema,
		PostgreSQLMajor: catalog.PostgreSQLMajor,
		Sections:        make([]SectionDigest, 0, len(sections)),
	}
	for _, section := range sections {
		encoded, err := json.Marshal(section.value)
		if err != nil {
			return CatalogManifest{}, fmt.Errorf("encode schema catalog section %s: %w", section.name, err)
		}
		manifest.Sections = append(manifest.Sections, SectionDigest{
			Name:   section.name,
			Count:  reflect.ValueOf(section.value).Len(),
			SHA256: fmt.Sprintf("%x", sha256.Sum256(encoded)),
		})
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return CatalogManifest{}, fmt.Errorf("encode complete schema catalog: %w", err)
	}
	manifest.SHA256 = fmt.Sprintf("%x", sha256.Sum256(encoded))
	return manifest, nil
}

func DiffGolden(want Golden, got Catalog) (string, error) {
	manifest, err := BuildManifest(got)
	if err != nil {
		return "", err
	}
	if want.Catalog.Schema != manifest.Schema || want.Catalog.PostgreSQLMajor != manifest.PostgreSQLMajor {
		return fmt.Sprintf("catalog identity differs: want schema=%s PostgreSQL=%d, got schema=%s PostgreSQL=%d", want.Catalog.Schema, want.Catalog.PostgreSQLMajor, manifest.Schema, manifest.PostgreSQLMajor), nil
	}
	if len(want.Catalog.Sections) != len(manifest.Sections) {
		return fmt.Sprintf("catalog section count differs: want %d, got %d", len(want.Catalog.Sections), len(manifest.Sections)), nil
	}
	for index := range want.Catalog.Sections {
		wantSection := want.Catalog.Sections[index]
		gotSection := manifest.Sections[index]
		if wantSection != gotSection {
			return fmt.Sprintf("catalog section %q differs: want count=%d sha256=%s, got count=%d sha256=%s", wantSection.Name, wantSection.Count, wantSection.SHA256, gotSection.Count, gotSection.SHA256), nil
		}
	}
	if want.Catalog.SHA256 != manifest.SHA256 {
		return fmt.Sprintf("complete catalog digest differs: want %s, got %s", want.Catalog.SHA256, manifest.SHA256), nil
	}
	return "", nil
}

func Diff(want Catalog, got Catalog) string {
	sections := []struct {
		name string
		want any
		got  any
	}{
		{"schema", struct {
			Schema          string
			PostgreSQLMajor int
		}{want.Schema, want.PostgreSQLMajor}, struct {
			Schema          string
			PostgreSQLMajor int
		}{got.Schema, got.PostgreSQLMajor}},
		{"extensions", want.Extensions, got.Extensions},
		{"relations", want.Relations, got.Relations},
		{"columns", want.Columns, got.Columns},
		{"dropped_columns", want.DroppedColumns, got.DroppedColumns},
		{"indexes", want.Indexes, got.Indexes},
		{"constraints", want.Constraints, got.Constraints},
		{"views", want.Views, got.Views},
		{"functions", want.Functions, got.Functions},
		{"sequences", want.Sequences, got.Sequences},
		{"triggers", want.Triggers, got.Triggers},
		{"rules", want.Rules, got.Rules},
		{"policies", want.Policies, got.Policies},
		{"types", want.Types, got.Types},
		{"migration_versions", want.MigrationVersions, got.MigrationVersions},
		{"active_record_metadata", want.ActiveRecordMetadata, got.ActiveRecordMetadata},
	}

	var differences []string
	for _, section := range sections {
		if reflect.DeepEqual(section.want, section.got) {
			continue
		}
		differences = append(differences, formatSectionDifference(section.name, section.want, section.got))
	}
	return strings.Join(differences, "\n")
}

type catalogSection struct {
	name  string
	value any
}

func catalogSections(catalog Catalog) []catalogSection {
	return []catalogSection{
		{"extensions", catalog.Extensions},
		{"relations", catalog.Relations},
		{"columns", catalog.Columns},
		{"dropped_columns", catalog.DroppedColumns},
		{"indexes", catalog.Indexes},
		{"constraints", catalog.Constraints},
		{"views", catalog.Views},
		{"functions", catalog.Functions},
		{"sequences", catalog.Sequences},
		{"triggers", catalog.Triggers},
		{"rules", catalog.Rules},
		{"policies", catalog.Policies},
		{"types", catalog.Types},
		{"migration_versions", catalog.MigrationVersions},
		{"active_record_metadata", catalog.ActiveRecordMetadata},
	}
}

func catalogSectionNames() []string {
	sections := catalogSections(Catalog{})
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		names = append(names, section.name)
	}
	return names
}

func isSHA256(value string) bool {
	return isLowerHex(value, 64)
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func formatSectionDifference(name string, want any, got any) string {
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantLines := strings.Split(string(wantJSON), "\n")
	gotLines := strings.Split(string(gotJSON), "\n")
	limit := len(wantLines)
	if len(gotLines) < limit {
		limit = len(gotLines)
	}
	line := 0
	for line < limit && wantLines[line] == gotLines[line] {
		line++
	}
	if line == limit && len(wantLines) == len(gotLines) {
		return name + " differs"
	}
	start := line - 2
	if start < 0 {
		start = 0
	}
	wantEnd := line + 3
	if wantEnd > len(wantLines) {
		wantEnd = len(wantLines)
	}
	gotEnd := line + 3
	if gotEnd > len(gotLines) {
		gotEnd = len(gotLines)
	}
	return fmt.Sprintf("%s differs near JSON line %d\n-want:\n%s\n+got:\n%s",
		name,
		line+1,
		strings.Join(wantLines[start:wantEnd], "\n"),
		strings.Join(gotLines[start:gotEnd], "\n"),
	)
}

func normalizeTimestampIDFunction(functions []Function) error {
	found := 0
	for index := range functions {
		if functions[index].Name != "timestamp_id" {
			continue
		}
		found++
		matches := timestampIDSaltPattern.FindAllStringIndex(functions[index].Definition, -1)
		if len(matches) != 1 {
			return fmt.Errorf("timestamp_id function contains %d 32-hex quoted salts, want exactly 1", len(matches))
		}
		functions[index].Definition = timestampIDSaltPattern.ReplaceAllString(functions[index].Definition, "'"+TimestampIDSaltPlaceholder+"'")
	}
	if found != 1 {
		return fmt.Errorf("public schema contains %d timestamp_id functions, want exactly 1", found)
	}
	return nil
}
