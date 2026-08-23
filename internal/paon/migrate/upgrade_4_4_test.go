package migrate

import (
	"context"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
)

var expectedMastodon44UpgradeVersions = []string{
	"20240918233930", "20241014010506", "20241022214312", "20241104082851", "20241111141355", "20241123160722", "20241123224956",
	"20241205103523", "20241205135901", "20241205135925", "20241205162640", "20241205163118", "20241206131513", "20241210140838",
	"20241212152158", "20241212152618", "20241212152734", "20241212152910", "20241212153054", "20241212153202", "20241212153254", "20241212154231", "20241212154346",
	"20241213130230", "20241213170027", "20241213170036", "20241213170043", "20241213170053",
	"20241216223425", "20241216223433", "20241216223446", "20241216223452", "20241216223852", "20241216223859", "20241216224211", "20241216224218", "20241216224229", "20241216224237", "20241216224507", "20241216224514", "20241216224520", "20241216224530", "20241216224813", "20241216224825",
	"20250103131909", "20250108111200", "20250129144440", "20250129144813", "20250221143646", "20250224144617", "20250305074104", "20250313123400", "20250328153843",
	"20250410144908", "20250411094808", "20250411095859", "20250422083912", "20250422084214", "20250422085027", "20250422085303", "20250425134308", "20250428095029", "20250428104538",
	"20250520192024", "20250520204643", "20250605110215", "20250627132728",
}

func TestMastodon44MigrationInventoryIsExactAndPhaseDisjoint(t *testing.T) {
	got := []string{}
	seen := map[string]UpgradePhase{}
	for _, phase := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract} {
		versions := mastodon44PhaseVersions(phase)
		if len(versions) == 0 {
			t.Fatalf("Mastodon 4.4 phase %s has no migration markers", phase)
		}
		for _, version := range versions {
			if previous, exists := seen[version]; exists {
				t.Fatalf("migration %s appears in both %s and %s", version, previous, phase)
			}
			seen[version] = phase
			got = append(got, version)
			if !paonschema.Mastodon44UpgradeVersionKnown(version) {
				t.Fatalf("phase inventory contains unreviewed version %s", version)
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), expectedMastodon44UpgradeVersions...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Mastodon 4.4 phase inventory = %#v, want %#v", got, want)
	}
	if len(got) != 67 || len(got) != paonschema.Mastodon44UpgradeVersionCount() {
		t.Fatalf("Mastodon 4.4 marker count = %d, schema inventory = %d, want 67", len(got), paonschema.Mastodon44UpgradeVersionCount())
	}
	if seen[Mastodon4422SchemaVersion] != UpgradePhaseContract {
		t.Fatalf("final marker %s phase = %s, want contract", Mastodon4422SchemaVersion, seen[Mastodon4422SchemaVersion])
	}
	if !(expectedMastodon44UpgradeVersions[0] < Mastodon4323SchemaVersion) {
		t.Fatal("test fixture must preserve the non-monotonic branch marker boundary")
	}
}

func TestMastodon44FinalMarkerIsWithheldUntilContract(t *testing.T) {
	steps := mastodon44ContractSteps()
	final := steps[len(steps)-1]
	if final.version != Mastodon4422SchemaVersion {
		t.Fatalf("last contract marker = %s, want %s", final.version, Mastodon4422SchemaVersion)
	}
	if strings.Contains(strings.Join(final.statements, "\n"), "CREATE TABLE") {
		t.Fatal("final marker must not defer additive catalog creation until contract")
	}
	additiveSource := sourceOfFunctionForMastodon44Test(t, "upgrade_4_4.go", "func ensureMastodon44FinalAdditiveCatalog", "func applyMastodon44Backfill")
	if !strings.Contains(additiveSource, "CREATE TABLE IF NOT EXISTS fasp_follow_recommendations") {
		t.Fatal("expand must create the additive catalog from the final upstream migration")
	}
	runSource := sourceOfFunctionForMastodon44Test(t, "upgrade_4_4.go", "func runMastodon44Phase", "func applyMastodon44Steps")
	if expandAt, ensureAt := strings.Index(runSource, "case UpgradePhaseExpand"), strings.Index(runSource, "ensureMastodon44FinalAdditiveCatalog(tx)"); expandAt < 0 || ensureAt < expandAt {
		t.Fatal("expand must install the final additive catalog without recording the final marker")
	}
}

func sourceOfFunctionForMastodon44Test(t *testing.T, filename string, start string, end string) string {
	t.Helper()
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	startAt := strings.Index(source, start)
	endAt := strings.Index(source, end)
	if startAt < 0 || endAt <= startAt {
		t.Fatalf("cannot locate function %s in %s", start, filename)
	}
	return source[startAt:endAt]
}

func TestLegacyTagTrendRowsRejectInvalidInputWithoutDatabase(t *testing.T) {
	if err := UpsertLegacyTagTrendRows(context.Background(), nil, nil); err == nil {
		t.Fatal("UpsertLegacyTagTrendRows(nil) error = nil")
	}
}
