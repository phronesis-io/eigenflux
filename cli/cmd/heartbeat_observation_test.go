package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

func TestHeartbeatSkillsObservationClosesValidationFailure(t *testing.T) {
	for _, missingRule := range []bool{true, false} {
		name := "access failure after valid Skills"
		if missingRule {
			name = "signed bundle missing required rule"
		}
		t.Run(name, func(t *testing.T) {
			var observed []maintenance.Event
			var mu sync.Mutex
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/maintenance/events:batch" {
					var batch struct {
						Events []maintenance.Event `json:"events"`
					}
					if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
						t.Error(err)
					}
					mu.Lock()
					observed = append(observed, batch.Events...)
					mu.Unlock()
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer api.Close()
			runtimeTestConfig(t, api.URL, true)
			dir := installHeartbeatTestRules(t)
			if missingRule {
				// Exercise validation after a valid local sync: this is a signed,
				// intact bundle whose publisher omitted a required rule.
				if err := os.Remove(filepath.Join(dir, "ef-profile", "references", "runtime-model.md")); err != nil {
					t.Fatal(err)
				}
				pub, key, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
				manifest, err := skills.GenerateManifest(dir, version, version, []string{"ef-broadcast", "ef-communication", "ef-profile"}, 1)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Sequence, manifest.ManagedBy, manifest.TarSHA256 = 1, skills.ManagedByValue, strings.Repeat("a", 64)
				if err := skills.SignManifest(manifest, key); err != nil {
					t.Fatal(err)
				}
				if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
					t.Fatal(err)
				}
			}
			err := heartbeatPlanCmd.RunE(&cobra.Command{}, nil)
			if err == nil {
				t.Fatal("invalid plan unexpectedly succeeded")
			}
			if missingRule && !strings.Contains(err.Error(), "required rule source is missing") {
				t.Fatalf("did not reach the post-sync validation branch: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			var events []maintenance.Event
			for _, event := range observed {
				if event.Component == "skills" {
					events = append(events, event)
				}
			}
			if len(events) != 2 || events[0].Phase != "check" || events[0].Result != "started" || events[1].Phase != "install" || events[0].AttemptID != events[1].AttemptID {
				t.Fatalf("Skills check has no correlated terminal observation: %+v", events)
			}
			want := "no_update"
			if missingRule {
				want = "failed"
			}
			if events[1].Result != want {
				t.Fatalf("Skills outcome=%s want=%s: %+v", events[1].Result, want, events)
			}
			if missingRule && events[1].ErrorCode != "skills_plan_failed" {
				t.Fatalf("missing validation error classification: %+v", events[1])
			}
		})
	}
}
