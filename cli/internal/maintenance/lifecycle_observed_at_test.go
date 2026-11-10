package maintenance

import (
	"strings"
	"testing"
	"time"
)

func TestLifecycleObservedAtRetainsOriginalTimeAfterUploadAndRetry(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	attempt := strings.Repeat("a", 64)
	if err := RecordLifecycle(scope, attempt, "trigger", "observed", RuntimeObservation{Product: "codex"}); err != nil {
		t.Fatal(err)
	}
	at, err := LifecycleObservedAt(scope, attempt, "trigger", "observed")
	if err != nil || at <= 0 || at > time.Now().UnixMilli() {
		t.Fatalf("missing original timestamp: %d %v", at, err)
	}
	if err := Flush(scope, time.Now(), func(events []Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := RecordLifecycle(scope, attempt, "trigger", "observed", RuntimeObservation{Product: "codex"}); err != nil {
		t.Fatal(err)
	}
	again, err := LifecycleObservedAt(scope, attempt, "trigger", "observed")
	if err != nil || again != at {
		t.Fatalf("upload or duplicate refreshed trigger: before=%d after=%d err=%v", at, again, err)
	}
	observed, err := LifecycleObserved(scope, attempt, "trigger", "observed")
	if err != nil || !observed {
		t.Fatalf("legacy boolean lookup changed: %v %v", observed, err)
	}
}

func TestLifecycleObservedAtMissingAndOtherAccountDoNotInventTime(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	attempt := strings.Repeat("b", 64)
	if at, err := LifecycleObservedAt(scope, attempt, "trigger", "observed"); err != nil || at != 0 {
		t.Fatalf("missing trigger invented: %d %v", at, err)
	}
	if err := RecordLifecycle(scope, attempt, "trigger", "observed", RuntimeObservation{}); err != nil {
		t.Fatal(err)
	}
	scope.AgentID = "2"
	if at, err := LifecycleObservedAt(scope, attempt, "trigger", "observed"); err != nil || at != 0 {
		t.Fatalf("trigger crossed accounts: %d %v", at, err)
	}
	if at, err := LifecycleObservedAt(scope, "invalid", "trigger", "observed"); err == nil || at != 0 {
		t.Fatal("invalid identity accepted")
	}
}
