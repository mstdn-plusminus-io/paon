package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"
)

const (
	streamingMetricsContentType      = "text/plain; version=0.0.4; charset=utf-8"
	streamingMetricWebSocket         = "websocket"
	streamingMetricEventSource       = "eventsource"
	streamingMetricChannelNameCount  = 13
	streamingMetricConnectionTypeMax = 2
)

var streamingMetricChannelNames = [streamingMetricChannelNameCount]string{
	"system",
	"user",
	"user:notification",
	"list",
	"direct",
	"public",
	"public:media",
	"public:local",
	"public:local:media",
	"public:remote",
	"public:remote:media",
	"hashtag",
	"hashtag:local",
}

type streamingMetricState struct {
	mu                    sync.RWMutex
	connectedClients      [streamingMetricConnectionTypeMax]int64
	connectedChannels     [streamingMetricConnectionTypeMax][streamingMetricChannelNameCount]int64
	redisChannelRefCounts map[string]int64
	redisMessagesReceived uint64
	messagesSent          [streamingMetricConnectionTypeMax]uint64
}

type streamingMetricSnapshot struct {
	connectedClients      [streamingMetricConnectionTypeMax]int64
	connectedChannels     [streamingMetricConnectionTypeMax][streamingMetricChannelNameCount]int64
	redisSubscriptions    int64
	redisMessagesReceived uint64
	messagesSent          [streamingMetricConnectionTypeMax]uint64
}

func (s *Server) streamingMetrics(c *echo.Context) error {
	var stats sql.DBStats
	metrics := &streamingMetricState{}
	if s != nil {
		metrics = &s.streamMetrics
	}
	if s != nil && s.db != nil {
		if sqlDB, err := s.db.DB(); err == nil {
			stats = sqlDB.Stats()
		}
	}

	return c.Blob(http.StatusOK, streamingMetricsContentType, []byte(metrics.prometheus(stats)))
}

func (m *streamingMetricState) trackClient(connectionType string) func() {
	index, ok := streamingMetricConnectionTypeIndex(connectionType)
	if !ok {
		return func() {}
	}

	m.mu.Lock()
	m.connectedClients[index]++
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.connectedClients[index] > 0 {
				m.connectedClients[index]--
			}
			m.mu.Unlock()
		})
	}
}

func (m *streamingMetricState) trackChannel(connectionType, channel string, count int64) func() {
	connectionIndex, ok := streamingMetricConnectionTypeIndex(connectionType)
	if !ok || count <= 0 {
		return func() {}
	}
	channelIndex, ok := streamingMetricChannelIndex(channel)
	if !ok {
		return func() {}
	}

	m.mu.Lock()
	m.connectedChannels[connectionIndex][channelIndex] += count
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			value := &m.connectedChannels[connectionIndex][channelIndex]
			if *value <= count {
				*value = 0
			} else {
				*value -= count
			}
			m.mu.Unlock()
		})
	}
}

func (m *streamingMetricState) trackRedisSubscriptions(channels []string) func() {
	uniqueChannels := make([]string, 0, len(channels))
	seen := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		if channel == "" {
			continue
		}
		if _, ok := seen[channel]; ok {
			continue
		}
		seen[channel] = struct{}{}
		uniqueChannels = append(uniqueChannels, channel)
	}
	if len(uniqueChannels) == 0 {
		return func() {}
	}

	m.mu.Lock()
	if m.redisChannelRefCounts == nil {
		m.redisChannelRefCounts = make(map[string]int64)
	}
	for _, channel := range uniqueChannels {
		m.redisChannelRefCounts[channel]++
	}
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			for _, channel := range uniqueChannels {
				if m.redisChannelRefCounts[channel] <= 1 {
					delete(m.redisChannelRefCounts, channel)
				} else {
					m.redisChannelRefCounts[channel]--
				}
			}
			m.mu.Unlock()
		})
	}
}

func (m *streamingMetricState) incrementRedisMessagesReceived() {
	m.mu.Lock()
	m.redisMessagesReceived++
	m.mu.Unlock()
}

func (m *streamingMetricState) incrementMessagesSent(connectionType string) {
	index, ok := streamingMetricConnectionTypeIndex(connectionType)
	if !ok {
		return
	}
	m.mu.Lock()
	m.messagesSent[index]++
	m.mu.Unlock()
}

func (m *streamingMetricState) snapshot() streamingMetricSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return streamingMetricSnapshot{
		connectedClients:      m.connectedClients,
		connectedChannels:     m.connectedChannels,
		redisSubscriptions:    int64(len(m.redisChannelRefCounts)),
		redisMessagesReceived: m.redisMessagesReceived,
		messagesSent:          m.messagesSent,
	}
}

func (m *streamingMetricState) prometheus(stats sql.DBStats) string {
	snapshot := m.snapshot()
	var out strings.Builder

	writeStreamingMetricHeader(&out, "pg_pool_total_connections", "The total number of clients existing within the pool", "gauge")
	fmt.Fprintf(&out, "pg_pool_total_connections %d\n\n", stats.OpenConnections)
	writeStreamingMetricHeader(&out, "pg_pool_idle_connections", "The number of clients which are not checked out but are currently idle in the pool", "gauge")
	fmt.Fprintf(&out, "pg_pool_idle_connections %d\n\n", stats.Idle)
	writeStreamingMetricHeader(&out, "pg_pool_waiting_queries", "The number of queued requests waiting on a client when all clients are checked out", "gauge")
	// database/sql exposes only cumulative WaitCount, not the number currently waiting.
	out.WriteString("pg_pool_waiting_queries 0\n\n")

	writeStreamingMetricHeader(&out, "connected_clients", "The number of clients connected to the streaming server", "gauge")
	for connectionIndex, connectionType := range streamingMetricConnectionTypes() {
		fmt.Fprintf(&out, "connected_clients{type=\"%s\"} %d\n", prometheusLabelValue(connectionType), snapshot.connectedClients[connectionIndex])
	}
	out.WriteByte('\n')

	writeStreamingMetricHeader(&out, "connected_channels", "The number of channels the streaming server is streaming to", "gauge")
	for connectionIndex, connectionType := range streamingMetricConnectionTypes() {
		for channelIndex, channel := range streamingMetricChannelNames {
			fmt.Fprintf(&out, "connected_channels{type=\"%s\",channel=\"%s\"} %d\n", prometheusLabelValue(connectionType), prometheusLabelValue(channel), snapshot.connectedChannels[connectionIndex][channelIndex])
		}
	}
	out.WriteByte('\n')

	writeStreamingMetricHeader(&out, "redis_subscriptions", "The number of Redis channels the streaming server is subscribed to", "gauge")
	fmt.Fprintf(&out, "redis_subscriptions %d\n\n", snapshot.redisSubscriptions)
	writeStreamingMetricHeader(&out, "redis_messages_received_total", "The total number of messages the streaming server has received from redis subscriptions", "counter")
	fmt.Fprintf(&out, "redis_messages_received_total %d\n\n", snapshot.redisMessagesReceived)
	writeStreamingMetricHeader(&out, "messages_sent_total", "The total number of messages the streaming server sent to clients per connection type", "counter")
	for connectionIndex, connectionType := range streamingMetricConnectionTypes() {
		fmt.Fprintf(&out, "messages_sent_total{type=\"%s\"} %d\n", prometheusLabelValue(connectionType), snapshot.messagesSent[connectionIndex])
	}

	return out.String()
}

func writeStreamingMetricHeader(out *strings.Builder, name, help, metricType string) {
	fmt.Fprintf(out, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func streamingMetricConnectionTypes() [streamingMetricConnectionTypeMax]string {
	return [streamingMetricConnectionTypeMax]string{streamingMetricWebSocket, streamingMetricEventSource}
}

func streamingMetricConnectionTypeIndex(connectionType string) (int, bool) {
	for index, candidate := range streamingMetricConnectionTypes() {
		if connectionType == candidate {
			return index, true
		}
	}
	return 0, false
}

func streamingMetricChannelIndex(channel string) (int, bool) {
	for index, candidate := range streamingMetricChannelNames {
		if channel == candidate {
			return index, true
		}
	}
	return 0, false
}

func prometheusLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
