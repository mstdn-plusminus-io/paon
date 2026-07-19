package api

import (
	"context"
	"errors"
	"strconv"
	"time"
)

const redisRetryClaimScript = `
local ready = KEYS[1]
local processing = KEYS[2]
local owners = KEYS[3]
local now = tonumber(ARGV[1])
local lease_until = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local owner = ARGV[4]

local expired = redis.call('ZRANGEBYSCORE', processing, '-inf', now)
for _, member in ipairs(expired) do
  redis.call('ZREM', processing, member)
  redis.call('HDEL', owners, member)
  redis.call('ZADD', ready, now, member)
end

local due = redis.call('ZRANGEBYSCORE', ready, '-inf', now, 'LIMIT', 0, limit)
local claimed = {}
for _, member in ipairs(due) do
  if redis.call('ZREM', ready, member) == 1 then
    redis.call('ZADD', processing, lease_until, member)
    redis.call('HSET', owners, member, owner)
    table.insert(claimed, member)
  end
end
return claimed
`

const redisRetryAckScript = `
if redis.call('HGET', KEYS[2], ARGV[1]) ~= ARGV[2] then return 0 end
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('HDEL', KEYS[2], ARGV[1])
return 1
`

const redisRetryReplaceScript = `
if redis.call('HGET', KEYS[3], ARGV[1]) ~= ARGV[2] then return 0 end
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('HDEL', KEYS[3], ARGV[1])
return 1
`

const redisRetryVisibilityTimeout = 5 * time.Minute

type redisRetryClaim struct {
	Member string
	Owner  string
}

func redisRetryQueueKeys(base string) (string, string, string) {
	return base, base + ":processing", base + ":owners"
}

func (s *Server) claimRedisRetryJobs(ctx context.Context, base string, limit int, now time.Time) ([]redisRetryClaim, error) {
	if s == nil || base == "" || limit <= 0 {
		return nil, nil
	}
	ready, processing, owners := redisRetryQueueKeys(base)
	owner := randomHex(16)
	leaseUntil := now.UTC().Add(redisRetryVisibilityTimeout).Unix()
	value, err := s.redisCommand(ctx, "EVAL", redisRetryClaimScript, "3", ready, processing, owners, strconv.FormatInt(now.UTC().Unix(), 10), strconv.FormatInt(leaseUntil, 10), strconv.Itoa(limit), owner)
	if err != nil {
		return nil, err
	}
	members, ok := redisStringArray(value)
	if !ok {
		return nil, errors.New("redis retry claim returned an invalid response")
	}
	claims := make([]redisRetryClaim, 0, len(members))
	for _, member := range members {
		claims = append(claims, redisRetryClaim{Member: member, Owner: owner})
	}
	return claims, nil
}

func (s *Server) acknowledgeRedisRetryJob(ctx context.Context, base string, claim redisRetryClaim) error {
	_, processing, owners := redisRetryQueueKeys(base)
	value, err := s.redisCommand(ctx, "EVAL", redisRetryAckScript, "2", processing, owners, claim.Member, claim.Owner)
	if err != nil {
		return err
	}
	if redisInteger(value) != 1 {
		return errors.New("redis retry acknowledgement lost claim ownership")
	}
	return nil
}

func (s *Server) replaceRedisRetryJob(ctx context.Context, base string, claim redisRetryClaim, successor string, runAt time.Time) error {
	ready, processing, owners := redisRetryQueueKeys(base)
	value, err := s.redisCommand(ctx, "EVAL", redisRetryReplaceScript, "3", ready, processing, owners, claim.Member, claim.Owner, strconv.FormatInt(runAt.UTC().Unix(), 10), successor)
	if err != nil {
		return err
	}
	if redisInteger(value) != 1 {
		return errors.New("redis retry replacement lost claim ownership")
	}
	return nil
}

func redisInteger(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}
