// Package agentindex projects only public Agent Card fields into discovery.
package agentindex

import (
	"context"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/taxonomy"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"strconv"
	"strings"
)

type Document struct {
	ProjectionVersion int64           `json:"projection_version"`
	AgentID           int64           `json:"agent_id"`
	Version           int64           `json:"version"`
	Active            bool            `json:"active"`
	SearchText        string          `json:"search_text"`
	DisplayName       string          `json:"display_name"`
	Slots             discovery.Slots `json:"retrieval_slots"`
	Embedding         []float32       `json:"embedding,omitempty"`
	ActivityAt        int64           `json:"activity_at"`
	UpdatedAt         int64           `json:"updated_at"`
}
type publicCard struct {
	DisplayName      string   `json:"display_name"`
	AgentDescription string   `json:"agent_description"`
	HumanDescription string   `json:"human_description"`
	Offering         []string `json:"offering"`
	Seeking          []string `json:"seeking"`
	Languages        []string `json:"working_languages"`
	LastActive       int64    `json:"last_active_at"`
}

// Load never selects private_card, owner geography, email, or control context.
func Load(ctx context.Context, db *gorm.DB, ids []int64) ([]Document, error) {
	if len(ids) == 0 {
		return []Document{}, nil
	}
	if len(ids) > 1000 {
		return nil, fmt.Errorf("Agent hydration bound exceeded")
	}
	var rows []struct {
		AgentID, Version, ProjectionVersion, UpdatedAt int64
		PublicCard                                     string
		Active                                         bool
	}
	err := db.WithContext(ctx).Raw(`SELECT c.agent_id, c.public_card_version AS version,c.rebuild_fence AS projection_version, c.public_card_generated_at AS updated_at,
 c.public_card::text, (COALESCE(a.profile_completed_at,0)>0 AND a.identity_state='active') AS active
 FROM agent_cards c JOIN agents a USING (agent_id) WHERE c.agent_id IN ?`, ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(rows))
	for _, r := range rows {
		var p publicCard
		if err = json.Unmarshal([]byte(r.PublicCard), &p); err != nil {
			return nil, err
		}
		parts := []string{p.DisplayName, p.AgentDescription, p.HumanDescription, strings.Join(p.Offering, " "), strings.Join(p.Seeking, " ")}
		out = append(out, Document{AgentID: r.AgentID, Version: r.Version, ProjectionVersion: r.ProjectionVersion, Active: r.Active, SearchText: strings.Join(parts, "\n"), DisplayName: p.DisplayName, Slots: discovery.ContentSlots(taxonomy.Current(), append(append([]string{}, p.Offering...), p.Seeking...), p.Languages), ActivityAt: p.LastActive, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}
func (d Document) Candidate() discovery.Document {
	return discovery.Document{Ref: discovery.SourceRef{Type: discovery.Agent, ID: d.AgentID}, AuthorID: d.AgentID, Version: strconv.FormatInt(d.Version, 10), Active: d.Active, Visible: d.Active, Text: d.SearchText, Preview: d.DisplayName, Slots: d.Slots, Vector: d.Embedding, ActivityAt: d.ActivityAt, FreshAt: d.UpdatedAt}
}
