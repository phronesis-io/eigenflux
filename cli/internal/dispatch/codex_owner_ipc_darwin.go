//go:build darwin

package dispatch

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func dialCodexOwnerIPC(ctx context.Context, binding Binding) (net.Conn, error) {
	home := binding.Env["CODEX_HOME"]
	if !filepath.IsAbs(home) || strings.ContainsRune(home, 0) {
		return nil, errors.New("main_notice_owner_home_invalid")
	}
	parent := filepath.Join(home, "ipc")
	path := filepath.Join(parent, "ipc.sock")
	uid := uint32(os.Getuid())
	private := func(path string, directory bool) bool {
		info, err := os.Lstat(path)
		if err != nil {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uid {
			return false
		}
		if directory {
			return info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0700
		}
		return info.Mode()&os.ModeSocket != 0 && info.Mode().Perm() == 0600
	}
	if !private(parent, true) || !private(path, false) {
		return nil, errors.New("main_notice_owner_socket_untrusted")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, errors.New("main_notice_owner_ipc_unavailable")
	}
	trusted := false
	if socket, ok := conn.(*net.UnixConn); ok {
		raw, err := socket.SyscallConn()
		if err == nil {
			controlErr := raw.Control(func(fd uintptr) {
				cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
				trusted = err == nil && cred.Uid == uid
			})
			trusted = trusted && controlErr == nil
		}
	}
	if !trusted {
		_ = conn.Close()
		return nil, errors.New("main_notice_owner_peer_mismatch")
	}
	return conn, nil
}
