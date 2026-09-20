package migrate

import (
	"context"
	"gorm.io/gorm"
	"strings"
	"testing"
)

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
