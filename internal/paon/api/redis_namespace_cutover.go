package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/redis/go-redis/v9"
)

type RedisNamespaceCutoverResult struct {
	Topologies        int
	Keys              int
	AsynqQueueMembers int
	Migrated          int
}

type redisNamespaceRename struct {
	source string
	target string
}

type redisNamespaceTransform struct {
	sourcePrefix string
	targetPrefix string
}

type redisNamespaceTopology struct {
	config     redisConnConfig
	names      []string
	transforms []redisNamespaceTransform
}

// CutoverRedisNamespace performs the explicit Mastodon 4.4 namespace
// migration. Every Paon process must be stopped by the operator before this is
// called. Dry-run executes the same complete collision preflight as confirm.
// Confirm uses RENAMENX so a concurrent target can never be overwritten.
func CutoverRedisNamespace(ctx context.Context, cfg config.Config, namespace string, dryRun bool) (RedisNamespaceCutoverResult, error) {
	var result RedisNamespaceCutoverResult
	namespace, err := validateRedisNamespaceCutoverName(namespace)
	if err != nil {
		return result, err
	}
	if configured := strings.TrimSuffix(strings.TrimSpace(cfg.RedisNamespace), ":"); configured != "" && configured != namespace {
		return result, fmt.Errorf("redis namespace cutover --prefix %q does not match configured REDIS_NAMESPACE %q", namespace, configured)
	}
	topologies := redisNamespaceCutoverTopologies(cfg, namespace)
	result.Topologies = len(topologies)
	for _, topology := range topologies {
		client := redisNamespaceCutoverClient(topology.config)
		if client == nil {
			return result, fmt.Errorf("redis namespace cutover %s: unsupported Redis topology", strings.Join(topology.names, "/"))
		}
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return result, fmt.Errorf("redis namespace cutover %s preflight: %w", strings.Join(topology.names, "/"), err)
		}
		renames, err := redisNamespaceRenamePlan(ctx, client, topology.transforms)
		if err != nil {
			_ = client.Close()
			return result, fmt.Errorf("redis namespace cutover %s preflight: %w", strings.Join(topology.names, "/"), err)
		}
		queueMembers, err := redisNamespaceAsynqQueueMemberPlan(ctx, client, namespace)
		if err != nil {
			_ = client.Close()
			return result, fmt.Errorf("redis namespace cutover %s asynq preflight: %w", strings.Join(topology.names, "/"), err)
		}
		result.Keys += len(renames)
		result.AsynqQueueMembers += len(queueMembers)
		if dryRun {
			_ = client.Close()
			continue
		}
		for _, rename := range renames {
			moved, err := client.RenameNX(ctx, rename.source, rename.target).Result()
			if err != nil {
				_ = client.Close()
				return result, fmt.Errorf("redis namespace cutover %s rename failed after %d changes: %w", strings.Join(topology.names, "/"), result.Migrated, err)
			}
			if !moved {
				_ = client.Close()
				return result, fmt.Errorf("redis namespace cutover %s target %q appeared after preflight; stopped after %d changes without overwriting it", strings.Join(topology.names, "/"), rename.target, result.Migrated)
			}
			result.Migrated++
		}
		for _, rename := range queueMembers {
			added, err := client.SAdd(ctx, "asynq:queues", rename.target).Result()
			if err != nil {
				_ = client.Close()
				return result, fmt.Errorf("redis namespace cutover %s asynq queue update failed after %d changes: %w", strings.Join(topology.names, "/"), result.Migrated, err)
			}
			if added == 0 {
				_ = client.Close()
				return result, fmt.Errorf("redis namespace cutover %s asynq target queue %q appeared after preflight", strings.Join(topology.names, "/"), rename.target)
			}
			if err := client.SRem(ctx, "asynq:queues", rename.source).Err(); err != nil {
				_ = client.Close()
				return result, fmt.Errorf("redis namespace cutover %s asynq source queue cleanup failed: %w", strings.Join(topology.names, "/"), err)
			}
			result.Migrated++
		}
		if err := client.Close(); err != nil {
			return result, fmt.Errorf("redis namespace cutover %s close: %w", strings.Join(topology.names, "/"), err)
		}
	}
	return result, nil
}

func validateRedisNamespaceCutoverName(namespace string) (string, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "", errors.New("redis namespace cutover requires a non-empty --prefix")
	}
	if strings.HasSuffix(namespace, ":") {
		return "", errors.New("redis namespace cutover --prefix must be the REDIS_NAMESPACE value without a trailing colon")
	}
	if len(namespace) > 200 || strings.IndexFunc(namespace, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune(`*?[]\\`, r)
	}) >= 0 {
		return "", errors.New("redis namespace cutover --prefix must not contain whitespace, controls, or Redis glob characters")
	}
	return namespace, nil
}

func redisNamespaceCutoverTopologies(cfg config.Config, namespace string) []redisNamespaceTopology {
	items := []struct {
		name       string
		config     redisConnConfig
		transforms []redisNamespaceTransform
	}{
		{name: "base", config: redisConfig(cfg), transforms: []redisNamespaceTransform{{sourcePrefix: namespace + ":"}}},
		{name: "sidekiq", config: sidekiqRedisConfig(cfg), transforms: []redisNamespaceTransform{
			{sourcePrefix: namespace + ":"},
			{sourcePrefix: "asynq:{" + namespace + ":", targetPrefix: "asynq:{"},
		}},
		{name: "cache", config: cacheRedisConfig(cfg), transforms: []redisNamespaceTransform{
			{sourcePrefix: namespace + "_cache:", targetPrefix: "cache:"},
			// Paon 4.2 used its base prefix for cache helpers, whereas Rails used
			// NAME_cache. Preserve both upgrade paths.
			{sourcePrefix: namespace + ":"},
		}},
	}
	byEndpoint := make(map[string]*redisNamespaceTopology)
	order := make([]string, 0, len(items))
	for _, item := range items {
		key := redisConnAvailabilityKey(item.config)
		topology := byEndpoint[key]
		if topology == nil {
			topology = &redisNamespaceTopology{config: item.config}
			byEndpoint[key] = topology
			order = append(order, key)
		}
		topology.names = append(topology.names, item.name)
		for _, transform := range item.transforms {
			duplicate := false
			for _, existing := range topology.transforms {
				if existing == transform {
					duplicate = true
					break
				}
			}
			if !duplicate {
				topology.transforms = append(topology.transforms, transform)
			}
		}
	}
	out := make([]redisNamespaceTopology, 0, len(order))
	for _, key := range order {
		out = append(out, *byEndpoint[key])
	}
	return out
}

func redisNamespaceCutoverClient(cfg redisConnConfig) redis.UniversalClient {
	tlsConfig := (*tls.Config)(nil)
	if cfg.tls {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if cfg.sentinelMaster != "" && len(cfg.sentinelAddrs) > 0 {
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:       cfg.sentinelMaster,
			SentinelAddrs:    append([]string(nil), cfg.sentinelAddrs...),
			SentinelUsername: cfg.sentinelUsername,
			SentinelPassword: cfg.sentinelPassword,
			Username:         cfg.username,
			Password:         cfg.password,
			DB:               cfg.db,
			DialTimeout:      5 * time.Second,
			ReadTimeout:      30 * time.Second,
			WriteTimeout:     30 * time.Second,
			TLSConfig:        tlsConfig,
		})
	}
	return redis.NewClient(&redis.Options{
		Network:      cfg.network,
		Addr:         cfg.address,
		Username:     cfg.username,
		Password:     cfg.password,
		DB:           cfg.db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		TLSConfig:    tlsConfig,
	})
}

func redisNamespaceRenamePlan(ctx context.Context, client redis.UniversalClient, transforms []redisNamespaceTransform) ([]redisNamespaceRename, error) {
	planBySource := make(map[string]redisNamespaceRename)
	for _, transform := range transforms {
		if transform.sourcePrefix == "" {
			continue
		}
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, redisGlobLiteral(transform.sourcePrefix)+"*", 1000).Result()
			if err != nil {
				return nil, err
			}
			for _, source := range keys {
				if !strings.HasPrefix(source, transform.sourcePrefix) {
					continue
				}
				target := transform.targetPrefix + strings.TrimPrefix(source, transform.sourcePrefix)
				if target == "" || target == source {
					return nil, fmt.Errorf("unsafe Redis key mapping %q -> %q", source, target)
				}
				if existing, ok := planBySource[source]; ok && existing.target != target {
					return nil, fmt.Errorf("ambiguous Redis key mapping %q -> %q or %q", source, existing.target, target)
				}
				planBySource[source] = redisNamespaceRename{source: source, target: target}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	plan := make([]redisNamespaceRename, 0, len(planBySource))
	targets := make(map[string]string, len(planBySource))
	for _, rename := range planBySource {
		if other, ok := targets[rename.target]; ok && other != rename.source {
			return nil, fmt.Errorf("multiple Redis keys map to target %q", rename.target)
		}
		targets[rename.target] = rename.source
		plan = append(plan, rename)
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].source < plan[j].source })
	for _, rename := range plan {
		exists, err := client.Exists(ctx, rename.target).Result()
		if err != nil {
			return nil, err
		}
		if exists != 0 {
			return nil, fmt.Errorf("target Redis key %q already exists; no keys were changed", rename.target)
		}
	}
	return plan, nil
}

func redisNamespaceAsynqQueueMemberPlan(ctx context.Context, client redis.UniversalClient, namespace string) ([]redisNamespaceRename, error) {
	members, err := client.SMembers(ctx, "asynq:queues").Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	prefix := namespace + ":"
	memberSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberSet[member] = struct{}{}
	}
	plan := make([]redisNamespaceRename, 0)
	for _, source := range members {
		if !strings.HasPrefix(source, prefix) {
			continue
		}
		target := strings.TrimPrefix(source, prefix)
		if target == "" {
			return nil, errors.New("empty Asynq queue name after namespace removal")
		}
		if _, collision := memberSet[target]; collision {
			return nil, fmt.Errorf("target Asynq queue %q already exists; no queue members were changed", target)
		}
		plan = append(plan, redisNamespaceRename{source: source, target: target})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].source < plan[j].source })
	return plan, nil
}

func redisGlobLiteral(value string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`*`, `\*`,
		`?`, `\?`,
		`[`, `\[`,
		`]`, `\]`,
	).Replace(value)
}
