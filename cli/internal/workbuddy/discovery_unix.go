//go:build !windows

package workbuddy

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func discoverControl(configDir string) (string, error) {
	u := sha1.Sum([]byte(strconv.Itoa(os.Getuid())))
	d := sha1.Sum([]byte(configDir))
	base := filepath.Join(os.TempDir(), "wb-"+hex.EncodeToString(u[:])[:6], hex.EncodeToString(d[:])[:12])
	paths, _ := filepath.Glob(filepath.Join(base, "sidecar-*.sock"))
	var live []string
	for _, p := range paths {
		if socketOwner(p) == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, err := sidecar(ctx, p, "session.list", map[string]any{})
			cancel()
			if err == nil {
				live = append(live, p)
			}
		}
	}
	if len(live) != 1 {
		return "", errors.New("workbuddy_unique_host_unavailable")
	}
	return live[0], nil
}
func discoverCLI() string {
	return "/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/bin/codebuddy"
}
func discoverNode(string) (string, error) { return exec.LookPath("node") }
func dialControl(ctx context.Context, path string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}
