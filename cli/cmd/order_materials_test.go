package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func materialTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	tempHome(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServerWithCommission(active.Name, "https://gateway.example.com", "", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}
	return server
}
func materialFlag(t *testing.T, c *cobra.Command, name, value string) {
	t.Helper()
	if err := c.Flags().Set(name, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Flags().Set(name, c.Flags().Lookup(name).DefValue) })
}
func TestCreateOrderUploadsLongTextAndResumesFinalizedPreparation(t *testing.T) {
	content := "  " + strings.Repeat("长文本\n", 10000) + "\n "
	local := filepath.Join(t.TempDir(), "request.txt")
	if err := os.WriteFile(local, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	finalized := false
	puts, finalizations := 0, 0
	keys := map[string]string{}
	var server *httptest.Server
	server = materialTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/object" {
			got, _ := io.ReadAll(r.Body)
			if string(got) != content {
				t.Error("text bytes changed")
			}
			puts++
			w.WriteHeader(200)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authentication")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		key := r.Header.Get("Idempotency-Key")
		if prev := keys[r.URL.Path]; prev != "" && prev != key {
			t.Error("unstable retry key")
		}
		keys[r.URL.Path] = key
		switch r.URL.Path {
		case "/api/v1/order-preparations":
			if finalized {
				fmt.Fprint(w, `{"code":0,"data":{"preparation_id":51,"order":{"order_id":51}}}`)
			} else {
				fmt.Fprint(w, `{"code":0,"data":{"preparation_id":51}}`)
			}
		case "/api/v1/order-preparations/51/uploads":
			fmt.Fprintf(w, `{"code":0,"data":{"grant":{"object_id":71,"method":"PUT","url":%q}}}`, server.URL+"/object")
		case "/api/v1/order-preparations/51/uploads/confirm":
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		case "/api/v1/orders":
			if _, ok := body["buyer_input"]; ok {
				t.Error("inline input leaked")
			}
			var files []materialFile
			_ = json.Unmarshal(body["input_files"], &files)
			if len(files) != 1 || files[0].LogicalPath != "inputs/request.txt" || files[0].ByteSize != int64(len(content)) || len(files[0].SHA256) != 64 {
				t.Errorf("bad manifest: %#v", files)
			}
			finalized = true
			finalizations++
			fmt.Fprint(w, `{"code":0,"data":{"order":{"order_id":51,"state":"pending_payment","version":2}}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	materialFlag(t, orderCreateCmd, "buyer-input-file", local)
	materialFlag(t, orderCreateCmd, "idempotency-key", "long-input")
	for i := 0; i < 2; i++ {
		if err := orderCreateCmd.RunE(orderCreateCmd, []string{"77"}); err != nil {
			t.Fatal(err)
		}
	}
	if puts != 1 || finalizations != 2 {
		t.Fatalf("puts=%d finalizations=%d", puts, finalizations)
	}
	if keys["/api/v1/order-preparations"] == keys["/api/v1/orders"] {
		t.Fatal("prepare/finalize key collision")
	}
}
func TestCreateOrderDoesNotFinalizeFailedUpload(t *testing.T) {
	materialTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/order-preparations":
			fmt.Fprint(w, `{"code":0,"data":{"preparation_id":51}}`)
		case "/api/v1/order-preparations/51/uploads":
			fmt.Fprint(w, `{"code":409,"msg":"upload rejected"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	materialFlag(t, orderCreateCmd, "buyer-input", "材料")
	if err := orderCreateCmd.RunE(orderCreateCmd, []string{"77"}); err == nil || !strings.Contains(err.Error(), "--preparation-id 51") {
		t.Fatalf("error=%v", err)
	}
}
func TestDeliveryTextRetrySkipsUploadAndPreservesManifest(t *testing.T) {
	delivered := false
	puts := 0
	first := ""
	var server *httptest.Server
	server = materialTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orders/51":
			state := "in_progress"
			if delivered {
				state = "validating"
			}
			fmt.Fprintf(w, `{"code":0,"data":{"order":{"state":%q}}}`, state)
		case "/api/v1/orders/51/uploads":
			fmt.Fprintf(w, `{"code":0,"data":{"grant":{"object_id":71,"method":"PUT","url":%q}}}`, server.URL+"/object")
		case "/object":
			puts++
			w.WriteHeader(200)
		case "/api/v1/orders/51/uploads/confirm":
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		case "/api/v1/orders/51/deliver":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "output_files") {
				t.Error("missing manifest")
			}
			if first == "" {
				first = string(body)
			} else if first != string(body) {
				t.Error("retry body changed")
			}
			delivered = true
			fmt.Fprint(w, `{"code":0,"data":{"order":{"state":"validating"}}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	})
	materialFlag(t, orderDeliverCmd, "text", "  complete\n")
	materialFlag(t, orderDeliverCmd, "expected-version", "2")
	for i := 0; i < 2; i++ {
		if err := orderDeliverCmd.RunE(orderDeliverCmd, []string{"51"}); err != nil {
			t.Fatal(err)
		}
	}
	if puts != 1 {
		t.Fatalf("puts=%d", puts)
	}
}
func TestCommissionDeleteUsesAuthenticatedStableMutation(t *testing.T) {
	calls := 0
	key := ""
	materialTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/api/v1/commissions/77" || r.Header.Get("Authorization") == "" {
			t.Errorf("bad delete request %s %s", r.Method, r.URL.Path)
		}
		got := r.Header.Get("Idempotency-Key")
		if got == "" || key != "" && got != key {
			t.Error("bad key")
		}
		key = got
		calls++
		fmt.Fprint(w, `{"code":0,"data":{}}`)
	})
	for i := 0; i < 2; i++ {
		if err := commissionDeleteCmd.RunE(commissionDeleteCmd, []string{"77"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestRetiredMaterialAndManualAcceptanceCommandsFailLocally(t *testing.T) {
	for _, command := range []*cobra.Command{orderSubmitMaterialsCmd, orderAcceptCmd} {
		if command.Deprecated == "" {
			t.Fatalf("%s must be deprecated", command.Name())
		}
		if err := command.RunE(command, []string{"51"}); err == nil {
			t.Fatalf("%s must not send a manual transition", command.Name())
		}
	}
}
