package hub

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/network/standard"
	clientws "github.com/gorilla/websocket"
	"github.com/hertz-contrib/websocket"
)

func TestRevocationClosesEstablishedConnectionBeforeNextPayload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(server.WithListener(listener), server.WithTransport(standard.NewTransporter))
	var active atomic.Bool
	active.Store(true)
	next := make(chan struct{})
	upgrader := websocket.HertzUpgrader{}
	h.GET("/stream", func(_ context.Context, c *app.RequestContext) {
		_ = upgrader.Upgrade(c, func(ws *websocket.Conn) {
			defer ws.Close()
			conn := Connection{Conn: ws, SessionActive: active.Load}
			if conn.WriteAuthenticated(websocket.TextMessage, []byte("first")) != nil {
				return
			}
			<-next
			_ = conn.WriteAuthenticated(websocket.TextMessage, []byte("private update"))
		})
	})
	go h.Run()
	defer h.Close()
	client, _, err := clientws.DefaultDialer.Dial("ws://"+listener.Addr().String()+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, first, err := client.ReadMessage()
	active.Store(false)
	close(next)
	if err != nil || string(first) != "first" {
		t.Fatalf("first frame %q: %v", first, err)
	}
	if _, payload, err := client.ReadMessage(); !clientws.IsCloseError(err, 4001) {
		t.Fatalf("expected revoked close before data, got %q: %v", payload, err)
	}
}
