package archive

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fluxspace.ai/hub/models"
)

// persistArch builds a minimal but fully verifiable archive. It uses the
// package's own contentHash()/skeletonHash() to fill Integrity, so Verify()
// passes exactly as it would for an archive produced by Build(). The variant
// session makes two archives with the same id have different bytes.
func persistArch(id, session string) Archive {
	a := Archive{
		Kind:      Kind,
		ID:        id,
		SessionID: session,
		Version:   Version,
		Config:    Config{Skeleton: &models.Skeleton{}},
	}
	content, err := a.contentHash()
	if err != nil {
		panic(err)
	}
	skeleton, err := a.skeletonHash()
	if err != nil {
		panic(err)
	}
	a.Integrity = Integrity{
		EventCount:   len(a.Events),
		ContentHash:  content,
		SkeletonHash: skeleton,
		Verified:     true,
	}
	return a
}

// seedMinimal writes two archives and closes the store, leaving a clean
// directory with a 2-event log and a matching snapshot for tamper scenarios.
func seedMinimal(t *testing.T, dir string) (Archive, Archive) {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	a1 := persistArch("replay_a1", "sess_a1")
	a2 := persistArch("replay_a2", "sess_a2")
	if _, err := s.Put(a1); err != nil {
		t.Fatalf("Put a1: %v", err)
	}
	if _, err := s.Put(a2); err != nil {
		t.Fatalf("Put a2: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return a1, a2
}

// seedFull exercises every mutating method so a restart can prove that
// archives, published state, the consent ledger and reprises all survive.
func seedFull(t *testing.T, dir string) (Archive, Archive, string) {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	a1 := persistArch("replay_a1", "sess_a1")
	a2 := persistArch("replay_a2", "sess_a2")
	if _, err := s.Put(a1); err != nil {
		t.Fatalf("Put a1: %v", err)
	}
	if _, err := s.Put(a2); err != nil {
		t.Fatalf("Put a2: %v", err)
	}
	if err := s.GrantConsent(a1.ID, "compA", "ownerA", true, 100); err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}
	if _, err := s.Publish(a2.ID, 200); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	r, err := s.CreateReprise(a1.ID, 300)
	if err != nil {
		t.Fatalf("CreateReprise: %v", err)
	}
	if _, err := s.CompleteReprise(r.ID, a2.ID, "sess_reprise"); err != nil {
		t.Fatalf("CompleteReprise: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return a1, a2, r.ID
}

func TestPersistRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	a1, a2, rID := seedFull(t, dir)

	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if s.Dir() == "" {
		t.Fatal("expected a durable directory")
	}
	if note := s.LoadNote(); note != nil {
		t.Fatalf("expected a clean load, got note: %v", note)
	}
	if s.Degraded() {
		t.Fatal("a freshly opened store must not be degraded")
	}

	list := s.List()
	if len(list) != 2 || list[0].ID != a1.ID || list[1].ID != a2.ID {
		t.Fatalf("archive order/count not recovered: %+v", list)
	}
	rec1, err := s.Get(a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec1.Archive.ContentHash() != a1.ContentHash() {
		t.Fatal("archive content hash changed across restart")
	}
	if err := rec1.Archive.Verify(); err != nil {
		t.Fatalf("reloaded archive does not verify: %v", err)
	}

	rec2, err := s.Get(a2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec2.Published || rec2.PublishedAt != 200 {
		t.Fatalf("published state not recovered: %+v", rec2)
	}

	grants := s.Ledger().Grants(a1.ID)
	if len(grants) != 1 || grants[0].CompanionID != "compA" || !grants[0].Accepted {
		t.Fatalf("consent ledger not recovered: %+v", grants)
	}

	reps := s.ListReprises(a1.ID)
	if len(reps) != 1 || reps[0].ID != rID {
		t.Fatalf("reprise list not recovered: %+v", reps)
	}
	r, err := s.GetReprise(rID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != RepriseStatusCompleted || r.NewSessionID != "sess_reprise" || r.Comparison == nil {
		t.Fatalf("reprise state not recovered: %+v", r)
	}
}

func TestPersistMemoryMode(t *testing.T) {
	s := NewStore()
	if s.Dir() != "" {
		t.Fatalf("memory store Dir must be empty, got %q", s.Dir())
	}
	a := persistArch("replay_m1", "sess_m1")
	if _, err := s.Put(a); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.GrantConsent(a.ID, "compA", "ownerA", true, 1); err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}
	if _, err := s.Publish(a.ID, 2); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := s.CreateReprise(a.ID, 3); err != nil {
		t.Fatalf("CreateReprise: %v", err)
	}
	if s.Degraded() {
		t.Fatal("a memory-only store must never become degraded")
	}
	if note := s.LoadNote(); note != nil {
		t.Fatalf("a memory-only store must have no load note, got %v", note)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.Get(a.ID); err != nil {
		t.Fatalf("Get after Close: %v", err)
	}
}

func TestPersistIdempotentPut(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := persistArch("replay_idem", "sess_idem")
	r1, err := s.Put(a)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Put(a) // identical bytes
	if err != nil {
		t.Fatalf("idempotent Put failed: %v", err)
	}
	if r1.Archive.ContentHash() != r2.Archive.ContentHash() {
		t.Fatal("idempotent Put changed the record")
	}
	conflict := persistArch("replay_idem", "sess_DIFFERENT")
	if _, err := s.Put(conflict); err == nil {
		t.Fatal("expected ARCHIVE_ID_CONFLICT for same id with different bytes")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, archiveEventFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 committed event, got %d", len(lines))
	}
}

// TestPersistLoadScenarios is table-driven over the on-disk failure and repair
// modes: the store must refuse to start on corruption, but start (with a note)
// on a log that is merely ahead of the snapshot or a torn tail.
func TestPersistLoadScenarios(t *testing.T) {
	type mutateFn func(t *testing.T, dir string, a1, a2 Archive)
	type checkFn func(t *testing.T, dir string)
	cases := []struct {
		name         string
		mutate       mutateFn
		wantErr      bool
		wantNote     bool
		noteContains string
		check        checkFn
	}{
		{
			name: "tampered archive bytes in snapshot refused",
			mutate: func(t *testing.T, dir string, a1, a2 Archive) {
				p := filepath.Join(dir, archiveStateFile)
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				tampered := bytes.Replace(raw, []byte(`"session_id":"sess_a1"`), []byte(`"session_id":"sess_XXXX"`), 1)
				if bytes.Equal(raw, tampered) {
					t.Fatal("session_id not found in snapshot")
				}
				if err := os.WriteFile(p, tampered, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "tampered event log refused",
			mutate: func(t *testing.T, dir string, a1, a2 Archive) {
				p := filepath.Join(dir, archiveEventFile)
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				tampered := bytes.Replace(raw, []byte(a1.ContentHash()), []byte("sha256:TAMPERED"), 1)
				if bytes.Equal(raw, tampered) {
					t.Fatal("content hash not found in event log")
				}
				if err := os.WriteFile(p, tampered, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "unknown snapshot version refused",
			mutate: func(t *testing.T, dir string, a1, a2 Archive) {
				p := filepath.Join(dir, archiveStateFile)
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				tampered := bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":999`), 1)
				if bytes.Equal(raw, tampered) {
					t.Fatal("version field not found in snapshot")
				}
				if err := os.WriteFile(p, tampered, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "snapshot ahead of log refused",
			mutate: func(t *testing.T, dir string, a1, a2 Archive) {
				p := filepath.Join(dir, archiveEventFile)
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				idx := bytes.IndexByte(raw, '\n')
				if idx < 0 {
					t.Fatal("no newline in event log")
				}
				if err := os.WriteFile(p, raw[:idx+1], 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "log ahead of snapshot starts with a note",
			mutate: func(t *testing.T, dir string, a1, a2 Archive) {
				p := filepath.Join(dir, archiveEventFile)
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
				var last event
				if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
					t.Fatal(err)
				}
				ev := event{
					Seq:         last.Seq + 1,
					Kind:        eventPut,
					ArchiveID:   "replay_a3",
					ContentHash: persistArch("replay_a3", "sess_a3").ContentHash(),
				}
				ev.Hash = eventHash(last.Hash, ev)
				line, err := json.Marshal(ev)
				if err != nil {
					t.Fatal(err)
				}
				raw = append(raw, append(line, '\n')...)
				if err := os.WriteFile(p, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantNote:     true,
			noteContains: "never applied",
		},
		{
			name: "torn tail truncated and reported",
			mutate: func(t *testing.T, dir string, a1, a2 Archive) {
				p := filepath.Join(dir, archiveEventFile)
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				raw = append(raw, []byte(`{"seq":3,"kind":"pu`)...) // no terminating newline
				if err := os.WriteFile(p, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantNote:     true,
			noteContains: "incomplete",
			check: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, archiveEventFile))
				if err != nil {
					t.Fatal(err)
				}
				if len(data) == 0 || data[len(data)-1] != '\n' {
					t.Fatal("torn tail was not truncated to a clean newline")
				}
				lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
				if len(lines) != 2 {
					t.Fatalf("expected 2 surviving events after truncation, got %d", len(lines))
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a1, a2 := seedMinimal(t, dir)
			tc.mutate(t, dir, a1, a2)
			s, err := OpenStore(dir)
			if tc.wantErr {
				if err == nil {
					s.Close()
					t.Fatal("expected OpenStore to refuse to load")
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			defer s.Close()
			note := s.LoadNote()
			if tc.wantNote {
				if note == nil {
					t.Fatal("expected a descriptive load note")
				}
				if tc.noteContains != "" && !strings.Contains(note.Error(), tc.noteContains) {
					t.Fatalf("load note %q does not contain %q", note.Error(), tc.noteContains)
				}
			} else if note != nil {
				t.Fatalf("unexpected load note: %v", note)
			}
			if tc.check != nil {
				tc.check(t, dir)
			}
		})
	}
}

func TestPersistWriteFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := persistArch("replay_wf1", "sess_wf1")
	if _, err := s.Put(a); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if s.Degraded() {
		t.Fatal("store degraded before any write failure")
	}

	// A closed log is one of the documented write-failure examples: the next
	// commit must fail, mark the store degraded, and fail closed thereafter.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b := persistArch("replay_wf2", "sess_wf2")
	if _, err := s.Put(b); err == nil {
		t.Fatal("expected Put to fail after the log was closed")
	}
	if !s.Degraded() {
		t.Fatal("store must be degraded after a write failure")
	}

	// Fail closed: a subsequent mutation must be refused before it changes state.
	c := persistArch("replay_wf3", "sess_wf3")
	if _, err := s.Put(c); err == nil {
		t.Fatal("expected fail-closed Put to be refused")
	}
	if _, err := s.Get(c.ID); err == nil {
		t.Fatal("fail-closed Put must not have inserted a record")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
}
