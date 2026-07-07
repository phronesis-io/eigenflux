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
-- ARGV[1] = field (conv_id % 1000), ARGV[2] = sender_id, ARGV[3] = bucket TTL, ARGV[4] = max initiator messages before a reply
-- lock value format: "<sender_id>:<count>" (count = messages the initiator has sent while still unbroken)
-- Returns: {status, last_sender_id}
--   status: 0 = already broken, 1 = ice just broken (both sides spoke),
--           2 = within the ice-break window (message allowed, lock set),
--           3 = ice-break limit reached (reject until the other side replies)
local bucket = KEYS[1]
local lock = KEYS[2]
local field = ARGV[1]
local sender = ARGV[2]
local ttl = tonumber(ARGV[3])
local maxMsgs = tonumber(ARGV[4])

local broken = redis.call('HGET', bucket, field)
if broken == '1' then
    return {0, ''}
end

local raw = redis.call('GET', lock)
if raw then
    -- Parse "<sender>:<count>"; tolerate a legacy "<sender>" value (no count) as count = 1
    local sep = string.find(raw, ':', 1, true)
    local lastSender
    local count
    if sep then
        lastSender = string.sub(raw, 1, sep - 1)
        count = tonumber(string.sub(raw, sep + 1)) or 1
    else
        lastSender = raw
        count = 1
    end

    if lastSender ~= sender then
        -- Different person replied → ice broken
        redis.call('HSET', bucket, field, '1')
        if ttl > 0 then redis.call('EXPIRE', bucket, ttl) end
        redis.call('DEL', lock)
        return {1, lastSender}
    end

    -- Same sender again, still before a reply from the other side
    if count >= maxMsgs then
        return {3, sender}
    end
    redis.call('SET', lock, sender .. ':' .. (count + 1), 'EX', 86400)
    return {2, sender}
end

-- First message → start the counter at 1
redis.call('SET', lock, sender .. ':1', 'EX', 86400)
return {2, sender}
`
)

const (
	IceStatusBroken       = 0 // Ice already broken
	IceStatusJustBroken   = 1 // Ice just broken (both sides spoke)
	IceStatusFirstMsg     = 2 // Within the ice-break window: message allowed, lock set
	IceStatusLimitReached = 3 // Ice-break message limit reached: reject until the other side replies
)

// MaxInitiatorMsgs is how many messages the initiator may send before the other
// side replies (the ice-break window). The next send after this is rejected with 429.
const MaxInitiatorMsgs = 3

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
	maxMsgs := strconv.Itoa(MaxInitiatorMsgs)

	result, err := ib.rdb.Eval(ctx, iceBreakScript, []string{bucketKey, lockKey}, field, senderStr, ttl, maxMsgs).Result()
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
		// A send was counted in the window; clear the lock (resets the counter for this conv)
		lockKey := fmt.Sprintf("pm:lock:%d", convID)
		return ib.rdb.Del(ctx, lockKey).Err()
	}
	return nil
}
