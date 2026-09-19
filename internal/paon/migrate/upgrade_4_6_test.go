package migrate

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
)

var expectedMastodon46UpgradeVersions = []string{
	"20251117023614", "20251118115657", "20251119093332",
	"20251201154910", "20251201155054", "20251202140424", "20251209093813", "20251217091936",
	"20260115153219", "20260119153538", "20260127141459", "20260127141820",
	"20260209142402", "20260209143308", "20260211132603", "20260212113020", "20260212131934", "20260217154542",
	"20260303144409", "20260310095021", "20260311152331", "20260311212130", "20260318144837", "20260319142348", "20260323105645", "20260325151755", "20260326112324",
	"20260410083500", "20260415133505", "20260420124030", "20260423141611", "20260425144553",
	"20260505155103", "20260611150940",
}

func TestMastodon46MigrationInventoryIsExactAndPhaseDisjoint(t *testing.T) {
	got := []string{}
	seen := map[string]UpgradePhase{}
	for _, phase := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract} {
		for _, version := range mastodon46PhaseVersions(phase) {
			if previous, exists := seen[version]; exists {
				t.Fatalf("migration %s appears in both %s and %s", version, previous, phase)
			}
			seen[version] = phase
			got = append(got, version)
			if !paonschema.Mastodon46UpgradeVersionKnown(version) {
				t.Fatalf("phase inventory contains unreviewed version %s", version)
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), expectedMastodon46UpgradeVersions...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Mastodon 4.6 phase inventory = %#v, want %#v", got, want)
	}
	if len(got) != 34 || len(got) != paonschema.Mastodon46UpgradeVersionCount() {
		t.Fatalf("Mastodon 4.6 marker count = %d, schema inventory = %d, want 34", len(got), paonschema.Mastodon46UpgradeVersionCount())
	}
	if seen[CurrentSchemaVersion] != UpgradePhaseContract {
		t.Fatalf("final marker %s phase = %s, want contract", CurrentSchemaVersion, seen[CurrentSchemaVersion])
	}
}

func TestMastodon46InventoryMatchesUpstreamMigrationFiles(t *testing.T) {
	entries, err := os.ReadDir("/home/mohemohe/develop/src/github.com/mastodon/mastodon/db/migrate")
	if err != nil {
		t.Skipf("upstream Mastodon checkout is unavailable: %v", err)
	}
	got := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < 14 || !strings.HasSuffix(name, ".rb") {
			continue
		}
		version := name[:14]
		if version > Mastodon4515SchemaVersion && version <= CurrentSchemaVersion {
			got = append(got, version)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), expectedMastodon46UpgradeVersions...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream v4.5.15..v4.6.6 migration files = %#v, reviewed inventory = %#v", got, want)
	}
}

func TestMastodon46FreshSchemaIdentityAndRails81Layout(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	for _, token := range []string{
		`CREATE TABLE public.collections`,
		`CREATE TABLE public.collection_items`,
		`CREATE TABLE public.collection_reports`,
		`CREATE TABLE public.email_subscriptions`,
		`CREATE TABLE public.tagged_objects`,
		`CREATE TABLE public.keypairs`,
		`schema_sha1', 'c7b4869d1d9d5614d86e2723464402c4976f6075'`,
		`INSERT INTO public.schema_migrations VALUES ('20260611150940')`,
		`md5(table_name || '__PAON_TIMESTAMP_ID_SALT__' || time_part::text)`,
	} {
		if !strings.Contains(schema, token) {
			t.Errorf("fresh schema is missing %q", token)
		}
	}
	if got := strings.Count(schema, "INSERT INTO public.schema_migrations VALUES"); got != 588 {
		t.Fatalf("fresh schema marker count = %d, want 588", got)
	}
	accounts := schemaTableBlock(t, schema, "accounts")
	assertColumnsInOrder(t, "accounts", accounts, []string{"actor_type", "also_known_as", "attribution_domains", "avatar_content_type", "avatar_description", "collections_url", "feature_approval_policy", "show_featured", "show_media", "show_media_replies", "username"})
	items := schemaTableBlock(t, schema, "collection_items")
	assertColumnsInOrder(t, "collection_items", items, []string{"id", "account_id", "activity_uri", "approval_last_verified_at", "approval_uri", "collection_id", "created_at", "object_uri", `"position"`, "state", "updated_at", "uri"})
}

func schemaTableBlock(t *testing.T, schema, table string) string {
	t.Helper()
	start := strings.Index(schema, "CREATE TABLE public."+table+" (")
	if start < 0 {
		t.Fatalf("fresh schema is missing table %s", table)
	}
	end := strings.Index(schema[start:], "\n);")
	if end < 0 {
		t.Fatalf("fresh schema table %s has no terminator", table)
	}
	return schema[start : start+end]
}

func assertColumnsInOrder(t *testing.T, table, block string, columns []string) {
	t.Helper()
	previous := -1
	for _, column := range columns {
		position := strings.Index(block, "\n    "+column+" ")
		if position < 0 {
			t.Fatalf("fresh schema table %s is missing column %s", table, column)
		}
		if position <= previous {
			t.Fatalf("fresh schema table %s does not use Rails 8.1 column order %#v", table, columns)
		}
		previous = position
	}
}
