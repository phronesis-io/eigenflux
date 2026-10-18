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

type streamShutdownFixture struct {
	child        *exec.Cmd
	reader       *os.File
	packet       []byte
	first        [1]byte
	done         <-chan error
	diagnostics  *bytes.Buffer
	disconnected <-chan struct{}
}

func startStreamShutdown(t *testing.T) streamShutdownFixture {
	t.Helper()
	packet, err := json.Marshal(map[string]interface{}{
		"type": "pm_push", "data": map[string]interface{}{
			"messages": []interface{}{}, "next_cursor": "42", "padding": strings.Repeat("x", 2<<20),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	disconnected := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(out, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(disconnected)
		if err := conn.WriteMessage(websocket.TextMessage, packet); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	cfg, name := runtimeTestConfig(t, server.URL, true)
	if err := cfg.UpdateServer(name, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")); err != nil {
		t.Fatal(err)
	}
	cache.SaveProfile(name, &cache.Profile{AgentID: "agent-1"})
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	t.Cleanup(func() { _ = writer.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStreamShutdownProcess$")
	child.Env = append(os.Environ(), "EF_STREAM_SHUTDOWN_PROCESS=1", "EF_STREAM_SHUTDOWN_HOME="+config.HomeDir(), "EF_STREAM_SHUTDOWN_SERVER="+name)
	var diagnostics bytes.Buffer
	child.Stdout, child.Stderr = writer, &diagnostics
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	_ = writer.Close()
	f := streamShutdownFixture{child: child, reader: reader, packet: packet, diagnostics: &diagnostics, disconnected: disconnected}
	_ = reader.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(reader, f.first[:]); err != nil {
		t.Fatalf("stream did not start writing received packet: %v", err)
	}
	_ = reader.SetReadDeadline(time.Time{})
	if f.first[0] != '{' {
		t.Fatalf("unexpected stream output prefix: %q", f.first)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	f.done = done
	return f
}

func TestStreamShutdownDrainsReceivedPacketBeforeExit(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(sig.String(), func(t *testing.T) {
			f := startStreamShutdown(t)
			if err := f.child.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			// The real socket reader remains blocked in its stdout write until
			// the consumer resumes, beyond the original one-second grace.
			select {
			case err := <-f.done:
				t.Fatalf("stream exited before packet output drained: %v; %s", err, f.diagnostics.String())
			case <-time.After(1200 * time.Millisecond):
			}
			rest, err := io.ReadAll(f.reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-f.done; err != nil {
				t.Fatalf("graceful stream exit failed: %v; %s", err, f.diagnostics.String())
			}
			got := append(f.first[:], rest...)
			if !bytes.Equal(got, append(f.packet, '\n')) {
				t.Fatalf("received packet was truncated: got=%d want=%d", len(got), len(f.packet)+1)
			}
		})
	}
}

func TestStreamShutdownTerminatesWhenOutputConsumerStalls(t *testing.T) {
	for _, tc := range []struct {
		name    string
		second  os.Signal
		message string
	}{
		{"drain timeout", nil, "timed out draining received packet output"},
		{"second TERM", syscall.SIGTERM, "interrupted before received packet output drained"},
		{"second INT", os.Interrupt, "interrupted before received packet output drained"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startStreamShutdown(t)
			if err := f.child.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			wait := 5 * time.Second
			if tc.second != nil {
				// Socket closure proves the first signal reached shutdown before
				// sending a second signal; stdout stays permanently unread.
				select {
				case <-f.disconnected:
				case <-time.After(5 * time.Second):
					t.Fatal("first signal did not close the stream socket")
				}
				if err := f.child.Process.Signal(tc.second); err != nil {
					t.Fatal(err)
				}
				wait = 1500 * time.Millisecond
			}
			select {
			case err := <-f.done:
				if err == nil || !strings.Contains(f.diagnostics.String(), tc.message) {
					t.Fatalf("expected explicit nonzero shutdown failure, got %v; %s", err, f.diagnostics.String())
				}
			case <-time.After(wait):
				t.Fatal("stalled output prevented bounded stream termination")
			}
		})
	}
}
