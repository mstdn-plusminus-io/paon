package migrate

import (
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

func TestMigrationSQLStatementsSkipsHeaderOnlyHandlers(t *testing.T) {
	if got := migrationSQLStatements("-- Mastodon migration.\n-- Go handler: example.\n"); len(got) != 0 {
		t.Fatalf("header-only migration produced executable SQL: %#v", got)
	}
	source := `-- First line; is documentation.
-- Second line.

CREATE FUNCTION example() RETURNS text LANGUAGE sql AS $body$ SELECT ';'; $body$;
INSERT INTO settings (var, value) VALUES ('theme', 'one;two');
`
	want := []string{
		`CREATE FUNCTION example() RETURNS text LANGUAGE sql AS $body$ SELECT ';'; $body$`,
		`INSERT INTO settings (var, value) VALUES ('theme', 'one;two')`,
	}
	if got := migrationSQLStatements(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration SQL = %#v, want %#v", got, want)
	}
}

func TestMigrationSQLStatementsPreservesInternalComments(t *testing.T) {
	source := `-- Documentation is not executable.
SELECT 'it''s; intact' AS "semi;colon" /* outer ; /* nested ; */ end */;
-- Between statements; remains attached to the next statement.
SELECT 2 /* final; comment */;`
	want := []string{
		`SELECT 'it''s; intact' AS "semi;colon" /* outer ; /* nested ; */ end */`,
		"-- Between statements; remains attached to the next statement.\nSELECT 2 /* final; comment */",
	}
	if got := migrationSQLStatements(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration SQL comments or quoted semicolons changed: got %#v, want %#v", got, want)
	}
}

func TestEmbeddedMigrationFilesMatchReleaseInventories(t *testing.T) {
	for _, release := range []struct {
		name     string
		versions func(UpgradePhase) []string
	}{
		{"4.3.23", mastodon43PhaseVersions},
		{"4.4.22", mastodon44PhaseVersions},
		{"4.5.15", mastodon45PhaseVersions},
		{"4.6.6", mastodon46PhaseVersions},
	} {
		t.Run(release.name, func(t *testing.T) {
			inventory := map[string]bool{}
			for _, phase := range upgradePhasesThrough(UpgradePhaseContract) {
				for _, version := range release.versions(phase) {
					path := fmt.Sprintf("migrations/%s/%s_%s.sql", release.name, version, phase)
					if _, err := migrationFiles.ReadFile(path); err != nil {
						t.Fatalf("migration inventory has no embedded file: %v", err)
					}
					inventory[path] = true
				}
			}
			paths, err := fs.Glob(migrationFiles, "migrations/"+release.name+"/*.sql")
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				if strings.HasSuffix(path, "_prepare.sql") || strings.HasSuffix(path, "_index.sql") {
					if len(embeddedMigrationStatements(path)) == 0 {
						t.Errorf("supporting migration has no SQL: %s", path)
					}
					continue
				}
				if !inventory[path] {
					t.Errorf("embedded migration is absent from the release inventory: %s", path)
				}
			}
		})
	}
}
