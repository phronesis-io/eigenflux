package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMsgImportantAndMarkHandledUseExactAuthenticatedResult(t *testing.T) {
	resultID := strings.Repeat("a", 64)
	var finds, marks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-v2" {
			t.Error("missing Agent V2 authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/pm/owner-notices":
			finds.Add(1)
			if r.URL.Query().Get("view") != "pending" || r.URL.Query().Get("conversations") != "42" {
				t.Errorf("unexpected lookup: %s", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"data":{"results":[{"result_id":%q,"conversation_id":"42","needs_action":true}]}}`, resultID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/pm/owner-notices/"+resultID+"/handled":
			if marks.Add(1) == 1 {
				fmt.Fprintf(w, `{"data":{"result_id":%q,"handled_at":3000,"handled_by":"agent"}}`, resultID)
			} else {
				fmt.Fprintf(w, `{"data":{"result_id":%q,"handled_at":3000}}`, strings.Repeat("b", 64))
			}
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	list, _, err := msgCmd.Find([]string{"important"})
	if err != nil {
		t.Fatal(err)
	}
	mark, _, err := msgCmd.Find([]string{"mark-handled"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = list.Flags().Set("conv-id", "")
		_ = mark.Flags().Set("result-id", "")
	})
	_ = list.Flags().Set("conv-id", "42")
	if err := list.RunE(list, nil); err != nil || finds.Load() != 1 {
		t.Fatalf("lookup requests=%d, err=%v", finds.Load(), err)
	}
	_ = mark.Flags().Set("result-id", "42")
	if err := mark.RunE(mark, nil); err == nil || marks.Load() != 0 {
		t.Fatalf("conversation ID accepted as a result: requests=%d, err=%v", marks.Load(), err)
	}
	_ = mark.Flags().Set("result-id", resultID)
	if err := mark.RunE(mark, nil); err != nil || marks.Load() != 1 {
		t.Fatalf("completion requests=%d, err=%v", marks.Load(), err)
	}
	if err := mark.RunE(mark, nil); err == nil {
		t.Fatal("accepted a completion receipt for a different result")
	}
}
