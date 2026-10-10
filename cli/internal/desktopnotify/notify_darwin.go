package desktopnotify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

const appName = "EigenFlux Notifications.app"

func platformRequest(ctx context.Context, action string, message Message) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// Every account shares one helper. Shared locks also keep Show/Check away
	// from the replacement gap while another account performs Setup.
	unlock, err := lockHelper(ctx, filepath.Join(home, "Library", "Application Support", "EigenFlux", "Notifications"), action == "enable")
	if err != nil {
		return err
	}
	defer unlock()
	app, err := installedHelper(ctx, action == "enable")
	if err != nil {
		return err
	}
	_, err = requestHelper(ctx, app, action, message)
	return err
}

type helperReceipt struct {
	OK   bool   `json:"ok"`
	Code string `json:"code"`
	PID  int    `json:"pid"`
}

func requestHelper(ctx context.Context, app, action string, message Message) (helperReceipt, error) {
	dir, err := os.MkdirTemp("", "eigenflux-notification-")
	if err != nil {
		return helperReceipt{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "request.eigenflux-notification")
	data, err := json.Marshal(struct {
		Action  string  `json:"action"`
		Message Message `json:"message"`
	}{action, message})
	if err != nil {
		return helperReceipt{}, err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return helperReceipt{}, err
	}
	// Files are passed as argv, never interpolated into AppleScript or a shell.
	if err := exec.CommandContext(ctx, "/usr/bin/open", "-g", "-a", app, path).Run(); err != nil {
		return helperReceipt{}, fmt.Errorf("start notification helper: %w", err)
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return helperReceipt{}, ctx.Err()
		case <-deadline.C:
			return helperReceipt{}, errors.New("notification helper timed out; check the system permission dialog")
		case <-ticker.C:
			result, err := os.ReadFile(path + ".result")
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return helperReceipt{}, err
			}
			var receipt helperReceipt
			if err := json.Unmarshal(result, &receipt); err != nil {
				return helperReceipt{}, errors.New("invalid notification helper response")
			}
			if receipt.OK {
				return receipt, nil
			}
			if receipt.Code == "permission_denied" || receipt.Code == "permission_required" {
				return helperReceipt{}, ErrPermission
			}
			return helperReceipt{}, fmt.Errorf("notification helper: %s", receipt.Code)
		}
	}
}

func installedHelper(ctx context.Context, install bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	destination := filepath.Join(home, "Library", "Application Support", "EigenFlux", "Notifications", appName)
	if !install {
		if _, err := os.Stat(filepath.Join(destination, "Contents", "MacOS", "EigenFluxNotifications")); err != nil {
			return "", ErrHelperMissing
		}
		return destination, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	source := filepath.Join(filepath.Dir(executable), appName)
	if _, err := os.Stat(filepath.Join(source, "Contents", "MacOS", "EigenFluxNotifications")); err != nil {
		return "", ErrHelperMissing
	}
	// The signed source bundle is retained verbatim. Never clear quarantine or
	// modify Gatekeeper settings; downloaded production bundles need notarization.
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", source).Run(); err != nil {
		return "", fmt.Errorf("verify notification helper signature: %w", err)
	}
	want, err := bundleDigest(source)
	if err != nil {
		return "", err
	}
	if have, err := bundleDigest(destination); err == nil && have == want {
		return destination, nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	copyPath := filepath.Join(stage, appName)
	if err := exec.CommandContext(ctx, "/usr/bin/ditto", source, copyPath).Run(); err != nil {
		return "", fmt.Errorf("install notification helper: %w", err)
	}
	if err := stopHelper(ctx, destination); err != nil {
		return "", err
	}
	old := filepath.Join(stage, "previous.app")
	if err := os.Rename(destination, old); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(copyPath, destination); err != nil {
		_ = os.Rename(old, destination)
		return "", err
	}
	return destination, nil
}

func lockHelper(ctx context.Context, directory string, exclusive bool) (func(), error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	mode := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		mode = unix.LOCK_EX | unix.LOCK_NB
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(int(file.Fd()), mode)
		if err == nil {
			return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Match only our installed executable, never a process name or message data.
func helperPIDs(processes, executable string) []int {
	var ids []int
	for _, line := range strings.Split(processes, "\n") {
		line = strings.TrimSpace(line)
		split := strings.IndexFunc(line, unicode.IsSpace)
		if split < 1 || strings.TrimSpace(line[split:]) != executable {
			continue
		}
		id, err := strconv.Atoi(line[:split])
		if err == nil && id > 1 {
			ids = append(ids, id)
		}
	}
	return ids
}

func stopHelper(ctx context.Context, app string) error {
	if _, err := os.Stat(app); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return fmt.Errorf("inspect notification helper before upgrade: %w", err)
	}
	ids := helperPIDs(string(output), filepath.Join(app, "Contents", "MacOS", "EigenFluxNotifications"))
	if len(ids) == 0 {
		return nil
	}
	if len(ids) != 1 {
		return errors.New("multiple notification helpers are running; cannot safely upgrade")
	}
	receipt, err := requestHelper(ctx, app, "quit", Message{})
	if err != nil {
		return fmt.Errorf("stop notification helper before upgrade: %w", err)
	}
	if receipt.PID != ids[0] {
		return errors.New("notification helper upgrade received an unexpected process ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if err := unix.Kill(receipt.PID, 0); errors.Is(err, unix.ESRCH) {
			return nil
		} else if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("notification helper did not exit before upgrade: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func bundleDigest(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("notification helper contains a non-regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}
