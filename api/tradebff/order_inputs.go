package tradebff

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
)

const orderInputWriteScope = "orders:inputs:write"

type orderInputUpload struct {
	ExpectedVersion int64  `json:"expected_version"`
	RequestID       string `json:"request_id"`
	LogicalPath     string `json:"logical_path"`
	ByteSize        *int64 `json:"byte_size"`
	SHA256          string `json:"sha256"`
	OrderID         string `json:"order_id"`
}

type orderInputGrant struct {
	ObjectID    string            `json:"object_id"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	ExpiresAt   int64             `json:"expires_at"`
	LogicalPath string            `json:"logical_path"`
	ByteSize    *int64            `json:"byte_size"`
	SHA256      string            `json:"sha256"`
	Confirmed   bool              `json:"confirmed"`
}

// TradeOrderInputUpload returns only a verified OSS PUT grant, never delegation
// credentials. Commission fences the buyer, question, version and upload object.
func (s *Service) TradeOrderInputUpload(ctx context.Context, c *app.RequestContext) {
	actor, orderID, ok := orderInputActor(c)
	if !ok {
		return
	}
	var request orderInputUpload
	if !decodeOrderInputBody(c, &request, "order_id") || !validOrderInputCommand(request.ExpectedVersion, request.RequestID) || !validOrderInputPath(request.LogicalPath) || request.ByteSize == nil || *request.ByteSize < 0 || *request.ByteSize > 64<<20 || !lowerHexDigest(request.SHA256) {
		replyError(c, http.StatusBadRequest, "INVALID_INPUT_UPLOAD", "补充材料上传参数无效")
		return
	}
	request.OrderID = orderID
	body, _ := json.Marshal(request)
	data, ok := s.orderInputMutation(ctx, c, actor, orderID, "input-uploads", "console.trade.orders.input_uploads.create", body)
	if !ok {
		return
	}
	var response struct {
		Grant orderInputGrant `json:"grant"`
	}
	if json.Unmarshal(data, &response) != nil || !safeOrderInputGrant(&response.Grant, request, time.Now()) {
		replyError(c, http.StatusBadGateway, "INVALID_INPUT_UPLOAD_GRANT", "补充材料上传授权无效")
		return
	}
	reply(c, http.StatusOK, response)
}

func (s *Service) TradeOrderInputUploadConfirm(ctx context.Context, c *app.RequestContext) {
	actor, orderID, ok := orderInputActor(c)
	if !ok {
		return
	}
	var request struct {
		ExpectedVersion int64  `json:"expected_version"`
		RequestID       string `json:"request_id"`
		ObjectID        string `json:"object_id"`
		OrderID         string `json:"order_id"`
	}
	if !decodeOrderInputBody(c, &request, "order_id") || !validOrderInputCommand(request.ExpectedVersion, request.RequestID) || !canonicalInputID(request.ObjectID) {
		replyError(c, http.StatusBadRequest, "INVALID_INPUT_CONFIRMATION", "补充材料确认参数无效")
		return
	}
	request.OrderID = orderID
	body, _ := json.Marshal(request)
	data, ok := s.orderInputMutation(ctx, c, actor, orderID, "input-uploads/confirm", "console.trade.orders.input_uploads.confirm", body)
	if !ok {
		return
	}
	var response struct {
		ObjectID    string `json:"object_id"`
		LogicalPath string `json:"logical_path"`
	}
	if json.Unmarshal(data, &response) != nil || response.ObjectID != request.ObjectID || !validOrderInputPath(response.LogicalPath) {
		replyError(c, http.StatusBadGateway, "INVALID_INPUT_CONFIRMATION_RECEIPT", "补充材料确认结果无效")
		return
	}
	reply(c, http.StatusOK, response)
}

func (s *Service) TradeOrderSupplementInputs(ctx context.Context, c *app.RequestContext) {
	actor, orderID, ok := orderInputActor(c)
	if !ok {
		return
	}
	var request struct {
		ExpectedVersion int64    `json:"expected_version"`
		RequestID       string   `json:"request_id"`
		Answer          string   `json:"answer"`
		ObjectIDs       []string `json:"object_ids,omitempty"`
		OrderID         string   `json:"order_id"`
	}
	if !decodeOrderInputBody(c, &request, "order_id") || !validOrderInputCommand(request.ExpectedVersion, request.RequestID) || !utf8.ValidString(request.Answer) || strings.TrimSpace(request.Answer) == "" || strings.ContainsRune(request.Answer, 0) || len(request.Answer) > 8000 || len(request.ObjectIDs) > 128 {
		replyError(c, http.StatusBadRequest, "INVALID_INPUT_SUPPLEMENT", "补充材料内容无效")
		return
	}
	seen := map[string]bool{}
	for _, id := range request.ObjectIDs {
		if !canonicalInputID(id) || seen[id] {
			replyError(c, http.StatusBadRequest, "INVALID_INPUT_SUPPLEMENT", "补充材料编号无效")
			return
		}
		seen[id] = true
	}
	request.OrderID = orderID
	body, _ := json.Marshal(request)
	data, ok := s.orderInputMutation(ctx, c, actor, orderID, "supplement-inputs", "console.trade.orders.inputs.supplement", body)
	if !ok {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var response struct {
		Order map[string]any `json:"order"`
	}
	if decoder.Decode(&response) != nil || inputIDString(response.Order["order_id"]) != orderID || inputIDString(response.Order["buyer_agent_id"]) != strconv.FormatInt(actor, 10) || response.Order["state"] != "awaiting_seller" || response.Order["version"] != json.Number(strconv.FormatInt(request.ExpectedVersion+1, 10)) {
		replyError(c, http.StatusBadGateway, "INVALID_INPUT_SUPPLEMENT_RECEIPT", "补充材料订单结果无效")
		return
	}
	question, _ := response.Order["input_request"].(map[string]any)
	if question["request_id"] != request.RequestID || question["status"] != "answered" || question["answer"] != request.Answer || !stringifyOrderInputIDs(response.Order) {
		replyError(c, http.StatusBadGateway, "INVALID_INPUT_SUPPLEMENT_RECEIPT", "补充材料订单结果无效")
		return
	}
	reply(c, http.StatusOK, response)
}

func orderInputActor(c *app.RequestContext) (int64, string, bool) {
	actor, ok := agentID(c)
	if !ok {
		replyError(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console Session 无效")
		return 0, "", false
	}
	orderID := c.Param("order_id")
	if !canonicalInputID(orderID) {
		replyError(c, http.StatusBadRequest, "INVALID_ORDER_ID", "订单号无效")
		return 0, "", false
	}
	key := string(c.GetHeader("Idempotency-Key"))
	if key == "" || key != strings.TrimSpace(key) || len(key) > 64 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		replyError(c, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key 无效")
		return 0, "", false
	}
	return actor, orderID, true
}

func (s *Service) orderInputMutation(ctx context.Context, c *app.RequestContext, actor int64, orderID, suffix, operation string, body []byte) (json.RawMessage, bool) {
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return nil, false
	}
	data, err := s.fetch(ctx, actor, orderInputWriteScope, operation, http.MethodPost, "/api/v1/orders/"+orderID+"/"+suffix, nil, body, string(c.GetHeader("Idempotency-Key")), true)
	if err != nil {
		status := upstreamStatus(err)
		var upstream *UpstreamError
		if errors.As(err, &upstream) {
			if suffix == "input-uploads/confirm" && upstream.Status == http.StatusNotFound && upstream.ErrorCode == "INPUT_UPLOAD_NOT_FOUND" {
				replyError(c, http.StatusNotFound, "INPUT_UPLOAD_NOT_FOUND", "材料尚未上传，请重试上传")
				return nil, false
			}
			if upstream.Status == http.StatusBadRequest || upstream.Status == http.StatusRequestEntityTooLarge || upstream.Status == http.StatusUnprocessableEntity {
				status = upstream.Status
			}
		}
		replyError(c, status, "COMMISSION_INPUT_REQUEST_FAILED", http.StatusText(status))
		return nil, false
	}
	return data, true
}

// Reject duplicate and unknown fields before signing a canonical mutation body.
func decodeOrderInputBody(c *app.RequestContext, target any, forbidden string) bool {
	body := c.Request.Body()
	if len(body) == 0 || len(body) > 32<<10 || !utf8.Valid(body) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || key != strings.ToLower(key) || seen[key] || key == forbidden {
			return false
		}
		seen[key] = true
		if d.Decode(new(json.RawMessage)) != nil {
			return false
		}
	}
	if _, err = d.Token(); err != nil || d.Decode(new(any)) != io.EOF {
		return false
	}
	d = json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(target) == nil
}

func canonicalInputID(value string) bool {
	return value == strings.TrimSpace(value) && positiveDecimal(value)
}

func lowerHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func validOrderInputCommand(version int64, requestID string) bool {
	return version > 0 && version < math.MaxInt64 && lowerHexDigest(requestID)
}

func validOrderInputPath(value string) bool {
	return utf8.ValidString(value) && len(value) <= 1024 && strings.HasPrefix(value, "inputs/") && len(value) > len("inputs/") && path.Clean(value) == value && !strings.ContainsRune(value, '\\') && strings.IndexFunc(value, unicode.IsControl) < 0
}

func safeOrderInputGrant(grant *orderInputGrant, request orderInputUpload, now time.Time) bool {
	if !canonicalInputID(grant.ObjectID) || grant.LogicalPath != request.LogicalPath || grant.ByteSize == nil || *grant.ByteSize != *request.ByteSize || grant.SHA256 != request.SHA256 {
		return false
	}
	if grant.Confirmed {
		return grant.Method == "" && grant.URL == "" && len(grant.Headers) == 0 && grant.ExpiresAt == 0
	}
	u, err := url.Parse(grant.URL)
	if err != nil || len(grant.URL) > 16<<10 || strings.Contains(grant.URL, delegationPrefix) || u.Scheme != "https" || u.User != nil || u.Port() != "" || strings.Contains(grant.URL, "#") || u.Opaque != "" || !publicOSSUploadHost(u.Hostname()) || u.Path == "" || u.Path == "/" || grant.Method != http.MethodPut || grant.ExpiresAt <= now.UnixMilli() {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || query.Get("x-oss-signature") == "" && (query.Get("Signature") == "" || query.Get("OSSAccessKeyId") == "") {
		return false
	}
	headers := make(map[string]string)
	seen := map[string]bool{}
	for name, value := range grant.Headers {
		name = strings.ToLower(name)
		if seen[name] || len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") || strings.Contains(value, delegationPrefix) {
			return false
		}
		seen[name] = true
		switch name {
		case "content-length":
			if value != strconv.FormatInt(*request.ByteSize, 10) {
				return false
			}
			// Fetch sends the File/Blob's actual length; browsers forbid setting it.
			continue
		case "x-oss-meta-sha256":
			if value != request.SHA256 {
				return false
			}
		case "x-oss-forbid-overwrite":
			if value != "true" {
				return false
			}
		case "content-type", "content-md5", "x-oss-date", "x-oss-security-token", "x-oss-content-sha256":
		default:
			return false
		}
		headers[name] = value
	}
	if !seen["content-length"] || !seen["x-oss-meta-sha256"] || !seen["x-oss-forbid-overwrite"] {
		return false
	}
	grant.Headers = headers
	return true
}

func publicOSSUploadHost(host string) bool {
	bucket, endpoint, ok := strings.Cut(strings.ToLower(host), ".")
	if !ok || len(bucket) < 3 || len(bucket) > 63 || bucket[0] == '-' || bucket[len(bucket)-1] == '-' || !strings.HasPrefix(endpoint, "oss-") || !strings.HasSuffix(endpoint, ".aliyuncs.com") || strings.Contains(endpoint, "-internal.") {
		return false
	}
	for _, r := range bucket {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	region := strings.TrimSuffix(strings.TrimPrefix(endpoint, "oss-"), ".aliyuncs.com")
	for _, r := range region {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return region != ""
}

func inputIDString(value any) string {
	switch value := value.(type) {
	case string:
		if canonicalInputID(value) {
			return value
		}
	case json.Number:
		if canonicalInputID(value.String()) {
			return value.String()
		}
	}
	return ""
}

func stringifyOrderInputIDs(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for name, child := range value {
			if number, ok := child.(json.Number); ok && (name == "id" || strings.HasSuffix(name, "_id")) {
				id := number.String()
				if !canonicalInputID(id) && id != "0" {
					return false
				}
				value[name] = id
			} else if !stringifyOrderInputIDs(child) {
				return false
			}
		}
	case []any:
		for _, child := range value {
			if !stringifyOrderInputIDs(child) {
				return false
			}
		}
	}
	return true
}
