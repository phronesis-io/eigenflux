package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchHandoffDrainsDestructivePMPullBeforeRestart(t *testing.T) {
	for _, journal := range []bool{true, false} {
		t.Run(strconv.FormatBool(journal), func(t *testing.T) {
			f := newDispatchWatchFixture(t, "")
			w := f.watch
			if !journal {
				w.journal = nil
			}
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
				// Fetch has already marked this message read on the server.
				close(entered)
				select {
				case <-finish:
				case <-r.Context().Done():
					t.Error("update cancelled pull before local intake")
					return
				}
				out.Header().Set("Content-Type", "application/json")
				batch := strings.Replace(dispatchTestMessages, `"next_cursor":""`, `"next_cursor":"next-page"`, 1)
				_, _ = io.WriteString(out, `{"code":0,"data":`+batch+`}`)
			}))
			defer func() { release(); server.Close() }()
			w.server.Endpoint = server.URL
			w.handoff = newWatchHandoff()
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
				t.Fatal("handoff completed before destructive pull was persisted")
			default:
			}
			// Standalone adoption, or a supervised host after restart_required,
			// cancels the old watch only when the handoff drain completes.
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
			select {
			case <-w.handoff.done:
			default:
				t.Fatal("handoff did not complete after intake")
			}
			if !delivered.Load() || pulls.Load() != 1 {
				t.Fatal("pull was lost or pagination continued after update")
			}
			if journal {
				jobs := w.journal.Snapshot()
				if len(jobs) != 1 || jobs[0].Status != "pending" || jobs[0].Message.ID != "incoming-1" {
					t.Fatalf("message not retained for replacement watch: %+v", jobs)
				}
			}
		})
	}
}

func TestWatchHandoffReleasesFailedPMPull(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	w := f.watch
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		w.handoff.request("1.0.1")
		_, _ = io.WriteString(out, `{"code":0,"data":{"messages":`)
	}))
	defer server.Close()
	w.server.Endpoint = server.URL
	w.handoff = newWatchHandoff()
	if err := w.pollPM(context.Background()); err == nil {
		t.Fatal("malformed pull succeeded")
	}
	select {
	case <-w.handoff.done:
	default:
		t.Fatal("failed pull left handoff blocked")
	}
}
