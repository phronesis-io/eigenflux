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
	if !ok || strings.Contains(entry.Message.Body, "private diagnostic") || strings.Contains(entry.Message.Body, "incoming") {
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
