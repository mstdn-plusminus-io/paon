package api

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestMastodon46MeiliSearchStoplightContract(t *testing.T) {
	if meiliSearchStoplightFailureThreshold != 10 {
		t.Fatalf("threshold = %d, want 10", meiliSearchStoplightFailureThreshold)
	}
	if meiliSearchStoplightCooldown != 5*time.Minute {
		t.Fatalf("cooldown = %s, want 5m", meiliSearchStoplightCooldown)
	}
	if got := meiliSearchStoplightKey("paon:"); got != "paon:search:meilisearch:stoplight" {
		t.Fatalf("key = %q", got)
	}
}

func TestMeiliSearchStoplightTracksTransportFailuresOnly(t *testing.T) {
	transportError := &url.Error{Op: "Post", URL: "https://search.example", Err: errors.New("connection refused")}
	if !meiliSearchStoplightTracksError(transportError) {
		t.Fatal("URL transport error was not tracked")
	}
	if !meiliSearchStoplightTracksError(context.DeadlineExceeded) {
		t.Fatal("deadline error was not tracked")
	}
	if meiliSearchStoplightTracksError(context.Canceled) {
		t.Fatal("caller cancellation was tracked as a backend failure")
	}
	if meiliSearchStoplightTracksError(errors.New("invalid query")) {
		t.Fatal("application error was tracked as a backend failure")
	}
}
