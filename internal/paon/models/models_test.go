package models

import (
	"database/sql"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestAccountStatAssociationIsReadOnly(t *testing.T) {
	accountSchema, err := schema.Parse(&Account{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	field := accountSchema.LookUpField("AccountStat")
	if field == nil {
		t.Fatal("AccountStat association is missing")
	}
	if field.Creatable || field.Updatable {
		t.Fatalf("AccountStat association permissions = creatable:%t updatable:%t, want read-only", field.Creatable, field.Updatable)
	}
	if !field.Readable {
		t.Fatal("AccountStat association must remain readable for Preload")
	}
	if accountSchema.Relationships.Relations["AccountStat"] == nil {
		t.Fatal("AccountStat association must remain available to Preload")
	}
}

func TestAccountFieldsUsesJSONValueForPostgreSQLJSONB(t *testing.T) {
	field, ok := reflect.TypeOf(Account{}).FieldByName("Fields")
	if !ok {
		t.Fatal("Account.Fields is missing")
	}
	if field.Type != reflect.TypeOf(JSONValue{}) {
		t.Fatalf("Account.Fields type = %v, want JSONValue so GORM does not bind JSONB as bytea", field.Type)
	}
}

func TestInt64ArrayScanPostgresLiteral(t *testing.T) {
	var ids Int64Array
	if err := ids.Scan("{3,1,2}"); err != nil {
		t.Fatal(err)
	}
	want := []int64{3, 1, 2}
	if len(ids) != len(want) {
		t.Fatalf("len = %d, want %d", len(ids), len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %#v, want %#v", ids, want)
		}
	}
}

func TestInt64ArrayValuePostgresLiteral(t *testing.T) {
	got, err := Int64Array{4, 5}.Value()
	if err != nil {
		t.Fatal(err)
	}
	if got != "{4,5}" {
		t.Fatalf("Value = %#v", got)
	}
}

func TestInt64ArrayValuePreservesNilForNullableRailsArrays(t *testing.T) {
	got, err := Int64Array(nil).Value()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("Value = %#v, want nil", got)
	}
}

func TestNullSafeStringScansNullableRailsColumns(t *testing.T) {
	var value NullSafeString = "stale"
	if err := value.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if value != "" {
		t.Fatalf("Scan(nil) = %q, want empty string", value)
	}
	if err := value.Scan([]byte("read write")); err != nil {
		t.Fatal(err)
	}
	if value != "read write" {
		t.Fatalf("Scan([]byte) = %q", value)
	}
	driverValue, err := value.Value()
	if err != nil {
		t.Fatal(err)
	}
	if driverValue != "read write" {
		t.Fatalf("Value = %#v", driverValue)
	}
}

func TestNullableRailsStringColumnsUseNullSafeString(t *testing.T) {
	modelTypes := tableColumnTypesFromModels([]any{
		Account{},
		AccountDomainBlock{},
		Notification{},
		PollVote{},
	})
	for _, check := range []struct {
		table  string
		column string
	}{
		{table: "accounts", column: "devices_url"},
		{table: "account_domain_blocks", column: "domain"},
		{table: "notifications", column: "type"},
		{table: "poll_votes", column: "uri"},
	} {
		if got := modelTypes[check.table][check.column]; got != reflect.TypeOf(NullSafeString("")) {
			t.Fatalf("%s.%s type = %s, want NullSafeString", check.table, check.column, got)
		}
	}
}

func TestStringArrayScanPostgresLiteral(t *testing.T) {
	var values StringArray
	if err := values.Scan(`{"yes","no, maybe","quote \" ok"}`); err != nil {
		t.Fatal(err)
	}
	want := []string{"yes", "no, maybe", `quote " ok`}
	if len(values) != len(want) {
		t.Fatalf("len = %d, want %d", len(values), len(want))
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("values = %#v, want %#v", values, want)
		}
	}
}

func TestStringArrayValuePreservesNilForNullableRailsArrays(t *testing.T) {
	got, err := StringArray(nil).Value()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("Value = %#v, want nil", got)
	}

	empty, err := StringArray{}.Value()
	if err != nil {
		t.Fatal(err)
	}
	if empty != "{}" {
		t.Fatalf("empty Value = %#v, want {}", empty)
	}
}

func TestUnavailableDomainTableName(t *testing.T) {
	if (UnavailableDomain{}).TableName() != "unavailable_domains" {
		t.Fatalf("table = %q", (UnavailableDomain{}).TableName())
	}
}

func TestAccountDeletionRequestTableName(t *testing.T) {
	if (AccountDeletionRequest{}).TableName() != "account_deletion_requests" {
		t.Fatalf("table = %q", (AccountDeletionRequest{}).TableName())
	}
}

func TestReportNoteTableName(t *testing.T) {
	if (ReportNote{}).TableName() != "report_notes" {
		t.Fatalf("table = %q", (ReportNote{}).TableName())
	}
}

func TestAdditionalRailsCompatibilityTableNames(t *testing.T) {
	tests := []struct {
		name  string
		table string
	}{
		{name: (AccountTag{}).TableName(), table: "accounts_tags"},
		{name: (Identity{}).TableName(), table: "identities"},
		{name: (Import{}).TableName(), table: "imports"},
		{name: (FollowRecommendationSuppression{}).TableName(), table: "follow_recommendation_suppressions"},
		{name: (PgHeroSpaceStat{}).TableName(), table: "pghero_space_stats"},
		{name: (PreviewCardStatus{}).TableName(), table: "preview_cards_statuses"},
		{name: (StatusTag{}).TableName(), table: "statuses_tags"},
		{name: (Tombstone{}).TableName(), table: "tombstones"},
	}
	for _, tt := range tests {
		if tt.name != tt.table {
			t.Fatalf("table = %q, want %q", tt.name, tt.table)
		}
	}
}

func TestCoreRailsSchemaColumnsRemainMapped(t *testing.T) {
	tests := []struct {
		name  string
		model any
		cols  []string
	}{
		{
			name:  "accounts_tags",
			model: AccountTag{},
			cols:  []string{"account_id", "tag_id"},
		},
		{
			name:  "identities",
			model: Identity{},
			cols:  []string{"provider", "uid", "created_at", "updated_at", "user_id"},
		},
		{
			name:  "imports",
			model: Import{},
			cols: []string{
				"type",
				"approved",
				"created_at",
				"updated_at",
				"data_file_name",
				"data_content_type",
				"data_file_size",
				"data_updated_at",
				"account_id",
				"overwrite",
			},
		},
		{
			name:  "preview_cards_statuses",
			model: PreviewCardStatus{},
			cols:  []string{"preview_card_id", "status_id"},
		},
		{
			name:  "statuses_tags",
			model: StatusTag{},
			cols:  []string{"status_id", "tag_id"},
		},
		{
			name:  "tombstones",
			model: Tombstone{},
			cols:  []string{"account_id", "uri", "created_at", "updated_at", "by_moderator"},
		},
		{
			name:  "users",
			model: User{},
			cols: []string{
				"admin",
				"last_emailed_at",
				"moderator",
				"sign_in_token",
				"sign_in_token_sent_at",
				"skip_sign_in_token",
			},
		},
		{
			name:  "oauth_access_tokens",
			model: OAuthAccessToken{},
			cols:  []string{"last_used_ip"},
		},
		{
			name:  "oauth_applications",
			model: OAuthApplication{},
			cols: []string{
				"uid",
				"secret",
				"scopes",
				"created_at",
				"updated_at",
				"superapp",
				"owner_type",
				"owner_id",
				"confidential",
			},
		},
		{
			name:  "pghero_space_stats",
			model: PgHeroSpaceStat{},
			cols:  []string{"database", "schema", "relation", "size", "captured_at"},
		},
		{
			name:  "status_stats",
			model: StatusStat{},
			cols:  []string{"created_at", "updated_at"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns := gormColumns(tt.model)
			for _, col := range tt.cols {
				if !columns[col] {
					t.Fatalf("%T missing gorm column %q", tt.model, col)
				}
			}
		})
	}
}

func primaryKeyColumnsFromModel(model any) []string {
	typ := reflect.TypeOf(model)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	columns := []string{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("gorm")
		if !strings.Contains(tag, "primaryKey") {
			continue
		}
		for _, part := range strings.Split(tag, ";") {
			if strings.HasPrefix(part, "column:") {
				columns = append(columns, strings.TrimPrefix(part, "column:"))
			}
		}
	}
	return columns
}

func gormColumns(model any) map[string]bool {
	typ := reflect.TypeOf(model)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	columns := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("gorm")
		for _, part := range strings.Split(tag, ";") {
			if strings.HasPrefix(part, "column:") {
				columns[strings.TrimPrefix(part, "column:")] = true
			}
		}
	}
	return columns
}

func tableColumnTypesFromModels(models []any) map[string]map[string]reflect.Type {
	out := map[string]map[string]reflect.Type{}
	for _, model := range models {
		typ := reflect.TypeOf(model)
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		tableNamer, ok := reflect.New(typ).Interface().(interface{ TableName() string })
		if !ok {
			continue
		}
		columns := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			for _, part := range strings.Split(field.Tag.Get("gorm"), ";") {
				if strings.HasPrefix(part, "column:") {
					columns[strings.TrimPrefix(part, "column:")] = field.Type
				}
			}
		}
		out[tableNamer.TableName()] = columns
	}
	return out
}

func specialColumnsFromSchema(schema string) map[string][]schemaSpecialColumn {
	out := map[string][]schemaSpecialColumn{}
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		table := match[1]
		for _, line := range strings.Split(match[2], "\n") {
			if col := schemaSpecialColumnFromLine(line); col.name != "" {
				out[table] = append(out[table], col)
			}
		}
	}
	return out
}

func schemaSpecialColumnFromLine(line string) schemaSpecialColumn {
	match := schemaSpecialColumnPattern.FindStringSubmatch(line)
	if len(match) == 0 {
		return schemaSpecialColumn{}
	}
	if strings.Contains(line, "array: true") {
		return schemaSpecialColumn{name: match[2], kind: match[1] + "[]"}
	}
	if match[1] == "json" || match[1] == "jsonb" || match[1] == "inet" {
		return schemaSpecialColumn{name: match[2], kind: match[1]}
	}
	return schemaSpecialColumn{}
}

func compatibleSpecialType(kind string, got reflect.Type) bool {
	switch kind {
	case "bigint[]":
		return got == reflect.TypeOf(Int64Array{})
	case "string[]", "text[]":
		return got == reflect.TypeOf(StringArray{})
	case "json", "jsonb":
		return got == reflect.TypeOf(JSONValue{}) || got == reflect.TypeOf([]byte{})
	case "inet":
		return got == reflect.TypeOf("") || got == reflect.TypeOf(sql.NullString{})
	default:
		return false
	}
}

type schemaSpecialColumn struct {
	name string
	kind string
}

func tableNamesFromSchema(schema string) []string {
	matches := schemaTablePattern.FindAllStringSubmatch(schema, -1)
	tables := make([]string, 0, len(matches))
	for _, match := range matches {
		tables = append(tables, match[1])
	}
	return tables
}

func railsSchemaTableBlock(t *testing.T, schema string, table string) string {
	t.Helper()
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		if match[1] == table {
			return match[0]
		}
	}
	t.Fatalf("Rails schema missing table %q", table)
	return ""
}

func tableNamesFromModels(src string) map[string]bool {
	matches := modelTablePattern.FindAllStringSubmatch(src, -1)
	tables := map[string]bool{}
	for _, match := range matches {
		tables[match[1]] = true
	}
	return tables
}

func tableColumnsFromSchema(schema string) map[string][]string {
	matches := schemaTablePattern.FindAllStringSubmatch(schema, -1)
	out := map[string][]string{}
	for _, match := range matches {
		table := match[1]
		createLine := match[0][:strings.Index(match[0], " do |t|")]
		columns := []string{}
		if !strings.Contains(createLine, "primary_key:") {
			columns = append(columns, "id")
		}
		for _, line := range strings.Split(match[2], "\n") {
			if col := schemaColumnName(line); col != "" {
				columns = append(columns, col)
			}
		}
		out[table] = columns
	}
	return out
}

func tablePrimaryKeysFromSchema(schema string) map[string][]string {
	matches := schemaTablePattern.FindAllStringSubmatch(schema, -1)
	out := map[string][]string{}
	for _, match := range matches {
		table := match[1]
		createLine := match[0][:strings.Index(match[0], " do |t|")]
		pkMatch := schemaPrimaryKeyPattern.FindStringSubmatch(createLine)
		if len(pkMatch) == 0 {
			out[table] = []string{"id"}
			continue
		}
		keys := []string{}
		for _, keyMatch := range schemaPrimaryKeyColumnPattern.FindAllStringSubmatch(pkMatch[1], -1) {
			keys = append(keys, keyMatch[1])
		}
		out[table] = keys
	}
	return out
}

func schemaColumnName(line string) string {
	match := schemaColumnPattern.FindStringSubmatch(line)
	if len(match) == 0 {
		return ""
	}
	return match[1]
}

func tableColumnsFromModels(src string) map[string]map[string]bool {
	matches := modelStructTablePattern.FindAllStringSubmatch(src, -1)
	out := map[string]map[string]bool{}
	for _, match := range matches {
		if match[1] != match[3] {
			continue
		}
		cols := map[string]bool{}
		for _, colMatch := range gormColumnPattern.FindAllStringSubmatch(match[2], -1) {
			cols[colMatch[1]] = true
		}
		out[match[4]] = cols
	}
	return out
}

var (
	schemaTablePattern            = regexp.MustCompile(`(?ms)create_table "([^"]+)".*? do \|t\|(.*?)\n  end`)
	schemaColumnPattern           = regexp.MustCompile(`^\s+t\.(?:bigint|binary|boolean|datetime|float|inet|integer|json|jsonb|string|text) "([^"]+)"`)
	schemaSpecialColumnPattern    = regexp.MustCompile(`^\s+t\.(bigint|inet|json|jsonb|string|text) "([^"]+)"`)
	modelTablePattern             = regexp.MustCompile(`func \(\w+\) TableName\(\) string \{\s*return "([^"]+)"\s*\}`)
	modelStructTablePattern       = regexp.MustCompile(`(?ms)type\s+(\w+)\s+struct\s*\{(.*?)\n\}\n\nfunc \((\w+)\) TableName\(\) string \{\s*return "([^"]+)"\s*\}`)
	gormColumnPattern             = regexp.MustCompile("`gorm:\"[^\"]*column:([^\";]+)[^\"]*\"`")
	schemaPrimaryKeyPattern       = regexp.MustCompile(`primary_key:\s*\[([^\]]+)\]`)
	schemaPrimaryKeyColumnPattern = regexp.MustCompile(`"([^"]+)"`)
)
