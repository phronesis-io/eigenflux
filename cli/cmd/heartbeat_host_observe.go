package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

type heartbeatHostInput struct {
	heartbeatStopInput
	Prompt             string `json:"prompt"`
	ExpectedPromptHash string `json:"-"`
}

var heartbeatRunContinuation = regexp.MustCompile(`^EigenFlux heartbeat run ([a-f0-9]{64}) (?:has no verified current-turn cycle|verification is unavailable)\.`)

func heartbeatNoPlanReason(attempt string) string {
	return "EigenFlux heartbeat run " + attempt + " has no verified current-turn cycle. Run its scoped heartbeat plan, execute the plan, and run cycle-check --finish before returning."
}

func heartbeatVerificationReason(attempt string) string {
	return "EigenFlux heartbeat run " + attempt + " verification is unavailable. Recover this run's exact current-turn cycle and its confirmed receipts before finishing. Preserve completed work; do not create a replacement plan or repeat business mutations. Report an unresolved local failure through the host notification protocol."
}

func heartbeatHostAttempt(session, turn string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + turn))
	return hex.EncodeToString(sum[:])
}

// A marker admits only the exact owner turn seen by UserPromptSubmit. It never
// stores prompts, transcript paths, tool inputs or raw host identifiers.
func heartbeatHostRunPath(scope maintenance.Scope, session, turn string) string {
	owner := sha256.Sum256([]byte(scope.AgentID + "\x00" + session))
	return filepath.Join(scope.Home, "servers", scope.Server, "data", "heartbeat-host-runs", hex.EncodeToString(owner[:]), heartbeatHostAttempt(session, turn)+".json")
}

func heartbeatHostRunAdmitted(scope maintenance.Scope, session, turn string) bool {
	_, ok := readHeartbeatHostRun(scope, session, heartbeatHostAttempt(session, turn))
	return ok
}

type heartbeatHostMarker struct {
	Cycle         string `json:"cycle_id"`
	OriginAttempt string `json:"origin_attempt,omitempty"`
}

func readHeartbeatHostRun(scope maintenance.Scope, session, attempt string) (string, bool) {
	marker, ok := readHeartbeatHostMarker(scope, session, attempt)
	return marker.Cycle, ok
}

func readHeartbeatHostMarker(scope maintenance.Scope, session, attempt string) (heartbeatHostMarker, bool) {
	var marker heartbeatHostMarker
	if len(attempt) != 64 {
		return marker, false
	}
	if _, err := hex.DecodeString(attempt); err != nil {
		return marker, false
	}
	p := filepath.Join(filepath.Dir(heartbeatHostRunPath(scope, session, "")), attempt+".json")
	f, err := os.Lstat(p)
	if err != nil || !f.Mode().IsRegular() || f.Size() > 256 || time.Since(f.ModTime()) < 0 || time.Since(f.ModTime()) >= maintenance.Retention {
		return marker, false
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &marker) != nil {
		return marker, false
	}
	if marker.OriginAttempt != "" {
		if _, err := hex.DecodeString(marker.OriginAttempt); err != nil || len(marker.OriginAttempt) != 64 {
			return heartbeatHostMarker{}, false
		}
	}
	return marker, true
}

func admitHeartbeatHostRun(scope maintenance.Scope, session, turn, continuationCycle string, origin ...string) error {
	p := heartbeatHostRunPath(scope, session, turn)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) && heartbeatHostRunAdmitted(scope, session, turn) {
		return nil
	}
	if err != nil {
		return err
	}
	originAttempt := heartbeatHostAttempt(session, turn)
	if len(origin) == 1 && origin[0] != "" {
		originAttempt = origin[0]
	}
	err = json.NewEncoder(f).Encode(heartbeatHostMarker{Cycle: continuationCycle, OriginAttempt: originAttempt})
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	// Prune only this helper's expired, hashed markers, never host scheduling data.
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, entry := range entries {
		if len(entry.Name()) != 69 || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if _, e := hex.DecodeString(strings.TrimSuffix(entry.Name(), ".json")); e != nil {
			continue
		}
		if info, e := entry.Info(); e == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > maintenance.Retention {
			_ = os.Remove(filepath.Join(filepath.Dir(p), entry.Name()))
		}
	}
	return err
}

func observeHeartbeatHost(scope maintenance.Scope, in heartbeatHostInput, thread string) error {
	if in.SessionID != thread || in.TurnID == "" || !runtimeObservationsEnabled() {
		return nil
	}
	attempt := heartbeatHostAttempt(in.SessionID, in.TurnID)
	env := maintenance.RuntimeObservation{Product: "codex", CLI: observationValue(version)}
	if in.Event == "UserPromptSubmit" {
		digest := sha256.Sum256([]byte(strings.TrimSpace(in.Prompt)))
		canonical := len(in.ExpectedPromptHash) == 64 && hex.EncodeToString(digest[:]) == in.ExpectedPromptHash
		continuation, cycle, originAttempt := false, "", attempt
		if !canonical {
			bound, err := maintenance.StopCycle(scope, in.SessionID)
			continuation = err == nil && bound != "" && strings.HasPrefix(strings.TrimSpace(in.Prompt), "EigenFlux cycle "+bound+" is still open.")
			if continuation {
				cycle = bound
			}
			if match := heartbeatRunContinuation.FindStringSubmatch(strings.TrimSpace(in.Prompt)); len(match) == 2 {
				var parent heartbeatHostMarker
				parent, continuation = readHeartbeatHostMarker(scope, in.SessionID, match[1])
				prompt := strings.TrimSpace(in.Prompt)
				continuation = continuation && (prompt == heartbeatNoPlanReason(match[1]) || prompt == heartbeatVerificationReason(match[1]))
				if continuation {
					cycle, originAttempt = parent.Cycle, parent.OriginAttempt
					if originAttempt == "" {
						originAttempt = match[1]
					}
				}
			}
		}
		if !canonical && !continuation {
			return nil
		}
		if err := admitHeartbeatHostRun(scope, in.SessionID, in.TurnID, cycle, originAttempt); err != nil {
			return err
		}
		_ = maintenance.RecordLifecycle(scope, attempt, "guard", "active", env)
		// A guard continuation is not another scheduled trigger.
		if canonical {
			return maintenance.RecordLifecycle(scope, attempt, "trigger", "observed", env)
		}
		return nil
	}
	if in.Event != "PermissionRequest" && in.Event != "Interrupt" {
		return nil
	}
	id, transcriptErr := heartbeatCycleForTurn(scope, in.heartbeatStopInput)
	if !heartbeatHostRunAdmitted(scope, in.SessionID, in.TurnID) && (transcriptErr != nil || id == "") {
		return nil
	}
	_ = maintenance.RecordLifecycle(scope, attempt, "guard", "active", env)
	if in.Event == "PermissionRequest" {
		// This is a real host approval request, not evidence of a final denial.
		if id != "" {
			_ = maintenance.ObserveCycle(scope, id, "permission_required")
		}
		return maintenance.RecordLifecycle(scope, attempt, "permission", "required", env)
	}
	if id != "" {
		_ = maintenance.ObserveCycle(scope, id, "interrupted")
	}
	return maintenance.RecordLifecycle(scope, attempt, "execution", "interrupted", env)
}

func init() {
	observe := &cobra.Command{Use: "host-observe", Short: "Observe scoped Codex triggers, approval requests and interruptions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var in heartbeatHostInput
		if err := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 1<<20)).Decode(&in); err != nil {
			return err
		}
		thread, _ := cmd.Flags().GetString("thread")
		expected, _ := cmd.Flags().GetString("expected-agent")
		in.ExpectedPromptHash, _ = cmd.Flags().GetString("prompt-hash")
		if thread == "" {
			return fmt.Errorf("--thread is required")
		}
		if in.SessionID == thread {
			scope, err := maintenanceScope(activeServerName())
			if err == nil && expected != "" && scope.AgentID == expected {
				if err := observeHeartbeatHost(scope, in, thread); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "[eigenflux:maintenance] component=runtime phase=lifecycle error_code=observation_unavailable")
				}
			}
		}
		// Observations never grant/deny permission, restart turns or perform network IO.
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{})
	}}
	observe.Flags().String("thread", "", "Exact owner thread; other chats pass through")
	observe.Flags().String("expected-agent", "", "Pinned Agent identity; account switches pass through")
	observe.Flags().String("prompt-hash", "", "Exact verified scheduler prompt SHA-256; other user prompts pass through")
	heartbeatCmd.AddCommand(observe)
}
