package migrate

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// Each release owns its upstream migration IDs. Files are embedded so the
// migration command works from any directory and needs no external SQL bundle.
//
//go:embed migrations/*/*.sql
var migrationFiles embed.FS

func embeddedMigrationSteps(release string, phase UpgradePhase) []upgradeStep {
	if _, err := migrationFiles.ReadDir("migrations/" + release); err != nil {
		panic(fmt.Sprintf("read embedded Mastodon %s migration directory: %v", release, err))
	}
	paths, err := fs.Glob(migrationFiles, "migrations/"+release+"/*_"+string(phase)+".sql")
	if err != nil {
		panic(fmt.Sprintf("list embedded Mastodon %s %s migrations: %v", release, phase, err))
	}
	steps := make([]upgradeStep, 0, len(paths))
	// fs.Glob returns lexical order, matching the upstream timestamp order
	// within each of the expand/backfill/validate/contract phases.
	for _, path := range paths {
		name := path[strings.LastIndexByte(path, '/')+1:]
		version, _, _ := strings.Cut(name, "_")
		steps = append(steps, upgradeStep{
			version:    version,
			phase:      string(phase),
			statements: embeddedMigrationStatements(path),
		})
	}
	return steps
}

func embeddedMigrationStatements(path string) []string {
	raw, err := migrationFiles.ReadFile(path)
	if err != nil {
		// Only compile-time paths reach here; a missing file is a broken build,
		// never an operator or database error.
		panic(fmt.Sprintf("read embedded migration %s: %v", path, err))
	}
	return migrationSQLStatements(string(raw))
}

func migrationSQLStatements(source string) []string {
	// Header-only files document Go backfills and deferred completion markers.
	// Remove their comments so they never become executable SQL statements.
	for {
		source = strings.TrimSpace(source)
		if !strings.HasPrefix(source, "--") {
			break
		}
		_, remaining, _ := strings.Cut(source, "\n")
		source = remaining
	}
	if source == "" {
		return nil
	}
	statements := splitSQLStatements(source)
	for index := range statements {
		statements[index] = strings.TrimSuffix(statements[index], ";")
	}
	return statements
}
