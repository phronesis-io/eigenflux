package metrics

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type StreamGroup struct {
	Stream string
	Group  string
}

func StartLagPoller(ctx context.Context, rdb *redis.Client, streams []StreamGroup, interval time.Duration) {
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
				ConsumerLag.WithLabelValues(sg.Stream, sg.Group).Set(float64(pending.Count))
			}
		}
	}
}
