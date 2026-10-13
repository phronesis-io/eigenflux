package cmd

import (
	"reflect"
	"testing"
)

func TestWatchReportsLostReadinessAndRecovery(t *testing.T) {
	w := &accountWatch{}
	var events []string
	w.emit = func(kind string, _ interface{}) error { events = append(events, kind); return nil }
	w.reportReady()
	w.wsOnline.Store(true)
	w.reportReady()
	w.runtimeOnline.Store(true)
	w.reportReady()
	w.reportReady()
	w.runtimeOnline.Store(false)
	w.reportReady()
	w.reportReady()
	if w.ready.Load() {
		t.Fatal("failed runtime lease retained readiness")
	}
	w.runtimeOnline.Store(true)
	w.reportReady()
	w.wsOnline.Store(false)
	w.reportReady()
	if w.ready.Load() {
		t.Fatal("disconnected PM socket retained readiness")
	}
	w.wsOnline.Store(true)
	w.reportReady()
	want := []string{"runtime_ready", "runtime_unavailable", "runtime_ready", "runtime_unavailable", "runtime_ready"}
	if !reflect.DeepEqual(events, want) || !w.ready.Load() {
		t.Fatalf("transitions=%v want=%v ready=%v", events, want, w.ready.Load())
	}
}
