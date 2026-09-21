package discoverysource

import (
	"context"
	"crypto/sha256"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/taxonomy"
	sortdal "eigenflux_server/rpc/sort/dal"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"math"
	"strconv"
	"strings"
	"time"
)

type Source struct {
	DB                                                           *gorm.DB
	Redis                                                        *redis.Client
	Commission                                                   commissionindex.Source
	BroadcastIndex, CommissionIndex, AgentIndex, RecallNamespace string
	BlockedAuthorEmails                                          []string
	DisableDedup                                                 bool
	DisabledChannels                                             map[string]bool
}

func (s *Source) Owner(ctx context.Context, id int64) (discovery.OwnerContext, error) {
	var row struct {
		Revision                         int64
		State, Compiled, Public, Private string
		CardVersion                      int64
		AgentID                          int64
	}
	err := s.DB.WithContext(ctx).Raw(`SELECT a.agent_id, COALESCE(o.active_context_revision,0) AS revision,
 COALESCE(o.state,'') AS state, COALESCE(r.compiled_context::text,'{}') AS compiled,
 COALESCE(c.public_card::text,'{}') AS public, COALESCE(c.private_card::text,'{}') AS private,
 COALESCE(c.card_version,0) AS card_version
 FROM agents a LEFT JOIN agent_onboarding_v2 o USING(agent_id)
 LEFT JOIN agent_context_revisions r ON r.agent_id=a.agent_id AND r.revision=o.active_context_revision
 LEFT JOIN agent_cards c ON c.agent_id=a.agent_id WHERE a.agent_id=?`, id).Scan(&row).Error
	if err != nil {
		return discovery.OwnerContext{}, err
	}
	if row.AgentID == 0 {
		return discovery.OwnerContext{}, discovery.Failure(404, "owner_not_found")
	}
	if row.State == "completed" && (row.Revision == 0 || row.Compiled == "{}") {
		return discovery.OwnerContext{}, discovery.Failure(503, "owner_context_unavailable")
	}
	var frozen struct {
		Intents []struct {
			WatchFor    string `json:"watch_for"`
			TriggerWhen string `json:"trigger_when"`
		} `json:"intent_actions"`
	}
	var public struct {
		Languages []string `json:"working_languages"`
		Seeking   []string `json:"seeking"`
	}
	var private struct {
		Focus     []string `json:"current_focus"`
		Demands   []string `json:"demands"`
		Interests []string `json:"interests_positive"`
	}
	for _, v := range []struct {
		raw string
		dst any
	}{{row.Compiled, &frozen}, {row.Public, &public}, {row.Private, &private}} {
		if err = json.Unmarshal([]byte(v.raw), v.dst); err != nil {
			return discovery.OwnerContext{}, err
		}
	}
	out := discovery.OwnerContext{Revision: fmt.Sprintf("%d:%d", row.Revision, row.CardVersion), Languages: public.Languages}
	for _, i := range frozen.Intents {
		v := strings.TrimSpace(i.WatchFor + " " + i.TriggerWhen)
		if v != "" {
			out.Clauses = append(out.Clauses, v)
		}
	}
	if len(out.Clauses) == 0 {
		for _, xs := range [][]string{public.Seeking, private.Demands, private.Focus, private.Interests} {
			for _, v := range xs {
				if strings.TrimSpace(v) != "" {
					out.Clauses = append(out.Clauses, v)
				}
			}
		}
	}
	return out, nil
}
func broadcast(d sortdal.Item) discovery.Document {
	out := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: d.ID}, AuthorID: d.AuthorAgentID, Version: strconv.FormatInt(d.UpdatedAt.UnixMilli(), 10), Text: d.Content + "\n" + d.Summary, Preview: d.Summary, Active: true, Visible: true, GroupID: d.GroupID, FreshAt: d.CreatedAt.UnixMilli(), SourceUpdatedAt: d.UpdatedAt.UnixMilli(), Quality: d.QualityScore, Vector: d.Embedding, Lexical: d.Score, ContentType: d.Type, SourceType: d.SourceType, URL: d.RawURL, Slots: d.RetrievalSlots}
	if d.Lang != "" {
		out.Slots.Lang = []string{d.Lang}
	}
	if d.ExpireTime != nil {
		out.ExpiresAt = d.ExpireTime.UnixMilli()
	}
	out.Version = broadcastVersion(out)
	return out
}
func commission(d commissionindex.Document) discovery.Document {
	p, dur := d.PriceFen, d.PromisedDeliveryMS
	return discovery.Document{Ref: discovery.SourceRef{Type: discovery.Commission, ID: d.CommissionID}, AuthorID: d.SellerAgentID, Version: strconv.FormatInt(d.CatalogueVersion, 10), Text: d.SearchText, Preview: d.Title, Active: d.Active, Visible: d.Active, PriceFen: &p, Currency: d.Currency, DurationMS: &dur, FreshAt: d.UpdatedAt.UnixMilli(), Fulfillment: float64(d.CompletionRateBPS) / 10000, Quality: float64(d.AverageRatingMilli) / 5000, Vector: d.Embedding, Slots: d.RetrievalSlots}
}
func (s *Source) Recall(ctx context.Context, c discovery.Context, k discovery.Kind, channel string, limit int) ([]discovery.Document, error) {
	if s.DisabledChannels[channel] {
		return []discovery.Document{}, nil
	}
	if strings.HasSuffix(channel, "recall") {
		if k != discovery.Broadcast {
			return nil, fmt.Errorf("invalid recall kind")
		}
		ids, err := recall.NewRedisRecallReader(s.Redis, s.RecallNamespace).FetchItemIDIndex(ctx, channel)
		if err != nil {
			return nil, err
		}
		if len(ids) > limit {
			ids = ids[:limit]
		}
		docs, err := sortdal.FetchItemsByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		out := make([]discovery.Document, 0, len(docs))
		for _, d := range docs {
			out = append(out, broadcast(d))
		}
		return out, nil
	}
	return s.search(ctx, c, k, channel, limit)
}
func (s *Source) Hydrate(ctx context.Context, owner int64, mode discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	if len(docs) > 1000 {
		return nil, fmt.Errorf("hydration bound exceeded")
	}
	byKind := map[discovery.Kind][]int64{}
	seen := map[string]bool{}
	for _, d := range docs {
		if !seen[d.Ref.Key()] {
			byKind[d.Ref.Type] = append(byKind[d.Ref.Type], d.Ref.ID)
			seen[d.Ref.Key()] = true
		}
	}
	out := []discovery.Document{}
	if ids := byKind[discovery.Broadcast]; len(ids) > 0 {
		var rows []struct {
			ItemID, AuthorAgentID, CreatedAt, UpdatedAt, GroupID                                   int64
			Status                                                                                 int
			RawContent, Summary, RawURL, BroadcastType, SourceType, Lang, Slots, Domains, Keywords string
			ExpireTime                                                                             string
			QualityScore                                                                           float64
		}
		err := s.DB.WithContext(ctx).Raw(`SELECT r.item_id,r.author_agent_id,r.raw_content,r.raw_url,r.created_at,p.updated_at,p.status,p.summary,p.broadcast_type,p.source_type,p.lang,p.domains,p.keywords,p.expire_time,p.group_id,p.quality_score,p.retrieval_slots::text AS slots FROM raw_items r JOIN processed_items p USING(item_id) WHERE r.item_id IN ?`, ids).Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			var slots discovery.Slots
			if err = json.Unmarshal([]byte(r.Slots), &slots); err != nil {
				return nil, err
			}
			d := broadcast(sortdal.Item{ID: r.ItemID, AuthorAgentID: r.AuthorAgentID, Content: r.RawContent, Summary: r.Summary, RawURL: r.RawURL, CreatedAt: time.UnixMilli(r.CreatedAt), UpdatedAt: time.UnixMilli(r.UpdatedAt), Type: r.BroadcastType, SourceType: r.SourceType, Lang: r.Lang, ExpireTime: parseExpiry(r.ExpireTime), GroupID: r.GroupID, QualityScore: r.QualityScore, RetrievalSlots: slots})
			if v := taxonomy.Current(); v != nil {
				labels := append(strings.Split(r.Domains, ","), strings.Split(r.Keywords, ",")...)
				langs := []string{}
				if r.Lang != "" {
					langs = []string{r.Lang}
				}
				d.Slots = discovery.ContentSlots(v, labels, langs)
			}
			d.Version = broadcastVersion(d)
			d.Active = r.Status == 3
			out = append(out, d)
		}
	}
	if ids := byKind[discovery.Agent]; len(ids) > 0 {
		rows, err := agentindex.Load(ctx, s.DB, ids)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, r.Candidate())
		}
	}
	if ids := byKind[discovery.Commission]; len(ids) > 0 {
		if s.Commission == nil {
			return nil, discovery.Failure(503, "commission_authority_unavailable")
		}
		// Catalogue currently offers only single-ID snapshots. Bound both total work
		// and concurrency; do not scan the catalogue or treat an RPC failure as absence.
		if len(ids) > 100 {
			return nil, fmt.Errorf("commission hydration bound exceeded")
		}
		values := make([]discovery.Document, len(ids))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(8)
		stats, err := s.Commission.BatchGetStatistics(ctx, ids)
		if err != nil {
			return nil, err
		}
		sm := map[int64]commissionindex.StatisticsSnapshot{}
		for _, v := range stats {
			sm[v.CommissionID] = v
		}
		for i, id := range ids {
			i, id := i, id
			g.Go(func() error {
				v, err := s.Commission.GetIndexSnapshot(gctx, id)
				if err != nil {
					return err
				}
				values[i] = commission(commissionindex.BuildDocument(v, sm[id], nil))
				return nil
			})
		}
		if err = g.Wait(); err != nil {
			return nil, err
		}
		out = append(out, values...)
	}
	authors := []int64{}
	for _, d := range out {
		authors = append(authors, d.AuthorID)
	}
	if len(authors) == 0 {
		return out, nil
	}
	var authorsNow []struct {
		AgentID              int64
		Email, IdentityState string
	}
	if err := s.DB.WithContext(ctx).Table("agents").Select("agent_id,email,identity_state").Where("agent_id IN ?", authors).Scan(&authorsNow).Error; err != nil {
		return nil, err
	}
	visible := map[int64]bool{}
	for _, a := range authorsNow {
		allowed := a.IdentityState == "active"
		for _, email := range s.BlockedAuthorEmails {
			if strings.EqualFold(a.Email, email) {
				allowed = false
			}
		}
		visible[a.AgentID] = allowed
	}
	for i := range out {
		out[i].Visible = out[i].Visible && visible[out[i].AuthorID]
	}
	var relations []struct {
		FromUID, ToUID int64
		RelType        int
	}
	err := s.DB.WithContext(ctx).Raw(`SELECT from_uid,to_uid,rel_type FROM user_relations WHERE (from_uid=? AND to_uid IN ?) OR (to_uid=? AND from_uid IN ?)`, owner, authors, owner, authors).Scan(&relations).Error
	if err != nil {
		return nil, err
	}
	blocked, known := map[int64]bool{}, map[int64]bool{}
	for _, r := range relations {
		peer := r.FromUID
		if peer == owner {
			peer = r.ToUID
		}
		if r.RelType == 2 {
			blocked[peer] = true
		}
		if r.RelType == 1 {
			known[peer] = true
		}
	}
	if mode == discovery.Recommendation && len(byKind[discovery.Agent]) > 0 {
		var contacts []int64
		err = s.DB.WithContext(ctx).Raw(`SELECT DISTINCT CASE WHEN participant_a=? THEN participant_b ELSE participant_a END FROM conversations WHERE msg_count>0 AND ((participant_a=? AND participant_b IN ?) OR (participant_b=? AND participant_a IN ?))`, owner, owner, byKind[discovery.Agent], owner, byKind[discovery.Agent]).Scan(&contacts).Error
		if err != nil {
			return nil, err
		}
		for _, id := range contacts {
			known[id] = true
		}
	}
	for i := range out {
		out[i].Blocked = blocked[out[i].AuthorID]
		out[i].KnownContact = known[out[i].AuthorID]
	}
	return out, nil
}
func (s *Source) Seen(ctx context.Context, owner int64, docs []discovery.Document) (map[string]bool, error) {
	out := map[string]bool{}
	if s.DisableDedup {
		return out, nil
	}
	groups := []int64{}
	for _, d := range docs {
		if d.Ref.Type == discovery.Broadcast && d.GroupID > 0 {
			groups = append(groups, d.GroupID)
		}
	}
	hits, err := bloomfilter.NewBloomFilter(s.Redis).CheckExists(ctx, owner, groups)
	if err != nil {
		return nil, err
	}
	pipe := s.Redis.Pipeline()
	cmds := map[string]*redis.BoolCmd{}
	for _, d := range docs {
		if d.Ref.Type == discovery.Broadcast {
			out[d.Ref.Key()] = hits[d.GroupID]
		} else {
			cmds[d.Ref.Key()] = pipe.SIsMember(ctx, fmt.Sprintf("impr:discovery:agent:%d:items", owner), d.Ref.Key())
		}
	}
	if len(cmds) > 0 {
		if _, err = pipe.Exec(ctx); err != nil {
			return nil, err
		}
	}
	for k, c := range cmds {
		out[k] = c.Val()
	}
	return out, nil
}

func parseExpiry(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	t := time.UnixMilli(1)
	return &t
}

func broadcastVersion(d discovery.Document) string {
	b, _ := json.Marshal(struct {
		ID, Author, Group, Expires       int64
		Text, Preview, Type, Source, URL string
		Slots                            discovery.Slots
		Quality                          float64
	}{d.Ref.ID, d.AuthorID, d.GroupID, d.ExpiresAt, d.Text, d.Preview, d.ContentType, d.SourceType, d.URL, d.Slots, math.Round(d.Quality*1e6) / 1e6})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
