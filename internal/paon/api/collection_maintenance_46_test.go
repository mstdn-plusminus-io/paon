package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMastodon46CollectionMaintenanceCadenceAndRetention(t *testing.T) {
	if collectionItemRetentionTTL != 24*time.Hour {
		t.Fatalf("collection item retention = %s", collectionItemRetentionTTL)
	}
	source, err := os.ReadFile("collection_maintenance_46.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"time.NewTimer(time.Second)", "time.NewTicker(24 * time.Hour)", "remote_collection_repair:last_known_good", "substring(collections.uri"} {
		if !strings.Contains(string(source), want) {
			t.Fatalf("repair scheduler missing %q", want)
		}
	}
}
