// discovery_forward_cleanup removes disposable retired projection fields without
// re-embedding content or overwriting concurrent projection updates.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/featureindex"
	"github.com/redis/go-redis/v9"
)

var replaceData = redis.NewScript(`
if redis.call('HGET',KEYS[1],'data') ~= ARGV[1] then return 0 end
redis.call('HSET',KEYS[1],'data',ARGV[2])
return 1
`)

func cleaned(raw string) (string, bool, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", false, err
	}
	if doc == nil {
		return "", false, fmt.Errorf("invalid forward document")
	}
	changed := false
	// Preserve broadcast evidence before discarding its source text.
	if _, broadcast := doc["item_id"]; broadcast && (doc["content"] != nil || doc["summary"] != nil) {
		var source struct {
			featureindex.BroadcastDocument
			Content string `json:"content"`
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			return "", false, err
		}
		if source.Lang != "" {
			source.Slots.Lang = []string{source.Lang}
		}
		doc["content_hash"], _ = json.Marshal(featureindex.BroadcastContentHash(source.ItemID, source.AuthorID, source.Content+"\n"+source.Summary, source.Slots))
	}
	for _, field := range []string{"embedding", "content", "summary", "search_text", "display_name", "title", "capability_description", "request_spec_text", "delivery_spec_text", "tags"} {
		if _, exists := doc[field]; exists {
			delete(doc, field)
			changed = true
		}
	}
	if b, ok := doc["retrieval_slots"]; ok {
		var slots map[string]json.RawMessage
		if err := json.Unmarshal(b, &slots); err != nil {
			return "", false, err
		}
		dirty := false
		for _, field := range []string{"category", "subtype", "intents", "taxonomy_version"} {
			if _, exists := slots[field]; exists {
				delete(slots, field)
				dirty = true
			}
		}
		if dirty {
			doc["retrieval_slots"], _ = json.Marshal(slots)
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	b, err := json.Marshal(doc)
	return string(b), true, err
}

func cleanup(ctx context.Context, r redis.UniversalClient, apply bool) (int, error) {
	count := 0
	for _, pattern := range []string{"discovery:forward:broadcast:*:*:item", "discovery:forward:agent:*:*:card", "discovery:forward:commission:*:*:catalogue"} {
		iter := r.Scan(ctx, 0, pattern, 100).Iterator()
		for iter.Next(ctx) {
			key := iter.Val()
			raw, err := r.HGet(ctx, key, "data").Result()
			if err == redis.Nil {
				continue
			}
			if err != nil {
				return count, err
			}
			updated, changed, err := cleaned(raw)
			if err != nil {
				return count, fmt.Errorf("invalid projection at %s: %w", key, err)
			}
			if !changed {
				continue
			}
			if apply {
				n, err := replaceData.Run(ctx, r, []string{key}, raw, updated).Int()
				if err != nil {
					return count, err
				}
				count += n
			} else {
				count++
			}
		}
		if err := iter.Err(); err != nil {
			return count, err
		}
	}
	return count, nil
}
func main() {
	apply := flag.Bool("apply", false, "Apply cleanup; default counts affected documents only")
	flag.Parse()
	cfg := config.Load()
	r := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	n, err := cleanup(ctx, r, *apply)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("apply=%t affected=%d", *apply, n)
}
