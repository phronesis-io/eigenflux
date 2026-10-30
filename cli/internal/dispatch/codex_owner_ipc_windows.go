//go:build windows

package dispatch

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Desktop 26.1013.40934's win32 router uses this fixed local endpoint. It is
// not scoped by CODEX_HOME; owner discovery and the pinned reader remain required.
const codexWindowsOwnerPipe = `\\.\pipe\codex-ipc`

func dialCodexOwnerIPC(ctx context.Context, binding Binding) (net.Conn, error) {
	home := binding.Env["CODEX_HOME"]
	if !filepath.IsAbs(home) || strings.ContainsRune(home, 0) {
		return nil, errors.New("main_notice_owner_home_invalid")
	}
	return dialCodexOwnerWindowsPipe(ctx, codexWindowsOwnerPipe)
}

// The path argument is internal so tests can use a unique isolated pipe. The
// production caller always passes the Desktop endpoint, never event/config data.
func dialCodexOwnerWindowsPipe(parent context.Context, path string) (net.Conn, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	// go-winio opens the client with anonymous impersonation, overlapped I/O
	// and working net.Conn deadlines. It never creates a pipe or starts a host.
	conn, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, errors.New("main_notice_owner_ipc_unavailable")
	}
	if err := verifyCodexOwnerWindowsPeer(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := parent.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// Check the connected server before sending even initialize. A pipe name or
// server-assigned client UUID is not an authentication or account boundary.
func verifyCodexOwnerWindowsPeer(conn net.Conn) error {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil || pid == 0 {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	defer windows.CloseHandle(process)
	var peer windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &peer); err != nil {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	defer peer.Close()
	current := windows.GetCurrentProcessToken()
	currentUser, err := current.GetTokenUser()
	if err != nil {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	peerUser, err := peer.GetTokenUser()
	if err != nil || currentUser.User.Sid == nil || peerUser.User.Sid == nil || !currentUser.User.Sid.Equals(peerUser.User.Sid) {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	currentSession, err := codexOwnerWindowsTokenSession(current)
	if err != nil {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	peerSession, err := codexOwnerWindowsTokenSession(peer)
	if err != nil || currentSession != peerSession {
		return errors.New("main_notice_owner_peer_mismatch")
	}
	return nil
}

func codexOwnerWindowsTokenSession(token windows.Token) (uint32, error) {
	var session, returned uint32
	err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &returned)
	if err != nil || returned != uint32(unsafe.Sizeof(session)) {
		return 0, errors.New("main_notice_owner_peer_mismatch")
	}
	return session, nil
}
