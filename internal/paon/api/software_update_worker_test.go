package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestSoftwareUpdateCheckConstantsMatchRailsScheduler(t *testing.T) {
	if softwareUpdateCheckWorkerInterval != 30*time.Minute {
		t.Fatalf("softwareUpdateCheckWorkerInterval = %s", softwareUpdateCheckWorkerInterval)
	}
}

func TestFetchSoftwareUpdateNoticesUsesRailsBodyLimit(t *testing.T) {
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })

	received := make(chan string, 1)
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		received <- req.Method + " " + req.URL.String() + " " + req.Header.Get("Accept") + " " + req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"updatesAvailable":[{"version":"9.9.9","urgent":true,"type":"major","releaseNotes":"notes"}]}`)),
			Header:     http.Header{},
			Request:    req,
		}, nil
	})}

	s := &Server{cfg: config.Config{UpdateCheckURL: "https://updates.example/check", MastodonVersion: "4.2.27"}}
	response, ok := s.fetchSoftwareUpdateNotices(context.Background())
	if !ok || len(response.UpdatesAvailable) != 1 || response.UpdatesAvailable[0].Version != "9.9.9" {
		t.Fatalf("response = %#v ok=%v", response, ok)
	}
	select {
	case got := <-received:
		if got != "GET https://updates.example/check?version=4.2.27 application/json Mastodon update checker" {
			t.Fatalf("request = %q", got)
		}
	default:
		t.Fatal("request was not captured")
	}

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(`{"updatesAvailable":[]}`)),
			Header:        http.Header{},
			ContentLength: maxActivityResourceBodySize + 1,
			Request:       req,
		}, nil
	})}
	if _, ok := s.fetchSoftwareUpdateNotices(context.Background()); ok {
		t.Fatal("software update check should reject an advertised body larger than Rails body_with_limit")
	}

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxActivityResourceBodySize+1))),
			Header:        http.Header{},
			ContentLength: -1,
			Request:       req,
		}, nil
	})}
	if _, ok := s.fetchSoftwareUpdateNotices(context.Background()); ok {
		t.Fatal("software update check should reject a streamed body larger than Rails body_with_limit")
	}
}

func TestSoftwareUpdateHelpers(t *testing.T) {
	s := &Server{cfg: config.Config{Version: "paon-go mastodon-compatible", MastodonVersion: "4.2.27"}}
	if got := s.currentSoftwareUpdateVersion(); got != "4.2.27" {
		t.Fatalf("current version = %q", got)
	}
	s.cfg.Version = "4.3.0+nightly"
	if got := s.currentSoftwareUpdateVersion(); got != "4.3.0" {
		t.Fatalf("current version = %q", got)
	}
	for input, want := range map[any]int{"patch": 0, "minor": 1, "major": 2, float64(2): 2, "bad": 0} {
		if got := softwareUpdateTypeValue(input); got != want {
			t.Fatalf("type value %#v = %d, want %d", input, got, want)
		}
	}
}
