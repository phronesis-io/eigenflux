package consolev2

import (
	"context"
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
		ContentClass  string `gorm:"column:content_class"`
		AuthorAgentID int64  `gorm:"column:author_agent_id"`
	}
	if err := s.db.Raw(`SELECT exposure.content_class, raw.author_agent_id
		FROM agent_feed_exposures exposure
		JOIN raw_items raw ON raw.item_id = exposure.source_id
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
	content, truncated := truncateRunes(item.RawContent, 120_000)
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
	reply(c, http.StatusOK, map[string]interface{}{
		"source_ref":    map[string]interface{}{"type": sourceType, "id": strconv.FormatInt(sourceID, 10)},
		"content_class": source.ContentClass, "author_identity": authorIdentity,
		"content": content, "content_truncated": truncated, "url": item.RawURL,
		"summary": item.Summary, "summary_zh": item.SummaryZh, "broadcast_type": item.BroadcastType,
		"domains": splitMetadataValues(item.Domains), "keywords": splitMetadataValues(item.Keywords),
		"source_type": item.SourceType, "expected_response": item.ExpectedResponse,
		"updated_at": item.UpdatedAt,
	})
}
