// discovery_backfill upgrades existing broadcast slots and the public Agent
// projection. Commission projection uses the existing commission_backfill tool.
package main

import (
	"context"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	sortdal "eigenflux_server/rpc/sort/dal"
	"encoding/json"
	"flag"
	"log"
	"time"
)

func main() {
	kind := flag.String("kind", "agent", "agent or broadcast")
	flag.Parse()
	cfg := config.Load()
	if !cfg.EnableNeedSearch {
		log.Fatal("ENABLE_NEED_SEARCH is required")
	}
	if _, err := searchindex.Configure(cfg.DiscoveryTaxonomyPath); err != nil {
		log.Fatal(err)
	}
	db.Init(cfg.PgDSN)
	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatal(err)
	}
	if *kind != "agent" && *kind != "broadcast" {
		log.Fatal("unsupported kind")
	}
	if *kind == "broadcast" {
		if err := es.EnsureRetrievalSlots(context.Background(), es.ReadIndexPattern); err != nil {
			log.Fatal(err)
		}
	}
	p := agentindex.Projector{DB: db.DB, Index: cfg.AgentDiscoveryIndex, Embedder: embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions)}
	if *kind == "agent" {
		if err := agentindex.Ensure(context.Background(), cfg.AgentDiscoveryIndex, cfg.EmbeddingDimensions); err != nil {
			log.Fatal(err)
		}
	}
	cursor := int64(0)
	count := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		ids := []int64{}
		query := "SELECT agent_id FROM agent_cards WHERE agent_id>? ORDER BY agent_id LIMIT 100"
		if *kind == "broadcast" {
			query = "SELECT item_id FROM processed_items WHERE item_id>? AND status=3 ORDER BY item_id LIMIT 100"
		}
		if err := db.DB.WithContext(ctx).Raw(query, cursor).Scan(&ids).Error; err != nil {
			cancel()
			log.Fatal(err)
		}
		if len(ids) == 0 {
			cancel()
			break
		}
		if *kind == "agent" {
			for _, id := range ids {
				if err := p.Project(ctx, id); err != nil {
					cancel()
					log.Fatal(err)
				}
			}
		} else {
			docs, err := sortdal.FetchItemsByIDs(ctx, ids)
			if err != nil {
				cancel()
				log.Fatal(err)
			}
			for i := range docs {
				if err = sortdal.IndexItem(ctx, &docs[i]); err != nil {
					cancel()
					log.Fatal(err)
				}
				b, err := json.Marshal(docs[i].RetrievalSlots)
				if err != nil {
					cancel()
					log.Fatal(err)
				}
				if err = db.DB.WithContext(ctx).Exec("UPDATE processed_items SET retrieval_slots=?::jsonb WHERE item_id=?", string(b), docs[i].ID).Error; err != nil {
					cancel()
					log.Fatal(err)
				}
			}
		}
		cursor = ids[len(ids)-1]
		count += len(ids)
		cancel()
		log.Printf("projected kind=%s scanned=%d cursor=%d", *kind, count, cursor)
	}
}
