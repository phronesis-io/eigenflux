package install

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/redis/go-redis/v9"
)

const statusKeyLifetime = 90 * 24 * time.Hour

var statusLimiter = newIPLimiter(60, time.Minute)

type statusCredential struct {
	Ref string `json:"ref"`
	Key string `json:"status_key"`
}

func statusCacheKey(ref string) string { return "install:ios-status:" + ref }

func validStatusKey(key string) bool {
	if len(key) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func rememberStatusKey(ctx context.Context, rdb *redis.Client, ref, hash string) error {
	if rdb == nil {
		return errors.New("install status cache is unavailable")
	}
	return rdb.Set(ctx, statusCacheKey(ref), hash, statusKeyLifetime).Err()
}

func authorizedStatusRefs(ctx context.Context, rdb *redis.Client, entries []statusCredential) ([]string, error) {
	if rdb == nil {
		return nil, errors.New("install status cache is unavailable")
	}
	keys := make([]string, len(entries))
	for i, entry := range entries {
		keys[i] = statusCacheKey(entry.Ref)
	}
	hashes, err := rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	var refs []string
	for i, entry := range entries {
		hash, _ := hashes[i].(string)
		digest := sha256.Sum256([]byte(entry.Key))
		expected, _ := hex.DecodeString(hash)
		if subtle.ConstantTimeCompare(digest[:], expected) == 1 {
			refs = append(refs, entry.Ref)
		}
	}
	return refs, nil
}

func installStatus(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	if !statusLimiter.Allow(clientIP(c)) {
		reply(c, http.StatusTooManyRequests, 429, "rate limited, try again later", nil)
		return
	}
	var body struct {
		Entries []statusCredential `json:"entries"`
	}
	raw, _ := c.Body()
	if len(raw) > 2048 || json.Unmarshal(raw, &body) != nil || len(body.Entries) == 0 || len(body.Entries) > 10 {
		reply(c, http.StatusBadRequest, 400, "invalid install status request", nil)
		return
	}
	states := make(map[string]string, len(body.Entries))
	for _, entry := range body.Entries {
		if !ValidTokenFormat(entry.Ref) || !validStatusKey(entry.Key) || states[entry.Ref] != "" {
			reply(c, http.StatusBadRequest, 400, "invalid install status request", nil)
			return
		}
		states[entry.Ref] = "unavailable"
	}
	refs, err := authorizedStatusRefs(ctx, mq.RDB, body.Entries)
	if err != nil {
		reply(c, http.StatusServiceUnavailable, 503, "install status temporarily unavailable", nil)
		return
	}
	if len(refs) > 0 {
		var rows []Token
		if err := db.DB.WithContext(ctx).Select("token", "status").Where("token IN ?", refs).Find(&rows).Error; err != nil {
			reply(c, http.StatusServiceUnavailable, 503, "install status temporarily unavailable", nil)
			return
		}
		for _, row := range rows {
			states[row.Token] = row.Status
		}
	}
	reply(c, http.StatusOK, 0, "success", states)
}
