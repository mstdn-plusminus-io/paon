package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestStreamingMetricsSSEPathContractMatchesNode(t *testing.T) {
	valid := []struct {
		url     string
		channel string
	}{
		{"/api/v1/streaming/user", "user"},
		{"/api/v1/streaming/user/notification", "user:notification"},
		{"/api/v1/streaming/public", "public"},
		{"/api/v1/streaming/public?only_media=true", "public:media"},
		{"/api/v1/streaming/public/local", "public:local"},
		{"/api/v1/streaming/public/local?only_media=1", "public:local:media"},
		{"/api/v1/streaming/public/remote", "public:remote"},
		{"/api/v1/streaming/public/remote?only_media=on", "public:remote:media"},
		{"/api/v1/streaming/hashtag", "hashtag"},
		{"/api/v1/streaming/hashtag/local", "hashtag:local"},
		{"/api/v1/streaming/direct", "direct"},
		{"/api/v1/streaming/list", "list"},
	}
	for _, test := range valid {
		t.Run(test.channel+"_valid", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.url, nil)
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			if got := streamingSSEChannel(c); got != test.channel {
				t.Fatalf("streamingSSEChannel(%q) = %q, want %q", test.url, got, test.channel)
			}
		})
	}

	invalid := []string{
		"/api/v1/streaming/public/media",
		"/api/v1/streaming/public/",
		"/api/v1/streaming/user/notification/",
		"/api/v1/streaming/public/local/media",
		"/api/v1/streaming?stream=public",
		"/api/v1/streaming/",
	}
	for _, requestURL := range invalid {
		t.Run(requestURL+"_invalid", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, requestURL, nil)
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			if got := streamingSSEChannel(c); got != "" {
				t.Fatalf("streamingSSEChannel(%q) = %q, want unknown channel", requestURL, got)
			}
			err := (&Server{}).streaming(c)
			var apiErr apiHTTPError
			if !errors.As(err, &apiErr) || apiErr.status != http.StatusBadRequest || apiErr.message != "Error: Unknown channel requested" {
				t.Fatalf("streaming(%q) error = %#v, want unknown-channel 400", requestURL, err)
			}
		})
	}
}

func TestStreamingMetricsRouteIsUnauthenticatedAndPrometheusCompatible(t *testing.T) {
	e := echo.New()
	s := &Server{echo: e}
	e.Use(s.apiAuthenticationGateMiddleware)
	s.routes()

	releaseClient := s.streamMetrics.trackClient(streamingMetricWebSocket)
	defer releaseClient()
	releaseChannel := s.streamMetrics.trackChannel(streamingMetricWebSocket, "public", 1)
	defer releaseChannel()
	releaseRedis := s.streamMetrics.trackRedisSubscriptions([]string{"paon:timeline:public"})
	defer releaseRedis()
	s.streamMetrics.incrementRedisMessagesReceived()
	s.streamMetrics.incrementMessagesSent(streamingMetricWebSocket)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != streamingMetricsContentType {
		t.Fatalf("Content-Type = %q", got)
	}
	body := rec.Body.String()
	assertPrometheus004(t, body)

	for _, expected := range []string{
		"pg_pool_total_connections 0",
		"pg_pool_idle_connections 0",
		"pg_pool_waiting_queries 0",
		`connected_clients{type="websocket"} 1`,
		`connected_clients{type="eventsource"} 0`,
		`connected_channels{type="websocket",channel="public"} 1`,
		"redis_subscriptions 1",
		"redis_messages_received_total 1",
		`messages_sent_total{type="websocket"} 1`,
		`messages_sent_total{type="eventsource"} 0`,
	} {
		if !strings.Contains(body, expected+"\n") {
			t.Errorf("metrics body does not contain %q", expected)
		}
	}
	for _, connectionType := range streamingMetricConnectionTypes() {
		for _, channel := range streamingMetricChannelNames {
			series := `connected_channels{type="` + connectionType + `",channel="` + channel + `"}`
			if !strings.Contains(body, series+" ") {
				t.Errorf("metrics body does not contain zero-primed series %q", series)
			}
		}
	}
}

func TestStreamingMetricsHandlerIsNilServerSafe(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	var server *Server

	if err := server.streamingMetrics(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "connected_clients") {
		t.Fatalf("nil server metrics response = %d %q", rec.Code, rec.Body.String())
	}
}

func TestStreamingMetricsExpositionUsesDefensibleDBStats(t *testing.T) {
	var metrics streamingMetricState
	body := metrics.prometheus(sql.DBStats{
		OpenConnections: 7,
		Idle:            3,
		WaitCount:       99,
	})

	for _, expected := range []string{
		"pg_pool_total_connections 7\n",
		"pg_pool_idle_connections 3\n",
		"pg_pool_waiting_queries 0\n",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics body does not contain %q", expected)
		}
	}
	if strings.Contains(body, "pg_pool_waiting_queries 99") {
		t.Fatal("cumulative database/sql WaitCount was exposed as a current waiting gauge")
	}
}

func TestStreamingMetricsInstrumentationIsConcurrencySafeAndRefcountsRedisChannels(t *testing.T) {
	var metrics streamingMetricState

	releaseFirst := metrics.trackRedisSubscriptions([]string{"prefix:a", "prefix:a", "prefix:b"})
	releaseSecond := metrics.trackRedisSubscriptions([]string{"prefix:b", "prefix:c"})
	if got := metrics.snapshot().redisSubscriptions; got != 3 {
		t.Fatalf("redis_subscriptions = %d, want 3 unique channels", got)
	}
	releaseFirst()
	releaseFirst()
	if got := metrics.snapshot().redisSubscriptions; got != 2 {
		t.Fatalf("redis_subscriptions after first release = %d, want 2", got)
	}
	releaseSecond()
	if got := metrics.snapshot().redisSubscriptions; got != 0 {
		t.Fatalf("redis_subscriptions after all releases = %d, want 0", got)
	}

	const workers = 64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releaseClient := metrics.trackClient(streamingMetricEventSource)
			releaseChannel := metrics.trackChannel(streamingMetricEventSource, "hashtag", 1)
			metrics.incrementRedisMessagesReceived()
			metrics.incrementMessagesSent(streamingMetricEventSource)
			releaseChannel()
			releaseClient()
		}()
	}
	wg.Wait()

	snapshot := metrics.snapshot()
	if snapshot.connectedClients[1] != 0 {
		t.Fatalf("connected eventsource clients = %d, want 0", snapshot.connectedClients[1])
	}
	hashtagIndex, _ := streamingMetricChannelIndex("hashtag")
	if snapshot.connectedChannels[1][hashtagIndex] != 0 {
		t.Fatalf("connected eventsource hashtag channels = %d, want 0", snapshot.connectedChannels[1][hashtagIndex])
	}
	if snapshot.redisMessagesReceived != workers {
		t.Fatalf("redis_messages_received_total = %d, want %d", snapshot.redisMessagesReceived, workers)
	}
	if snapshot.messagesSent[1] != workers {
		t.Fatalf("eventsource messages_sent_total = %d, want %d", snapshot.messagesSent[1], workers)
	}
}

func TestStreamingMetricsInstrumentationHooksCoverStreamingPaths(t *testing.T) {
	checks := map[string][]string{
		"streaming.go": {
			"s.streamMetrics.trackClient(streamingMetricEventSource)",
			"s.streamMetrics.trackChannel(streamingMetricEventSource, channel, 1)",
			"s.streamMetrics.trackChannel(streamingMetricEventSource, \"system\"",
			"s.streamMetrics.incrementMessagesSent(streamingMetricEventSource)",
		},
		"streaming_websocket.go": {
			"s.streamMetrics.trackClient(streamingMetricWebSocket)",
			"s.streamMetrics.trackChannel(streamingMetricWebSocket, channel, 1)",
			"s.streamMetrics.trackChannel(streamingMetricWebSocket, \"system\"",
			"s.streamMetrics.incrementMessagesSent(streamingMetricWebSocket)",
		},
		"redis_pubsub.go": {
			"s.streamMetrics.trackRedisSubscriptions(prefixed)",
			"s.streamMetrics.incrementRedisMessagesReceived()",
		},
	}

	for filename, snippets := range checks {
		source, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, snippet := range snippets {
			if !strings.Contains(string(source), snippet) {
				t.Errorf("%s does not contain instrumentation hook %q", filename, snippet)
			}
		}
	}
}

func assertPrometheus004(t *testing.T, body string) {
	t.Helper()
	metricName := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	seenTypes := map[string]bool{}
	for lineNumber, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "# HELP ") {
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			if len(fields) != 4 || (fields[3] != "gauge" && fields[3] != "counter") {
				t.Fatalf("invalid Prometheus TYPE line %d: %q", lineNumber+1, line)
			}
			seenTypes[fields[2]] = true
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid Prometheus sample line %d: %q", lineNumber+1, line)
		}
		name := strings.SplitN(fields[0], "{", 2)[0]
		if !metricName.MatchString(name) || !seenTypes[name] {
			t.Fatalf("sample line %d has invalid or untyped metric name %q", lineNumber+1, name)
		}
		if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
			t.Fatalf("sample line %d has invalid value %q", lineNumber+1, fields[1])
		}
	}
}
