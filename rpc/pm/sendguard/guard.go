package sendguard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	duplicateWindow = time.Minute
	pendingTTL      = duplicateWindow
	keyPrefix       = "pm:send:dedupe:v1:"
)

var ErrInProgress = errors.New("MESSAGE_SEND_IN_PROGRESS: an identical message is already being sent; wait before retrying")

type Result struct {
	MsgID  int64
	ConvID int64
}

type Lease struct {
	key   string
	token string
}

type Guard struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Guard {
	return &Guard{rdb: rdb}
}

func Fingerprint(senderID int64, targetKind string, targetID int64, content string) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d\n%s\n%d\n%d\n", senderID, targetKind, targetID, len(content))
	hash.Write([]byte(content))
	return keyPrefix + hex.EncodeToString(hash.Sum(nil))
}

func (g *Guard) Acquire(ctx context.Context, key string) (*Lease, *Result, error) {
	if g == nil || g.rdb == nil {
		return nil, nil, errors.New("message send guard is unavailable")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, nil, fmt.Errorf("generate message send token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	pendingValue := "pending:" + token

	for attempt := 0; attempt < 2; attempt++ {
		acquired, err := g.rdb.SetNX(ctx, key, pendingValue, pendingTTL).Result()
		if err != nil {
			return nil, nil, fmt.Errorf("acquire message send guard: %w", err)
		}
		if acquired {
			return &Lease{key: key, token: token}, nil, nil
		}

		value, err := g.rdb.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read message send guard: %w", err)
		}
		if strings.HasPrefix(value, "pending:") {
			return nil, nil, ErrInProgress
		}
		result, err := parseDone(value)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	}

	return nil, nil, ErrInProgress
}

func (g *Guard) Complete(ctx context.Context, lease *Lease, result Result) error {
	if g == nil || g.rdb == nil || lease == nil {
		return errors.New("message send guard completion is unavailable")
	}
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("SET", KEYS[1], ARGV[2], "PX", ARGV[3]) else return false end`
	pendingValue := "pending:" + lease.token
	doneValue := fmt.Sprintf("done:%d:%d", result.MsgID, result.ConvID)
	completed, err := g.rdb.Eval(ctx, script, []string{lease.key}, pendingValue, doneValue, duplicateWindow.Milliseconds()).Result()
	if err != nil {
		return fmt.Errorf("complete message send guard: %w", err)
	}
	if completed == nil {
		return errors.New("message send guard lease expired before completion")
	}
	return nil
}

func (g *Guard) Release(ctx context.Context, lease *Lease) error {
	if g == nil || g.rdb == nil || lease == nil {
		return nil
	}
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	if err := g.rdb.Eval(ctx, script, []string{lease.key}, "pending:"+lease.token).Err(); err != nil {
		return fmt.Errorf("release message send guard: %w", err)
	}
	return nil
}

func parseDone(value string) (*Result, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "done" {
		return nil, errors.New("invalid message send guard state")
	}
	msgID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, errors.New("invalid message send guard msg_id")
	}
	convID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, errors.New("invalid message send guard conv_id")
	}
	return &Result{MsgID: msgID, ConvID: convID}, nil
}
