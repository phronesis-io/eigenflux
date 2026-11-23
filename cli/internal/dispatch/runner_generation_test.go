//go:build !windows

package dispatch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func generationFixture(t *testing.T, script string) Binding {
	t.Helper()
	b := fixtureBinding(t, "native", "")
	b.Host = "codex"
	b.Args = []string{"--sandbox", "workspace-write", "--ask-for-approval", "never", "-m", "bound-model"}
	exe := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nset -eu\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b.Command = []string{exe}
	return b
}

const generationFixtureOutput = `printf '%s\n' '{"type":"thread.started","thread_id":"01a0d200-9d41-7cd2-9214-3f04761910c4"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","text":"private tool output"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"decision"}}'
printf '%s\n' '{"type":"turn.completed"}'`

func TestGenerationTracksChildAndFinalResult(t *testing.T) {
	b := generationFixture(t, `
test "$1" != '--help' || { echo '  --no-daemon'; exit 0; }
test "$1:$2" != 'exec:--help' || { echo ' --ignore-user-config --ignore-rules --add-dir'; exit 0; }
echo "$@" > args
cat > prompt
`+generationFixtureOutput)
	if !SupportsGenerationTracking(context.Background(), b) {
		t.Fatal("supported native fixture was not admitted")
	}
	var evidence []GenerationRunEvidence
	ctx := WithGenerationObserver(context.Background(), func(e GenerationRunEvidence) error {
		if len(evidence) == 0 {
			gone, err := GenerationProcessGone(e.PID)
			if gone || err != nil {
				t.Errorf("group during start: %v, %v", gone, err)
			}
			if data, err := os.ReadFile(filepath.Join(b.WorkDir, "prompt")); err != nil && !errors.Is(err, os.ErrNotExist) || len(data) != 0 {
				t.Error("prompt released before start evidence")
			}
		}
		evidence = append(evidence, e)
		return nil
	})
	result, err := (Runner{Binding: b}).Run(ctx, Request{Kind: "commission_fulfillment", Prompt: "input"})
	if err != nil || result.Text != "decision" || len(evidence) != 3 {
		t.Fatalf("result=%v evidence=%v err=%v", result, evidence, err)
	}
	if evidence[0].SessionID != "" || evidence[1].SessionID != noticeSession || evidence[1].Result != nil || *evidence[2].Result != result {
		t.Fatalf("wrong evidence transitions: %v", evidence)
	}
	if gone, err := GenerationProcessGone(evidence[0].PID); err != nil || !gone {
		t.Fatalf("group after Wait: %v %v", gone, err)
	}
	args, err := os.ReadFile(filepath.Join(b.WorkDir, "args"))
	if err != nil || !strings.Contains(string(args), "--no-daemon exec --json") || !strings.Contains(string(args), "--sandbox workspace-write") {
		t.Fatalf("generation args changed: %q %v", args, err)
	}
}

func TestGenerationNeverContinuesWithoutDurableEvidence(t *testing.T) {
	for _, phase := range []string{"start", "thread"} {
		t.Run(phase, func(t *testing.T) {
			b := generationFixture(t, `cat > prompt
printf '%s\n' '{"type":"thread.started","thread_id":"01a0d200-9d41-7cd2-9214-3f04761910c4"}'
exec sleep 10`)
			storageErr := errors.New("journal unavailable")
			var pid int
			ctx := WithGenerationObserver(context.Background(), func(e GenerationRunEvidence) error {
				pid = e.PID
				if phase == "start" || e.SessionID != "" {
					return storageErr
				}
				return nil
			})
			start := time.Now()
			_, err := (Runner{Binding: b}).Run(ctx, Request{Kind: "commission_fulfillment", Prompt: "private prompt"})
			if !errors.Is(err, storageErr) || time.Since(start) > 4*time.Second || pid == 0 {
				t.Fatalf("did not stop on incomplete journal: %v", err)
			}
			if gone, err := GenerationProcessGone(pid); err != nil || !gone {
				t.Fatalf("generation group survived: %v %v", gone, err)
			}
			if phase == "start" {
				data, err := os.ReadFile(filepath.Join(b.WorkDir, "prompt"))
				if err != nil && !errors.Is(err, os.ErrNotExist) || len(data) > 0 {
					t.Fatal("failed start released the prompt")
				}
			}
		})
	}
}

func TestGenerationFailureHasNoFinalReceipt(t *testing.T) {
	b := generationFixture(t, "cat >/dev/null\n"+generationFixtureOutput+"\nexit 1")
	var last GenerationRunEvidence
	ctx := WithGenerationObserver(context.Background(), func(e GenerationRunEvidence) error { last = e; return nil })
	_, err := (Runner{Binding: b}).Run(ctx, Request{Kind: "commission_fulfillment", Prompt: "input"})
	if err == nil || last.SessionID != noticeSession || last.Result != nil {
		t.Fatalf("wrong failure evidence %+v %v", last, err)
	}
}

func TestGenerationVerifierScopeAndCompatibility(t *testing.T) {
	b := generationFixture(t, `if [ "$1" = "--help" ]; then echo legacy; exit 0; fi
cat >/dev/null
`+generationFixtureOutput)
	if SupportsGenerationTracking(context.Background(), b) {
		t.Fatal("legacy Codex admitted")
	}
	if r, err := (Runner{Binding: b}).Run(context.Background(), Request{Kind: "commission_fulfillment"}); err != nil || r.Text != "decision" {
		t.Fatalf("legacy generation failed: %v", err)
	}
	args := localGenerationArgs(b.Args, Request{GenerationVerifyOnly: true})
	if strings.Contains(strings.Join(args, " "), "workspace-write") || !reflect.DeepEqual(args[:5], []string{"--no-daemon", "--ask-for-approval", "never", "-m", "bound-model"}) || !strings.Contains(strings.Join(args, " "), "--sandbox read-only") {
		t.Fatalf("invalid verifier mode: %v", args)
	}
	verify := Request{Kind: "commission_fulfillment", GenerationVerifyOnly: true}
	for _, mutate := range []func(*Binding, *Request){func(b *Binding, _ *Request) { b.Mode = "acp" }, func(b *Binding, _ *Request) { b.Args = []string{"--remote", "ws://elsewhere"} }, func(b *Binding, _ *Request) { b.Command = append(b.Command, "wrapper") }, func(_ *Binding, r *Request) { r.SessionID = "old" }} {
		changed, req := b, verify
		mutate(&changed, &req)
		if _, err := (Runner{Binding: changed}).Run(context.Background(), req); err == nil {
			t.Fatal("unsupported verifier invocation executed")
		}
	}
}

func TestGenerationLocalOnlyScope(t *testing.T) {
	b := generationFixture(t, `cat >/dev/null
pwd > "$EF_FIXTURE_DIR/cwd"
printf '%s\n' "$@" > "$EF_FIXTURE_DIR/args"
`+generationFixtureOutput)
	b.Env["EF_FIXTURE_DIR"] = t.TempDir()
	dir, err := os.MkdirTemp(b.WorkDir, ".eigenflux-fulfillment-order-")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Kind: "commission_fulfillment", Prompt: "input", GenerationLocalDirectory: dir}
	ctx := WithGenerationObserver(context.Background(), func(GenerationRunEvidence) error { return nil })
	if !AllowsLocalGeneration(b) {
		t.Fatal("explicit workspace-write binding was rejected")
	}
	if _, err := (Runner{Binding: b}).Run(ctx, req); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(b.Env["EF_FIXTURE_DIR"], "args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"--add-dir\n" + dir + "\n", "--sandbox\nworkspace-write\n", "--ask-for-approval\nnever\n", "sandbox_workspace_write.network_access=false", "web_search=\"disabled\"", "features.plugins=false", "features.hooks=false", "features.apps=false", "mcp_servers={}", "--ignore-user-config", "--ignore-rules"} {
		if !strings.Contains(string(args), expected) {
			t.Fatalf("local-only args lack %q", expected)
		}
	}
	cwd, err := os.ReadFile(filepath.Join(b.Env["EF_FIXTURE_DIR"], "cwd"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(cwd)) == b.WorkDir || filepath.Base(strings.TrimSpace(string(cwd))) == "." {
		t.Fatal("verifier inherited project cwd")
	}
	if _, err := os.Stat(strings.TrimSpace(string(cwd))); !os.IsNotExist(err) {
		t.Fatalf("temporary cwd still present: %v", err)
	}
	for _, dir := range []string{b.WorkDir, t.TempDir(), filepath.Join(b.WorkDir, ".eigenflux-fulfillment-missing")} {
		changed := req
		changed.GenerationLocalDirectory = dir
		if _, err := (Runner{Binding: b}).Run(ctx, changed); err == nil {
			t.Fatalf("accepted invalid output directory %q", dir)
		}
	}
	// An explicitly unrestricted binding may be narrowed on this retry.
	broader := b
	broader.Args = []string{"--sandbox", "danger-full-access"}
	if !AllowsLocalGeneration(broader) {
		t.Fatal("refused to narrow an explicitly writable binding")
	}
	if got := strings.Join(localGenerationArgs(broader.Args, req), " "); strings.Contains(got, "danger-full-access") || !strings.Contains(got, "--sandbox workspace-write") {
		t.Fatalf("unexpected sandbox for recovery: %s", got)
	}
	for _, args := range [][]string{nil, {"--sandbox", "read-only"}, {"--sandbox", "workspace-write", "--sandbox", "read-only"}} {
		changed := b
		changed.Args = args
		if AllowsLocalGeneration(changed) {
			t.Fatalf("widened binding %v", args)
		}
		if _, err := (Runner{Binding: changed}).Run(ctx, req); err == nil {
			t.Fatal("local generation widened configured permissions")
		}
	}
}

// The outer test kills only its own fixture watch. Its no-daemon agent survives
// that crash until the test explicitly lets it exit.
func TestGenerationWatchCrashKeepsOriginalGroupVisible(t *testing.T) {
	const watchRoot = "EF_GENERATION_CRASH_FIXTURE"
	if dir := os.Getenv(watchRoot); dir != "" {
		b := Binding{Host: "codex", Mode: "native", Command: []string{filepath.Join(dir, "codex")}, WorkDir: dir, TimeoutSeconds: 10}
		ctx := WithGenerationObserver(context.Background(), func(e GenerationRunEvidence) error {
			if e.SessionID != "" {
				return os.WriteFile(filepath.Join(dir, "thread"), []byte(strconv.Itoa(e.PID)), 0600)
			}
			return nil
		})
		_, _ = (Runner{Binding: b}).Run(ctx, Request{Kind: "commission_fulfillment", Prompt: "fixture"})
		return
	}
	dir := t.TempDir()
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"01a0d200-9d41-7cd2-9214-3f04761910c4"}'
i=0
while [ ! -e release ] && [ "$i" -lt 100 ]; do sleep 0.05; i=$((i+1)); done
: > finished
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	watch := exec.Command(exe, "-test.run=^TestGenerationWatchCrashKeepsOriginalGroupVisible$")
	watch.Env = append(os.Environ(), watchRoot+"="+dir)
	if err := watch.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if watch.ProcessState == nil {
			_ = watch.Process.Kill()
			_ = watch.Wait()
		}
	}()
	var pid int
	for until := time.Now().Add(4 * time.Second); time.Now().Before(until); {
		data, _ := os.ReadFile(filepath.Join(dir, "thread"))
		pid, _ = strconv.Atoi(string(data))
		if pid > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 1 {
		t.Fatal("no early thread/PID receipt")
	}
	if err := watch.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = watch.Wait()
	if gone, err := GenerationProcessGone(pid); err != nil || gone {
		t.Fatalf("orphaned worker misclassified as finished: %v %v", gone, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Container PID 1 may not reap an orphan immediately. In that case the group
	// remains visible and recovery conservatively waits. A normal reaped group is
	// covered by TestGenerationTracksChildAndFinalResult.
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		if _, err := os.Stat(filepath.Join(dir, "finished")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("orphan did not finish its original invocation")
}
