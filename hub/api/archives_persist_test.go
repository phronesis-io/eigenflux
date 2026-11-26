package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/session"
)

// durableArchiveServer drives real matches through the Manager (not fixtures)
// against a Store backed by a temporary HUB_ARCHIVE_DIR.
func durableArchiveServer(t *testing.T) (*httptest.Server, *archive.Store, string, string) {
	t.Helper()
	mgr := session.NewManager()
	sessionID := finishedSession(t, mgr, "inv_durable_archive", 0)
	dir := t.TempDir()
	store, err := archive.OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	srv := &Server{Manager: mgr, Archives: store}
	mux := http.NewServeMux()
	RegisterSessions(mux, srv)
	RegisterArchives(mux, srv)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store, dir, sessionID
}

func createArchiveOverHTTP(t *testing.T, ts *httptest.Server, sessionID string) string {
	t.Helper()
	status, created := doJSON(t, http.MethodPost, ts.URL+"/hub/sessions/"+sessionID+"/archive", nil)
	if status != http.StatusCreated {
		t.Fatalf("archive create = %d %v", status, created)
	}
	summary, ok := created["summary"].(map[string]any)
	if !ok {
		t.Fatalf("created body has no summary: %v", created)
	}
	return summary["id"].(string)
}

// A consent grant accepted over HTTP must survive a restart. Before ADR-0011
// the handler wrote straight to Ledger().Record, which never reached the
// durable event log — a restarted Hub would forget the grant.
func TestArchiveHTTPConsentGrantIsDurablyRecorded(t *testing.T) {
	ts, _, dir, sessionID := durableArchiveServer(t)
	archiveID := createArchiveOverHTTP(t, ts, sessionID)

	status, body := doJSON(t, http.MethodPost, ts.URL+"/hub/archives/"+archiveID+"/consent",
		map[string]any{"companion_id": "comp_alpha", "owner_id": "owner_alpha", "accepted": true})
	if status != http.StatusOK {
		t.Fatalf("consent = %d %v", status, body)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "archive.jsonl"))
	if err != nil {
		t.Fatalf("read archive event log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected one put and one consent_grant event, got %d lines: %s", len(lines), raw)
	}
	if !strings.Contains(lines[0], `"kind":"put"`) {
		t.Fatalf("first event is not a put: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"kind":"consent_grant"`) {
		t.Fatalf("consent was not committed to the log: %s", lines[1])
	}

	reloaded, err := archive.OpenStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer reloaded.Close()
	if grants := reloaded.Ledger().Grants(archiveID); len(grants) != 1 {
		t.Fatalf("consent grant did not survive restart: %v", grants)
	}
}

// A failed durable write must not be reported as a caller error, and the whole
// archive surface must then refuse to serve state that was never recorded.
func TestArchiveHTTPDegradesAfterWriteFailure(t *testing.T) {
	ts, store, _, sessionID := durableArchiveServer(t)
	archiveID := createArchiveOverHTTP(t, ts, sessionID)

	// Closing the append handle makes the very next commit fail. This is the
	// same shape as a disk-full or permission error on the event log.
	if err := store.Close(); err != nil {
		t.Fatalf("close append handle: %v", err)
	}

	status, body := doJSON(t, http.MethodPost, ts.URL+"/hub/archives/"+archiveID+"/consent",
		map[string]any{"companion_id": "comp_beta", "owner_id": "owner_beta", "accepted": true})
	if status != http.StatusInternalServerError {
		t.Fatalf("failed durable write = %d %v; want 500", status, body)
	}
	if code := nested(body, "error", "code"); code != "ARCHIVE_PERSIST_FAILED" {
		t.Fatalf("code = %v; want ARCHIVE_PERSIST_FAILED", code)
	}

	for _, path := range []string{"/hub/archives", "/hub/archives/" + archiveID} {
		status, body := doJSON(t, http.MethodGet, ts.URL+path, nil)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("GET %s after degradation = %d %v; want 503", path, status, body)
		}
		if code := nested(body, "error", "code"); code != "ARCHIVE_STORE_DEGRADED" {
			t.Fatalf("GET %s code = %v; want ARCHIVE_STORE_DEGRADED", path, code)
		}
	}
}
