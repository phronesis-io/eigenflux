package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestWatchHandoffDrainsDestructivePMPullBeforeRestart(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	release := func() { finishOnce.Do(func() { close(finish) }) }
	defer release()
	var pulls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if pulls.Add(1) != 1 {
			t.Error("started another destructive pull after admission closed")
			return
		}
		// The real fetch API marks the row read before writing the response.
		close(entered)
		select {
		case <-finish:
		case <-r.Context().Done():
			t.Error("update cancelled pull before local intake")
			return
		}
		out.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(out, `{"code":0,"data":{"messages":[{"msg_id":"incoming-1","conv_id":"conversation","sender_id":"peer","receiver_id":"agent-1","content":"preserve me","created_at":%d}],"next_cursor":"next-page"}}`, time.Now().UnixMilli())
	}))
	defer func() { release(); server.Close() }()
	cfg, name := watchTestConfig(t, server.URL, true)
	srv, _ := cfg.GetActive(name)
	identity, _ := auth.LoadV2Credentials(name)
	cache.SaveProfile(name, &cache.Profile{AgentID: identity.AgentID})
	w := &accountWatch{server: *srv, identity: *identity, handoff: newWatchHandoff()}
	var delivered atomic.Bool
	w.emit = func(kind string, _ interface{}) error {
		if kind == "pm_push" {
			delivered.Store(true)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.pollPM(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("PM pull did not start")
	}
	w.handoff.request("1.0.1")
	select {
	case <-w.handoff.done:
		t.Fatal("handoff completed before destructive pull intake")
	default:
	}
	go func() {
		select {
		case <-w.handoff.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PM pull did not drain")
	}
	if !delivered.Load() || pulls.Load() != 1 {
		t.Fatal("pull was lost or pagination continued after update")
	}
	paths, err := filepath.Glob(filepath.Join(cache.ServerDataDir(name), "messages", "*", "agent-peer.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("message cache missing: %v %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var messages []cache.CachedMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MsgID != "incoming-1" {
		t.Fatalf("message did not survive in account cache: %+v", messages)
	}
}

type watchUpdateEvents struct{ events chan watchEvent }

func (out watchUpdateEvents) Write(data []byte) (int, error) {
	var event watchEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	select {
	case out.events <- event:
	default:
	}
	return len(data), nil
}

type blockingPMWriter struct {
	watchUpdateEvents
	entered chan struct{}
	finish  chan struct{}
}

func (out blockingPMWriter) Write(data []byte) (int, error) {
	var event watchEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	if event.Type == "pm_push" {
		close(out.entered)
		<-out.finish
	}
	return out.watchUpdateEvents.Write(data)
}

func TestWatchHandoffWaitsForPMStdoutWrite(t *testing.T) {
	t.Setenv(watchSupervisedEnv, "")
	cfg, name := watchTestConfig(t, "http://127.0.0.1:1", true)
	srv, _ := cfg.GetActive(name)
	identity, _ := auth.LoadV2Credentials(name)
	w := &accountWatch{home: config.HomeDir(), server: *srv, identity: *identity, scope: "test", wake: make(chan struct{}, 1)}
	out := blockingPMWriter{watchUpdateEvents: watchUpdateEvents{events: make(chan watchEvent, 64)}, entered: make(chan struct{}), finish: make(chan struct{})}
	var finishOnce sync.Once
	release := func() { finishOnce.Do(func() { close(out.finish) }) }
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.run(ctx, out) }()
	select {
	case <-out.events:
	case <-ctx.Done():
		t.Fatal("watch did not start")
	}
	if !w.handoff.begin() {
		t.Fatal("pull admission denied")
	}
	intake := make(chan error, 1)
	go func() {
		err := w.deliverPM(ctx, "pm_push", json.RawMessage(`{"messages":[],"next_cursor":""}`), "http_poll")
		w.handoff.end()
		intake <- err
	}()
	select {
	case <-out.entered:
	case <-ctx.Done():
		t.Fatal("PM event did not reach writer")
	}
	w.handoff.request("1.0.1")
	select {
	case <-w.handoff.done:
		t.Fatal("handoff completed while PM stdout was blocked")
	default:
	}
	release()
	if err := <-intake; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != errWatchCLIUpdated {
			t.Fatalf("handoff did not adopt after stdout flush: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("handoff remained blocked after stdout flush")
	}
}

func TestWatchHandoffRuntimeStandaloneAndSupervised(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		name := "standalone"
		if supervised {
			name = "supervised"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(watchSupervisedEnv, "")
			if supervised {
				t.Setenv(watchSupervisedEnv, "1")
			}
			cfg, name := watchTestConfig(t, "http://127.0.0.1:1", true)
			srv, _ := cfg.GetActive(name)
			identity, _ := auth.LoadV2Credentials(name)
			w := &accountWatch{home: config.HomeDir(), server: *srv, identity: *identity, scope: "test", wake: make(chan struct{}, 1)}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out := watchUpdateEvents{events: make(chan watchEvent, 64)}
			done := make(chan error, 1)
			go func() { done <- w.run(ctx, out) }()
			select {
			case <-out.events:
			case <-ctx.Done():
				t.Fatal("watch did not start")
			}
			w.handoff.request("1.0.1")
			if !supervised {
				select {
				case err := <-done:
					if err != errWatchCLIUpdated {
						t.Fatalf("handoff failed: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("standalone handoff blocked")
				}
				return
			}
			for {
				select {
				case event := <-out.events:
					if event.Type != "restart_required" {
						continue
					}
					data := event.Data.(map[string]interface{})
					if data["ready"] != true || data["cli_version"] != "1.0.1" {
						t.Fatalf("invalid handoff event: %+v", event)
					}
					select {
					case err := <-done:
						t.Fatalf("supervised watch exited before host: %v", err)
					default:
					}
					cancel()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					return
				case <-ctx.Done():
					t.Fatal("supervised handoff missing")
				}
			}
		})
	}
}

func TestWatchReadinessInvalidatesAndRecovers(t *testing.T) {
	_, name := watchTestConfig(t, "http://127.0.0.1:1", true)
	w := &accountWatch{home: config.HomeDir()}
	w.server.Name = name
	var events []string
	w.emit = func(kind string, _ interface{}) error { events = append(events, kind); return nil }
	w.wsOnline.Store(true)
	w.runtimeOnline.Store(true)
	w.reportReady()
	w.runtimeOnline.Store(false)
	w.reportReady()
	w.reportReady()
	w.runtimeOnline.Store(true)
	w.reportReady()
	if strings.Join(events, ",") != "runtime_ready,runtime_unavailable,runtime_ready" {
		t.Fatalf("readiness history wrong: %v", events)
	}
}

func TestWatchReadyCorrelatesOnlyProvenAccountUpgrade(t *testing.T) {
	for _, tc := range []string{"matching", "other-version", "other-principal", "other-agent", "no-proof"} {
		t.Run(tc, func(t *testing.T) {
			cfg, name := watchTestConfig(t, "http://127.0.0.1:1", true)
			server, _ := cfg.GetActive(name)
			identity, _ := auth.LoadV2Credentials(name)
			identity.PrincipalID = "principal-1"
			if err := auth.SaveV2Credentials(name, identity); err != nil {
				t.Fatal(err)
			}
			w := &accountWatch{home: config.HomeDir(), server: *server, identity: *identity}
			w.emit = func(string, interface{}) error { return nil }
			proof := maintenance.NewEvent(maintenance.NewID(), "cli", "manual", "install", "installed")
			proof.FromVersion, proof.ToVersion = "0.6.6", version
			proofScope, principal := w.maintenanceScope(), identity.PrincipalID
			switch tc {
			case "other-version":
				proof.ToVersion = "9.9.9"
			case "other-principal":
				principal = "principal-2"
			case "other-agent":
				proofScope.AgentID = "agent-2"
			}
			if tc != "no-proof" {
				if err := maintenance.RememberCLIAdoption(proofScope, principal, proof); err != nil {
					t.Fatal(err)
				}
			}
			w.wsOnline.Store(true)
			w.runtimeOnline.Store(true)
			w.reportReady()
			state, err := maintenance.Snapshot(w.maintenanceScope())
			if err != nil {
				t.Fatal(err)
			}
			if tc != "matching" {
				if len(state.Events) != 0 {
					t.Fatalf("unproven runtime attributed to an upgrade: %+v", state.Events)
				}
				return
			}
			if len(state.Events) != 1 {
				t.Fatalf("upgrade adoption observations: %+v", state.Events)
			}
			event := state.Events[0]
			if event.AttemptID != proof.AttemptID || event.Result != "runtime_ready" || event.RunningVersion != version || event.FromVersion != proof.FromVersion {
				t.Fatalf("runtime adoption lost installation proof: %+v", event)
			}
			// A reconnect must not create another attempt or readiness receipt.
			w.runtimeOnline.Store(false)
			w.reportReady()
			w.runtimeOnline.Store(true)
			w.reportReady()
			state, err = maintenance.Snapshot(w.maintenanceScope())
			if err != nil || len(state.Events) != 1 {
				t.Fatalf("duplicate adoption: %+v %v", state, err)
			}
		})
	}
}
