package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

func TestProvisionInstallReportAcknowledgement(t *testing.T) {
	const ref = "EF-Abcd1234"
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"conversion", `{"code":0,"data":{"converted":true,"attribution":{"ref":"EF-Abcd1234"}}}`, 200, true},
		{"duplicate", `{"code":0,"data":{"converted":false,"attribution":{"ref":"EF-Abcd1234"}}}`, 200, true},
		{"unexpected HTTP success", `{"code":0,"data":{"converted":true,"attribution":{"ref":"EF-Abcd1234"}}}`, 201, false},
		{"business rejection", `{"code":1,"msg":"invalid"}`, 200, false},
		{"missing receipt", `{"code":0,"data":{}}`, 200, false},
		{"wrong ref", `{"code":0,"data":{"converted":true,"attribution":{"ref":"EF-other123"}}}`, 200, false},
		{"temporary failure", `{"code":1}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/install/report" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected report: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Ref      string            `json:"ref"`
					Metadata map[string]string `json:"metadata"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Ref != ref || body.Metadata["via"] != "cli_v2_provision" {
					t.Errorf("invalid request: %+v %v", body, err)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer endpoint.Close()
			err := reportProvisionedInstall(config.Server{Endpoint: endpoint.URL + "/"}, ref, client.Meta{})
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v want success=%v", err, tc.ok)
			}
		})
	}
	if err := reportProvisionedInstall(config.Server{Endpoint: "http://127.0.0.1:1"}, "invalid", client.Meta{}); err == nil {
		t.Fatal("invalid referral accepted")
	}
}

func TestV2ProvisionRetriesSavedInstallAfterReportFailure(t *testing.T) {
	const ref = "EF-Abcd1234"
	reportCalls := 0
	provisionFail := false
	selectedName := "selected"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/agent-identities/registration-challenges", "/api/v2/agent-identities/recovery-challenges":
			fmt.Fprint(w, `{"code":0,"data":{"bootstrap_grant":"grant","nonce":"nonce"}}`)
		case "/api/v2/agent-identities/provision":
			if provisionFail {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":1}`)
				return
			}
			var body provisionV2Request
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Ref != ref {
				t.Errorf("saved ref lost: %+v %v", body, err)
			}
			fmt.Fprint(w, `{"code":0,"data":{"agent_id":"agent-1","access_token":"new-token","refresh_token":"refresh","expires_at":4102444800000,"created":false}}`)
		case "/api/v1/install/report":
			reportCalls++
			credentials, err := auth.LoadV2Credentials(selectedName)
			if err != nil || credentials.AccessToken != "new-token" {
				t.Errorf("report ran before credential save: %v", err)
			}
			if reportCalls == 1 {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":1}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"converted":false,"attribution":{"ref":"EF-Abcd1234"}}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer endpoint.Close()
	// The default endpoint must never receive the selected server's referral.
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", false)
	if err := cfg.AddServer(selectedName, endpoint.URL); err != nil {
		t.Fatal(err)
	}
	serverFlag = selectedName
	selected, err := cfg.GetActive(selectedName)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.RememberInstallRef(*selected, selected.Endpoint, ref); err != nil {
		t.Fatal(err)
	}
	command := &cobra.Command{}
	command.Flags().String("mode", "skill", "")
	command.Flags().String("runtime-name", "codex", "")
	command.Flags().Bool("no-handoff", true, "")
	for i, want := range []bool{false, true, true} {
		data, err := captureHeartbeatStdout(t, func() error { return agentV2ProvisionCmd.RunE(command, nil) })
		if err != nil {
			t.Fatalf("attempt %d: report failure aborted provision: %v", i, err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(data), &result); err != nil || result["agent_id"] != "agent-1" || result["install_reported"] != want {
			t.Fatalf("attempt %d result=%s err=%v", i, data, err)
		}
	}
	if reportCalls != 3 {
		t.Fatalf("report calls=%d", reportCalls)
	}
	provisionFail = true
	if _, err := captureHeartbeatStdout(t, func() error { return agentV2ProvisionCmd.RunE(command, nil) }); err == nil {
		t.Fatal("failed provision accepted")
	}
	if reportCalls != 3 {
		t.Fatal("failed provision reported an installation")
	}
}
