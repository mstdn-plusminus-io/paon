package cutover

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

type Report struct {
	Queues      map[string]int64 `json:"queues"`
	Retry       int64            `json:"retry"`
	Scheduled   int64            `json:"scheduled"`
	Dead        int64            `json:"dead"`
	Processes   int64            `json:"processes"`
	InFlight    int64            `json:"in_flight"`
	UniqueLocks int64            `json:"unique_locks"`
}

func (report Report) Pending() int64 {
	total := report.Retry + report.Scheduled + report.Dead + report.Processes + report.InFlight + report.UniqueLocks
	for _, count := range report.Queues {
		total += count
	}
	return total
}

func (report Report) Safe() bool { return report.Pending() == 0 }

func (report Report) String() string {
	queueNames := make([]string, 0, len(report.Queues))
	for name := range report.Queues {
		queueNames = append(queueNames, name)
	}
	sort.Strings(queueNames)
	var out strings.Builder
	for _, name := range queueNames {
		fmt.Fprintf(&out, "queue[%s]=%d\n", name, report.Queues[name])
	}
	fmt.Fprintf(&out, "retry=%d\nscheduled=%d\ndead=%d\nprocesses=%d\nin_flight=%d\nunique_locks=%d\npending=%d\n", report.Retry, report.Scheduled, report.Dead, report.Processes, report.InFlight, report.UniqueLocks, report.Pending())
	return out.String()
}

func InspectSidekiq(ctx context.Context, client redis.UniversalClient, namespace string) (Report, error) {
	if client == nil {
		return Report{}, fmt.Errorf("sidekiq preflight: Redis client is required")
	}
	prefix := sidekiqNamespacePrefix(namespace)
	report := Report{Queues: map[string]int64{}}
	queueNames, err := client.SMembers(ctx, prefix+"queues").Result()
	if err != nil && err != redis.Nil {
		return Report{}, fmt.Errorf("read Sidekiq queues: %w", err)
	}
	for _, name := range queueNames {
		count, err := client.LLen(ctx, prefix+"queue:"+name).Result()
		if err != nil {
			return Report{}, fmt.Errorf("read Sidekiq queue %s: %w", name, err)
		}
		report.Queues[name] = count
	}
	for key, destination := range map[string]*int64{"retry": &report.Retry, "schedule": &report.Scheduled, "dead": &report.Dead} {
		count, err := client.ZCard(ctx, prefix+key).Result()
		if err != nil {
			return Report{}, fmt.Errorf("read Sidekiq %s set: %w", key, err)
		}
		*destination = count
	}
	report.Processes, err = client.SCard(ctx, prefix+"processes").Result()
	if err != nil {
		return Report{}, fmt.Errorf("read Sidekiq processes: %w", err)
	}
	report.InFlight, err = countHashEntries(ctx, client, prefix+"*:work")
	if err != nil {
		return Report{}, fmt.Errorf("read Sidekiq in-flight work: %w", err)
	}
	report.UniqueLocks, err = countKeys(ctx, client, prefix+"uniquejobs:*")
	if err != nil {
		return Report{}, fmt.Errorf("read Sidekiq unique locks: %w", err)
	}
	return report, nil
}

func sidekiqNamespacePrefix(namespace string) string {
	namespace = strings.Trim(strings.TrimSpace(namespace), ":")
	if namespace == "" {
		return ""
	}
	return namespace + ":"
}

func countKeys(ctx context.Context, client redis.UniversalClient, pattern string) (int64, error) {
	var cursor uint64
	var total int64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			return 0, err
		}
		total += int64(len(keys))
		cursor = next
		if cursor == 0 {
			return total, nil
		}
	}
}

func countHashEntries(ctx context.Context, client redis.UniversalClient, pattern string) (int64, error) {
	var cursor uint64
	var total int64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			return 0, err
		}
		for _, key := range keys {
			count, err := client.HLen(ctx, key).Result()
			if err != nil {
				return 0, err
			}
			total += count
		}
		cursor = next
		if cursor == 0 {
			return total, nil
		}
	}
}
