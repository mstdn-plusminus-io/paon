package migrate

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestMigrationTargets(t *testing.T) {
	for _, version := range []string{"", "4.3.23", "4.4.22", "4.5.15", "4.6.6"} {
		t.Run(version, func(t *testing.T) {
			target, err := requestedMigrationTarget(version)
			if err != nil {
				t.Fatal(err)
			}
			if version == "" && target.release != LatestTargetVersion {
				t.Fatalf("default target = %s", target.release)
			}
			if _, err := schemaFiles.ReadFile(target.snapshot); err != nil {
				t.Fatalf("target schema is not embedded: %v", err)
			}
		})
	}
	for _, invalid := range []string{"4.2.19", "4.3", "v4.6.6", "4.7.0"} {
		if _, err := requestedMigrationTarget(invalid); err == nil {
			t.Errorf("accepted unsupported target %q", invalid)
		}
	}
}

func TestBulkMigrationOptionsFailBeforeDatabaseAccess(t *testing.T) {
	for _, test := range []struct {
		name    string
		options Options
		want    string
	}{
		{"invalid target", Options{TargetVersion: "4.7.0"}, "unsupported migration target"},
		{"unacknowledged all", Options{All: true}, "requires --acknowledge-contract"},
		{"conflicting phase", Options{All: true, Phase: UpgradePhaseExpand, AcknowledgeContract: true}, "cannot be combined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// No connection is configured: a query would panic rather than pass.
			applied, err := RunWithOptions(context.Background(), &gorm.DB{}, test.options)
			if applied || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunWithOptions = %v, %v; want %q", applied, err, test.want)
			}
		})
	}
}

func TestMigrationAfterTargetUsesReleaseInventory(t *testing.T) {
	target, err := requestedMigrationTarget("4.3.23")
	if err != nil {
		t.Fatal(err)
	}
	if !migrationAfterTarget("20240918233930", target) {
		t.Fatal("4.4 migration predating the final 4.3 marker must reject a 4.3 target")
	}
	if migrationAfterTarget(Mastodon4323SchemaVersion, target) {
		t.Fatal("the requested final marker must be accepted")
	}
}

func TestMigrationTargetOptionsFromEnv(t *testing.T) {
	t.Setenv("PAON_MIGRATION_TARGET_VERSION", "4.4.22")
	t.Setenv("PAON_MIGRATION_ALL", "true")
	options := OptionsFromEnv()
	if options.TargetVersion != "4.4.22" || !options.All {
		t.Fatalf("migration target options = %#v", options)
	}
}
