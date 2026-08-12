package config

import (
	"strings"
	"testing"
)

func TestExperimentalFeaturesFromEnv(t *testing.T) {
	t.Setenv("EXPERIMENTAL_FEATURES", " FASP, alpha fasp ,BETA ")
	got := experimentalFeaturesFromEnv()
	want := []string{"fasp", "alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("experimental features = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("experimental features = %#v, want %#v", got, want)
		}
	}
	if !(&Config{ExperimentalFeatures: got}).ExperimentalFeatureEnabled(" FASP ") {
		t.Fatal("normalized fasp feature was not enabled")
	}
	if (&Config{ExperimentalFeatures: got}).ExperimentalFeatureEnabled("unknown") {
		t.Fatal("unknown experimental feature was enabled")
	}
}

func TestFASPAsynqQueueIsValid(t *testing.T) {
	t.Setenv("ASYNQ_QUEUES", "fasp")
	cfg := FromEnv()
	if err := cfg.ValidateRuntime(); err != nil && strings.Contains(err.Error(), "ASYNQ_QUEUES") {
		t.Fatalf("fasp Asynq queue rejected: %v", err)
	}
}

func TestFASPWorkerRequiresFASPQueueWhenQueuesAreRestricted(t *testing.T) {
	cfg := FromEnv()
	cfg.ProcessRole = "worker"
	cfg.ExperimentalFeatures = []string{"fasp"}
	cfg.AsynqQueues = []string{"default"}
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "requires the fasp queue") {
		t.Fatalf("restricted worker queue validation error = %v", err)
	}

	cfg.AsynqQueues = []string{"default", "fasp"}
	if err := cfg.ValidateRuntime(); err != nil && strings.Contains(err.Error(), "requires the fasp queue") {
		t.Fatalf("worker with fasp queue rejected: %v", err)
	}
}
