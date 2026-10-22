//go:build windows

package workbuddy

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

var pipeUUID = regexp.MustCompile(`^[a-f0-9]{8}$`)

func discoverControl(configDir string) (string, error) {
	d := sha1.Sum([]byte(configDir))
	scope := hex.EncodeToString(d[:])[:12]
	data, err := os.ReadFile(filepath.Join(windowsHostTempDir(), "wb", scope, "sidecar.pid"))
	if err != nil {
		return "", errors.New("workbuddy_host_not_running")
	}
	var pid struct {
		ControlPipeUUID string `json:"controlPipeUuid"`
	}
	if json.Unmarshal(data, &pid) != nil || !pipeUUID.MatchString(pid.ControlPipeUUID) {
		return "", errors.New("workbuddy_host_pid_invalid")
	}
	path := `\\.\pipe\workbuddy-` + scope + `-sidecar-control-` + pid.ControlPipeUUID
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = sidecar(ctx, path, "session.list", map[string]any{}); err != nil {
		return "", err
	}
	return path, nil
}
func socketOwner(path string) error {
	if !strings.HasPrefix(path, `\\.\pipe\workbuddy-`) || strings.ContainsAny(path, "\r\n\x00") || strings.Contains(path[9:], `\`) {
		return errors.New("workbuddy_untrusted_socket")
	}
	return nil
}
func discoverCLI() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `$paths = @(Get-Process -Name WorkBuddy -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Path -Unique); $bytes = [Text.Encoding]::UTF8.GetBytes((ConvertTo-Json -InputObject $paths -Compress)); $stream = [Console]::OpenStandardOutput(); $stream.Write($bytes,0,$bytes.Length); $stream.Flush()`)
	out, _ := cmd.Output()
	var processPaths []string
	if json.Unmarshal(out, &processPaths) != nil {
		var path string
		if json.Unmarshal(out, &path) == nil && path != "" {
			processPaths = []string{path}
		}
	}
	var installs []string
	for _, exe := range processPaths {
		if filepath.IsAbs(exe) && strings.EqualFold(filepath.Base(exe), "WorkBuddy.exe") {
			p := filepath.Join(filepath.Dir(exe), "resources", "app.asar.unpacked", "cli", "bin", "codebuddy")
			if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
				installs = append(installs, p)
			}
		}
	}
	if len(installs) == 1 {
		return installs[0]
	}
	if len(installs) > 1 {
		return ""
	}

	roots := []string{os.Getenv("WORKBUDDY_INSTALL_DIR"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "WorkBuddy"), filepath.Join(os.Getenv("LOCALAPPDATA"), "WorkBuddy"), filepath.Join(os.Getenv("ProgramFiles"), "WorkBuddy"), filepath.Join(os.Getenv("ProgramFiles(x86)"), "WorkBuddy")}
	if p, e := exec.LookPath("WorkBuddy.exe"); e == nil {
		roots = append([]string{filepath.Dir(p)}, roots...)
	}
	for _, r := range roots {
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		p := filepath.Join(r, "resources", "app.asar.unpacked", "cli", "bin", "codebuddy")
		if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}
func discoverNode(configDir string) (string, error) {
	roots := []string{filepath.Join(configDir, "binaries", "node", "versions"), filepath.Join(configDir, "binaries", "node")}
	for _, r := range roots {
		entries, _ := os.ReadDir(r)
		sort.Slice(entries, func(i, j int) bool { return nodeVersionLess(entries[i].Name(), entries[j].Name()) })
		for i := len(entries) - 1; i >= 0; i-- {
			name := entries[i].Name()
			if strings.Contains(name, ".installing.") || strings.Contains(name, ".__extract_temp__") || strings.Contains(name, ".extracting.") {
				continue
			}
			p := filepath.Join(r, name, "node.exe")
			if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
				return p, nil
			}
		}
	}
	return exec.LookPath("node.exe")
}
func dialControl(parent context.Context, path string) (net.Conn, error) {
	if err := socketOwner(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err = verifyWindowsPeer(c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func verifyWindowsPeer(c net.Conn) error {
	fail := errors.New("workbuddy_untrusted_pipe_peer")
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return fail
	}
	var pid uint32
	if windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid) != nil || pid == 0 {
		return fail
	}
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fail
	}
	defer windows.CloseHandle(p)
	var peer windows.Token
	if windows.OpenProcessToken(p, windows.TOKEN_QUERY, &peer) != nil {
		return fail
	}
	defer peer.Close()
	current := windows.GetCurrentProcessToken()
	u, err := current.GetTokenUser()
	if err != nil {
		return fail
	}
	v, err := peer.GetTokenUser()
	if err != nil || u.User.Sid == nil || v.User.Sid == nil || !u.User.Sid.Equals(v.User.Sid) {
		return fail
	}
	session := func(t windows.Token) (uint32, error) {
		var s, n uint32
		e := windows.GetTokenInformation(t, windows.TokenSessionId, (*byte)(unsafe.Pointer(&s)), 4, &n)
		return s, e
	}
	a, err := session(current)
	if err != nil {
		return fail
	}
	b, err := session(peer)
	if err != nil || a != b {
		return fail
	}
	return nil
}

// Match Node os.tmpdir(), whose Windows precedence is TEMP before TMP.
func windowsHostTempDir() string {
	for _, k := range []string{"TEMP", "TMP"} {
		if p := os.Getenv(k); p != "" {
			return filepath.Clean(p)
		}
	}
	return filepath.Join(os.Getenv("SystemRoot"), "temp")
}
func nodeVersionLess(a, b string) bool {
	x := strings.Split(strings.TrimPrefix(a, "v"), ".")
	y := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		var m, n int
		if i < len(x) {
			m, _ = strconv.Atoi(x[i])
		}
		if i < len(y) {
			n, _ = strconv.Atoi(y[i])
		}
		if m != n {
			return m < n
		}
	}
	return a < b
}
