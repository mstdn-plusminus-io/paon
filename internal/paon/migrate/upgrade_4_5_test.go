package migrate

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
)

var expectedMastodon45UpgradeVersions = []string{
	"20250717003848", "20250805075010", "20250819100545", "20250820084312", "20250828222741",
	"20250902221600", "20250909100506", "20250911163952", "20250912082651", "20250924170259",
	"20251002140103", "20251007100627", "20251007100813", "20251007142305", "20251023210145",
}

func TestMastodon45MigrationInventoryIsExactAndPhaseDisjoint(t *testing.T) {
	got := []string{}
	seen := map[string]UpgradePhase{}
	for _, phase := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract} {
		for _, version := range mastodon45PhaseVersions(phase) {
			if previous, exists := seen[version]; exists {
				t.Fatalf("migration %s appears in both %s and %s", version, previous, phase)
			}
			seen[version] = phase
			got = append(got, version)
			if !paonschema.Mastodon45UpgradeVersionKnown(version) {
				t.Fatalf("phase inventory contains unreviewed version %s", version)
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), expectedMastodon45UpgradeVersions...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Mastodon 4.5 phase inventory = %#v, want %#v", got, want)
	}
	if len(got) != 15 || len(got) != paonschema.Mastodon45UpgradeVersionCount() {
		t.Fatalf("Mastodon 4.5 marker count = %d, schema inventory = %d, want 15", len(got), paonschema.Mastodon45UpgradeVersionCount())
	}
	if seen[CurrentSchemaVersion] != UpgradePhaseContract {
		t.Fatalf("final marker %s phase = %s, want contract", CurrentSchemaVersion, seen[CurrentSchemaVersion])
	}
	if len(mastodon45ValidateSteps()) != 0 {
		t.Fatal("Mastodon 4.5 must not invent a schema_migrations marker for Paon's validation phase")
	}
}

func TestMastodon45ReplacementIndexesAreExpandedBeforeContract(t *testing.T) {
	expand := strings.Join([]string{
		`CREATE INDEX IF NOT EXISTS index_quotes_on_account_id_and_quoted_account_id_and_id`,
		`CREATE INDEX IF NOT EXISTS index_quotes_on_quoted_status_id_and_id`,
	}, "\n")
	contract := strings.Join(mastodon45ContractSteps()[0].statements, "\n")
	for _, token := range strings.Split(expand, "\n") {
		if !strings.Contains(contract, token) {
			t.Fatalf("contract does not preserve replacement index %q", token)
		}
	}
	if !strings.Contains(contract, "DROP INDEX IF EXISTS index_quotes_on_quoted_status_id") {
		t.Fatal("contract does not remove the v4.4 quote index")
	}
}

func TestMastodon45FreshSchemaContainsExactCatalogDelta(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	for _, token := range []string{
		`"following_url" character varying DEFAULT '' NOT NULL`,
		`"id_scheme" integer DEFAULT 1`,
		`"parent_status_id" bigint`,
		`"parent_account_id" bigint`,
		`"delivery_last_failed_at" timestamp(6) without time zone`,
		`"quotes_count" bigint DEFAULT 0 NOT NULL`,
		`CREATE TABLE "username_blocks"`,
		`index_quotes_on_account_id_and_quoted_account_id_and_id`,
		`index_quotes_on_quoted_status_id_and_id`,
		`index_follows_on_target_account_id_and_account_id`,
		`index_statuses_on_conversation_id`,
		`('20251023210145')`,
		`801766beefdd9b1d55fe6f8bf3bed91392aebab1`,
	} {
		if !strings.Contains(schema, token) {
			t.Errorf("fresh schema is missing %q", token)
		}
	}
	usernameBlocksStart := strings.Index(schema, `CREATE TABLE "username_blocks" (`)
	if usernameBlocksStart < 0 {
		t.Fatal("fresh schema is missing table username_blocks")
	}
	usernameBlocksEnd := strings.Index(schema[usernameBlocksStart:], "\n);")
	if usernameBlocksEnd < 0 {
		t.Fatal("fresh schema table username_blocks has no terminator")
	}
	usernameBlocks := schema[usernameBlocksStart : usernameBlocksStart+usernameBlocksEnd]
	for _, token := range []string{
		`"created_at" timestamp(6) without time zone NOT NULL`,
		`"updated_at" timestamp(6) without time zone NOT NULL`,
	} {
		if !strings.Contains(usernameBlocks, token) {
			t.Errorf("fresh schema username_blocks is missing %q", token)
		}
	}
	for _, obsolete := range []string{
		`CREATE INDEX "index_follows_on_target_account_id"`,
		`CREATE INDEX "index_quotes_on_account_id_and_quoted_account_id"`,
		`CREATE INDEX "index_quotes_on_quoted_status_id"`,
	} {
		if strings.Contains(schema, obsolete) {
			t.Errorf("fresh schema retains obsolete v4.4 index %q", obsolete)
		}
	}
}

func TestMastodon45FreshSchemaAppendsColumnsInUpstreamMigrationOrder(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	checks := map[string][]string{
		"accounts":       {`"attribution_domains"`, `"following_url"`, `"id_scheme"`},
		"conversations":  {`"updated_at"`, `"parent_status_id"`, `"parent_account_id"`},
		"fasp_providers": {`"updated_at"`, `"delivery_last_failed_at"`},
		"status_stats":   {`"untrusted_reblogs_count"`, `"quotes_count"`},
	}
	for table, columns := range checks {
		start := strings.Index(schema, `CREATE TABLE "`+table+`" (`)
		if start < 0 {
			t.Fatalf("fresh schema is missing table %s", table)
		}
		end := strings.Index(schema[start:], "\n);")
		if end < 0 {
			t.Fatalf("fresh schema table %s has no terminator", table)
		}
		block := schema[start : start+end]
		previous := -1
		for _, column := range columns {
			position := strings.Index(block, column)
			if position < 0 {
				t.Fatalf("fresh schema table %s is missing %s", table, column)
			}
			if position <= previous {
				t.Fatalf("fresh schema table %s column %s is not appended in upstream order %#v", table, column, columns)
			}
			previous = position
		}
	}
}

func TestMastodon45InventoryMatchesUpstreamMigrationFiles(t *testing.T) {
	upstream := "/home/mohemohe/develop/src/github.com/mastodon/mastodon/db/migrate"
	entries, err := os.ReadDir(upstream)
	if err != nil {
		t.Skipf("upstream Mastodon checkout is unavailable: %v", err)
	}
	got := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < len("20250717003848") || !strings.HasSuffix(name, ".rb") {
			continue
		}
		version := name[:len("20250717003848")]
		if version > Mastodon4422SchemaVersion && version <= CurrentSchemaVersion {
			got = append(got, version)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), expectedMastodon45UpgradeVersions...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream v4.4.22..v4.5.15 migration files = %#v, reviewed inventory = %#v", got, want)
	}
}

func TestMastodon45BlockedUsernameNormalization(t *testing.T) {
	if got := normalizeMastodon45BlockedUsername("M4ST0D0N"); got != "mastodon" {
		t.Fatalf("normalize blocked username = %q, want mastodon", got)
	}
	for _, value := range []any{nil, false, "", "  ", []any{}, map[string]any{}} {
		if !rubyBlankSettingValue(value) {
			t.Fatalf("rubyBlankSettingValue(%#v) = false", value)
		}
	}
	for _, value := range []any{true, "public", []any{"x"}, map[string]any{"x": true}} {
		if rubyBlankSettingValue(value) {
			t.Fatalf("rubyBlankSettingValue(%#v) = true", value)
		}
	}
}

func TestMastodon45RubyTruthyMatchesRubySemantics(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "nil", value: nil, want: false},
		{name: "false", value: false, want: false},
		{name: "true", value: true, want: true},
		{name: "zero", value: 0, want: true},
		{name: "empty string", value: "", want: true},
		{name: "false string", value: "false", want: true},
		{name: "empty array", value: []any{}, want: true},
		{name: "empty map", value: map[string]any{}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mastodon45RubyTruthy(test.value); got != test.want {
				t.Fatalf("mastodon45RubyTruthy(%#v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestMastodon45BlockedUsernameSeedInventoryIsExact(t *testing.T) {
	want := []mastodon45UsernameBlockSeed{}
	for _, username := range []string{
		"abuse", "account", "accounts", "admin", "administration", "administrator", "admins",
		"help", "helpdesk", "instance", "mod", "moderator", "moderators", "mods", "owner", "root",
		"security", "server", "staff", "support", "webmaster",
	} {
		want = append(want, mastodon45UsernameBlockSeed{username: username, exact: true})
	}
	for _, username := range []string{"mastodon", "mastadon"} {
		want = append(want, mastodon45UsernameBlockSeed{username: username})
	}
	got := mastodon45UsernameBlockSeeds()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Mastodon 4.5 username block seeds = %#v, want %#v", got, want)
	}
	if len(got) != 23 {
		t.Fatalf("Mastodon 4.5 username block seed count = %d, want 23", len(got))
	}
}
