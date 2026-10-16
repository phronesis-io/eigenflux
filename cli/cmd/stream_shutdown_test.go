//go:build !windows

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/config"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

// Run the real stream command in a process so OS signals and stdout
// backpressure exercise its actual socket reader and shutdown branch.
func TestStreamShutdownProcess(t *testing.T) {
	if os.Getenv("EF_STREAM_SHUTDOWN_PROCESS") != "1" {
		return
	}
	config.SetHomeDir(os.Getenv("EF_STREAM_SHUTDOWN_HOME"))
	serverFlag, formatFlag = os.Getenv("EF_STREAM_SHUTDOWN_SERVER"), "json"
	command := &cobra.Command{}
	command.Flags().String("cursor", "", "")
	command.Flags().Bool("once", false, "")
	if err := streamCmd.RunE(command, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestStreamShutdownDrainsReceivedPacketBeforeExit(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(sig.String(), func(t *testing.T) {
			packet, err := json.Marshal(map[string]interface{}{
				"type": "pm_push", "data": map[string]interface{}{
					"messages": []interface{}{}, "next_cursor": "42", "padding": strings.Repeat("x", 2<<20),
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
				conn, err := upgrader.Upgrade(out, req, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				if err := conn.WriteMessage(websocket.TextMessage, packet); err != nil {
					return
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			cfg, name := runtimeTestConfig(t, server.URL, true)
			if err := cfg.UpdateServer(name, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")); err != nil {
				t.Fatal(err)
			}
			cache.SaveProfile(name, &cache.Profile{AgentID: "agent-1"})
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStreamShutdownProcess$")
			child.Env = append(os.Environ(), "EF_STREAM_SHUTDOWN_PROCESS=1", "EF_STREAM_SHUTDOWN_HOME="+config.HomeDir(), "EF_STREAM_SHUTDOWN_SERVER="+name)
			var diagnostics bytes.Buffer
			child.Stdout, child.Stderr = writer, &diagnostics
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer child.Process.Kill()
			_ = writer.Close()
			_ = reader.SetReadDeadline(time.Now().Add(5 * time.Second))
			var first [1]byte
			if _, err := io.ReadFull(reader, first[:]); err != nil {
				t.Fatalf("stream did not start writing received packet: %v", err)
			}
			_ = reader.SetReadDeadline(time.Time{})
			if first[0] != '{' {
				t.Fatalf("unexpected stream output prefix: %q", first)
			}
			if err := child.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			// Exceed the old SIGINT one-second abandonment window while the
			// real reader is blocked midway through its stdout write.
			select {
			case err := <-done:
				t.Fatalf("stream exited before packet output drained: %v; %s", err, diagnostics.String())
			case <-time.After(1200 * time.Millisecond):
			}
			rest, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("graceful stream exit failed: %v; %s", err, diagnostics.String())
			}
			got := append(first[:], rest...)
			if !bytes.Equal(got, append(packet, '\n')) {
				t.Fatalf("received packet was truncated: got=%d want=%d", len(got), len(packet)+1)
			}
		})
	}
}
