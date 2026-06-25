package official

import (
	"context"
	"fmt"
	"testing"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/mq"
)

func TestAllowReplyLimits(t *testing.T) {
	cfg := config.Load()
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	ctx := context.Background()

	clean := func(userID int64) {
		mq.RDB.Del(ctx,
			fmt.Sprintf("official:llm:umin:%d", userID),
			fmt.Sprintf("official:llm:uday:%d", userID),
		)
	}

	t.Run("per_minute_cap", func(t *testing.T) {
		// High global + daily so the per-minute gate is the binding one.
		s := &Sender{cfg: &config.Config{OfficialChatPerUserPerMin: 2, OfficialChatDailyPerUser: 1000, OfficialChatGlobalPerMin: 100000}}
		const userID int64 = 9_100_000_000_000_000_211
		clean(userID)
		t.Cleanup(func() { clean(userID) })

		if !s.AllowReply(ctx, userID) || !s.AllowReply(ctx, userID) {
			t.Fatal("first two replies within the per-minute cap should be allowed")
		}
		if s.AllowReply(ctx, userID) {
			t.Fatal("third reply in the same minute must be denied")
		}
	})

	t.Run("per_day_cap", func(t *testing.T) {
		// High per-minute + global so the daily gate is the binding one.
		s := &Sender{cfg: &config.Config{OfficialChatPerUserPerMin: 1000, OfficialChatDailyPerUser: 3, OfficialChatGlobalPerMin: 100000}}
		const userID int64 = 9_100_000_000_000_000_212
		clean(userID)
		t.Cleanup(func() { clean(userID) })

		for i := range 3 {
			if !s.AllowReply(ctx, userID) {
				t.Fatalf("reply %d within the daily cap should be allowed", i+1)
			}
		}
		if s.AllowReply(ctx, userID) {
			t.Fatal("4th reply over the daily cap must be denied")
		}
	})
}
