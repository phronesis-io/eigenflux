package metrics

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type StreamGroup struct {
	Stream string
	Group  string
}

// shortStreamName strips the "stream:" prefix for consistent metric labels.
func shortStreamName(s string) string {
	return strings.TrimPrefix(s, "stream:")
}

func StartLagPoller(ctx context.Context, rdb *redis.Client, streams []StreamGroup, interval time.Duration) {
	if rdb == nil {
		log.Println("lag poller not started: redis client is nil")
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, sg := range streams {
				pending, err := rdb.XPending(ctx, sg.Stream, sg.Group).Result()
				if err != nil {
					log.Printf("lag poll error for %s/%s: %v", sg.Stream, sg.Group, err)
					continue
				}
				ConsumerLag.WithLabelValues(shortStreamName(sg.Stream), sg.Group).Set(float64(pending.Count))
			}
		}
	}
}
