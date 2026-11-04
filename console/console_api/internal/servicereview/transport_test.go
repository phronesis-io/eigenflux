package servicereview

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/base"
	api "github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/commission"
	"github.com/phronesis-io/eigenflux-commission/kitex_gen/eigenflux/commission/commissionservice"
	"github.com/phronesis-io/eigenflux-commission/pkg/rpcauth"
)

type transportService struct {
	api.CommissionService
	mu       sync.Mutex
	listed   *api.ListModerationsReq
	resolved *api.ResolveModerationReq
}

func (s *transportService) ListModerations(ctx context.Context, req *api.ListModerationsReq) (*api.ListModerationsResp, error) {
	if err := rpcauth.RequireNoAgent(ctx); err != nil {
		return &api.ListModerationsResp{BaseResp: rpcauth.Response(err)}, nil
	}
	s.mu.Lock()
	s.listed = req
	s.mu.Unlock()
	return &api.ListModerationsResp{BaseResp: &base.BaseResp{}, ResultJson: `{"items":[{"moderation":{"id":"9223372036854775806","commission_id":"9223372036854775805","status":"review","draft_version":3},"listing":{"title":"Frozen submitted service"}}],"next_cursor":"9223372036854775806"}`}, nil
}
func (s *transportService) ResolveModeration(ctx context.Context, req *api.ResolveModerationReq) (*api.ResolveModerationResp, error) {
	if err := rpcauth.RequireNoAgent(ctx); err != nil {
		return &api.ResolveModerationResp{BaseResp: rpcauth.Response(err)}, nil
	}
	s.mu.Lock()
	s.resolved = req
	s.mu.Unlock()
	return &api.ResolveModerationResp{BaseResp: &base.BaseResp{}, ResultJson: `{"id":"9223372036854775806","status":"pass","reviewer":"Alice","review_reason":"Verified scope"}`}, nil
}

func TestConsoleRoutesThroughRealCommissionRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := &transportService{}
	rpcServer := commissionservice.NewServer(handler, server.WithListener(listener), server.WithMetaHandler(transmeta.ServerTTHeaderHandler))
	done := make(chan error, 1)
	go func() { done <- rpcServer.Run() }()
	t.Cleanup(func() {
		if err := rpcServer.Stop(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("RPC server did not stop")
		}
	})
	rpc, err := NewClient(listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, err := rpc.ListModerations(context.Background(), &api.ListModerationsReq{Status: "review", Limit: 20})
		if err == nil && response.BaseResp.Code == 0 {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("RPC server not ready")
	}
	status, body := request(t, rpc, http.MethodGet, "/console/api/v1/service-reviews?status=review&cursor=9223372036854775700&limit=10", "")
	if status != 200 || !strings.Contains(body, `"id":"9223372036854775806"`) || !strings.Contains(body, "Frozen submitted service") {
		t.Fatalf("list status %d: %s", status, body)
	}
	handler.mu.Lock()
	listed := handler.listed
	handler.mu.Unlock()
	if listed.Cursor != 9223372036854775700 || listed.Limit != 10 || listed.Status != "review" {
		t.Fatalf("RPC list: %+v", listed)
	}
	status, body = request(t, rpc, http.MethodPost, "/console/api/v1/service-reviews/9223372036854775806/resolve", `{"decision":"pass","operator":" Alice ","reason":" Verified scope "}`)
	if status != 200 || !strings.Contains(body, `"status":"pass"`) {
		t.Fatalf("resolve status %d: %s", status, body)
	}
	handler.mu.Lock()
	resolved := handler.resolved
	handler.mu.Unlock()
	if resolved.ModerationId != 9223372036854775806 || resolved.Reviewer != "Alice" || resolved.Reason != "Verified scope" || resolved.Decision != "pass" {
		t.Fatalf("RPC resolve: %+v", resolved)
	}
	// TTHeader must carry the identity guard even though Console calls carry none.
	agentContext := metainfo.WithPersistentValue(context.Background(), rpcauth.AgentIDKey, "42")
	denied, err := rpc.ListModerations(agentContext, &api.ListModerationsReq{Status: "review", Limit: 1})
	if err != nil || denied.BaseResp.Code != 403 {
		t.Fatalf("propagated seller bypassed RPC guard: %+v %v", denied, err)
	}
}
