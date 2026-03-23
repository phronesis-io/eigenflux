package icebreak

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const (
	// Ice break Lua script
	iceBreakScript = `
-- KEYS[1] = bucket hash key, KEYS[2] = lock key
-- ARGV[1] = field (conv_id % 1000), ARGV[2] = sender_id, ARGV[3] = bucket TTL
-- Returns: {status, last_sender_id}
--   status: 0 = already broken, 1 = ice just broken (both sides spoke), 2 = first message (lock set)
local bucket = KEYS[1]
local lock = KEYS[2]
local field = ARGV[1]
local sender = ARGV[2]
local ttl = tonumber(ARGV[3])

local broken = redis.call('HGET', bucket, field)
if broken == '1' then
    return {0, ''}
end

local lastSender = redis.call('GET', lock)
if lastSender and lastSender ~= sender then
    -- Different person replied → ice broken
    redis.call('HSET', bucket, field, '1')
    if ttl > 0 then redis.call('EXPIRE', bucket, ttl) end
    redis.call('DEL', lock)
    return {1, lastSender}
end

-- First message or same sender again → set/refresh lock
redis.call('SET', lock, sender, 'EX', 86400)
return {2, sender}
`
)


const (
	IceStatusBroken      = 0 // Ice already broken
	IceStatusJustBroken  = 1 // Ice just broken (both sides spoke)
	IceStatusFirstMsg    = 2 // First message (lock set)
)

type IceBreaker struct {
	rdb *redis.Client
}

func NewIceBreaker(rdb *redis.Client) *IceBreaker {
	return &IceBreaker{rdb: rdb}
}

// CheckAndSetIceBreak checks and updates ice break status atomically
func (ib *IceBreaker) CheckAndSetIceBreak(ctx context.Context, convID, senderID int64) (int, int64, error) {
	bucketKey := fmt.Sprintf("pm:ice:h:%d", convID/1000)
	lockKey := fmt.Sprintf("pm:lock:%d", convID)
	field := strconv.FormatInt(convID%1000, 10)
	senderStr := strconv.FormatInt(senderID, 10)
	ttl := "604800" // 7 days

	result, err := ib.rdb.Eval(ctx, iceBreakScript, []string{bucketKey, lockKey}, field, senderStr, ttl).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("ice break lua script failed: %w", err)
	}

	resultSlice, ok := result.([]interface{})
	if !ok || len(resultSlice) != 2 {
		return 0, 0, fmt.Errorf("unexpected ice break result format")
	}

	status, err := strconv.Atoi(fmt.Sprintf("%v", resultSlice[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse status: %w", err)
	}

	lastSenderStr := fmt.Sprintf("%v", resultSlice[1])
	var lastSenderID int64
	if lastSenderStr != "" {
		lastSenderID, _ = strconv.ParseInt(lastSenderStr, 10, 64)
	}

	return status, lastSenderID, nil
}

// RollbackIceBreak rolls back ice break state on transaction failure
func (ib *IceBreaker) RollbackIceBreak(ctx context.Context, convID int64, status int) error {
	if status == IceStatusJustBroken {
		// Ice was just broken, need to rollback
		bucketKey := fmt.Sprintf("pm:ice:h:%d", convID/1000)
		field := strconv.FormatInt(convID%1000, 10)
		return ib.rdb.HDel(ctx, bucketKey, field).Err()
	} else if status == IceStatusFirstMsg {
		// Lock was set, delete it
		lockKey := fmt.Sprintf("pm:lock:%d", convID)
		return ib.rdb.Del(ctx, lockKey).Err()
	}
	return nil
}
