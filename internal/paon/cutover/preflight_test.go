package cutover

import (
	"strings"
	"testing"
)

func TestReportOnlyAllowsCompletelyDrainedSidekiq(t *testing.T) {
	empty := Report{Queues: map[string]int64{"default": 0, "push": 0}}
	if !empty.Safe() || empty.Pending() != 0 {
		t.Fatalf("empty report = %#v", empty)
	}
	fields := []func(*Report){
		func(r *Report) { r.Queues["default"] = 1 },
		func(r *Report) { r.Retry = 1 },
		func(r *Report) { r.Scheduled = 1 },
		func(r *Report) { r.Dead = 1 },
		func(r *Report) { r.Processes = 1 },
		func(r *Report) { r.InFlight = 1 },
		func(r *Report) { r.UniqueLocks = 1 },
	}
	for index, mutate := range fields {
		report := Report{Queues: map[string]int64{"default": 0}}
		mutate(&report)
		if report.Safe() || report.Pending() != 1 {
			t.Fatalf("unsafe report %d = %#v", index, report)
		}
	}
}

func TestSidekiqNamespacePrefixMatchesRedisNamespace(t *testing.T) {
	for input, want := range map[string]string{"": "", "mastodon": "mastodon:", "mastodon:": "mastodon:", ":mastodon:": "mastodon:"} {
		if got := sidekiqNamespacePrefix(input); got != want {
			t.Fatalf("sidekiqNamespacePrefix(%q) = %q, want %q", input, got, want)
		}
	}
	report := Report{Queues: map[string]int64{"push": 0, "default": 0}}
	if text := report.String(); !strings.HasPrefix(text, "queue[default]=0\nqueue[push]=0\n") || !strings.HasSuffix(text, "pending=0\n") {
		t.Fatalf("report output = %q", text)
	}
}
