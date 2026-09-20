package api

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

type statsDClient struct {
	conn      net.Conn
	namespace string
}

func newStatsDClient(cfg config.Config) *statsDClient {
	// StatsD is a Paon compatibility extension. Mastodon 4.3 replaced it with
	// OpenTelemetry, so never emit both metric streams for the same operation.
	if cfg.OpenTelemetryEnabled {
		return nil
	}
	addr := strings.TrimSpace(cfg.StatsDAddr)
	if addr == "" {
		return nil
	}
	conn, err := net.DialTimeout("udp", addr, time.Second)
	if err != nil {
		return nil
	}
	return &statsDClient{conn: conn, namespace: cfg.StatsDNamespace}
}

func statsDMiddleware(cfg config.Config) echo.MiddlewareFunc {
	client := newStatsDClient(cfg)
	if client == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()
			original := c.Response()
			recorder := &statsDStatusRecorder{ResponseWriter: original}
			c.SetResponse(recorder)
			defer c.SetResponse(original)
			err := next(c)
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			client.timing("web.request", time.Since(start))
			client.increment("web.status." + strconv.Itoa(status))
			return err
		}
	}
}

type statsDStatusRecorder struct {
	http.ResponseWriter
	status int
}

const statsDInformantInterval = 10 * time.Second

func (r *statsDStatusRecorder) WriteHeader(statusCode int) {
	if r.status == 0 {
		r.status = statusCode
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *statsDStatusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func (c *statsDClient) increment(name string) {
	c.write(name + ":1|c")
}

func (c *statsDClient) timing(name string, duration time.Duration) {
	c.write(fmt.Sprintf("%s:%d|ms", name, duration.Milliseconds()))
}

func (c *statsDClient) gauge(name string, value int64) {
	c.write(fmt.Sprintf("%s:%d|g", name, value))
}

func asynqStatsDHandler(client *statsDClient, taskType string, handler func(context.Context, *asynq.Task) error) func(context.Context, *asynq.Task) error {
	if client == nil {
		return handler
	}
	taskMetric := "sidekiq.job." + sanitizeStatsDMetricPart(taskType)
	return func(ctx context.Context, task *asynq.Task) error {
		start := time.Now()
		err := handler(ctx, task)
		client.timing(taskMetric+".runtime", time.Since(start))
		if err != nil {
			client.increment(taskMetric + ".failure")
			return err
		}
		client.increment(taskMetric + ".success")
		return nil
	}
}

func asynqStatsDMiddleware(client *statsDClient) asynq.MiddlewareFunc {
	return func(next asynq.Handler) asynq.Handler {
		if client == nil {
			return next
		}
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
			taskType := ""
			if task != nil {
				taskType = task.Type()
			}
			return asynqStatsDHandler(client, taskType, next.ProcessTask)(ctx, task)
		})
	}
}

func (s *Server) runStatsDInformantWorker(ctx context.Context) {
	client := newStatsDClient(s.cfg)
	if client == nil {
		return
	}
	s.collectStatsDInformants(ctx, client)
	ticker := time.NewTicker(statsDInformantInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectStatsDInformants(ctx, client)
		}
	}
}

func (s *Server) collectStatsDInformants(ctx context.Context, client *statsDClient) {
	if client == nil {
		return
	}
	s.collectStatsDDBInformant(client)
	s.collectStatsDCacheInformant(ctx, client)
}

func (s *Server) collectStatsDDBInformant(client *statsDClient) {
	if s == nil || s.db == nil {
		return
	}
	sqlDB, err := s.db.DB()
	if err != nil || sqlDB == nil {
		client.increment("db.pool.unavailable")
		return
	}
	emitStatsDDBStats(client, sqlDB.Stats())
}

func emitStatsDDBStats(client *statsDClient, stats sql.DBStats) {
	client.gauge("db.pool.open", int64(stats.OpenConnections))
	client.gauge("db.pool.in_use", int64(stats.InUse))
	client.gauge("db.pool.idle", int64(stats.Idle))
	client.gauge("db.pool.wait_count", stats.WaitCount)
	client.timing("db.pool.wait_duration", stats.WaitDuration)
}

func (s *Server) collectStatsDCacheInformant(ctx context.Context, client *statsDClient) {
	if s == nil {
		return
	}
	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	start := time.Now()
	value, err := s.redisCommand(pingCtx, "PING")
	if err != nil {
		client.increment("cache.redis.ping.failure")
		return
	}
	if pong, ok := value.(string); !ok || !strings.EqualFold(pong, "PONG") {
		client.increment("cache.redis.ping.failure")
		return
	}
	client.timing("cache.redis.ping", time.Since(start))
	client.increment("cache.redis.ping.success")
}

func (c *statsDClient) write(metric string) {
	if c == nil || c.conn == nil {
		return
	}
	line := sanitizeStatsDMetric(metric)
	if c.namespace != "" {
		line = c.namespace + "." + line
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	_, _ = c.conn.Write([]byte(line))
}

func sanitizeStatsDMetric(metric string) string {
	metric = strings.TrimSpace(metric)
	if metric == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(" ", "_", "\n", "_", "\r", "_", "\t", "_")
	return strings.Trim(replacer.Replace(metric), ".")
}

func sanitizeStatsDMetricPart(metric string) string {
	metric = sanitizeStatsDMetric(metric)
	replacer := strings.NewReplacer(":", "_", "/", "_", "\\", "_", "|", "_", "@", "_")
	return replacer.Replace(metric)
}
