package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestDiscoveryInputGuards(t *testing.T) {
	p := filepath.Join(t.TempDir(), "filters.json")
	if err := os.WriteFile(p, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoveryFile(p); err == nil {
		t.Fatal("array accepted")
	}
	for _, args := range [][]string{{}, {""}, {"  "}, {"query", "extra"}} {
		search := newDiscoveryCommands()[0]
		search.SetArgs(args)
		if err := search.Execute(); err == nil {
			t.Fatalf("accepted invalid search arguments: %q", args)
		}
	}
	for _, tc := range []struct {
		command int
		flag    string
	}{{0, "need"}, {0, "file"}, {1, "needs"}} {
		command := newDiscoveryCommands()[tc.command]
		command.SetArgs([]string{"--" + tc.flag, "123"})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("accepted internal flag %s: %v", tc.flag, err)
		}
	}
}

func TestDiscoveryFilePreservesLargeMoney(t *testing.T) {
	p := filepath.Join(t.TempDir(), "filters.json")
	if err := os.WriteFile(p, []byte(`{"budget_max_fen":9007199254740993}`), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := discoveryFile(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) != `{"budget_max_fen":9007199254740993}` {
		t.Fatal(string(raw), err)
	}
}

// The capture root is the only Need authoring surface.
func TestNeedCaptureIsTheOnlyNeedLifecycle(t *testing.T) {
	var needs []*cobra.Command
	for _, command := range rootCmd.Commands() {
		if command.Name() == "need" {
			needs = append(needs, command)
		}
	}
	if len(needs) != 1 {
		t.Fatalf("need roots: %d", len(needs))
	}
	if children := needs[0].Commands(); len(children) != 1 || children[0].Name() != "input" {
		t.Fatal("parallel discovery Need lifecycle still registered")
	}
	for _, path := range [][]string{{"input", "create"}, {"input", "get"}, {"input", "list"}} {
		command, rest, err := needs[0].Find(path)
		if err != nil || len(rest) != 0 || command.Name() != path[len(path)-1] || command.RunE == nil {
			t.Fatalf("need %v is unreachable: %v, %v", path, rest, err)
		}
		if len(path) == 2 && command.Parent().Name() != "input" {
			t.Fatalf("wrong capture command: %v", path)
		}
	}
}

// Exercise Cobra parsing and the HTTP boundary: discovery never asks the caller
// to select a stored Need or serialize an inline one.
func TestDiscoveryCLIRequestContract(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command int
		args    []string
		body    map[string]any
	}{
		{"search", 0, []string{"数据库 index review", "--types", "agent", "--limit", "3"}, map[string]any{"query": "数据库 index review", "source_kinds": []any{"agent"}, "limit": float64(3)}},
		{"search", 0, []string{"design", "--cursor", "next-page", "--limit", "2"}, map[string]any{"query": "design", "cursor": "next-page", "limit": float64(2)}},
		{"recommendations", 1, nil, map[string]any{"limit": float64(20)}},
		{"recommendations", 1, []string{"--types", "agent", "--limit", "5"}, map[string]any{"source_kinds": []any{"agent"}, "limit": float64(5)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v2/discovery/"+tc.name {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Idempotency-Key") != "discovery-retry" {
					t.Error("retry key not forwarded")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body, tc.body) {
					t.Errorf("body = %#v, want %#v", body, tc.body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"data":{"items":[]}}`))
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			command := newDiscoveryCommands()[tc.command]
			command.SetArgs(append(tc.args, "--idempotency-key", "discovery-retry"))
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("request count = %d", calls)
			}
		})
	}
}

func TestDiscoveryTaxonomyRoute(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/taxonomy/search" || r.URL.Query().Get("query") != "database" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	command := newDiscoveryCommands()[2]
	command.SetArgs([]string{"search", "database"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("request count = %d", calls)
	}
}
