package integrationprofile

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"eigenflux_server/pkg/agentcard"
	embcodec "eigenflux_server/pkg/embedding"
	profiledal "eigenflux_server/rpc/profile/dal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type KeywordExtractor interface {
	ExtractKeywords(context.Context, string) ([]string, string, error)
}
type Embedder interface {
	GetEmbedding(context.Context, string) ([]float32, error)
	Model() string
}

type MatchingProfile struct {
	AgentID        int64 `gorm:"primaryKey"`
	SourceRevision int64
	ProfileVersion int64
	ContentHash    string
	Keywords       string `gorm:"type:jsonb"`
	Embedding      []byte
	EmbeddingModel string
	UpdatedAt      int64
}

func (MatchingProfile) TableName() string { return "integration_matching_profiles" }

// Processor performs model calls outside transactions. Both the context lease
// and the profile version must still match before any facts can be committed.
type Processor struct {
	Store     *Store
	Extractor KeywordExtractor
	Embedder  Embedder
	Rebuild   func(context.Context, int64)
}

func (p *Processor) Process(ctx context.Context, agentID int64) error {
	job, err := p.Store.Claim(ctx, agentID)
	if err != nil || job == nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = p.Store.Fail(cleanup, job)
		}
	}()
	bound, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	db := p.Store.database(bound)
	version, data, err := profiledal.GetProfileVersionAndData(db, agentID)
	if err != nil {
		return err
	}
	agent, err := profiledal.GetAgentByID(db, agentID)
	if err != nil {
		return err
	}
	fields := make(map[string]json.RawMessage, len(job.Input.Fields)+1)
	for key, value := range job.Input.Fields {
		fields[key] = value
	}
	text := job.Input.Summary
	if text == "" {
		// Merge partial input with current facts before matching. An omitted
		// field preserves its value; explicit empty values clear that field.
		facts := map[string]json.RawMessage{}
		for key, value := range data {
			if _, ok := agentcard.LookupField(key); ok && key != "interests_negative" {
				facts[key] = value
			}
		}
		facts["agent_name"], _ = json.Marshal(agent.AgentName)
		facts["agent_description"], _ = json.Marshal(agent.Bio)
		for key, value := range fields {
			if key != "interests_negative" {
				facts[key] = value
			}
		}
		raw, _ := json.Marshal(facts)
		text = string(raw)
	}
	if p.Extractor == nil || p.Embedder == nil {
		return errors.New("integration processors unavailable")
	}
	keywords, _, err := p.Extractor.ExtractKeywords(bound, text)
	if err != nil {
		return errors.New("integration extraction failed")
	}
	keywords, err = cleanKeywords(keywords)
	if err != nil {
		return err
	}
	if job.Input.Summary != "" {
		fields["current_focus"], _ = json.Marshal(keywords)
	}
	// A control change while extraction was running must stop the next phase.
	if err := db.Transaction(func(tx *gorm.DB) error { return lockManaged(tx, agentID) }); err != nil {
		return classified(err)
	}
	vector, err := p.Embedder.GetEmbedding(bound, text)
	if err != nil {
		return errors.New("integration embedding failed")
	}
	if !validVector(vector) {
		return errors.New("invalid integration embedding")
	}
	encodedKeywords, _ := json.Marshal(keywords)
	applied, err := p.Store.Commit(bound, job, func(tx *gorm.DB, revision int64) (int64, error) {
		locked, err := profiledal.GetAgentByIDForUpdate(tx, agentID)
		if err != nil {
			return 0, err
		}
		current, previous, err := profiledal.GetProfileVersionAndData(tx, agentID)
		if err != nil {
			return 0, err
		}
		if current != version {
			return 0, errors.New("integration profile changed during processing")
		}
		merge := map[string]json.RawMessage{}
		updates := map[string]any{}
		old := map[string]json.RawMessage{}
		paths := make([]string, 0, len(fields))
		for key, value := range fields {
			spec, _ := agentcard.LookupField(key)
			paths = append(paths, key)
			if spec.Storage == agentcard.StorageAgents {
				var text string
				if err := json.Unmarshal(value, &text); err != nil {
					return 0, err
				}
				if key == "agent_name" {
					old[key], _ = json.Marshal(locked.AgentName)
					updates["agent_name"] = text
					updates["agent_name_en"] = ""
				} else {
					old[key], _ = json.Marshal(locked.Bio)
					updates["bio"] = text
					if text != locked.Bio {
						if err := profiledal.InsertBioHistory(tx, agentID, locked.Bio, text, "enterprise_context", ""); err != nil {
							return 0, err
						}
					}
				}
			} else {
				old[key] = previous[key]
				merge[key] = value
			}
		}
		if len(updates) > 0 {
			if err := profiledal.UpdateAgentFields(tx, agentID, updates); err != nil {
				return 0, err
			}
		}
		next, conflict, err := profiledal.ApplyVersionedProfileDataUpdate(tx, agentID, version, merge)
		if err != nil {
			return 0, err
		}
		if conflict {
			return 0, errors.New("integration profile conflict")
		}
		sort.Strings(paths)
		pathsJSON, _ := json.Marshal(paths)
		oldJSON, _ := json.Marshal(old)
		newJSON, _ := json.Marshal(fields)
		if err := profiledal.InsertProfileChangeEvent(tx, &profiledal.ProfileChangeEvent{AgentID: agentID, SourceVersion: next, ActorType: "agent", ActorID: strconv.FormatInt(agentID, 10), Source: "enterprise_context", ChangedPaths: string(pathsJSON), PreviousValues: string(oldJSON), NewValues: string(newJSON)}); err != nil {
			return 0, err
		}
		now, err := clock(tx)
		if err != nil {
			return 0, err
		}
		projection := MatchingProfile{AgentID: agentID, SourceRevision: revision, ProfileVersion: next, ContentHash: job.ContentHash, Keywords: string(encodedKeywords), Embedding: embcodec.Encode(vector), EmbeddingModel: p.Embedder.Model(), UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}}, UpdateAll: true}).Create(&projection).Error; err != nil {
			return 0, err
		}
		return next, nil
	})
	if err != nil {
		return err
	}
	committed = applied
	if applied && p.Rebuild != nil {
		p.Rebuild(ctx, agentID)
	}
	return nil
}

func cleanKeywords(input []string) ([]string, error) {
	if len(input) > 20 {
		return nil, errors.New("invalid integration keywords")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, word := range input {
		word = strings.TrimSpace(word)
		if !utf8.ValidString(word) || utf8.RuneCountInString(word) > 200 {
			return nil, errors.New("invalid integration keyword")
		}
		if word != "" && !seen[word] {
			seen[word] = true
			out = append(out, word)
		}
	}
	return out, nil
}
func validVector(v []float32) bool {
	if len(v) == 0 {
		return false
	}
	var norm float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		norm += f * f
	}
	return norm > 0
}

// Run uses the latest-input row as a durable queue; worker ownership never
// depends on an SDK process surviving or on a volatile Redis message.
func (p *Processor) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var ids []int64
			if p.Store.DB == nil {
				continue
			}
			now, err := clock(p.Store.database(ctx))
			if err != nil {
				continue
			}
			if err := p.Store.database(ctx).Model(&State{}).Where("(status = 'pending' AND retry_at <= ?) OR (status = 'processing' AND lease_until <= ?)", now, now).Order("accepted_at").Limit(20).Pluck("agent_id", &ids).Error; err != nil {
				continue
			}
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				_ = p.Process(ctx, id)
			}
		}
	}
}

// LoadMatching bypasses legacy profile caches so public-bio extraction cannot
// replace a managed Agent's private matching projection.
func LoadMatching(ctx context.Context, db *gorm.DB, agentID int64) (*MatchingProfile, error) {
	var row MatchingProfile
	err := db.WithContext(ctx).First(&row, "agent_id = ?", agentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
