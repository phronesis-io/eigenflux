// Command gatewaystandin is a standalone, container-runnable implementation of
// the EigenFlux gateway envelope that the FluxSpace Hub's GatewayPort client
// speaks (POST /api/v1/relations/apply, POST /api/v1/pm/send,
// POST /api/v1/pm/fetch).
//
// It exists ONLY so the Hub can exercise its real HTTP client end-to-end against
// a process running in a separate container — the closest available stand-in for
// the G10 live-stack e2e. It is a faithful double of the in-test s1Gateway, not
// the real EigenFlux gateway, and must never be used in production. The real
// gateway (api/main.go, the eigenflux_server) needs PostgreSQL, Redis, etcd and
// nine kitex backend microservices that are not part of this repository.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"fluxspace.ai/hub/models"
)

// gateway replays an auto-accept reply for every invitation PM it receives, so
// a caller running the full S1 loop (send -> fetch -> parse -> confirm) over
// real HTTP observes a confirmed negotiation exactly as it would against any
// conforming gateway.
type gateway struct {
	mu      sync.Mutex
	replies []map[string]any
	seq     int
}

func (g *gateway) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		ok := func(data any) {
			buf, _ := json.Marshal(map[string]any{"code": 0, "msg": "ok", "data": data})
			_, _ = w.Write(buf)
		}
		switch r.URL.Path {
		case "/api/v1/relations/apply":
			ok(map[string]any{"request_id": "req_1"})
		case "/api/v1/pm/send":
			var req struct {
				ReceiverID string `json:"receiver_id"`
				Content    string `json:"content"`
			}
			_ = json.Unmarshal(body, &req)
			g.mu.Lock()
			g.seq++
			seq := g.seq
			if content := strings.TrimSpace(req.Content); content != "" {
				var inv models.Invitation
				if err := json.Unmarshal([]byte(content), &inv); err == nil && inv.Kind == models.InvitationKind {
					reply, _ := json.Marshal(models.InvitationReply{
						Kind:             models.ReplyKind,
						InvitationID:     inv.InvitationID,
						Action:           models.ReplyAccept,
						AgentID:          inv.ToAgentID,
						SelectedProposal: inv.SelectedProposal,
					})
					g.replies = append(g.replies, map[string]any{
						"msg_id":      "msg_reply_1",
						"conv_id":     "conv_1",
						"sender_id":   inv.ToAgentID,
						"receiver_id": inv.FromAgentID,
						"content":     string(reply),
						"is_read":     false,
						"created_at":  int64(1),
					})
				}
			}
			g.mu.Unlock()
			ok(map[string]any{"msg_id": "msg_" + itoa(seq), "conv_id": "conv_1"})
		case "/api/v1/pm/fetch":
			g.mu.Lock()
			messages := append([]map[string]any(nil), g.replies...)
			g.mu.Unlock()
			ok(map[string]any{"messages": messages, "next_cursor": "cursor_1"})
		default:
			http.NotFound(w, r)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	g := &gateway{}
	srv := &http.Server{Addr: addr, Handler: g.handler()}
	log.Printf("gatewaystandin listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
