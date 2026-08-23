package api

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

var adminSidekiqRequiredQueues = []string{"default", "push", "mailers", "pull", "removal", "remote_removal", "scheduler", "ingress"}

const (
	paonGoWorkerProcessesKey     = "paon:worker:processes"
	paonGoWorkerProcessKeyPrefix = "paon:worker:process:"
	paonGoWorkerHeartbeatTTL     = 90 * time.Second
)

func (s *Server) adminDashboardSidekiqProcessCheck() (adminDashboardSystemCheck, bool) {
	missing := s.adminDashboardMissingSidekiqQueues()
	if len(missing) == 0 {
		return adminDashboardSystemCheck{}, false
	}
	return adminDashboardSystemCheck{
		Key:   "sidekiq_process_check",
		Value: strings.Join(missing, ", "),
	}, true
}

func (s *Server) adminDashboardMissingSidekiqQueues() []string {
	covered := map[string]struct{}{}
	for _, queue := range s.adminDashboardPaonGoWorkerQueues() {
		covered[queue] = struct{}{}
	}
	for _, queue := range s.adminDashboardRailsSidekiqProcessQueues() {
		covered[queue] = struct{}{}
	}

	missing := make([]string, 0)
	for _, queue := range adminSidekiqRequiredQueues {
		if _, ok := covered[queue]; !ok {
			missing = append(missing, queue)
		}
	}
	return missing
}

func (s *Server) adminDashboardPaonGoWorkerQueues() []string {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.adminDashboardPaonGoWorkerQueuesFromHeartbeat(ctx, time.Now().UTC())
}

func paonGoWorkerQueueCoverage(cfg config.Config) []string {
	// Asynq covers queued Rails workers, while dedicated goroutines cover Rails'
	// scheduler and ingress queue boundaries.
	weights := paonGoAsynqQueueWeightsForConfig(cfg)
	queues := make([]string, 0, len(weights)+2)
	for queue := range weights {
		queues = append(queues, asynqLogicalQueueName(cfg.RedisNamespace, queue))
	}
	queues = append(queues, "scheduler", "ingress")
	sort.Strings(queues)
	return queues
}

func (s *Server) adminDashboardPaonGoWorkerQueuesFromHeartbeat(ctx context.Context, now time.Time) []string {
	key := redisConfig(s.cfg).prefix + paonGoWorkerProcessesKey
	nowUnix := strconv.FormatInt(now.UTC().Unix(), 10)
	_, _ = s.redisCommand(ctx, "ZREMRANGEBYSCORE", key, "-inf", nowUnix)
	value, err := s.redisCommand(ctx, "ZRANGEBYSCORE", key, nowUnix, "+inf")
	if err != nil {
		return nil
	}
	identities, ok := redisStringArray(value)
	if !ok {
		return nil
	}
	covered := map[string]struct{}{}
	for _, identity := range identities {
		for _, queue := range s.paonGoWorkerQueuesForIdentity(ctx, identity) {
			covered[queue] = struct{}{}
		}
	}
	out := make([]string, 0, len(covered))
	for queue := range covered {
		out = append(out, queue)
	}
	sort.Strings(out)
	return out
}

func (s *Server) paonGoWorkerQueuesForIdentity(ctx context.Context, identity string) []string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil
	}
	value, err := s.redisCommand(ctx, "HGET", redisConfig(s.cfg).prefix+paonGoWorkerProcessKeyPrefix+identity, "queues")
	if err != nil {
		return nil
	}
	raw, _ := value.(string)
	return sidekiqProcessQueuesFromRedis(raw)
}

func paonGoWorkerIdentity() string {
	host, _ := os.Hostname()
	host = strings.TrimSpace(host)
	if host == "" {
		host = "unknown"
	}
	return host + ":" + strconv.Itoa(os.Getpid())
}

func (s *Server) runPaonGoWorkerHeartbeat(ctx context.Context) {
	identity := paonGoWorkerIdentity()
	s.recordPaonGoWorkerHeartbeat(ctx, identity, time.Now().UTC())
	ticker := time.NewTicker(paonGoWorkerHeartbeatTTL / 3)
	defer ticker.Stop()
	defer s.clearPaonGoWorkerHeartbeat(context.Background(), identity)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.recordPaonGoWorkerHeartbeat(ctx, identity, now.UTC())
		}
	}
}

func (s *Server) recordPaonGoWorkerHeartbeat(ctx context.Context, identity string, now time.Time) {
	identity = strings.TrimSpace(identity)
	if s == nil || identity == "" {
		return
	}
	cfg := redisConfig(s.cfg)
	processesKey := cfg.prefix + paonGoWorkerProcessesKey
	processKey := cfg.prefix + paonGoWorkerProcessKeyPrefix + identity
	queues, err := json.Marshal(paonGoWorkerQueueCoverage(s.cfg))
	if err != nil {
		return
	}
	expiresAt := strconv.FormatInt(now.UTC().Add(paonGoWorkerHeartbeatTTL).Unix(), 10)
	updatedAt := strconv.FormatInt(now.UTC().Unix(), 10)
	ttl := strconv.FormatInt(int64(paonGoWorkerHeartbeatTTL/time.Second), 10)
	_, _ = s.redisCommand(ctx, "ZREMRANGEBYSCORE", processesKey, "-inf", updatedAt)
	_, _ = s.redisCommand(ctx, "ZADD", processesKey, expiresAt, identity)
	_, _ = s.redisCommand(ctx, "HSET", processKey, "queues", string(queues), "updated_at", updatedAt)
	_, _ = s.redisCommand(ctx, "EXPIRE", processKey, ttl)
}

func (s *Server) clearPaonGoWorkerHeartbeat(ctx context.Context, identity string) {
	identity = strings.TrimSpace(identity)
	if s == nil || identity == "" {
		return
	}
	cfg := redisConfig(s.cfg)
	_, _ = s.redisCommand(ctx, "ZREM", cfg.prefix+paonGoWorkerProcessesKey, identity)
	_, _ = s.redisCommand(ctx, "DEL", cfg.prefix+paonGoWorkerProcessKeyPrefix+identity)
}

func (s *Server) adminDashboardRailsSidekiqProcessQueues() []string {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, "SMEMBERS", redisConfig(s.cfg).prefix+"processes")
	if err != nil {
		return nil
	}
	identities, ok := redisStringArray(value)
	if !ok {
		return nil
	}
	queues := map[string]struct{}{}
	for _, identity := range identities {
		for _, queue := range s.adminDashboardRailsSidekiqProcessQueuesForIdentity(ctx, identity) {
			queues[queue] = struct{}{}
		}
	}
	out := make([]string, 0, len(queues))
	for queue := range queues {
		out = append(out, queue)
	}
	sort.Strings(out)
	return out
}

func (s *Server) adminDashboardRailsSidekiqProcessQueuesForIdentity(ctx context.Context, identity string) []string {
	if strings.TrimSpace(identity) == "" {
		return nil
	}
	value, err := s.redisCommand(ctx, "HGET", redisConfig(s.cfg).prefix+identity, "queues")
	if err != nil {
		return nil
	}
	raw, _ := value.(string)
	return sidekiqProcessQueuesFromRedis(raw)
}

func sidekiqProcessQueuesFromRedis(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var queues []string
	if err := json.Unmarshal([]byte(raw), &queues); err != nil {
		queues = strings.Split(raw, ",")
	}
	out := make([]string, 0, len(queues))
	seen := map[string]struct{}{}
	for _, queue := range queues {
		queue = strings.TrimSpace(strings.Trim(queue, `"`))
		if queue == "" {
			continue
		}
		if _, ok := seen[queue]; ok {
			continue
		}
		seen[queue] = struct{}{}
		out = append(out, queue)
	}
	return out
}
