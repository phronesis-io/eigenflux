package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

func updateTestDraft() map[string]json.RawMessage {
	var draft map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"requires_materials":true,"title":"Title","capability_description":"Capability","request_spec_text":"Inputs","delivery_spec_text":"Outputs","tags":["tag"],"price_fen":100,"currency":"USD","promised_delivery_ms":1000,"request_spec_schema":"{}","delivery_spec_schema":"{}","fulfillment_skill":"test-skill","future_field":{"keep":true}}`), &draft)
	return draft
}

func updateTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	addCommissionInputFlags(cmd)
	cmd.Flags().Int64("expected-version", 0, "")
	addIdempotencyFlag(cmd)
	return cmd
}

func TestMergeCommissionDraftPreservesOmittedFields(t *testing.T) {
	for _, tt := range []struct {
		name    string
		flags   map[string]string
		changed map[string]string
	}{
		{"price", map[string]string{"price-fen": "10"}, map[string]string{"price_fen": "10"}},
		{"explicit-zero-and-false", map[string]string{"price-fen": "0", "requires-materials": "false"}, map[string]string{"price_fen": "0", "requires_materials": "false"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := updateTestCommand()
			before := updateTestDraft()
			after := updateTestDraft()
			for k, v := range tt.flags {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatal(err)
				}
			}
			changed, err := mergeCommissionDraft(cmd, after)
			if err != nil {
				t.Fatal(err)
			}
			if len(changed) != len(tt.changed) {
				t.Fatal(changed)
			}
			for k, v := range before {
				if want, ok := tt.changed[k]; ok {
					if string(after[k]) != want {
						t.Fatalf("%s=%s", k, after[k])
					}
				} else if !reflect.DeepEqual(v, after[k]) {
					t.Fatalf("omitted %s changed", k)
				}
			}
		})
	}
}

func TestMergeCommissionDraftRejectsInvalidChanges(t *testing.T) {
	for _, tt := range []struct{ flag, value string }{{"title", ""}, {"fulfillment-skill", ""}, {"price-fen", "-1"}, {"promised-delivery-ms", "0"}, {"tags", ""}, {"", ""}} {
		t.Run(tt.flag, func(t *testing.T) {
			cmd := updateTestCommand()
			if tt.flag != "" {
				_ = cmd.Flags().Set(tt.flag, tt.value)
			}
			if _, err := mergeCommissionDraft(cmd, updateTestDraft()); err == nil {
				t.Fatal("invalid change accepted")
			}
		})
	}
}

func TestCommissionPartialUpdateVersionGuardAndPayload(t *testing.T) {
	for _, scenario := range []string{"success", "stale", "malformed", "read_error", "put_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			tempHome(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			active, err := cfg.GetActive("")
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if scenario == "read_error" {
						http.Error(w, "unavailable", 503)
						return
					}
					if scenario == "malformed" {
						_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"commission": map[string]any{"definition": map[string]any{"commission_id": "77"}, "draft_version": 3, "draft": updateTestDraft()}}})
					return
				}
				writes++
				var body struct {
					Version int64                      `json:"expected_draft_version"`
					Input   map[string]json.RawMessage `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := updateTestDraft()
				want["price_fen"] = json.RawMessage(`10`)
				if body.Version != 3 || !reflect.DeepEqual(body.Input, want) || r.Header.Get(idempotencyHeader) == "" {
					t.Errorf("unsafe update: %+v", body)
				}
				if scenario == "put_conflict" {
					http.Error(w, "version conflict", 409)
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"draft_version":4}}`))
			}))
			defer server.Close()
			if err := cfg.UpdateServerWithCommission(active.Name, "https://gateway.example.com", "", server.URL); err != nil {
				t.Fatal(err)
			}
			if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
				t.Fatal(err)
			}
			cmd := updateTestCommand()
			_ = cmd.Flags().Set("price-fen", "10")
			v := "3"
			if scenario == "stale" {
				v = "2"
			}
			_ = cmd.Flags().Set("expected-version", v)
			err = updateCommissionDraft(cmd, []string{"77"})
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failure accepted")
			}
			if scenario == "stale" && !strings.Contains(err.Error(), "version changed") {
				t.Fatal(err)
			}
			expectedWrites := 0
			if scenario == "success" || scenario == "put_conflict" {
				expectedWrites = 1
			}
			if writes != expectedWrites {
				t.Fatalf("writes=%d want %d", writes, expectedWrites)
			}
		})
	}
}
