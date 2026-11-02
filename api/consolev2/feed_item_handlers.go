package consolev2

import (
	"context"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/pgcsource"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"

	itemdal "eigenflux_server/rpc/item/dal"
)

func splitMetadataValues(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (s *Service) getFeedSourceItem(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	sourceType := strings.ToLower(strings.TrimSpace(c.Param("source_type")))
	sourceID, err := strconv.ParseInt(c.Param("source_id"), 10, 64)
	if sourceType != "broadcast" || err != nil || sourceID <= 0 {
		fail(c, http.StatusBadRequest, "UNSUPPORTED_SOURCE_REF", "only numeric broadcast source references are supported in this release", nil)
		return
	}
	var source struct {
		RawNotes      string
		AuthorEmail   string
		ContentClass  string `gorm:"column:content_class"`
		AuthorAgentID int64  `gorm:"column:author_agent_id"`
	}
	if err := s.db.Raw(`SELECT exposure.content_class, raw.author_agent_id, raw.raw_notes, a.email AS author_email
		FROM agent_feed_exposures exposure
		JOIN raw_items raw ON raw.item_id = exposure.source_id
		LEFT JOIN agents a ON a.agent_id=raw.author_agent_id
 WHERE exposure.agent_id = ? AND exposure.source_type = ? AND exposure.source_id = ?
		LIMIT 1`, agentIDValue, sourceType, sourceID).Scan(&source).Error; err != nil {
		fail(c, http.StatusInternalServerError, "FEED_SOURCE_READ_FAILED", "could not authorize Feed source", nil)
		return
	}
	if source.ContentClass == "" {
		fail(c, http.StatusNotFound, "FEED_SOURCE_NOT_FOUND", "Feed source was not found", nil)
		return
	}
	item, err := itemdal.GetItemByID(s.db, sourceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "FEED_SOURCE_NOT_FOUND", "Feed source was not found", nil)
			return
		}
		fail(c, http.StatusInternalServerError, "FEED_SOURCE_READ_FAILED", "could not read Feed source", nil)
		return
	}
	body := item.RawContent
	var fields pgcsource.Fields
	if config.EmailMatchesAnySuffix(source.AuthorEmail, s.pgcEmailSuffixes) {
		body, fields = pgcsource.Read(body, source.RawNotes)
	}
	content, truncated := truncateRunes(body, 120_000)
	var authorIdentity interface{}
	if source.ContentClass == "ugc" && source.AuthorAgentID > 0 {
		identities, identityErr := s.resolveIdentityAssertions([]int64{source.AuthorAgentID})
		if identityErr != nil {
			fail(c, http.StatusInternalServerError, "FEED_SOURCE_READ_FAILED", "could not resolve Feed source identity", nil)
			return
		}
		if identity, exists := identities[source.AuthorAgentID]; exists {
			authorIdentity = map[string]interface{}{
				"agent_id": identity.SubjectID, "short_id": identity.ShortID, "agent_name": identity.DisplayName,
				"verification_level": identity.VerificationLevel,
			}
		}
	}
	payload := map[string]interface{}{
		"source_ref":    map[string]interface{}{"type": sourceType, "id": strconv.FormatInt(sourceID, 10)},
		"content_class": source.ContentClass, "author_identity": authorIdentity,
		"content": content, "content_truncated": truncated, "url": item.RawURL,
		"summary": item.Summary, "summary_zh": item.SummaryZh, "broadcast_type": item.BroadcastType,
		"domains": splitMetadataValues(item.Domains), "keywords": splitMetadataValues(item.Keywords),
		"source_type": item.SourceType, "expected_response": item.ExpectedResponse,
		"updated_at": item.UpdatedAt,
	}
	if fields.Title != "" {
		payload["source_title"] = fields.Title
	}
	if fields.Timestamp != "" {
		payload["source_timestamp"] = fields.Timestamp
	}
	reply(c, http.StatusOK, payload)
}
