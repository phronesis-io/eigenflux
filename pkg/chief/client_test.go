package chief

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := NewClient(srv.URL, 5*time.Second)
	return c, srv.Close
}

func TestHealth_OK(t *testing.T) {
	c, stop := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"service":"kovaloop-ledger","status":"ok"}`))
	})
	defer stop()
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health err: %v", err)
	}
}

func TestHealth_Error(t *testing.T) {
	c, stop := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`{"detail":"down"}`))
	})
	defer stop()
	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	ce, ok := err.(*ChiefError)
	if !ok {
		t.Fatalf("expected *ChiefError, got %T", err)
	}
	if ce.StatusCode != 503 {
		t.Fatalf("status %d", ce.StatusCode)
	}
}

func TestListEntries(t *testing.T) {
	c, stop := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/ledger/entries") {
			t.Fatalf("path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("agentId") != "42" || q.Get("type") != "agent_transfer" || q.Get("limit") != "10" {
			t.Fatalf("query %v", q)
		}
		w.Write([]byte(`{"entries":[
			{"entryId":"e1","entryType":"agent_transfer","agentId":"42","asset":"USDC","availableDeltaAtomic":"10000",
			 "metadata":{"fromAgentId":"99","toAgentId":"42","transferId":"t_abc","transactionState":"SETTLED","txHash":"h","settlementRecordId":"s"},
			 "createdAt":"2026-06-09T08:34:52Z"}
		]}`))
	})
	defer stop()
	entries, err := c.ListEntries(context.Background(), "42", "agent_transfer", 10)
	if err != nil {
		t.Fatalf("ListEntries err: %v", err)
	}
	if len(entries) != 1 || entries[0].Metadata.TransferID != "t_abc" {
		t.Fatalf("entries %+v", entries)
	}
	if entries[0].AvailableDeltaAtomic != "10000" {
		t.Fatalf("delta %s", entries[0].AvailableDeltaAtomic)
	}
}

func TestGetAccount(t *testing.T) {
	c, stop := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ledger/accounts/42" {
			t.Fatalf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"agentId":"42","asset":"USDC","availableAtomic":"50000"}`))
	})
	defer stop()
	acc, err := c.GetAccount(context.Background(), "42")
	if err != nil {
		t.Fatalf("GetAccount err: %v", err)
	}
	if acc.AgentID != "42" || acc.AvailableAtomic != "50000" {
		t.Fatalf("acc %+v", acc)
	}
}
