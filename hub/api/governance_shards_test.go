package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/govern"
	"fluxspace.ai/hub/session"
)

// shardsHTTPServer starts an authenticated Hub whose governance state is backed
// by a real durable store pre-split across three rotated shards plus the active
// file. Registering governance on the mux and wrapping it in WithAuth mirrors
// main.go, so the new endpoint is exercised through the default-deny gate.
func shardsHTTPServer(t *testing.T) (string, *Governance, *govern.Components) {
	t.Helper()
	dir := t.TempDir()
	components, err := govern.OpenWithOptions(dir, govern.WithRotateThreshold(0))
	if err != nil {
		t.Fatalf("open governance state in %s: %v", dir, err)
	}
	if note := components.LoadNote(); note != nil {
		t.Fatalf("unexpected load note on fresh store: %v", note)
	}

	batch := func(n int) {
		for i := 0; i < n; i++ {
			if err := components.Commit(govern.AuditEntry{
				CompanionID: "comp_shards", Operation: "authorize", Tier: govern.TierReadOnly,
				Outcome: govern.OutcomeAllowed, Detail: "seed", At: 1000,
			}); err != nil {
				t.Fatalf("seed commit: %v", err)
			}
		}
	}
	batch(3)
	if err := components.Store.Rotate(); err != nil {
		t.Fatalf("rotate 1: %v", err)
	}
	batch(3)
	if err := components.Store.Rotate(); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}
	batch(3)
	if err := components.Store.Rotate(); err != nil {
		t.Fatalf("rotate 3: %v", err)
	}
	batch(2) // active file, seq 10..11

	clock := func() int64 { return 1794000000000 }
	mux := http.NewServeMux()
	srv := &Server{
		Manager:        session.NewManagerWithClock(clock),
		Archives:       archive.NewStore(),
		Governance:     &Governance{Components: components},
		OwnerTokens:    map[string]string{"tok-owner": "owner_1"},
		SpectatorToken: "spectator-secret",
		Clock:          clock,
	}
	RegisterGovernance(mux, srv)
	httpSrv := httptest.NewServer(WithAuth(mux, srv))
	t.Cleanup(func() {
		httpSrv.Close()
		_ = components.Store.Close()
	})
	return httpSrv.URL, srv.Governance, components
}

// TestAuditShardsHTTPAuthAndShape proves the exact endpoint behaviour: 401
// without a bearer token, 200 with one, and a spectator token cannot reach it.
// The 200 body is checked against the real ShardInventory() so the JSON shape
// and every reported number are verified end to end.
func TestAuditShardsHTTPAuthAndShape(t *testing.T) {
	url, _, components := shardsHTTPServer(t)
	path := url + "/hub/audit/shards"

	t.Run("401 without a token", func(t *testing.T) {
		status, body := doAuthed(t, http.MethodGet, path, "", nil)
		if status != http.StatusUnauthorized || codeOf(body) != "AUTH_REQUIRED" {
			t.Fatalf("no credential = %d %v, want 401 AUTH_REQUIRED", status, body)
		}
	})

	t.Run("200 with owner token returns the real inventory", func(t *testing.T) {
		status, body := doAuthed(t, http.MethodGet, path, "tok-owner", nil)
		if status != http.StatusOK {
			t.Fatalf("owner GET shards = %d %v, want 200", status, body)
		}
		inv, err := components.Store.ShardInventory()
		if err != nil {
			t.Fatalf("ShardInventory: %v", err)
		}
		if got, _ := body["count"].(float64); int(got) != len(inv) {
			t.Fatalf("count = %v, want %d", body["count"], len(inv))
		}
		shards, ok := body["shards"].([]any)
		if !ok || len(shards) != len(inv) {
			t.Fatalf("shards = %v, want a %d-element array", body["shards"], len(inv))
		}
		for i, raw := range shards {
			m, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("shards[%d] is not an object: %v", i, raw)
			}
			for _, key := range []string{
				"name", "path", "first_seq", "last_seq", "entries", "bytes",
				"first_hash", "last_hash", "active",
			} {
				if _, present := m[key]; !present {
					t.Fatalf("shards[%d] is missing JSON key %q", i, key)
				}
			}
			want := inv[i]
			if m["name"].(string) != want.Name {
				t.Errorf("shards[%d].name = %v, want %q", i, m["name"], want.Name)
			}
			if int64(m["first_seq"].(float64)) != want.FirstSeq ||
				int64(m["last_seq"].(float64)) != want.LastSeq ||
				int(m["entries"].(float64)) != want.Entries ||
				int64(m["bytes"].(float64)) != want.Bytes {
				t.Errorf("shards[%d] numbers = %v, want seq %d..%d / %d entries / %d bytes",
					i, m, want.FirstSeq, want.LastSeq, want.Entries, want.Bytes)
			}
			if m["first_hash"].(string) != want.FirstHash || m["last_hash"].(string) != want.LastHash {
				t.Errorf("shards[%d] hashes = %v / %v, want %q / %q",
					i, m["first_hash"], m["last_hash"], want.FirstHash, want.LastHash)
			}
			if m["active"].(bool) != want.Active {
				t.Errorf("shards[%d].active = %v, want %v", i, m["active"], want.Active)
			}
			// Exactly one file — the last — is the active one.
			wantActive := i == len(shards)-1
			if m["active"].(bool) != wantActive {
				t.Errorf("shards[%d].active = %v, want %v", i, m["active"], wantActive)
			}
		}
	})

	t.Run("spectator token cannot reach the privileged read", func(t *testing.T) {
		status, body := doSpectator(t, http.MethodGet, path, "spectator-secret")
		if status != http.StatusUnauthorized {
			t.Fatalf("spectator reached the shard inventory: %d %v", status, body)
		}
		if codeOf(body) != "AUTH_REQUIRED" {
			t.Fatalf("code = %v, want AUTH_REQUIRED", codeOf(body))
		}
	})
}

// TestAuditShardsDegradedOverHTTP proves the new endpoint keeps the existing
// GOVERNANCE_DEGRADED behaviour: once a decision cannot be recorded, the whole
// governance surface (including GET /hub/audit/shards) answers 503 rather than
// serving state from a store that just failed a write.
func TestAuditShardsDegradedOverHTTP(t *testing.T) {
	dir := t.TempDir()
	ts, gov, _ := governanceServerAt(t, dir)
	base := ts.URL + "/hub/companions/comp_s"

	// Break the durable half without stopping the server: the next decision
	// cannot be recorded, so the Hub must refuse to make it and degrade.
	if err := gov.Store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	status, body := doJSON(t, http.MethodPut, base+"/permissions",
		map[string]any{"tier": govern.TierReadOnly})
	if status != http.StatusInternalServerError || nested(body, "error", "code") != "GOVERNANCE_PERSIST_FAILED" {
		t.Fatalf("recording failure = %d %v, want 500 GOVERNANCE_PERSIST_FAILED", status, body)
	}

	status, body = doJSON(t, http.MethodGet, ts.URL+"/hub/audit/shards", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("degraded shards endpoint = %d %v, want 503", status, body)
	}
	if code := nested(body, "error", "code"); code != "GOVERNANCE_DEGRADED" {
		t.Fatalf("degraded code = %v, want GOVERNANCE_DEGRADED", code)
	}
}
