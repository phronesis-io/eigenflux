package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
)

func TestWatchHandoffDrainsAllAdmittedLanes(t *testing.T) {
	h := newWatchHandoff()
	for range 3 {
		if !h.begin() {
			t.Fatal("initial admission denied")
		}
	}
	h.request("1.0.1")
	if h.begin() {
		t.Fatal("admitted a new task during handoff")
	}
	for range 2 {
		h.end()
		select {
		case <-h.done:
			t.Fatal("handoff before every existing task completed")
		default:
		}
	}
	h.end()
	select {
	case <-h.done:
	default:
		t.Fatal("idle handoff not released")
	}
	h.request("1.0.2")
	if h.version() != "1.0.1" {
		t.Fatal("handoff target mutated")
	}
	idle := newWatchHandoff()
	idle.request("1.0.1")
	select {
	case <-idle.done:
	default:
		t.Fatal("idle handoff blocked")
	}
}

func TestWatchHandoffPreservesPendingPMAndCompletesCurrentReply(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	w := f.watch
	w.handoff = newWatchHandoff()
	entered, finish := make(chan struct{}), make(chan struct{})
	w.runAgent = func(ctx context.Context, req dispatch.Request) (dispatch.Result, error) {
		close(entered)
		select {
		case <-finish:
			return dispatchTestDecision(req, "reply"), nil
		case <-ctx.Done():
			return dispatch.Result{}, ctx.Err()
		}
	}
	if err := w.deliverPM(context.Background(), "pm_push", json.RawMessage(dispatchTestMessages), "socket"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.dispatchLoop(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("task did not start")
	}
	w.handoff.request("1.0.1")
	second := strings.ReplaceAll(dispatchTestMessages, "incoming-1", "incoming-2")
	if err := w.deliverPM(ctx, "pm_push", json.RawMessage(second), "socket"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.handoff.done:
		t.Fatal("current reply interrupted")
	default:
	}
	close(finish)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("lane did not drain")
	}
	pending, replied := 0, 0
	for _, job := range w.journal.Snapshot() {
		if job.Status == "pending" {
			pending++
		}
		if job.Status == "replied" {
			replied++
		}
	}
	if pending != 1 || replied != 1 || f.sends.Load() != 1 {
		t.Fatalf("business state not preserved: pending=%d replied=%d sends=%d", pending, replied, f.sends.Load())
	}
	reopened, err := dispatch.OpenJournal(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range reopened.Snapshot() {
		if job.Status == "unknown" {
			t.Fatal("completed job recovered as interrupted")
		}
	}
}

func TestWatchHandoffClosesEveryLaneBeforeClaim(t *testing.T) {
	w := newCommissionWatch(t, "https://api.example.test", "pm_push", "commission_order")
	intake := claimCommissionIntake(t, w)
	decision := dispatch.CommissionIntakeDecision{Version: 1, RequestID: intake.ID, OrderID: "901", OrderVersion: 3, Outcome: "ready", Summary: "Inputs inspected", InspectedFiles: []string{}}
	if err := w.journal.CompleteCommissionIntakeAndQueue(intake.ID, decision, "intake", true); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.AddCommissionNotification(commissionTestNotification(82, 902, "42")); err != nil {
		t.Fatal(err)
	}
	w.handoff = newWatchHandoff()
	w.handoff.request("1.0.1")
	before := w.journal.Snapshot()
	for _, lane := range []func(context.Context) error{w.dispatchLoop, w.commissionDispatchLoop, w.commissionFulfillmentLoop} {
		if err := lane(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, w.journal.Snapshot()) {
		t.Fatal("closed admission claimed a pending task")
	}
}

func TestWatchReexecPinsIdentityAndPreservesEnvironment(t *testing.T) {
	identity := auth.V2Credentials{AgentID: "agent-1", PrincipalID: "principal-1"}
	env := watchReexecEnvironment([]string{"PATH=/usr/bin", "CUSTOM=secret value", "EIGENFLUX_HOME=old", watchAgentEnv + "=old", watchPrincipalEnv + "=old", updateReexecEnv + "=1", updateAttemptEnv + "=old", updateIdentityEnv + "=old", updateFromVersionEnv + "=old"}, "/stable home", identity)
	want := []string{"PATH=/usr/bin", "CUSTOM=secret value", "EIGENFLUX_HOME=/stable home", watchAgentEnv + "=agent-1", watchPrincipalEnv + "=principal-1"}
	if !reflect.DeepEqual(env, want) {
		t.Fatal("lost inherited environment or retained stale identity")
	}
	args := []string{"--server", "old", "watch", "--dispatch", "--desktop-notifications=false", "--homedir", "old", "--"}
	got := heartbeatReexecArgs(args, "/stable home", "pinned server")
	if !reflect.DeepEqual(got, append(args[:len(args)-1], "--homedir", "/stable home", "--server", "pinned server")) {
		t.Fatalf("arguments changed: %q", got)
	}
	t.Setenv(watchAgentEnv, identity.AgentID)
	t.Setenv(watchPrincipalEnv, identity.PrincipalID)
	if err := watchExpectedIdentity(&identity); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(watchExpectedIdentity(&auth.V2Credentials{AgentID: "different", PrincipalID: "principal-1"}), errWatchIdentity) {
		t.Fatal("replacement adopted another account")
	}
}

func TestWatchReexecProcessReleasesAccountLock(t *testing.T) {
	if os.Getenv("EF_WATCH_REEXEC_FIXTURE") != "" {
		return
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestWatchReexecProcessHelper$")
	child.Env = append(os.Environ(), "EF_WATCH_REEXEC_FIXTURE=first", "EF_WATCH_REEXEC_HOME="+dir)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("handoff failed: %v %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "adopted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		PID, OriginalPID int
		Env              string
		Args             []string
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Env != "agent" || len(result.Args) != 2 || result.Args[1] != "literal value" {
		t.Fatalf("lost identity or args: %+v", result)
	}
	if runtime.GOOS != "windows" && result.PID != result.OriginalPID {
		t.Fatal("POSIX handoff changed PID")
	}
}

func TestWatchReexecProcessHelper(t *testing.T) {
	stage := os.Getenv("EF_WATCH_REEXEC_FIXTURE")
	if stage == "" {
		return
	}
	home := os.Getenv("EF_WATCH_REEXEC_HOME")
	release, err := watchstate.Acquire(home, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if stage == "first" {
		env := watchReexecEnvironment(os.Environ(), home, auth.V2Credentials{AgentID: "agent", PrincipalID: "principal"})
		for i, v := range env {
			if strings.HasPrefix(v, "EF_WATCH_REEXEC_FIXTURE=") {
				env[i] = "EF_WATCH_REEXEC_FIXTURE=second"
			}
		}
		env = append(env, "EF_WATCH_REEXEC_PID="+strconv.Itoa(os.Getpid()))
		release()
		if err := reexecWatch(os.Args[0], []string{"-test.run=^TestWatchReexecProcessHelper$", "--", "literal value"}, env); err != nil {
			t.Fatal(err)
		}
		return
	}
	original, _ := strconv.Atoi(os.Getenv("EF_WATCH_REEXEC_PID"))
	result := struct {
		PID, OriginalPID int
		Env              string
		Args             []string
	}{os.Getpid(), original, os.Getenv(watchAgentEnv), os.Args[len(os.Args)-2:]}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(filepath.Join(home, "adopted.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWatchHandoffWaitsForPaidFulfillmentDelivery(t *testing.T) {
	order := paidCommissionTestOrder()
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order }, true)
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	queueCheckedPaidCommission(t, w)
	w.handoff = newWatchHandoff()
	entered, finish := make(chan struct{}), make(chan struct{})
	w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
		data := fulfillmentTestPayload(t, request)
		close(entered)
		select {
		case <-finish:
		case <-ctx.Done():
			return dispatch.Result{}, ctx.Err()
		}
		if err := os.WriteFile(filepath.Join(data.OutputDirectory, "report.txt"), []byte("Verified paid report."), 0600); err != nil {
			return dispatch.Result{}, err
		}
		result := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Report generated", SelfCheck: "Read the contractual report and verified its content", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
		raw, _ := json.Marshal(result)
		return dispatch.Result{Text: string(raw), SessionID: "paid-session"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.commissionFulfillmentLoop(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("paid work did not start")
	}
	w.handoff.request("1.0.1")
	select {
	case <-w.handoff.done:
		t.Fatal("paid work interrupted by update")
	default:
	}
	close(finish)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("paid work did not drain")
	}
	select {
	case <-w.handoff.done:
	default:
		t.Fatal("handoff not released after delivery")
	}
	jobs, err := dispatch.ReadJournalStatus(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := false
	for _, job := range jobs {
		if job.Kind == "commission_fulfillment" {
			confirmed = job.Status == "completed" && job.Code == "commission_delivery_confirmed" && job.CommissionFulfillment != nil
		}
	}
	if !confirmed {
		t.Fatalf("handoff lost paid delivery receipt: %+v", jobs)
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

func TestWatchHandoffRuntimeStandaloneAndSupervised(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		t.Run(strconv.FormatBool(supervised), func(t *testing.T) {
			if supervised {
				t.Setenv(watchSupervisedEnv, "1")
			} else {
				t.Setenv(watchSupervisedEnv, "")
			}
			f := newDispatchWatchFixture(t, "")
			w := f.watch
			entered, finish := make(chan struct{}), make(chan struct{})
			w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
				close(entered)
				select {
				case <-finish:
					return dispatchTestDecision(request, "no_reply"), nil
				case <-ctx.Done():
					return dispatch.Result{}, ctx.Err()
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out := watchUpdateEvents{events: make(chan watchEvent, 64)}
			done := make(chan error, 1)
			go func() { done <- w.run(ctx, out) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("watch did not dispatch")
			}
			w.handoff.request("1.0.1")
			select {
			case err := <-done:
				t.Fatalf("watch interrupted active work: %v", err)
			default:
			}
			close(finish)
			if !supervised {
				select {
				case err := <-done:
					if !errors.Is(err, errWatchCLIUpdated) {
						t.Fatalf("missing adoption: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("standalone handoff blocked")
				}
				for len(out.events) > 0 {
					if event := <-out.events; event.Type == "restart_required" {
						t.Fatal("standalone raced a host supervisor")
					}
				}
				return
			}
			ready := false
			for !ready {
				select {
				case event := <-out.events:
					if event.Type == "restart_required" {
						data := event.Data.(map[string]interface{})
						ready = data["ready"] == true && data["cli_version"] == "1.0.1"
					}
				case err := <-done:
					t.Fatalf("supervised watch exited before host: %v", err)
				case <-ctx.Done():
					t.Fatal("supervised handoff event missing")
				}
			}
			if w.handoff.begin() {
				t.Fatal("supervised watch resumed work before host replaced it")
			}
			select {
			case err := <-done:
				t.Fatalf("supervised watch exited before host stop: %v", err)
			default:
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("host cancellation blocked")
			}
		})
	}
}
