package servicereview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/base"
	api "github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/commission"
)

type fakeClient struct {
	listRequest    *api.ListModerationsReq
	resolveRequest *api.ResolveModerationReq
	code           int32
	payload        string
	err            error
}

func (f *fakeClient) ListModerations(_ context.Context, req *api.ListModerationsReq, _ ...callopt.Option) (*api.ListModerationsResp, error) {
	f.listRequest = req
	return &api.ListModerationsResp{BaseResp: &base.BaseResp{Code: f.code, Msg: "private SQL credential"}, ResultJson: f.payload}, f.err
}
func (f *fakeClient) ResolveModeration(_ context.Context, req *api.ResolveModerationReq, _ ...callopt.Option) (*api.ResolveModerationResp, error) {
	f.resolveRequest = req
	return &api.ResolveModerationResp{BaseResp: &base.BaseResp{Code: f.code, Msg: "private SQL credential"}, ResultJson: f.payload}, f.err
}
func request(t *testing.T, rpc Client, method, path, body string) (int, string) {
	t.Helper()
	h := server.New()
	Register(h, rpc)
	result := ut.PerformRequest(h.Engine, method, path, &ut.Body{Body: bytes.NewBufferString(body), Len: len(body)}, ut.Header{Key: "Content-Type", Value: "application/json"}).Result()
	return result.StatusCode(), string(result.Body())
}
func TestListKeepsSnapshotAndDecimalIDs(t *testing.T) {
	f := &fakeClient{payload: `{"items":[{"moderation":{"id":"9223372036854775806","commission_id":"9223372036854775805"},"listing":{"title":"Submitted title","request_spec_text":"Exact submitted input"}}],"next_cursor":"9223372036854775806"}`}
	status, body := request(t, f, http.MethodGet, "/console/api/v1/service-reviews", "")
	if status != 200 || f.listRequest.Status != "review" || f.listRequest.Cursor != 0 || f.listRequest.Limit != 20 {
		t.Fatalf("status %d, request %+v: %s", status, f.listRequest, body)
	}
	var result struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	var expected, actual any
	if err := json.Unmarshal([]byte(f.payload), &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.Data, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("RPC result changed: %s", result.Data)
	}
	if !strings.Contains(string(result.Data), `"id":"9223372036854775806"`) || !strings.Contains(string(result.Data), "Exact submitted input") {
		t.Fatal(body)
	}
	status, _ = request(t, f, http.MethodGet, "/console/api/v1/service-reviews?status=reject&cursor=9223372036854775806&limit=100", "")
	if status != 200 || f.listRequest.Cursor != 9223372036854775806 || f.listRequest.Status != "reject" || f.listRequest.Limit != 100 {
		t.Fatalf("paging request %+v, status %d", f.listRequest, status)
	}
}
func TestListRejectsInvalidQueryBeforeRPC(t *testing.T) {
	for _, query := range []string{"status=unknown", "cursor=-1", "cursor=1e3", "cursor=9223372036854775808", "limit=0", "limit=101", "limit=1.5"} {
		t.Run(query, func(t *testing.T) {
			f := &fakeClient{}
			status, _ := request(t, f, http.MethodGet, "/console/api/v1/service-reviews?"+query, "")
			if status != 400 || f.listRequest != nil {
				t.Fatalf("status %d, request %+v", status, f.listRequest)
			}
		})
	}
}
func TestResolveRequiresAuditedDecision(t *testing.T) {
	for _, body := range []string{`{}`, `{"decision":"review","operator":"Alice","reason":"Scope"}`, `{"decision":"pass","operator":" ","reason":"Scope"}`, `{"decision":"reject","operator":"Alice","reason":" "}`, `not json`, `{"decision":"pass","operator":"` + strings.Repeat("中", 67) + `","reason":"Scope"}`, `{"decision":"reject","operator":"Alice","reason":"` + strings.Repeat("中", 2667) + `"}`} {
		f := &fakeClient{}
		status, _ := request(t, f, http.MethodPost, "/console/api/v1/service-reviews/42/resolve", body)
		if status != 400 || f.resolveRequest != nil {
			t.Fatalf("invalid body accepted: %d", status)
		}
	}
	f := &fakeClient{payload: `{"id":"9223372036854775806","status":"pass"}`}
	status, body := request(t, f, http.MethodPost, "/console/api/v1/service-reviews/9223372036854775806/resolve", `{"decision":"pass","operator":" Alice ","reason":" 已核对 "}`)
	if status != 200 || f.resolveRequest.ModerationId != 9223372036854775806 || f.resolveRequest.Reviewer != "Alice" || f.resolveRequest.Reason != "已核对" {
		t.Fatalf("status %d, request %+v: %s", status, f.resolveRequest, body)
	}
	for _, id := range []string{"0", "-1", "9223372036854775808", "1e4"} {
		f := &fakeClient{}
		status, _ := request(t, f, http.MethodPost, "/console/api/v1/service-reviews/"+id+"/resolve", `{"decision":"pass","operator":"Alice","reason":"OK"}`)
		if status != 400 || f.resolveRequest != nil {
			t.Fatalf("accepted id %s", id)
		}
	}
}
func TestRPCConflictsAndFailuresAreSanitized(t *testing.T) {
	for _, code := range []int32{400, 403, 404, 409, 500} {
		f := &fakeClient{code: code}
		status, body := request(t, f, http.MethodPost, "/console/api/v1/service-reviews/42/resolve", `{"decision":"reject","operator":"Alice","reason":"Clarify"}`)
		want := int(code)
		if code == 500 {
			want = 503
		}
		if status != want || strings.Contains(body, "private") {
			t.Fatalf("code %d: status %d body %s", code, status, body)
		}
	}
	for _, f := range []*fakeClient{{err: errors.New("private SQL credential")}, {payload: "invalid JSON"}} {
		status, body := request(t, f, http.MethodGet, "/console/api/v1/service-reviews", "")
		if status != 503 || strings.Contains(body, "private") {
			t.Fatalf("status %d body %s", status, body)
		}
	}
}
func TestClientRejectsInvalidPort(t *testing.T) {
	for _, port := range []int{0, -1, 65536} {
		if _, err := NewClient(port); err == nil {
			t.Fatalf("accepted port %d", port)
		}
	}
}
