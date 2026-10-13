package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestAttentionJSONAndStdinUploadEquivalent(t *testing.T) {
	for _, prefill := range []bool{false, true} {
		request := validAttentionPublishRequest()
		endpoint := attentionPublishEndpoint
		if prefill {
			request = validAttentionPrefillRequest()
			endpoint = attentionPrefillEndpoint
		}
		payload, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v2"+endpoint {
				t.Errorf("unexpected upload route: %s %s", r.Method, r.URL.Path)
			}
			var got attentionPublishRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, request) {
				t.Errorf("upload changed payload: %+v, err=%v", got, err)
			}
			calls++
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}))
		runtimeTestConfig(t, server.URL, true)
		for _, input := range []string{"json", "stdin"} {
			command := &cobra.Command{}
			command.Flags().Bool("stdin", false, "")
			command.Flags().String("json", "", "")
			if input == "json" {
				err = command.Flags().Set("json", string(payload))
			} else {
				err = command.Flags().Set("stdin", "true")
				command.SetIn(strings.NewReader(string(payload)))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := runAttentionUpload(command, endpoint, prefill); err != nil {
				t.Fatal(err)
			}
		}
		server.Close()
		if calls != 2 {
			t.Fatalf("got %d uploads, want 2", calls)
		}
	}
}

func TestRuntimeArgumentsOverrideEnvironment(t *testing.T) {
	t.Setenv("EIGENFLUX_MODE", "plugin")
	t.Setenv("EIGENFLUX_MODEL", "environment-model")
	previousMeta := clientMeta
	previousMode, previousModel, previousHome := runtimeModeFlag, runtimeModelFlag, homeDirFlag
	t.Cleanup(func() {
		clientMeta = previousMeta
		runtimeModeFlag, runtimeModelFlag, homeDirFlag = previousMode, previousModel, previousHome
	})
	homeDirFlag = ""
	command := &cobra.Command{}
	command.Flags().StringVar(&runtimeModeFlag, "runtime-mode", "", "")
	command.Flags().StringVar(&runtimeModelFlag, "runtime-model", "", "")
	if err := command.ParseFlags([]string{"--runtime-mode", "skill", "--runtime-model", "current-model"}); err != nil {
		t.Fatal(err)
	}
	if err := rootCmd.PersistentPreRunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if clientMeta.Mode != "skill" || clientMeta.Model != "current-model" {
		t.Fatalf("explicit runtime metadata did not win: %+v", clientMeta)
	}
	if err := command.Flags().Set("runtime-mode", "unknown"); err != nil {
		t.Fatal(err)
	}
	if err := rootCmd.PersistentPreRunE(command, nil); err == nil {
		t.Fatal("invalid explicit mode was accepted")
	}
}

func TestAttentionInputSelectionRejectsAmbiguityBeforeUpload(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
		stdin, json         bool
	}{
		{name: "missing", want: "exactly one"},
		{name: "ambiguous", stdin: true, json: true, payload: "{}", want: "exactly one"},
		{name: "empty JSON", json: true, want: "payload is empty"},
		{name: "malformed JSON", json: true, payload: "{", want: "typed JSON object"},
		{name: "oversized JSON", json: true, payload: strings.Repeat(" ", attentionBodyLimit+1), want: "32 KiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := &cobra.Command{}
			command.Flags().Bool("stdin", false, "")
			command.Flags().String("json", "", "")
			if tc.stdin {
				if err := command.Flags().Set("stdin", "true"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.json {
				if err := command.Flags().Set("json", tc.payload); err != nil {
					t.Fatal(err)
				}
			}
			err := runAttentionUpload(command, attentionPublishEndpoint, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
