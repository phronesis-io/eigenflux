package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GenerationRunEvidence is private to the admitted local generation attempt.
// Only the final protocol result may be a receipt; progress and tool output never are.
type GenerationRunEvidence struct {
	PID       int     `json:"pid"`
	SessionID string  `json:"session_id,omitempty"`
	Result    *Result `json:"result,omitempty"`
}

type generationObserverKey struct{}

func WithGenerationObserver(ctx context.Context, observe func(GenerationRunEvidence) error) context.Context {
	return context.WithValue(ctx, generationObserverKey{}, observe)
}

// An older Codex or a custom/remote binding still fulfills normally. Only opt in
// when we can own a fresh local execution and observe its complete process group.
func SupportsGenerationTracking(ctx context.Context, b Binding) bool {
	if !localCodexGeneration(b) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, _, err := (Runner{Binding: b}).runProcess(ctx, []string{"--help"}, "")
	if err != nil || !bytes.Contains(out, []byte("--no-daemon")) {
		return false
	}
	out, _, err = (Runner{Binding: b}).runProcess(ctx, []string{"exec", "--help"}, "")
	return err == nil && bytes.Contains(out, []byte("--ignore-user-config")) && bytes.Contains(out, []byte("--ignore-rules")) && bytes.Contains(out, []byte("--add-dir"))
}

func localCodexGeneration(b Binding) bool {
	if runtime.GOOS == "windows" || b.Mode != "native" || b.Host != "codex" || len(b.Command) != 1 || filepath.Base(b.Command[0]) != "codex" {
		return false
	}
	// Only options whose effects are understood here; custom adapters keep their existing path.
	for i := 0; i < len(b.Args); i += 2 {
		if i+1 >= len(b.Args) || strings.HasPrefix(b.Args[i+1], "-") || b.Args[i+1] == "" {
			return false
		}
		switch b.Args[i] {
		case "-m", "--model", "-s", "--sandbox", "-a", "--ask-for-approval":
		default:
			return false
		}
	}
	return true
}

// Replaying generation must not expand the original binding's write authority.
// Default profiles and custom permission arguments continue through the ordinary runner.
func AllowsLocalGeneration(b Binding) bool {
	if !localCodexGeneration(b) {
		return false
	}
	var sandbox string
	for i := 0; i < len(b.Args); i += 2 {
		if b.Args[i] == "--sandbox" || b.Args[i] == "-s" {
			sandbox = b.Args[i+1]
		}
	}
	return sandbox == "workspace-write"
}

func validLocalGenerationDirectory(b Binding, dir string) bool {
	if !validCommissionDirectory(dir) || filepath.Dir(dir) != filepath.Clean(b.WorkDir) || !strings.HasPrefix(filepath.Base(dir), ".eigenflux-fulfillment-") {
		return false
	}
	info, err := os.Lstat(dir)
	return err == nil && info.IsDir()
}

func localGenerationArgs(configured []string, req Request) []string {
	args := []string{"--no-daemon", "--ask-for-approval", "never"}
	for i := 0; i+1 < len(configured); i += 2 {
		if configured[i] == "--model" || configured[i] == "-m" {
			args = append(args, configured[i:i+2]...)
		}
	}
	args = append(args, "exec", "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules")
	if req.GenerationVerifyOnly {
		args = append(args, "--ephemeral", "--sandbox", "read-only")
	} else {
		args = append(args, "--sandbox", "workspace-write", "--add-dir", req.GenerationLocalDirectory,
			"-c", "sandbox_workspace_write.network_access=false", "-c", "sandbox_workspace_write.writable_roots=[]",
			"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true", "-c", "sandbox_workspace_write.exclude_slash_tmp=true")
	}
	args = append(args, "-c", "mcp_servers={}", "-c", "web_search=\"disabled\"")
	for _, feature := range []string{"apps", "plugins", "remote_plugin", "hooks", "multi_agent", "multi_agent_v2", "computer_use", "browser_use", "image_generation", "skill_mcp_dependency_install", "shell_snapshot"} {
		args = append(args, "-c", "features."+feature+"=false")
	}
	return append(args, "-")
}

type generationProcess struct {
	observe  func(GenerationRunEvidence) error
	evidence GenerationRunEvidence
	ready    chan struct{}
	failed   chan error
	prompt   *strings.Reader
	out      *boundedOutput
	scanned  int
}

func newGenerationProcess(observe func(GenerationRunEvidence) error) *generationProcess {
	return &generationProcess{observe: observe, ready: make(chan struct{}), failed: make(chan error, 1)}
}

func (g *generationProcess) started(pid int) error {
	g.evidence.PID = pid
	err := g.observe(g.evidence)
	if err != nil {
		g.prompt = nil
	}
	close(g.ready)
	return err
}

// Cmd.Start starts its I/O goroutines before returning. Hold the prompt and any
// early stdout until the journal has committed the process ID.
func (g *generationProcess) Read(p []byte) (int, error) {
	<-g.ready
	if g.prompt == nil {
		return 0, io.EOF
	}
	return g.prompt.Read(p)
}

func (g *generationProcess) Write(p []byte) (int, error) {
	<-g.ready
	next := len(g.out.data)
	n, err := g.out.Write(p)
	if err != nil || g.prompt == nil || g.evidence.SessionID != "" {
		return n, err
	}
	for {
		end := bytes.IndexByte(g.out.data[next:], '\n')
		if end < 0 {
			return n, nil
		}
		line := g.out.data[g.scanned : next+end]
		next += end + 1
		g.scanned = next
		var event struct {
			Type string `json:"type"`
			ID   string `json:"thread_id"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "thread.started" && event.ID != "" && len(event.ID) <= 256 && !strings.ContainsAny(event.ID, " \t\r\n") {
			g.evidence.SessionID = event.ID
			if err := g.observe(g.evidence); err != nil {
				g.failed <- err
				return n, err
			}
			return n, nil
		}
	}
}

func (g *generationProcess) completed(result Result) error {
	if result.SessionID == "" || result.SessionID != g.evidence.SessionID {
		return errors.New("generation_session_unconfirmed")
	}
	// The existing agent-result decoder admits at most maxRunnerOutput bytes.
	// A large reply can still complete normally without duplicating it in the journal.
	if len(result.Text) <= 64<<10 {
		g.evidence.Result = &result
		return g.observe(g.evidence)
	}
	return nil
}
