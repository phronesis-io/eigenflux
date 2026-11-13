package cmd

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/selfupdate"
)

// The old stable entry deliberately has no finalizer. The official wrapper
// must copy the new version child, then remove both the old entry and packages.
func TestUninstallVersionedPackageOfficialWrapper(t *testing.T) {
	testUninstallVersionedPackage(t, "wrapper")
}

func TestUninstallVersionedPackageReporterRetriesPartialReceipt(t *testing.T) {
	testUninstallVersionedPackage(t, "partial")
}

func TestUninstallVersionedPackageReporterRetriesWindowsLockedExecutable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows file sharing semantics")
	}
	testUninstallVersionedPackage(t, "windows_lock")
}

func testUninstallVersionedPackage(t *testing.T, scenario string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds isolated native CLIs")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	buildDir := filepath.Join(root, "build")
	_ = os.MkdirAll(buildDir, 0700)
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	name := "eigenflux"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	built := filepath.Join(buildDir, name)
	build := exec.Command("go", "build", "-ldflags", "-X main.Version=9.8.7 -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64="+base64.StdEncoding.EncodeToString(public), "-o", built, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v %s", err, out)
	}
	binary, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	stableSource := filepath.Join(buildDir, "stable.go")
	stableCode := fmt.Sprintf(`package main
import ("os"; "os/exec"; "path/filepath")
func main() {
 if len(os.Args)>1 && os.Args[1]=="uninstall-finalize" { os.Exit(99) }
 root,_:=os.Executable();root,_=filepath.EvalSymlinks(root)
 child:=filepath.Join(root+".versions","9.8.7",%q)
 cmd:=exec.Command(child,os.Args[1:]...)
 cmd.Env=append(os.Environ(),"EIGENFLUX_INSTALL_ROOT="+root,"EIGENFLUX_INSTALL_CHILD=1")
 cmd.Stdout=os.Stdout;cmd.Stderr=os.Stderr;cmd.Stdin=os.Stdin
 if err:=cmd.Run();err!=nil { if e,ok:=err.(*exec.ExitError);ok {os.Exit(e.ExitCode())};os.Exit(98) }
}`, name)
	if err := os.WriteFile(stableSource, []byte(stableCode), 0600); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(root, name)
	build = exec.Command("go", "build", "-o", stable, stableSource)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build old stable entry: %v %s", err, out)
	}
	home := filepath.Join(root, "agent", ".eigenflux")
	_ = os.MkdirAll(home, 0700)
	var environment []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "EIGENFLUX_") && !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "USERPROFILE=") {
			environment = append(environment, value)
		}
	}
	environment = append(environment, "HOME="+root, "USERPROFILE="+root, "EIGENFLUX_HOME="+home)
	directory := filepath.Join(stable+".versions", "9.8.7")
	_ = os.MkdirAll(directory, 0700)
	if err := os.WriteFile(filepath.Join(directory, name), binary, 0755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		app := filepath.Join(directory, "EigenFlux Notifications.app")
		_ = os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0700)
		helper, err := os.ReadFile("/usr/bin/true")
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(app, "Contents", "MacOS", "EigenFluxNotifications"), helper, 0755)
		plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>ai.eigenflux.uninstall-fixture</string><key>CFBundleExecutable</key><string>EigenFluxNotifications</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleVersion</key><string>9.8.7</string><key>CFBundleShortVersionString</key><string>9.8.7</string></dict></plist>`
		_ = os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0600)
		if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
			t.Fatalf("sign isolated helper: %v %s", err, out)
		}
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	if err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(directory, path)
		header := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate}
		info, _ := entry.Info()
		header.SetMode(info.Mode())
		out, err := writer.CreateHeader(header)
		if err == nil {
			_, err = out.Write(data)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	artifact := func(data []byte) selfupdate.Artifact {
		hash := sha256.Sum256(data)
		return selfupdate.Artifact{SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}
	}
	platform := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		platform += ".exe"
	}
	manifest := selfupdate.Manifest{Version: "9.8.7", Artifacts: map[string]selfupdate.Artifact{platform: artifact(binary)}, Packages: map[string]selfupdate.Artifact{platform + ".zip": artifact(archive.Bytes())}}
	if err := selfupdate.Sign(&manifest, key); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(directory, "release.json"), data, 0600)
	_ = os.WriteFile(filepath.Join(directory, ".package.zip"), archive.Bytes(), 0600)
	if err := selfupdate.ActivateInstallation(stable, "9.8.7", public); err != nil {
		t.Fatal(err)
	}
	feedbackEndpoint := uninstallFeedbackTestServer(t, nil)
	invoke := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(stable, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		cmd.Env = environment
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		return out
	}
	invoke("installation", "record", "--host", "codex", "--skills-target", "")
	configureUninstallFeedbackTest(t, home, feedbackEndpoint)
	// Current-Agent scope keeps the shared immutable package and activation.
	invoke("uninstall", "--scope", "current", "--apply", "--host-cleanup-confirmed", "--reason-category", "declined")
	for _, path := range []string{stable, directory, stable + ".current.json"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("current scope removed a shared package: %s: %v", path, err)
		}
	}
	invoke("installation", "record", "--host", "codex", "--skills-target", "")
	configureUninstallFeedbackTest(t, home, feedbackEndpoint)
	_ = os.WriteFile(filepath.Join(home, "credentials.keep"), []byte("fixture"), 0600)
	foreign := filepath.Join(directory, "foreign.txt")
	_ = os.WriteFile(foreign, []byte("preserve"), 0600)
	reject := exec.Command(stable, "--homedir", home, "uninstall", "--scope", "all", "--apply", "--host-cleanup-confirmed", "--reason-category", "declined")
	reject.Env = environment
	if out, err := reject.CombinedOutput(); err == nil {
		t.Fatalf("foreign package content accepted: %s", out)
	}
	for _, path := range []string{stable, foreign, filepath.Join(home, "credentials.keep")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("ownership failure mutated the installation: %s: %v", path, err)
		}
	}
	_ = os.Remove(foreign)
	started := invoke("uninstall", "--scope", "all", "--begin", "--reason-category", "declined")
	var receipt struct {
		AttemptID string `json:"attempt_id"`
	}
	if json.Unmarshal(started, &receipt) != nil || receipt.AttemptID == "" {
		t.Fatal("missing begin receipt")
	}
	if scenario != "wrapper" {
		receiptPath := filepath.Join(home, "uninstall-"+receipt.AttemptID+".json")
		rawReceipt, err := os.ReadFile(receiptPath)
		if err != nil {
			t.Fatal(err)
		}
		var applied uninstallObservation
		if err := json.Unmarshal(rawReceipt, &applied); err != nil {
			t.Fatal(err)
		}
		applied.ReadyToFinalize = true
		if err := saveMaintenance(receiptPath, &applied); err != nil {
			t.Fatal(err)
		}
		// The standalone binary remains runnable after the active version is
		// partially removed; the old stable entry cannot serve as a reporter.
		finalize := func() ([]byte, error) {
			cmd := exec.Command(built, "--homedir", home, "uninstall-finalize", "--attempt", receipt.AttemptID, "--remove-original", "--format", "json")
			cmd.Env = environment
			return cmd.Output()
		}
		var unlock func()
		if scenario == "windows_lock" {
			unlock = lockUninstallTestExecutable(t, filepath.Join(directory, name), environment)
		} else {
			if err := os.Remove(filepath.Join(directory, name)); err != nil {
				t.Fatal(err)
			}
			// Model an interruption after some authentication metadata was also
			// removed. The signed ZIP and receipt still authenticate survivors.
			if err := os.Remove(filepath.Join(directory, "release.json")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			broken := exec.Command(stable, "--homedir", home, "version")
			broken.Env = environment
			if out, err := broken.CombinedOutput(); err == nil {
				t.Fatalf("stable entry unexpectedly survived missing active child: %s", out)
			}
		}
		if out, err := finalize(); err == nil {
			t.Fatalf("blocked finalization unexpectedly succeeded: %s", out)
		}
		for _, path := range []string{stable, filepath.Join(directory, ".package.zip"), filepath.Join(home, "credentials.keep"), built} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("failed finalization lost recovery material: %s: %v", path, err)
			}
		}
		if scenario == "windows_lock" {
			for _, path := range []string{filepath.Join(directory, name), filepath.Join(directory, "release.json")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("locked payload lost its authentication metadata: %s: %v", path, err)
				}
			}
			unlock()
		} else if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		var partial uninstallObservation
		receiptData, err := os.ReadFile(filepath.Join(home, "uninstall-"+receipt.AttemptID+".json"))
		if err != nil || json.Unmarshal(receiptData, &partial) != nil || partial.AttemptID != receipt.AttemptID || partial.Pending || partial.Result != "partial" || partial.PackageCleanup == nil {
			t.Fatalf("missing resumable partial receipt: %v %s", err, receiptData)
		}
		out, err := finalize()
		var result struct {
			Result           string `json:"result"`
			LocalDataRemoved bool   `json:"local_data_removed"`
		}
		if err != nil || json.Unmarshal(out, &result) != nil || result.Result != "removed" || !result.LocalDataRemoved {
			t.Fatalf("same receipt failed to recover with standalone reporter: %v %s", err, out)
		}
		if _, err := os.Stat(built); err != nil {
			t.Fatalf("cleanup crossed into independent reporter: %v", err)
		}
	} else {
		args := []string{stable, "--homedir", home, "--scope", "all", "--attempt", receipt.AttemptID, "--apply", "--host-cleanup-confirmed", "--format", "json"}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell.exe", append([]string{"-NoProfile", "-NonInteractive", "-File", stable + ".uninstall.ps1"}, args...)...)
		} else {
			cmd = exec.Command("sh", append([]string{stable + ".uninstall.sh"}, args...)...)
		}
		cmd.Env = environment
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("versioned official wrapper: %v %s", err, out)
		}
	}
	for _, path := range []string{stable, stable + ".versions", stable + ".current.json", stable + ".install.json", stable + ".uninstall.sh", stable + ".uninstall.ps1", filepath.Join(home, "credentials.keep")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("owned installation retained: %s: %v", path, err)
		}
	}
}

// A separate process opens the real package executable without FILE_SHARE_DELETE.
// This exercises the native failure rather than mocking os.Remove.
func lockUninstallTestExecutable(t *testing.T, file string, environment []string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	script := `$file = [IO.File]::Open($env:EIGENFLUX_TEST_LOCK_TARGET, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read); [Console]::Out.WriteLine('ready'); [Console]::Out.Flush(); [void][Console]::In.ReadLine(); $file.Dispose()`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(append([]string{}, environment...), "EIGENFLUX_TEST_LOCK_TARGET="+file)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	closed := false
	unlock := func() {
		if closed {
			return
		}
		closed = true
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("release executable lock: %v %s", err, stderr.String())
		}
		cancel()
	}
	t.Cleanup(unlock)
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("executable lock was not acquired: %v %s", scanner.Err(), stderr.String())
	}
	return unlock
}
