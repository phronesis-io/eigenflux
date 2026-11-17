package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
)

func failPMPrompt(t *testing.T, f *dispatchWatchFixture, id string) dispatch.Job {
	t.Helper()
	f.historyFailures.Store(9)
	data := strings.ReplaceAll(dispatchTestMessages, "incoming-1", id)
	if err := f.watch.deliverPM(context.Background(), "pm_push", json.RawMessage(data), "socket"); err != nil {
		t.Fatal(err)
	}
	job, ok, err := f.watch.journal.Next()
	if err != nil || !ok {
		t.Fatalf("missing job: %v", err)
	}
	if err := f.watch.dispatchJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestPMDesktopBlockerDedupAndRecovery(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	path := filepath.Join(t.TempDir(), "desktop.json")
	q, err := desktopqueue.Open(path, f.watch.scope)
	if err != nil {
		t.Fatal(err)
	}
	f.watch.desktop = q
	a := failPMPrompt(t, f, "incoming-1")
	b := failPMPrompt(t, f, "incoming-2")
	for i := 0; i < 2; i++ {
		if err := f.watch.enqueueDesktopPMBlockers(); err != nil {
			t.Fatal(err)
		}
	}
	if pending, _ := q.Counts(); pending != 1 {
		t.Fatalf("same conversation/reason notified %d times", pending)
	}
	entry, ok := q.Next(time.Now())
	if !ok || !strings.HasSuffix(entry.Message.URL, "/dashboard/messages") || strings.Contains(entry.Message.Body, "private diagnostic") || strings.Contains(entry.Message.Body, "incoming") {
		t.Fatalf("private data in alert: %+v", entry)
	}
	if err := q.Complete(entry.Message.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.watch.desktop, err = desktopqueue.Open(path, f.watch.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.watch.enqueueDesktopPMBlockers(); err != nil {
		t.Fatal(err)
	}
	if pending, delivered := f.watch.desktop.Counts(); pending != 0 || delivered != 1 {
		t.Fatalf("restart replay: %d %d", pending, delivered)
	}
	f.historyFailures.Store(0)
	f.watch.runAgent = func(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
		return dispatchTestDecision(req, "reply"), nil
	}
	for _, job := range []dispatch.Job{a, b} {
		if err := f.watch.journal.Retry(job.ID); err != nil {
			t.Fatal(err)
		}
		next, ok, err := f.watch.journal.Next()
		if err != nil || !ok {
			t.Fatalf("retry claim: %v", err)
		}
		if err := f.watch.dispatchJob(context.Background(), next); err != nil {
			t.Fatal(err)
		}
	}
	if f.watch.pmDesktopBlockerCurrent(entry.Message.ID) {
		t.Fatal("resolved alert still current")
	}
	failPMPrompt(t, f, "incoming-3")
	if err := f.watch.enqueueDesktopPMBlockers(); err != nil {
		t.Fatal(err)
	}
	if pending, delivered := f.watch.desktop.Counts(); pending != 1 || delivered != 1 {
		t.Fatalf("new failure after recovery was suppressed: %d %d", pending, delivered)
	}
}

func TestPMDesktopSkipsResolvedFailureAfterPermissionWait(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	job := failPMPrompt(t, f, "incoming-1")
	var err error
	f.watch.desktop, err = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), f.watch.scope)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	provider := &fakeDesktopProvider{setup: func() error {
		if err := f.watch.journal.Reconcile(job.ID, "no_reply", ""); err != nil {
			return err
		}
		cancel()
		return nil
	}}
	f.watch.desktopProvider = provider
	err = f.watch.desktopLoop(ctx)
	if !errors.Is(err, context.Canceled) || provider.calls != 0 {
		t.Fatalf("stale notification shown: %v calls=%d", err, provider.calls)
	}
	if pending, _ := f.watch.desktop.Counts(); pending != 0 {
		t.Fatal("resolved reminder remained pending")
	}
}

func TestPMDesktopCancelledPreparationIsSilent(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	job := dispatchTestNext(t, f.watch)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.watch.dispatchJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if state := dispatchTestStatus(t, f.watch, job.ID); state.Code != "prompt_cancelled" {
		t.Fatalf("wrong cancellation state: %+v", state)
	}
	if len(f.watch.pmDesktopBlockers()) != 0 {
		t.Fatal("normal cancellation produced an alert")
	}
}

type pmNativeDesktopProvider struct {
	notifier *desktopnotify.Notifier
	cancel   context.CancelFunc
	shown    int
}

func (p *pmNativeDesktopProvider) Setup(ctx context.Context) error { return p.notifier.Check(ctx) }
func (p *pmNativeDesktopProvider) Show(ctx context.Context, message desktopnotify.Message) error {
	err := p.notifier.Show(ctx, message)
	if err == nil {
		p.shown++
		p.cancel()
	}
	return err
}

// Sends one real OS notification using the installed helper and existing
// permission. Fixture APIs fail before any Agent invocation or real PM send.
func TestPMPromptNativeDesktopFailureNotice(t *testing.T) {
	if os.Getenv("EIGENFLUX_PM_DESKTOP_SMOKE") != "1" {
		t.Skip("set EIGENFLUX_PM_DESKTOP_SMOKE=1 for a real OS notification")
	}
	f := newDispatchWatchFixture(t, "")
	failPMPrompt(t, f, "incoming-1")
	var err error
	f.watch.desktop, err = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), f.watch.scope)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	provider := &pmNativeDesktopProvider{notifier: desktopnotify.New(), cancel: cancel}
	if err := provider.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	f.watch.desktopProvider = provider
	err = f.watch.desktopLoop(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if provider.shown != 1 {
		t.Fatalf("OS submission count: %d", provider.shown)
	}
	if pending, delivered := f.watch.desktop.Counts(); pending != 0 || delivered != 1 {
		t.Fatalf("missing durable OS receipt: %d %d", pending, delivered)
	}
	t.Log("failed PM preparation -> durable alert -> installed native helper -> OS submitted once; banner visibility not asserted")
}

func TestCodexPMOwnerNoticeDesktopFallback(t *testing.T) {
	for _, scenario := range []string{"unbound", "missing", "archived", "busy", "not-submitted", "unconfirmed", "desktop-disabled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := newDispatchWatchFixture(t, "")
			w := f.watch
			w.binding.Host = "codex"
			w.binding.MainSessionID = "bound-main"
			switch scenario {
			case "unbound", "desktop-disabled":
				w.binding.MainSessionID = ""
			case "missing":
				w.binding.Mode = "native"
				w.binding.MainSessionID = "12345678-1234-1234-1234-123456789abc"
				w.binding.Env = map[string]string{"CODEX_HOME": t.TempDir()}
			}
			if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), w.binding); err != nil {
				t.Fatal(err)
			}
			if scenario == "missing" {
				if err := w.enableDispatch(); err != nil {
					t.Fatalf("missing main thread prevented Watch from starting: %v", err)
				}
			}
			q, err := desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), w.scope)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "desktop-disabled" {
				w.desktop = q
			}
			w.runPMMainNotice = func(_ context.Context, _ string, commit func() error) (dispatch.Result, error) {
				switch scenario {
				case "archived":
					return dispatch.Result{}, dispatch.ErrMainSessionUnavailable
				case "busy":
					return dispatch.Result{}, dispatch.ErrMainNoticeBusy
				case "not-submitted", "unconfirmed":
					if err := commit(); err != nil {
						return dispatch.Result{}, err
					}
					if scenario == "not-submitted" {
						return dispatch.Result{}, dispatch.ErrMainNoticeNotSubmitted
					}
					return dispatch.Result{}, context.DeadlineExceeded
				default:
					t.Fatal("submitted to a missing main conversation")
					return dispatch.Result{}, nil
				}
			}
			job := dispatchTestNext(t, w)
			notice := dispatch.PMOwnerNotice{Topic: "topic", Change: "first", Summary: "private message text"}
			if err := dispatch.SavePMNotice(ctx, *w.binding, job, notice); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := w.deliverCodexPMOwnerNotice(ctx); err != nil {
					t.Fatal(err)
				}
			}
			count, _ := q.Counts()
			pending, err := dispatch.HasPendingPMNotices(ctx, *w.binding)
			wantDesktop := scenario == "unbound" || scenario == "missing" || scenario == "archived"
			wantPending := scenario == "busy" || scenario == "not-submitted" || scenario == "desktop-disabled"
			if err != nil || count > 1 || (count == 1) != wantDesktop || pending != wantPending {
				t.Fatalf("desktop=%d pending=%v err=%v", count, pending, err)
			}
			if wantDesktop {
				entry, ok := q.Next(time.Now())
				if !ok || strings.Contains(entry.Message.Body, notice.Summary) || !strings.HasSuffix(entry.Message.URL, "/dashboard/messages") {
					t.Fatalf("incorrect notification: %+v", entry)
				}
			}
		})
	}
}

func TestHeartbeatPMDigestDesktopResult(t *testing.T) {
	for _, showErr := range []error{nil, desktopnotify.ErrPermission, context.DeadlineExceeded} {
		label := "success"
		if showErr != nil {
			label = showErr.Error()
		}
		t.Run(label, func(t *testing.T) {
			b := dispatch.Binding{Endpoint: "https://www.eigenflux.ai", Scope: "account"}
			entries := []dispatch.PMNoticeEntry{{ID: "notice-a"}, {ID: "notice-b"}}
			shows := 0
			submitted, err := presentHeartbeatPMDigest(context.Background(), b, entries, func(_ context.Context, message desktopnotify.Message) error {
				shows++
				if !strings.HasSuffix(message.URL, "/dashboard/messages") {
					t.Fatalf("wrong notification target: %s", message.URL)
				}
				return showErr
			})
			wantErr := showErr
			if errors.Is(showErr, desktopnotify.ErrPermission) {
				wantErr = nil
			}
			if shows != 1 || submitted != (showErr == nil) || !errors.Is(err, wantErr) {
				t.Fatalf("shows=%d submitted=%v err=%v", shows, submitted, err)
			}
		})
	}
}

func TestCodexPMDesktopQueueReceiptSurvivesInterruptedHandoff(t *testing.T) {
	ctx := context.Background()
	f := newDispatchWatchFixture(t, "")
	w := f.watch
	w.binding.Host = "codex"
	w.binding.MainSessionID = "recovered-main"
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), w.binding); err != nil {
		t.Fatal(err)
	}
	job := dispatchTestNext(t, w)
	if err := dispatch.SavePMNotice(ctx, *w.binding, job, dispatch.PMOwnerNotice{Topic: "materials", Change: "ready", Summary: "Ready"}); err != nil {
		t.Fatal(err)
	}
	entry, found, err := dispatch.NextPMNotice(ctx, *w.binding, time.Now())
	if err != nil || !found {
		t.Fatalf("expected pending notice: %v", err)
	}
	path := filepath.Join(t.TempDir(), "desktop.json")
	q, err := desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	message, err := pmOwnerDesktopMessage(w.server.Endpoint, w.scope, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(message); err != nil {
		t.Fatal(err)
	}
	// Restart after desktop persistence but before acknowledging the PM spool.
	w.desktop, err = desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	w.runPMMainNotice = func(context.Context, string, func() error) (dispatch.Result, error) {
		t.Fatal("desktop receipt was ignored after the main chat became available")
		return dispatch.Result{}, nil
	}
	if err := w.deliverCodexPMOwnerNotice(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := dispatch.HasPendingPMNotices(ctx, *w.binding)
	queued, _ := w.desktop.Counts()
	if err != nil || pending || queued != 1 {
		t.Fatalf("PM pending=%v desktop queued=%d err=%v", pending, queued, err)
	}
}
