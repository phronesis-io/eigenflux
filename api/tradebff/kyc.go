package tradebff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// Never expose provider diagnostics or identity data in browser errors.
func replyWalletError(c *app.RequestContext, err error) {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		messages := map[string]string{
			"WALLET_KYC_REQUIRED":         "请先验证当前支付宝账号",
			"WALLET_INVALID_ARGUMENT":     "请检查提交的信息或提现金额",
			"WALLET_UNPROCESSABLE":        "当前不满足提现条件，请刷新余额和提现方式",
			"WALLET_BLOCKED":              "当前账户暂不可提现",
			"WALLET_CONFLICT":             "账户状态已变化，请重新查询",
			"WALLET_IDEMPOTENCY_CONFLICT": "请求状态不一致，请先核对提现记录",
			"WALLET_PROVIDER_UNKNOWN":     "结果正在确认，请查询记录，不要重复提交",
			"WALLET_RATE_LIMITED":         "操作过于频繁，请稍后重试",
		}
		if message, ok := messages[upstream.ErrorCode]; ok && upstream.Status >= 400 && upstream.Status <= 599 {
			replyError(c, upstream.Status, upstream.ErrorCode, message)
			return
		}
	}
	replyError(c, upstreamStatus(err), "COMMISSION_REQUEST_FAILED", "服务暂不可用，请先查询当前状态")
}

func (s *Service) WalletKYC(ctx context.Context, c *app.RequestContext) {
	s.kyc(ctx, c, http.MethodGet, "/api/v1/wallet/kyc", "payout:read", "wallet.kyc.read", nil, false)
}

func (s *Service) StartWalletKYC(ctx context.Context, c *app.RequestContext) {
	var input map[string]string
	if len(c.Request.Body()) > 2048 || json.Unmarshal(c.Request.Body(), &input) != nil || len(input) != 2 || strings.TrimSpace(input["user_name"]) == "" || len(input["user_name"]) > 200 || len(input["cert_no"]) != 18 {
		replyError(c, 400, "WALLET_INVALID_ARGUMENT", "请填写姓名和18位身份证号码")
		return
	}
	body, _ := json.Marshal(map[string]string{"user_name": input["user_name"], "cert_no": input["cert_no"]})
	s.kyc(ctx, c, http.MethodPost, "/api/v1/wallet/kyc", "payout:bind", "wallet.kyc.start", body, false)
}

func (s *Service) AuthorizeWalletKYC(ctx context.Context, c *app.RequestContext) {
	var input map[string]string
	if len(c.Request.Body()) > 256 || json.Unmarshal(c.Request.Body(), &input) != nil || len(input) != 1 || !positiveDecimal(input["verification_id"]) {
		replyError(c, 400, "WALLET_INVALID_ARGUMENT", "验证编号无效")
		return
	}
	body, _ := json.Marshal(input)
	s.kyc(ctx, c, http.MethodPost, "/api/v1/wallet/kyc/authorization", "payout:bind", "wallet.kyc.authorize", body, true)
}

func (s *Service) kyc(ctx context.Context, c *app.RequestContext, method, path, scope, operation string, body []byte, includeLink bool) {
	identifier, ok := agentID(c)
	if !ok {
		replyError(c, 401, "CONSOLE_SESSION_REQUIRED", "Console Session 无效")
		return
	}
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return
	}
	key := strings.TrimSpace(string(c.GetHeader("Idempotency-Key")))
	mutation := method != http.MethodGet
	if mutation && key == "" {
		replyError(c, 400, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key 不能为空")
		return
	}
	data, err := s.fetch(ctx, identifier, scope, operation, method, path, nil, body, key, mutation)
	if err != nil {
		replyWalletError(c, err)
		return
	}
	v, ok := decodeObject(data)["verification"].(map[string]interface{})
	if !ok {
		replyError(c, 502, "INVALID_KYC_RESPONSE", "无法确认验证状态")
		return
	}
	result := map[string]interface{}{}
	for _, name := range []string{"verification_id", "binding_id", "state", "expires_at", "verified_at"} {
		result[name] = v[name]
	}
	if includeLink && v["state"] == "pending" {
		link, err := url.Parse(stringValue(v["authorization_url"]))
		if err != nil || link.Scheme != "https" || link.Hostname() == "" || link.User != nil || link.RawPath != "" || link.Path != "/api/v1/public/wallet/kyc/launch" || link.Fragment != "" || link.Query().Get("ticket") == "" {
			replyError(c, 502, "INVALID_KYC_RESPONSE", "无法获取安全的验证链接")
			return
		}
		result["authorization_url"] = link.String()
		result["authorization_expires_at"] = v["authorization_expires_at"]
	}
	reply(c, 200, map[string]interface{}{"verification": result})
}
