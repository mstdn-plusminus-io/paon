//go:build integration

package cutover

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestSidekiqDrainPreflightAgainstRedisWireFixtures(t *testing.T) {
	options, err := redis.ParseURL(os.Getenv("PAON_TEST_REDIS_URL"))
	if err != nil {
		t.Fatalf("PAON_TEST_REDIS_URL is required: %v", err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx := context.Background()
	namespace := "mastodon_test_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	prefix := sidekiqNamespacePrefix(namespace)
	fixture := `{"class":"ActivityPub::DeliveryWorker","args":["https://remote.example/inbox",1,"{}"],"queue":"push","retry":16,"jid":"abc"}`
	if err := client.SAdd(ctx, prefix+"queues", "push").Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.LPush(ctx, prefix+"queue:push", fixture).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, prefix+"retry", redis.Z{Score: 2_000_000_000, Member: fixture}).Err(); err != nil {
		t.Fatal(err)
	}
	report, err := InspectSidekiq(ctx, client, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if report.Safe() || report.Queues["push"] != 1 || report.Retry != 1 {
		t.Fatalf("unsafe fixture report = %#v", report)
	}
	if err := client.Del(ctx, prefix+"queues", prefix+"queue:push", prefix+"retry").Err(); err != nil {
		t.Fatal(err)
	}
	report, err = InspectSidekiq(ctx, client, namespace)
	if err != nil || !report.Safe() {
		t.Fatalf("drained report = %#v, %v", report, err)
	}
}
