package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/pkg/db"
)

func (s *AuthServiceImpl) ResolveGatewayPrincipal(ctx context.Context, req *auth.ResolveGatewayPrincipalReq) (*auth.ResolveGatewayPrincipalResp, error) {
	denied := &auth.ResolveGatewayPrincipalResp{BaseResp: &base.BaseResp{Code: 404, Msg: "invalid principal"}}
	if req == nil || len(req.Fingerprint) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return denied, nil
	}
	var principal struct {
		AgentID   int64
		PublicKey []byte
		Status    string
	}
	result := db.DB.WithContext(ctx).Raw(`SELECT p.agent_id,p.public_key,CASE WHEN p.status='active' AND p.revoked_at IS NULL AND a.identity_state='active' AND o.state='completed' THEN 'active' ELSE 'inactive' END AS status
        FROM agent_principals p
        LEFT JOIN agents a ON a.agent_id=p.agent_id
        LEFT JOIN agent_onboarding_v2 o ON o.agent_id=p.agent_id
        WHERE p.key_fingerprint=? AND p.key_type='ed25519-v1'`, req.Fingerprint).Scan(&principal)
	if result.Error != nil {
		return &auth.ResolveGatewayPrincipalResp{BaseResp: &base.BaseResp{Code: 503, Msg: "principal lookup unavailable"}}, nil
	}
	if result.RowsAffected != 1 || len(principal.PublicKey) != ed25519.PublicKeySize {
		return denied, nil
	}
	if principal.Status != "active" {
		return &auth.ResolveGatewayPrincipalResp{BaseResp: &base.BaseResp{Code: 403, Msg: "inactive principal"}}, nil
	}
	return &auth.ResolveGatewayPrincipalResp{AgentId: &principal.AgentID, PublicKey: principal.PublicKey, Status: &principal.Status, BaseResp: &base.BaseResp{}}, nil
}
