package skills

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRetainedSkillsDiagnostics(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	path := filepath.Join(opts.Into, "ef-broadcast", "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nversion: 0.1.0-local\n---\nuser change"), 0600); err != nil {
		t.Fatal(err)
	}
	src := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "---\nversion: 0.2.0\n---\nnext"}})
	server, _ := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast"}, 101)
	opts.CDNBase = server.URL
	opts.Quiet = true
	res, err := Sync(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diagnostics) != 1 {
		t.Fatalf("diagnostics missing: %+v", res)
	}
	d := res.Diagnostics[0]
	if d.Path != filepath.Join(res.SkillsDir, "ef-broadcast") || d.LocalVersion != "0.1.0-local" || d.TargetVersion != "0.2.0" || !strings.Contains(d.Hint, "Back up") || !strings.Contains(d.Hint, "--into") {
		t.Fatalf("incomplete diagnostic: %+v", d)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "user change") {
		t.Fatal("local edits overwritten")
	}
}

func TestSyncIncompatibleReleaseDiagnostics(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	state, err := readSyncSnapshot(opts.Into)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(opts.Into, "ef-broadcast", "SKILL.md"))
	remote := *state.manifest
	remote.MinCLIVersion = "99.0.0"
	remote.Revision = "target-revision"
	remote.Sequence++
	res, err := syncDecision(opts, opts.Into, state, &remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diagnostics) != 1 {
		t.Fatalf("missing compatibility diagnostic: %+v", res)
	}
	d := res.Diagnostics[0]
	if d.Path != opts.Into || d.LocalRevision != state.manifest.Revision || d.TargetRevision != remote.Revision || !strings.Contains(d.Reason, "99.0.0") || !strings.Contains(d.Hint, "Upgrade the CLI") {
		t.Fatalf("incomplete compatibility diagnostic: %+v", d)
	}
	after, _ := os.ReadFile(filepath.Join(opts.Into, "ef-broadcast", "SKILL.md"))
	if string(before) != string(after) {
		t.Fatal("incompatible release replaced local skills")
	}
}

func TestSyncManifestFailureRetainsDiagnostic(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("quiet=%t", quiet), func(t *testing.T) {
			opts, installed, _ := installedSyncFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			opts.CDNBase, opts.Quiet, opts.IfStale = server.URL, quiet, true
			file := filepath.Join(opts.Into, "ef-broadcast", "SKILL.md")
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Sync(opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Source != "local" || !res.NoNetwork || res.Atomic || len(res.Diagnostics) != 1 {
				t.Fatalf("unexpected fallback: %+v", res)
			}
			d := res.Diagnostics[0]
			if d.Path != res.SkillsDir || d.LocalRevision != installed.Revision || !strings.Contains(d.Reason, "remote manifest unavailable") || !strings.Contains(d.Reason, "503") || !strings.Contains(d.Hint, "skills sync --into") {
				t.Fatalf("incomplete fallback diagnostic: %+v", d)
			}
			after, err := os.ReadFile(file)
			if err != nil || string(before) != string(after) {
				t.Fatalf("local installation changed: %v", err)
			}
		})
	}
}

func TestSyncSameReleaseReportsLocalModification(t *testing.T) {
	opts, manifest, _ := installedSyncFixture(t)
	file := filepath.Join(opts.Into, "ef-broadcast", "SKILL.md")
	if err := os.WriteFile(file, []byte("user customization"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Sync(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Preserved) != 1 || len(res.Diagnostics) != 1 || res.VerifiedManifest {
		t.Fatalf("same-release modification hidden: %+v", res)
	}
	local, err := ReadLocalManifest(opts.Into)
	if err != nil || local.Sequence != manifest.Sequence || local.Revision != manifest.Revision {
		t.Fatalf("unexpected release change: %+v, %v", local, err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "user customization" {
		t.Fatal("local modification overwritten")
	}
}
