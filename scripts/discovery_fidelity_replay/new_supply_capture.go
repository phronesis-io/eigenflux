package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/dedup"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Admission evidence for unpublished candidates. This reads author authority,
// relations and consumption; it never hydrates a nonexistent production item or
// creates one. Content/extraction/grouping provenance remains in the input bundle.
func captureNewSupply(cfg *config.Config, dir string, db *gorm.DB, remote *redis.Client) {
	var items []supplyItem
	b, err := os.ReadFile(filepath.Join(dir, "new-supply-unverified.json"))
	must(err)
	must(json.Unmarshal(b, &items))
	if len(items) == 0 || len(items) > 50 {
		panic("new supply must contain 1-50 candidates")
	}
	var cohort []struct{ AgentID int64 }
	b, err = os.ReadFile(filepath.Join(dir, "cohort.json"))
	must(err)
	must(json.Unmarshal(b, &cohort))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	accepted := []supplyItem{}
	rejected := []map[string]any{}
	for _, x := range items {
		d := restoreDocs([]storedDocument{x.Document})[0]
		if d.Ref.ID <= 0 || d.AuthorID <= 0 || d.GroupID <= 0 || x.Evidence.Content == "" {
			panic("invalid new supply input")
		}
		var count int64
		must(db.WithContext(ctx).Table("raw_items").Where("item_id = ?", d.Ref.ID).Count(&count).Error)
		if count != 0 {
			panic("new supply ID exists in production")
		}
		duplicate, err := dedup.HashExists(ctx, remote, dedup.ComputeContentHash(x.Evidence.Content))
		must(err)
		if duplicate || d.Quality < cfg.QualityThreshold {
			rejected = append(rejected, map[string]any{"id": d.Ref.ID, "exact_duplicate": duplicate, "below_quality": d.Quality < cfg.QualityThreshold})
			continue
		}
		var author struct{ Email, IdentityState string }
		must(db.WithContext(ctx).Table("agents").Select("email,identity_state").Where("agent_id = ?", d.AuthorID).Take(&author).Error)
		if !config.EmailMatchesAnySuffix(author.Email, cfg.PGCEmailSuffixes) {
			panic("new supply author is not PGC")
		}
		d.Visible = author.IdentityState == "active"
		for _, email := range cfg.BlockedAgentEmails {
			if strings.EqualFold(author.Email, email) {
				d.Visible = false
			}
		}
		d.Text = x.Evidence.Content + "\n" + x.Evidence.Summary
		d.NeedExclusionText = true
		d.Preview = x.Evidence.Summary
		d.Version = featureindex.BroadcastContentHash(d.Ref.ID, d.AuthorID, d.Text, d.Slots)
		x.Document = storeDocs([]discovery.Document{d})[0]
		x.Owners = map[int64]supplyOwner{}

		accepted = append(accepted, x)
	}

	owners := []int64{}
	authors := []int64{}
	docs := []discovery.Document{}
	for _, a := range cohort {
		owners = append(owners, a.AgentID)
	}
	for _, x := range accepted {
		authors = append(authors, x.Document.AuthorID)
		docs = append(docs, restoreDocs([]storedDocument{x.Document})[0])
	}
	var relations []struct {
		FromUID, ToUID int64
		RelType        int
	}
	if len(authors) > 0 {
		must(db.WithContext(ctx).Raw(`SELECT from_uid,to_uid,rel_type FROM user_relations WHERE (from_uid IN ? AND to_uid IN ?) OR (from_uid IN ? AND to_uid IN ?)`, owners, authors, authors, owners).Scan(&relations).Error)
	}
	var workers sync.WaitGroup
	var mu sync.Mutex
	slots := make(chan struct{}, 4)
	for _, a := range cohort {
		slots <- struct{}{}
		workers.Add(1)
		go func(owner int64) {
			defer workers.Done()
			defer func() { <-slots }()
			source := &discovery.Source{Redis: remote}
			seen, err := source.Seen(ctx, owner, docs)
			must(err)
			mu.Lock()
			defer mu.Unlock()
			for i := range accepted {
				d := accepted[i].Document
				fact := supplyOwner{Seen: seen[d.Ref.Key()]}
				for _, r := range relations {
					if (r.FromUID == owner && r.ToUID == d.AuthorID) || (r.ToUID == owner && r.FromUID == d.AuthorID) {
						fact.Blocked = fact.Blocked || r.RelType == 2
						fact.KnownContact = fact.KnownContact || r.RelType == 1
					}
				}
				accepted[i].Owners[owner] = fact
			}
		}(a.AgentID)
	}
	workers.Wait()
	save(filepath.Join(dir, "new-supply.json"), accepted)
	save(filepath.Join(dir, "new-supply-rejected.json"), rejected)
	fmt.Printf("new supply authority: %d admitted, %d rejected, %d owners; no production writes\n", len(accepted), len(rejected), len(cohort))
}
