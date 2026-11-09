package dispatch

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var ErrMainSessionUnavailable = errors.New("main_session_unavailable")
var ErrMainSessionMismatch = errors.New("main_session_mismatch")

// ValidateMainSession reads only host-owned metadata. It never selects a recent
// thread, copies conversation content, or creates a replacement session.
func (b Binding) ValidateMainSession() error {
	if b.MainSessionID == "" {
		return nil
	}
	if b.Host != "codex" && (b.Mode == "acp" || len(b.MainCommand) != 0) {
		if !validACPMainID(b.MainSessionID) {
			return errors.New("main_session_id must be an exact nonempty ACP owner session ID")
		}
		return nil
	}
	_, err := b.mainSessionPath()
	return err
}

func (b Binding) mainSessionPath() (string, error) {
	if b.Mode != "native" || b.Host != "codex" {
		return "", errors.New("main_session_id currently requires the native Codex adapter")
	}
	for _, arg := range b.Args {
		if arg == "--last" || arg == "--ephemeral" || arg == "--worktree" {
			return "", errors.New("main session does not allow last, ephemeral, or worktree overrides")
		}
	}
	id := b.MainSessionID
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return "", errors.New("main_session_id must be an exact Codex UUID")
	}
	if raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", "")); err != nil || len(raw) != 16 || id != strings.ToLower(id) {
		return "", errors.New("main_session_id must be an exact lowercase Codex UUID")
	}
	home := b.Env["CODEX_HOME"]
	if !filepath.IsAbs(home) {
		return "", errors.New("main session requires an explicit absolute env.CODEX_HOME")
	}
	root, err := mainSessionRoot(filepath.Join(home, "sessions"))
	if err != nil {
		return "", ErrMainSessionUnavailable
	}
	found := ""
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), "-"+id+".jsonl") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 4096), maxRunnerOutput)
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if !scan.Scan() || json.Unmarshal(scan.Bytes(), &meta) != nil || meta.Type != "session_meta" || meta.Payload.ID != id {
			return ErrMainSessionUnavailable
		}
		found = path
		return fs.SkipAll
	})
	if err != nil || found == "" {
		return "", ErrMainSessionUnavailable
	}
	return found, nil
}

// Resolve only the sessions root. WalkDir still does not follow links inside it.
func mainSessionRoot(root string) (string, error) {
	for range 40 {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		// Windows can report a directory junction as ModeIrregular, leaving it
		// unresolved by EvalSymlinks. Readlink also understands these junctions.
		target, err := os.Readlink(resolved)
		if err != nil {
			return resolved, nil
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(resolved), target)
		}
		root = target
	}
	return "", ErrMainSessionUnavailable
}

// SetMainSession requires a stopped watcher/account lock and an explicit old
// target. Keep the binding revision so updates preserve the existing dedup journal.
func SetMainSession(b Binding, expectedRevision, previous, next string) (Binding, error) {
	if b.Revision != expectedRevision || b.MainSessionID != previous {
		return b, errors.New("main session binding changed; read watch status again")
	}
	if next == "" {
		return b, errors.New("a nonempty main session ID is required")
	}
	b.MainSessionID = next
	if err := b.ValidateMainSession(); err != nil {
		return Binding{}, err
	}
	return b, nil
}
