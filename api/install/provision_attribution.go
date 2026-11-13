package install

import (
	"errors"

	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/invite"
	"gorm.io/gorm"
)

// ProvisionAttribution describes attribution written in the provision transaction.
// Callers may emit events only after that transaction commits.
type ProvisionAttribution struct {
	Channel        string
	InviteCode     string
	InviterAgentID int64
}

// BindProvisionedAgent binds a signed install ref to a newly inserted V2 Agent.
// The caller must verify the provision proof and call this only in the new-Agent
// branch, using the same transaction that creates the identity and credentials.
// The identity comes from that transaction, never public /report metadata; the
// internal email alias can later be bound to a human without losing attribution.
func BindProvisionedAgent(tx *gorm.DB, ref string, agentID int64, now int64) (ProvisionAttribution, error) {
	var result ProvisionAttribution
	if ref == "" || !ValidTokenFormat(ref) || agentID <= 0 {
		return result, nil
	}
	var token Token
	if err := tx.Where("token = ?", ref).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, nil
		}
		return result, err
	}
	if token.CreatedAt > now {
		return result, nil
	}
	if token.Channel != "" && token.Channel != "unknown" {
		update := tx.Exec(`UPDATE agents SET acquisition_channel = ?
			WHERE agent_id = ? AND acquisition_channel = '' AND created_at >= ?`,
			token.Channel, agentID, token.CreatedAt)
		if update.Error != nil {
			return result, update.Error
		}
		if update.RowsAffected == 1 {
			result.Channel = token.Channel
		}
	}
	if token.Channel == "facebook" && (token.MetaFbc != "" || token.MetaFbp != "") {
		if err := tx.Model(&Token{}).Where("token = ? AND meta_provisioned_at = 0", ref).
			Update("meta_provisioned_at", now).Error; err != nil {
			return result, err
		}
	}
	var code *invite.Code
	if agentidentity.ValidShortID(token.InviteCode) {
		inviterID, err := agentidentity.Lookup(tx.Statement.Context, tx, token.InviteCode)
		if err != nil && !errors.Is(err, agentidentity.ErrNotFound) {
			return result, err
		}
		if err == nil {
			code = &invite.Code{Code: token.InviteCode, Kind: invite.KindKOL, AgentID: inviterID}
		}
	} else if invite.ValidFormat(token.InviteCode) {
		var err error
		code, err = invite.GetByCode(tx, token.InviteCode)
		if err != nil {
			return result, err
		}
	}
	if code == nil || (code.Kind == invite.KindKOL && code.AgentID == agentID) {
		return result, nil
	}
	update := tx.Exec(`UPDATE agents SET invited_by_code = ?, inviter_agent_id = ?, invited_at = ?
		WHERE agent_id = ? AND invited_by_code = '' AND created_at >= ?`,
		code.Code, code.AgentID, now, agentID, token.CreatedAt)
	if update.Error != nil {
		return result, update.Error
	}
	if update.RowsAffected == 1 {
		result.InviteCode, result.InviterAgentID = code.Code, code.AgentID
	}
	return result, nil
}
