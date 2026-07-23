//go:build integration

package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestOpenAppliesDatabaseLockTimeout(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := Open(config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 2,
		DatabaseMaxIdleConns: 1,
		DatabaseLockTimeout:  750 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()

	const lockID int64 = 7_501_924_638
	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", lockID) //nolint:errcheck

	startedAt := time.Now()
	err = database.WithContext(ctx).Exec("SELECT pg_advisory_lock(?)", lockID).Error
	elapsed := time.Since(startedAt)
	if err == nil {
		t.Fatal("competing advisory lock unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("competing advisory lock error = %v, want lock timeout", err)
	}
	if elapsed < 500*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("competing advisory lock elapsed = %s, want approximately 750ms", elapsed)
	}
}
