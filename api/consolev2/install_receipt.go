package consolev2

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/logger"
)

func (s *Service) acknowledgeInstallReceipt(ctx context.Context, c *app.RequestContext) {
	id, _ := agentID(c)
	// Only a newly provisioned, instrumented identity can enter this cohort.
	// Retries and reprovisioning preserve the first acknowledgement time.
	result := s.db.WithContext(ctx).Exec(`UPDATE agent_install_receipts
		SET acknowledged_at = coalesce(acknowledged_at, floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint)
		WHERE agent_id = ? AND receipt_supported`, id)
	if result.Error != nil {
		logger.Ctx(ctx).Error("persist Agent installation receipt", "agent_id", id, "err", result.Error)
		fail(c, http.StatusServiceUnavailable, "INSTALL_RECEIPT_UNAVAILABLE", "installation receipt could not be recorded", nil)
		return
	}
	reply(c, http.StatusOK, map[string]bool{"recorded": result.RowsAffected == 1})
}
