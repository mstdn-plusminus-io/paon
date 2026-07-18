package api

import (
	"context"
	"strconv"
	"strings"
	"time"
)

const (
	indexingWorkerInterval  = time.Minute
	indexingImportBatchSize = 1000
	indexingScanBatchSize   = 10 * indexingImportBatchSize
)

type queuedMeiliIndex struct {
	IndexName string
	Handle    func(context.Context, []int64)
}

func (s *Server) runIndexingWorker(ctx context.Context) {
	s.runSchedulerWithRedisLock(ctx, "indexing_scheduler", 30*time.Minute, func() {
		s.processQueuedMeiliIndexes(ctx)
	})
	ticker := time.NewTicker(indexingWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "indexing_scheduler", 30*time.Minute, func() {
				s.processQueuedMeiliIndexes(ctx)
			})
		}
	}
}

func (s *Server) processQueuedMeiliIndexes(ctx context.Context) int {
	if s == nil || !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return 0
	}
	total := 0
	for _, index := range s.queuedMeiliIndexes() {
		total += s.processQueuedMeiliIndex(ctx, index)
	}
	return total
}

func (s *Server) queuedMeiliIndexes() []queuedMeiliIndex {
	return []queuedMeiliIndex{
		{IndexName: "AccountsIndex", Handle: s.indexQueuedMeiliAccounts},
		{IndexName: "TagsIndex", Handle: s.indexQueuedMeiliTags},
		{IndexName: "PublicStatusesIndex", Handle: s.indexQueuedMeiliStatuses},
		{IndexName: "StatusesIndex", Handle: s.indexQueuedMeiliStatuses},
	}
}

func (s *Server) processQueuedMeiliIndex(ctx context.Context, index queuedMeiliIndex) int {
	key := redisConfig(s.cfg).prefix + "chewy:queue:" + index.IndexName
	cursor := "0"
	processed := 0
	for {
		value, err := s.redisCommand(ctx, "SSCAN", key, cursor, "COUNT", strconv.Itoa(indexingScanBatchSize))
		if err != nil {
			return processed
		}
		next, members, ok := redisScanResult(value)
		if !ok {
			return processed
		}
		for _, batch := range stringBatches(members, indexingImportBatchSize) {
			ids := parsePositiveInt64s(batch)
			if len(ids) > 0 {
				index.Handle(ctx, ids)
				processed += len(ids)
			}
			_, _ = s.redisCommand(ctx, append([]string{"SREM", key}, batch...)...)
		}
		if next == "0" {
			return processed
		}
		cursor = next
	}
}

func (s *Server) indexQueuedMeiliAccounts(ctx context.Context, ids []int64) {
	for _, id := range ids {
		s.meiliIndexAccountBestEffort(ctx, id)
	}
}

func (s *Server) indexQueuedMeiliTags(ctx context.Context, ids []int64) {
	s.meiliIndexTagsBestEffort(ctx, ids)
}

func (s *Server) indexQueuedMeiliStatuses(ctx context.Context, ids []int64) {
	for _, id := range ids {
		s.meiliIndexStatusBestEffort(ctx, id)
	}
}

func redisScanResult(value any) (string, []string, bool) {
	items, ok := value.([]any)
	if !ok || len(items) != 2 {
		return "", nil, false
	}
	cursor, ok := items[0].(string)
	if !ok {
		return "", nil, false
	}
	members, ok := redisStringArray(items[1])
	return cursor, members, ok
}

func stringBatches(values []string, size int) [][]string {
	if size <= 0 || len(values) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(values)+size-1)/size)
	for len(values) > 0 {
		n := size
		if len(values) < n {
			n = len(values)
		}
		out = append(out, values[:n])
		values = values[n:]
	}
	return out
}

func parsePositiveInt64s(values []string) []int64 {
	out := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}
