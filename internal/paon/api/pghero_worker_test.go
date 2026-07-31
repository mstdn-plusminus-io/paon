package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestPgHeroSpaceStatsWorkerMatchesRailsScheduler(t *testing.T) {
	if pgheroSpaceStatsWorkerInterval != 24*time.Hour {
		t.Fatalf("pgheroSpaceStatsWorkerInterval = %s", pgheroSpaceStatsWorkerInterval)
	}
	src, err := os.ReadFile("pghero_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runPgHeroSpaceStatsWorker", `s.capturePgHeroSpaceStats(ctx)`},
		{"capturePgHeroSpaceStats", `s.db.Dialector.Name() != "postgres"`},
		{"capturePgHeroSpaceStats", `if statsDB := s.pgHeroStatsDatabase(); statsDB != s.db`},
		{"capturePgHeroSpaceStats", `rows, err := s.pgHeroSpaceStatRows(ctx, s.db)`},
		{"capturePgHeroSpaceStats", `statsDB.WithContext(ctx).CreateInBatches(rows, 100)`},
		{"capturePgHeroSpaceStats", `s.db.WithContext(ctx).Exec(pgheroSpaceStatsInsertSQL)`},
		{"capturePgHeroSpaceStats", `s.capturePgHeroOtherSpaceStats(ctx, statsDB, total)`},
		{"capturePgHeroOtherSpaceStats", `s.pgHeroOtherDB == nil`},
		{"capturePgHeroOtherSpaceStats", `rows, err := s.pgHeroSpaceStatRows(ctx, s.pgHeroOtherDB)`},
		{"capturePgHeroOtherSpaceStats", `statsDB.WithContext(ctx).CreateInBatches(rows, 100)`},
		{"pgHeroStatsDatabase", `s.pgHeroStatsDB != nil`},
		{"pgHeroSpaceStatRows", `database.WithContext(ctx).Raw(pgheroSpaceStatsSelectSQL).Scan(&rows)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	for _, want := range []string{
		"INSERT INTO pghero_space_stats (database, schema, relation, size, captured_at)",
		"current_database() AS database",
		"pg_total_relation_size(pg_class.oid) AS size",
		"pg_class.relkind IN ('r', 'm')",
	} {
		if !strings.Contains(pgheroSpaceStatsInsertSQL, want) {
			t.Fatalf("pghero SQL missing %q", want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runPgHeroSpaceStatsWorker)") {
		t.Fatal("StartBackgroundWorkers does not start PgHero space stats worker")
	}
	serverSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`pgHeroStatsDB, err := paondb.OpenPgHeroStats(cfg)`,
		`pgHeroOtherDB, err := paondb.OpenPgHeroOther(cfg)`,
		`pgHeroStatsDB: pgHeroStatsDB`,
		`pgHeroOtherDB: pgHeroOtherDB`,
	} {
		if !strings.Contains(string(serverSrc), want) {
			t.Fatalf("server.go missing PgHero stats DB setup %q", want)
		}
	}
}
