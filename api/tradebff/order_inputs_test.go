package tradebff

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

const inputTestOrder = "9007199254740993"
const inputTestObject = "9007199254740995"

var inputTestRequest = strings.Repeat("a", 64)
var inputTestSHA = strings.Repeat("b", 64)

func inputUploadBody() string {
	return `{"expected_version":3,"request_id":"` + inputTestRequest + `","logical_path":"inputs/request.txt","byte_size":3,"sha256":"` + inputTestSHA + `"}`
}

func inputConfirmationBody() string {
	return `{"expected_version":3,"request_id":"` + inputTestRequest + `","object_id":"` + inputTestObject + `"}`
}

func inputSupplementBody() string {
	return `{"expected_version":3,"request_id":"` + inputTestRequest + `","answer":"The finance team.","object_ids":["` + inputTestObject + `"]}`
}

func inputGrantFixture() map[string]any {
	return map[string]any{
		"object_id": inputTestObject, "method": "PUT", "url": "https://bucket.oss-cn-hangzhou.aliyuncs.com/workspaces/1/objects/" + inputTestObject + "?x-oss-signature=" + strings.Repeat("c", 64),
		"expires_at": time.Now().Add(time.Minute).UnixMilli(), "logical_path": "inputs/request.txt", "byte_size": 3, "sha256": inputTestSHA, "confirmed": false,
		"headers": map[string]string{"Content-Length": "3", "x-oss-meta-sha256": inputTestSHA, "x-oss-forbid-overwrite": "true"},
	}
}

func inputOrderFixture() map[string]any {
	return map[string]any{
		"order": map[string]any{
			"order_id": json.Number(inputTestOrder), "buyer_agent_id": json.Number("42"), "seller_agent_id": json.Number("84"),
			"workspace_id": json.Number("9007199254740997"), "version": 4, "state": "awaiting_seller",
			"contract":      map[string]any{"commission_id": json.Number("9007199254740999")},
			"input_request": map[string]any{"request_id": inputTestRequest, "status": "answered", "answer": "The finance team."},
		},
	}
}

type inputMutationCall struct {
	method, path, token, key string
	body                     []byte
}

func inputMutationService(t *testing.T, data any, status int) (*Service, *[]inputMutationCall) {
	t.Helper()
	s := configuredService(t, "http://127.0.0.1:1")
	calls := []inputMutationCall{}
	s.client.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, inputMutationCall{r.Method, r.URL.RequestURI(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.Header.Get("Idempotency-Key"), body})
		envelope := map[string]any{"code": 0, "data": data}
		if status >= 400 {
			envelope = map[string]any{"code": status, "msg": "private-upstream-diagnostic " + delegationPrefix + "secret"}
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(payload))), Header: http.Header{}}, nil
	})
	return s, &calls
}

func assertInputMutationBinding(t *testing.T, s *Service, call inputMutationCall, suffix, operation string) {
	t.Helper()
	claims := tokenClaims(t, call.token)
	if call.path != "/api/v1/orders/"+inputTestOrder+"/"+suffix || call.method != http.MethodPost || call.key != "input-test-key" || claims.Subject != "42" || claims.Scope != orderInputWriteScope || claims.Method != http.MethodPost || claims.Operation != operation || claims.BodySHA256 != delegationDigest(call.body) || claims.IdempotencyKeySHA256 != delegationDigest([]byte(call.key)) {
		t.Fatalf("invalid scoped mutation: path=%s claims=%+v", call.path, claims)
	}
	parts := strings.Split(call.token, ".")
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(s.delegator.privateKey.Public().(ed25519.PublicKey), delegationTranscript("current", parts[1]), signature) {
		t.Fatal("mutation delegation was not signed")
	}
	var bound map[string]any
	if json.Unmarshal(call.body, &bound) != nil || bound["order_id"] != inputTestOrder {
		t.Fatalf("path resource was not bound: %s", call.body)
	}
	bound["order_id"] = "9007199254740994"
	other, _ := json.Marshal(bound)
	if delegationDigest(other) == claims.BodySHA256 {
		t.Fatal("delegation can be reused for another Order")
	}
}

func TestTradeOrderInputMutationsBindActorResourceAndBody(t *testing.T) {
	for _, tc := range []struct {
		name, body, suffix, operation string
		data                          any
		run                           func(*Service, context.Context, *app.RequestContext)
	}{
		{"upload", inputUploadBody(), "input-uploads", "console.trade.orders.input_uploads.create", map[string]any{"grant": inputGrantFixture(), "delegation_token": delegationPrefix + "secret"}, (*Service).TradeOrderInputUpload},
		{"confirm", inputConfirmationBody(), "input-uploads/confirm", "console.trade.orders.input_uploads.confirm", map[string]any{"object_id": inputTestObject, "logical_path": "inputs/request.txt", "delegation_token": delegationPrefix + "secret"}, (*Service).TradeOrderInputUploadConfirm},
		{"supplement", inputSupplementBody(), "supplement-inputs", "console.trade.orders.inputs.supplement", inputOrderFixture(), (*Service).TradeOrderSupplementInputs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, calls := inputMutationService(t, tc.data, http.StatusOK)
			c := paymentContext(inputTestOrder, tc.body, "input-test-key", true)
			c.Request.SetRequestURI("/ignored?agent_id=99&order_id=1")
			tc.run(s, context.Background(), c)
			if c.Response.StatusCode() != http.StatusOK || len(*calls) != 1 {
				t.Fatalf("status=%d calls=%d body=%s", c.Response.StatusCode(), len(*calls), c.Response.Body())
			}
			assertInputMutationBinding(t, s, (*calls)[0], tc.suffix, tc.operation)
			if string(c.Response.Header.Peek("Cache-Control")) != "private, no-store" || strings.Contains(string(c.Response.Body()), delegationPrefix) {
				t.Fatalf("private response cached or leaked delegation: %s", c.Response.Body())
			}
			if tc.name == "upload" {
				var response struct {
					Data struct{ Grant orderInputGrant } `json:"data"`
				}
				if json.Unmarshal(c.Response.Body(), &response) != nil || response.Data.Grant.ObjectID != inputTestObject || response.Data.Grant.Headers["content-length"] != "" || response.Data.Grant.Headers["x-oss-meta-sha256"] != inputTestSHA || response.Data.Grant.Headers["x-oss-forbid-overwrite"] != "true" {
					t.Fatalf("unsafe or unusable browser grant: %s", c.Response.Body())
				}
			}
			if tc.name == "supplement" {
				for _, id := range []string{inputTestOrder, "42", "84", "9007199254740997", "9007199254740999"} {
					if !strings.Contains(string(c.Response.Body()), `:"`+id+`"`) {
						t.Fatalf("ID was not preserved as a string: %s", c.Response.Body())
					}
				}
			}
		})
	}
}

func TestTradeOrderInputMutationsRejectUntrustedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, id, body, key string
		auth                bool
		status              int
	}{
		{"no session", inputTestOrder, inputUploadBody(), "key", false, 401},
		{"no key", inputTestOrder, inputUploadBody(), "", true, 400},
		{"blank key", inputTestOrder, inputUploadBody(), " ", true, 400},
		{"long key", inputTestOrder, inputUploadBody(), strings.Repeat("a", 65), true, 400},
		{"zero order", "0", inputUploadBody(), "key", true, 400},
		{"leading zero", "01", inputUploadBody(), "key", true, 400},
		{"spaced id", " 1", inputUploadBody(), "key", true, 400},
		{"order overflow", "9223372036854775808", inputUploadBody(), "key", true, 400},
		{"order injection", "1/input-uploads", inputUploadBody(), "key", true, 400},
		{"browser actor", inputTestOrder, strings.TrimSuffix(inputUploadBody(), "}") + `,"agent_id":"99"}`, "key", true, 400},
		{"browser order", inputTestOrder, strings.TrimSuffix(inputUploadBody(), "}") + `,"order_id":"2"}`, "key", true, 400},
		{"mixed case order", inputTestOrder, strings.TrimSuffix(inputUploadBody(), "}") + `,"Order_Id":"2"}`, "key", true, 400},
		{"duplicate field", inputTestOrder, strings.TrimSuffix(inputUploadBody(), "}") + `,"expected_version":4}`, "key", true, 400},
		{"trailing value", inputTestOrder, inputUploadBody() + `{}`, "key", true, 400},
		{"null body", inputTestOrder, `null`, "key", true, 400},
		{"body array", inputTestOrder, `[]`, "key", true, 400},
		{"body limit", inputTestOrder, strings.Repeat(" ", 32<<10) + inputUploadBody(), "key", true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, calls := inputMutationService(t, nil, http.StatusOK)
			c := paymentContext(tc.id, tc.body, tc.key, tc.auth)
			s.TradeOrderInputUpload(context.Background(), c)
			if c.Response.StatusCode() != tc.status || len(*calls) != 0 {
				t.Fatalf("status=%d calls=%d body=%s", c.Response.StatusCode(), len(*calls), c.Response.Body())
			}
		})
	}
	for name, replacement := range map[string][2]string{
		"version absent": {`"expected_version":3,`, ""}, "zero version": {`"expected_version":3`, `"expected_version":0`},
		"request absent": {inputTestRequest, ""}, "uppercase request": {inputTestRequest, strings.Repeat("A", 64)},
		"path traversal": {"inputs/request.txt", "inputs/../outputs/report.txt"}, "wrong namespace": {"inputs/request.txt", "outputs/report.txt"},
		"empty name": {"inputs/request.txt", "inputs/"}, "control path": {"inputs/request.txt", `inputs/req\u0000.txt`},
		"size absent": {`"byte_size":3,`, ""}, "negative size": {`"byte_size":3`, `"byte_size":-1`},
		"large size": {`"byte_size":3`, `"byte_size":67108865`}, "short hash": {inputTestSHA, "bbb"},
	} {
		t.Run(name, func(t *testing.T) {
			s, calls := inputMutationService(t, nil, http.StatusOK)
			c := paymentContext(inputTestOrder, strings.Replace(inputUploadBody(), replacement[0], replacement[1], 1), "key", true)
			s.TradeOrderInputUpload(context.Background(), c)
			if c.Response.StatusCode() != 400 || len(*calls) != 0 {
				t.Fatalf("status=%d calls=%d body=%s", c.Response.StatusCode(), len(*calls), c.Response.Body())
			}
		})
	}
}

func TestTradeOrderInputGrantRejectsUnsafeStorageAndHeaderData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"http", func(g map[string]any) {
			g["url"] = "http://bucket.oss-cn-hangzhou.aliyuncs.com/file?Signature=s&OSSAccessKeyId=k"
		}},
		{"non OSS", func(g map[string]any) { g["url"] = "https://evil.test/file?x-oss-signature=s" }},
		{"suffix attack", func(g map[string]any) {
			g["url"] = "https://bucket.oss-cn-hangzhou.aliyuncs.com.evil.test/file?x-oss-signature=s"
		}},
		{"internal OSS", func(g map[string]any) {
			g["url"] = "https://bucket.oss-cn-hangzhou-internal.aliyuncs.com/file?x-oss-signature=s"
		}},
		{"credentials", func(g map[string]any) {
			g["url"] = "https://user:secret@bucket.oss-cn-hangzhou.aliyuncs.com/file?x-oss-signature=s"
		}},
		{"fragment", func(g map[string]any) { g["url"] = g["url"].(string) + "#secret" }},
		{"port", func(g map[string]any) {
			g["url"] = "https://bucket.oss-cn-hangzhou.aliyuncs.com:8443/file?x-oss-signature=s"
		}},
		{"unsigned URL", func(g map[string]any) { g["url"] = "https://bucket.oss-cn-hangzhou.aliyuncs.com/file" }},
		{"expired", func(g map[string]any) { g["expires_at"] = time.Now().Add(-time.Second).UnixMilli() }},
		{"wrong method", func(g map[string]any) { g["method"] = "POST" }},
		{"wrong object", func(g map[string]any) { g["object_id"] = "01" }},
		{"numeric object", func(g map[string]any) { g["object_id"] = json.Number(inputTestObject) }},
		{"wrong path", func(g map[string]any) { g["logical_path"] = "inputs/other.txt" }},
		{"wrong size", func(g map[string]any) { g["byte_size"] = 4 }},
		{"missing size", func(g map[string]any) { delete(g, "byte_size") }},
		{"wrong hash", func(g map[string]any) { g["sha256"] = strings.Repeat("c", 64) }},
		{"authorization", func(g map[string]any) { g["headers"].(map[string]string)["Authorization"] = "Bearer private" }},
		{"cookie", func(g map[string]any) { g["headers"].(map[string]string)["Cookie"] = "private" }},
		{"arbitrary OSS header", func(g map[string]any) { g["headers"].(map[string]string)["x-oss-callback"] = "arbitrary" }},
		{"wrong length header", func(g map[string]any) { g["headers"].(map[string]string)["Content-Length"] = "4" }},
		{"wrong hash header", func(g map[string]any) {
			g["headers"].(map[string]string)["x-oss-meta-sha256"] = strings.Repeat("c", 64)
		}},
		{"overwrite allowed", func(g map[string]any) { g["headers"].(map[string]string)["x-oss-forbid-overwrite"] = "false" }},
		{"duplicate case header", func(g map[string]any) { g["headers"].(map[string]string)["content-length"] = "3" }},
		{"header newline", func(g map[string]any) { g["headers"].(map[string]string)["Content-Type"] = "text/plain\r\nX-Evil: yes" }},
		{"delegation in URL", func(g map[string]any) { g["url"] = g["url"].(string) + "&token=" + delegationPrefix + "secret" }},
		{"delegation in header", func(g map[string]any) {
			g["headers"].(map[string]string)["x-oss-security-token"] = delegationPrefix + "secret"
		}},
		{"missing metadata", func(g map[string]any) { delete(g["headers"].(map[string]string), "x-oss-meta-sha256") }},
		{"false confirmation", func(g map[string]any) { g["confirmed"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := inputGrantFixture()
			tc.change(grant)
			s, _ := inputMutationService(t, map[string]any{"grant": grant}, http.StatusOK)
			c := paymentContext(inputTestOrder, inputUploadBody(), "input-test-key", true)
			s.TradeOrderInputUpload(context.Background(), c)
			if c.Response.StatusCode() != http.StatusBadGateway || strings.Contains(string(c.Response.Body()), "secret") || strings.Contains(string(c.Response.Body()), "evil.test") {
				t.Fatalf("unsafe grant exposed: %d %s", c.Response.StatusCode(), c.Response.Body())
			}
		})
	}
}

func TestTradeOrderInputConfirmedAndEmptyFiles(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		grant := inputGrantFixture()
		grant["byte_size"] = 0
		grant["headers"].(map[string]string)["Content-Length"] = "0"
		if confirmed {
			grant["confirmed"], grant["method"], grant["url"], grant["expires_at"], grant["headers"] = true, "", "", int64(0), map[string]string{}
		}
		s, _ := inputMutationService(t, map[string]any{"grant": grant}, http.StatusOK)
		c := paymentContext(inputTestOrder, strings.Replace(inputUploadBody(), `"byte_size":3`, `"byte_size":0`, 1), "input-test-key", true)
		s.TradeOrderInputUpload(context.Background(), c)
		if c.Response.StatusCode() != http.StatusOK {
			t.Fatalf("confirmed=%v: %d %s", confirmed, c.Response.StatusCode(), c.Response.Body())
		}
	}
}

func TestTradeOrderInputConfirmationAndSupplementRejectForgedReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		data       any
		run        func(*Service, context.Context, *app.RequestContext)
	}{
		{"other object", inputConfirmationBody(), map[string]any{"object_id": "123", "logical_path": "inputs/request.txt"}, (*Service).TradeOrderInputUploadConfirm},
		{"numeric object", inputConfirmationBody(), map[string]any{"object_id": json.Number(inputTestObject), "logical_path": "inputs/request.txt"}, (*Service).TradeOrderInputUploadConfirm},
		{"other path namespace", inputConfirmationBody(), map[string]any{"object_id": inputTestObject, "logical_path": "outputs/report.txt"}, (*Service).TradeOrderInputUploadConfirm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := inputMutationService(t, tc.data, http.StatusOK)
			c := paymentContext(inputTestOrder, tc.body, "input-test-key", true)
			tc.run(s, context.Background(), c)
			if c.Response.StatusCode() != http.StatusBadGateway {
				t.Fatalf("accepted forged receipt: %s", c.Response.Body())
			}
		})
	}
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"wrong order", "order_id", "123"}, {"wrong buyer", "buyer_agent_id", "99"}, {"wrong state", "state", "completed"}, {"wrong version", "version", 3},
		{"wrong question", "input_request", map[string]any{"request_id": strings.Repeat("c", 64), "status": "answered", "answer": "The finance team."}},
		{"unanswered question", "input_request", map[string]any{"request_id": inputTestRequest, "status": "waiting_buyer", "answer": "The finance team."}},
		{"changed answer", "input_request", map[string]any{"request_id": inputTestRequest, "status": "answered", "answer": "different"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := inputOrderFixture()
			data["order"].(map[string]any)[tc.field] = tc.value
			s, _ := inputMutationService(t, data, http.StatusOK)
			c := paymentContext(inputTestOrder, inputSupplementBody(), "input-test-key", true)
			s.TradeOrderSupplementInputs(context.Background(), c)
			if c.Response.StatusCode() != http.StatusBadGateway || strings.Contains(string(c.Response.Body()), `"order"`) {
				t.Fatalf("accepted or disclosed mismatched order: %s", c.Response.Body())
			}
		})
	}
}

func TestTradeOrderSupplementRejectsInvalidObjectsAndAnswers(t *testing.T) {
	for name, replacement := range map[string][2]string{
		"empty answer": {"The finance team.", " "}, "oversize answer": {"The finance team.", strings.Repeat("a", 8001)},
		"null answer": {`"answer":"The finance team."`, `"answer":null`}, "null object": {`"` + inputTestObject + `"`, "null"},
		"numeric object": {`"` + inputTestObject + `"`, inputTestObject}, "leading zero": {inputTestObject, "01"},
		"duplicate object": {`"` + inputTestObject + `"`, `"` + inputTestObject + `","` + inputTestObject + `"`},
		"too many objects": {`"` + inputTestObject + `"`, strings.Trim(strings.Repeat(`"123",`, 129), ",")},
	} {
		t.Run(name, func(t *testing.T) {
			s, calls := inputMutationService(t, nil, http.StatusOK)
			c := paymentContext(inputTestOrder, strings.Replace(inputSupplementBody(), replacement[0], replacement[1], 1), "input-test-key", true)
			s.TradeOrderSupplementInputs(context.Background(), c)
			if c.Response.StatusCode() != http.StatusBadRequest || len(*calls) != 0 {
				t.Fatalf("invalid supplement called upstream: %d %s", c.Response.StatusCode(), c.Response.Body())
			}
		})
	}
}

func TestTradeOrderInputMutationPreservesAuthorizationAndVersionErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 413, 422, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s, _ := inputMutationService(t, nil, status)
			c := paymentContext(inputTestOrder, inputUploadBody(), "input-test-key", true)
			s.TradeOrderInputUpload(context.Background(), c)
			want := status
			if status == 500 {
				want = 503
			}
			if c.Response.StatusCode() != want || strings.Contains(string(c.Response.Body()), "private-upstream") || strings.Contains(string(c.Response.Body()), delegationPrefix) {
				t.Fatalf("status=%d want=%d body=%s", c.Response.StatusCode(), want, c.Response.Body())
			}
		})
	}
	for _, tc := range []struct {
		body string
		run  func(*Service, context.Context, *app.RequestContext)
	}{
		{inputUploadBody(), (*Service).TradeOrderInputUpload},
		{inputConfirmationBody(), (*Service).TradeOrderInputUploadConfirm},
		{inputSupplementBody(), (*Service).TradeOrderSupplementInputs},
	} {
		for _, auth := range []bool{false, true} {
			c := paymentContext(inputTestOrder, tc.body, "input-test-key", auth)
			tc.run(NewUnavailable(""), context.Background(), c)
			want := http.StatusUnauthorized
			if auth {
				want = http.StatusServiceUnavailable
			}
			if c.Response.StatusCode() != want {
				t.Fatalf("auth=%v: status=%d want=%d", auth, c.Response.StatusCode(), want)
			}
		}
	}
}

func TestTradeOrderInputConfirmationPreservesOnlyExplicitMissingUpload(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		status        int
		want          bool
	}{
		{"confirmed missing upload", `{"code":404,"error_code":"INPUT_UPLOAD_NOT_FOUND"}`, 404, true},
		{"ordinary missing order", `{"code":404,"error_code":"ORDER_NOT_FOUND"}`, 404, false},
		{"message is not a code", `{"code":404,"msg":"INPUT_UPLOAD_NOT_FOUND"}`, 404, false},
		{"nested field is not a code", `{"code":404,"data":{"error_code":"INPUT_UPLOAD_NOT_FOUND"}}`, 404, false},
		{"storage outage", `{"code":503,"error_code":"INPUT_UPLOAD_NOT_FOUND"}`, 503, false},
		{"invalid state", `{"code":422,"error_code":"INPUT_UPLOAD_NOT_FOUND"}`, 422, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := configuredService(t, "http://127.0.0.1:1")
			s.client.http.Transport = fileRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.payload)), Header: http.Header{}}, nil
			})
			c := paymentContext(inputTestOrder, inputConfirmationBody(), "input-test-key", true)
			s.TradeOrderInputUploadConfirm(context.Background(), c)
			if got := strings.Contains(string(c.Response.Body()), `"code":"INPUT_UPLOAD_NOT_FOUND"`); got != tc.want || c.Response.StatusCode() != tc.status {
				t.Fatalf("status=%d missing=%v want=%v body=%s", c.Response.StatusCode(), got, tc.want, c.Response.Body())
			}
		})
	}
}
