package discovery

import (
	"context"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/featureindex"

	"eigenflux_server/pkg/cache"

	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/recall"

	sortdal "eigenflux_server/rpc/sort/dal"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Source struct {
	ContextCache                                                 *cache.DiscoveryCache
	DB                                                           *gorm.DB
	Redis                                                        *redis.Client
	BroadcastIndex, CommissionIndex, AgentIndex, RecallNamespace string
	BlockedAuthorEmails                                          []string
	DisableDedup                                                 bool
	DisabledChannels                                             map[string]bool
}

func (s *Source) loadOwner(ctx context.Context, id int64) (OwnerContext, error) {
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
		return OwnerContext{}, err
	}
	if row.AgentID == 0 {
		return OwnerContext{}, Failure(404, "owner_not_found")
	}
	if row.State == "completed" && (row.Revision == 0 || row.Compiled == "{}") {
		return OwnerContext{}, Failure(503, "owner_context_unavailable")
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
			return OwnerContext{}, err
		}
	}
	out := OwnerContext{Revision: fmt.Sprintf("%d:%d", row.Revision, row.CardVersion), Languages: public.Languages}
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
func broadcast(d sortdal.Item) Document {
	out := Document{Ref: SourceRef{Type: Broadcast, ID: d.ID}, AuthorID: d.AuthorAgentID, Version: strconv.FormatInt(d.UpdatedAt.UnixMilli(), 10), Text: d.Content + "\n" + d.Summary, Preview: d.Summary, Active: true, Visible: true, GroupID: d.GroupID, FreshAt: d.CreatedAt.UnixMilli(), SourceUpdatedAt: d.UpdatedAt.UnixMilli(), Quality: d.QualityScore, Lexical: d.Score, ContentType: d.Type, SourceType: d.SourceType, URL: d.RawURL, Slots: d.RetrievalSlots}
	if d.Lang != "" {
		out.Slots.Lang = []string{d.Lang}
	}
	if d.ExpireTime != nil {
		out.ExpiresAt = d.ExpireTime.UnixMilli()
	}
	out.Version = broadcastVersion(out)
	return out
}
func commission(d featureindex.CommissionDocument) Document {
	p, dur := d.PriceFen, d.PromisedDeliveryMS
	return Document{Ref: SourceRef{Type: Commission, ID: d.CommissionID}, AuthorID: d.SellerAgentID, Version: strconv.FormatInt(d.CatalogueVersion, 10), StatisticsVersion: d.StatisticsVersion, Text: d.SearchText, Preview: d.Title, Active: d.Active, Visible: d.Active, PriceFen: &p, Currency: d.Currency, DurationMS: &dur, FreshAt: d.UpdatedAt.UnixMilli(), Fulfillment: float64(d.CompletionRateBPS) / 10000, Quality: float64(d.AverageRatingMilli) / 5000, Slots: d.RetrievalSlots}
}
func (s *Source) Recall(ctx context.Context, c Context, k Kind, channel string, limit int) ([]Document, error) {
	if channel == "exact" {
		if c.Origin != "query" || k != Agent && k != Commission {
			return nil, fmt.Errorf("invalid exact lookup scope")
		}
		if k == Agent {
			return s.exactAgents(ctx, c, limit)
		}
		return s.search(ctx, c, k, channel, 1)
	}
	if s.DisabledChannels[channel] {
		return []Document{}, nil
	}
	if strings.HasSuffix(channel, "recall") {
		if k != Broadcast {
			return nil, fmt.Errorf("invalid recall kind")
		}
		ids, err := recall.NewRedisRecallReader(s.Redis, s.RecallNamespace).FetchItemIDIndex(ctx, channel)
		if err != nil {
			return nil, err
		}
		if len(ids) > limit {
			ids = ids[:limit]
		}
		out := make([]Document, 0, len(ids))
		for _, id := range ids {
			out = append(out, Document{Ref: SourceRef{Type: Broadcast, ID: id}, NeedExclusionText: len(c.Filters.ExcludeTerms) > 0})
		}
		return out, nil
	}
	return s.search(ctx, c, k, channel, limit)
}
func (s *Source) Hydrate(ctx context.Context, owner int64, mode Mode, docs []Document) ([]Document, error) {
	if len(docs) > 1000 {
		return nil, fmt.Errorf("hydration bound exceeded")
	}
	byKind := map[Kind][]int64{}
	seen := map[string]bool{}
	for _, d := range docs {
		if !seen[d.Ref.Key()] {
			byKind[d.Ref.Type] = append(byKind[d.Ref.Type], d.Ref.ID)
			seen[d.Ref.Key()] = true
		}
	}
	ctx = featureindex.EnsureRequestCache(ctx)
	batches := []featureindex.ReadRequest{}
	if ids := byKind[Broadcast]; len(ids) > 0 {
		batches = append(batches, featureindex.ReadRequest{Forward: (featureindex.BroadcastIndex{Redis: s.Redis}).Forward(), IDs: ids, Components: []string{"item"}})
	}
	groupsToRead := map[string]*featureindex.ReadRequest{}
	for _, d := range docs {
		if d.Ref.Type == Broadcast || d.Ref.Type == Agent && d.ExactMatch != "" {
			continue
		}
		if d.SourceIndex == "" {
			return nil, fmt.Errorf("missing forward index generation")
		}
		namespace := string(d.Ref.Type) + ":" + d.SourceIndex
		batch := groupsToRead[namespace]
		if batch == nil {
			components := []string{"card"}
			if d.Ref.Type == Commission {
				components = []string{"catalogue", "statistics"}
			}
			batch = &featureindex.ReadRequest{Forward: featureindex.Forward{Redis: s.Redis, Namespace: namespace}, Components: components}
			groupsToRead[namespace] = batch
		}
		batch.IDs = append(batch.IDs, d.Ref.ID)
	}
	for _, batch := range groupsToRead {
		batches = append(batches, *batch)
	}
	if err := featureindex.Prefetch(ctx, batches); err != nil {
		return nil, err
	}
	out := []Document{}
	if ids := byKind[Broadcast]; len(ids) > 0 {
		rows, err := (featureindex.BroadcastIndex{DB: s.DB, Redis: s.Redis}).Read(ctx, ids)
		if err != nil {
			return nil, err
		}
		// Source visibility is authoritative even when scalar features are cached.
		// This small batch read does not fetch content, embeddings or ranking fields.
		var states []struct {
			ItemID     int64
			Status     int
			ExpireTime string
		}
		if err := s.DB.WithContext(ctx).Table("processed_items").Select("item_id,status,expire_time").Where("item_id IN ?", ids).Scan(&states).Error; err != nil {
			return nil, err
		}
		for _, state := range states {
			r, ok := rows[state.ItemID]
			if !ok {
				continue
			}
			d := Document{Ref: SourceRef{Type: Broadcast, ID: r.ItemID}, AuthorID: r.AuthorID,
				Version: r.ContentHash, Active: r.Active && state.Status == 3, Visible: true,
				GroupID: r.GroupID, FreshAt: r.CreatedAt, SourceUpdatedAt: r.UpdatedAt,
				Quality: r.QualityScore, ContentType: r.BroadcastType, SourceType: r.SourceType, URL: r.URL, Slots: r.Slots}
			if r.Lang != "" {
				d.Slots.Lang = []string{r.Lang}
			}
			if expiry := parseExpiry(state.ExpireTime); expiry != nil {
				d.ExpiresAt = expiry.UnixMilli()
			} else {
				d.ExpiresAt = 0
			}
			out = append(out, d)
		}
	}
	// Pool recall carries IDs only. Fetch display summaries in one bounded DB
	// batch; full source text is read only when exclusion terms require it.
	if err := s.hydratePoolText(ctx, docs, out); err != nil {
		return nil, err
	}
	// Ranking data comes only from the generation-specific forward projection.
	// Exact Agent identity lookups do not rank by features and retain DB hydration.
	exactIDs := []int64{}
	groups := map[Kind]map[string][]int64{Agent: {}, Commission: {}}
	prior := map[string]Document{}
	for _, d := range docs {
		prior[d.Ref.Key()] = d
		if d.Ref.Type == Agent && d.ExactMatch != "" {
			exactIDs = append(exactIDs, d.Ref.ID)
			continue
		}
		if d.Ref.Type != Agent && d.Ref.Type != Commission {
			continue
		}
		if d.SourceIndex == "" {
			return nil, fmt.Errorf("missing forward index generation")
		}
		groups[d.Ref.Type][d.SourceIndex] = append(groups[d.Ref.Type][d.SourceIndex], d.Ref.ID)
	}
	if len(exactIDs) > 0 {
		rows, err := featureindex.LoadAgents(ctx, s.DB, exactIDs)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out = append(out, agentDocument(row))
		}
	}
	add := func(d Document, index string) {
		before := prior[d.Ref.Key()]
		if before.Version != d.Version || d.Ref.Type == Agent && before.ProjectionVersion != d.ProjectionVersion {
			metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), "forward_version").Inc()
			return
		}
		d.SourceIndex = index
		out = append(out, d)
	}
	for index, ids := range groups[Agent] {
		rows, err := (featureindex.AgentIndex{Redis: s.Redis, IndexName: index}).Read(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if d, ok := rows[id]; ok {
				add(agentDocument(d), index)
			} else {
				metrics.DiscoveryRejected.WithLabelValues("agent", "forward_missing").Inc()
			}
		}
	}
	for index, ids := range groups[Commission] {
		rows, err := (featureindex.CommissionIndex{Redis: s.Redis, IndexName: index}).Read(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if d, ok := rows[id]; ok {
				add(commission(d), index)
			} else {
				metrics.DiscoveryRejected.WithLabelValues("commission", "forward_missing").Inc()
			}
		}
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
		ProfileCompletedAt   int64
		Email, IdentityState string
		AgentName, ShortID   string
	}
	if err := s.DB.WithContext(ctx).Table("agents").Select("agent_id,email,identity_state,agent_name,short_id,profile_completed_at").Where("agent_id IN ?", authors).Scan(&authorsNow).Error; err != nil {
		return nil, err
	}
	visible := map[int64]bool{}
	names := map[int64]string{}
	profileReady := map[int64]bool{}
	for _, a := range authorsNow {
		allowed := a.IdentityState == "active"
		for _, email := range s.BlockedAuthorEmails {
			if strings.EqualFold(a.Email, email) {
				allowed = false
			}
		}
		visible[a.AgentID] = allowed
		names[a.AgentID] = agentidentity.DisplayName(a.AgentName, a.ShortID)
		profileReady[a.AgentID] = a.ProfileCompletedAt > 0
	}
	for i := range out {
		out[i].Visible = out[i].Visible && visible[out[i].AuthorID]
		if out[i].Ref.Type == Agent {
			out[i].Preview = names[out[i].Ref.ID]
			out[i].Visible = out[i].Visible && profileReady[out[i].Ref.ID]
		}
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
	if mode == Recommendation && len(byKind[Agent]) > 0 {
		var contacts []int64
		err = s.DB.WithContext(ctx).Raw(`SELECT DISTINCT CASE WHEN participant_a=? THEN participant_b ELSE participant_a END FROM conversations WHERE msg_count>0 AND ((participant_a=? AND participant_b IN ?) OR (participant_b=? AND participant_a IN ?))`, owner, owner, byKind[Agent], owner, byKind[Agent]).Scan(&contacts).Error
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
func (s *Source) Seen(ctx context.Context, owner int64, docs []Document) (map[string]bool, error) {
	out := map[string]bool{}
	if s.DisableDedup {
		return out, nil
	}
	groups := []int64{}
	for _, d := range docs {
		if d.Ref.Type == Broadcast && d.GroupID > 0 {
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
		if d.Ref.Type == Broadcast {
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
	return featureindex.ParseExpiry(raw)
}

func broadcastVersion(d Document) string {
	return featureindex.BroadcastContentHash(d.Ref.ID, d.AuthorID, d.Text, d.Slots)
}

func agentDocument(d featureindex.AgentDocument) Document {
	return Document{Ref: SourceRef{Type: Agent, ID: d.AgentID}, AuthorID: d.AgentID, Version: strconv.FormatInt(d.Version, 10), ProjectionVersion: d.ProjectionVersion, Active: d.Active, Visible: d.Active, Text: d.SearchText, Preview: d.DisplayName, Slots: d.Slots, ActivityAt: d.ActivityAt, FreshAt: d.UpdatedAt}
}
