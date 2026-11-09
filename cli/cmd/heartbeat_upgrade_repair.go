package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"cli.eigenflux.ai/internal/maintenance"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

// One account-scoped acceptance request survives updater/watch restarts. Dispatch
// is not acceptance: only an actual canonical scheduled full cycle closes it.
type upgradeRepair struct {
	Version         int      `json:"version"`
	Scope           string   `json:"scope"`
	ID              string   `json:"id"`
	CreatedAt       int64    `json:"created_at"`
	Status          string   `json:"status"`
	Blockers        []string `json:"blockers,omitempty"`
	Binding         string   `json:"binding,omitempty"`
	DispatchThread  string   `json:"dispatch_thread,omitempty"`
	SchedulerThread string   `json:"scheduler_thread,omitempty"`
	NextAttempt     int64    `json:"next_attempt,omitempty"`
	Attempts        int      `json:"attempts,omitempty"`
	Instruction     string   `json:"instruction,omitempty"`
	Acknowledged    string   `json:"acknowledged,omitempty"`
	Generation      int      `json:"generation,omitempty"`
	Notification    string   `json:"notification,omitempty"`
	NoticeID        string   `json:"notice_id,omitempty"`
}

const upgradeRepairInstruction = "Read the signed ef-onboarding/references/upgrade-repair.md and finish this existing account's upgrade acceptance. Do all authorized repairs automatically; collect genuinely required owner actions into one prompt. Never ask the owner to run CLI commands. Preserve identity, task IDs, cadence, paused state and existing permissions."

func repairStatusValid(status string) bool {
	switch status {
	case "pending", "submitted", "awaiting_run", "user_action", "blocked", "deferred", "verified":
		return true
	}
	return false
}

func repairBlockers(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, s := range in {
		switch s {
		case "hook_review", "policy_approval", "host_restart", "permission_forbidden", "permission_unknown", "scheduler_missing", "scheduler_paused", "scheduler_unknown", "skills_unavailable", "host_unavailable", "foreground_repair":
		default:
			return nil, fmt.Errorf("unknown repair blocker")
		}
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

func upgradeRepairPath() (string, string, error) {
	path, scope, err := watchRecoveryNoticeScope(nil)
	if err != nil {
		return "", "", err
	}
	return path + ".upgrade-repair", scope, nil
}

func readUpgradeRepair(path, scope string) (upgradeRepair, error) {
	var r upgradeRepair
	info, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<10 {
		return r, errors.New("invalid upgrade repair receipt")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(b, &r) != nil || r.Version != 1 || r.Scope != scope || !validWatchRecoveryNoticeID(r.ID) || r.CreatedAt <= 0 || !repairStatusValid(r.Status) || r.Attempts < 0 {
		return upgradeRepair{}, errors.New("invalid upgrade repair receipt")
	}
	if _, err := repairBlockers(r.Blockers); err != nil {
		return upgradeRepair{}, err
	}
	return r, nil
}

func mutateUpgradeRepair(create bool, mutate func(*upgradeRepair) error) (*upgradeRepair, error) {
	return mutateUpgradeRepairScoped("", create, mutate)
}

func mutateUpgradeRepairScoped(expectedScope string, create bool, mutate func(*upgradeRepair) error) (*upgradeRepair, error) {
	path, scope, err := upgradeRepairPath()
	if err != nil {
		return nil, err
	}
	if expectedScope != "" && expectedScope != scope {
		return nil, errWatchIdentity
	}
	lock, err := lockWatchRecoveryNotice(path)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	r, err := readUpgradeRepair(path, scope)
	if os.IsNotExist(err) && create {
		r = upgradeRepair{Version: 1, Scope: scope, ID: maintenance.NewID(), CreatedAt: time.Now().UnixMilli(), Status: "pending"}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if mutate != nil {
		if err = mutate(&r); err != nil {
			return nil, err
		}
	}
	current, currentScope, err := upgradeRepairPath()
	if err != nil || current != path || currentScope != scope {
		return nil, errWatchIdentity
	}
	if err = saveMaintenance(path, r); err != nil {
		return nil, err
	}
	r.Instruction = upgradeRepairInstruction
	if len(r.Blockers) > 0 && (r.Status == "user_action" || r.Status == "blocked" || r.Status == "deferred") {
		r.NoticeID = repairNoticeHash(r)
		r.Notification = "required"
		if r.Acknowledged == r.NoticeID {
			r.Notification = "unchanged"
		}
	}
	return &r, nil
}

func repairNoticeHash(r upgradeRepair) string {
	b, _ := json.Marshal(r.Blockers)
	hash := sha256.Sum256(append([]byte(fmt.Sprintf("%s\x00%d\x00", r.ID, r.Generation)), b...))
	return hex.EncodeToString(hash[:])
}

func setRepairOutcome(r *upgradeRepair, status string, blockers []string) {
	old, _ := json.Marshal(r.Blockers)
	next, _ := json.Marshal(blockers)
	if string(old) != string(next) {
		r.Generation++
	}
	r.Status, r.Blockers, r.NextAttempt = status, blockers, 0
}

func acknowledgeUpgradeRepair(id, notice string) error {
	current, err := currentUpgradeRepair(false)
	if err != nil {
		return err
	}
	if current.ID != id || current.NoticeID == "" || current.NoticeID != notice {
		return errors.New("stale repair notice")
	}
	_, err = mutateUpgradeRepair(false, func(r *upgradeRepair) error {
		if r.ID != id || repairNoticeHash(*r) != notice {
			return errors.New("repair notice changed")
		}
		r.Acknowledged = notice
		return nil
	})
	return err
}

func currentUpgradeRepair(create bool) (*upgradeRepair, error) {
	return mutateUpgradeRepair(create, nil)
}

// Hook setup supplies the scheduler thread after canonical prompt validation.
// It can differ from the PM main chat used to deliver the maintenance turn.
func bindUpgradeRepairScheduler(scope maintenance.Scope, thread string) {
	if !guardThreadID.MatchString(thread) {
		return
	}
	_, _ = mutateUpgradeRepair(false, func(r *upgradeRepair) error {
		identity, err := watchBindingIdentity()
		home, homeErr := watchstate.CanonicalHome(scope.Home)
		if err != nil || homeErr != nil || identity.AgentID != scope.AgentID || identity.Home != home || identity.Server != scope.Server {
			return errWatchIdentity
		}
		if r.SchedulerThread != thread {
			r.Generation++
			r.SchedulerThread = thread
			if r.Status == "verified" {
				r.Status = "awaiting_run"
				r.CreatedAt = time.Now().UnixMilli()
			}
		}
		return nil
	})
}

// A caller cannot report success or host trust. User-action receipts describe
// remaining work; the Stop hook below independently observes final acceptance.
func reportUpgradeRepair(id, status string, blockers []string) (*upgradeRepair, error) {
	if status != "awaiting_run" && status != "user_action" && status != "blocked" && status != "deferred" {
		return nil, errors.New("repair report cannot assert acceptance")
	}
	normalized, err := repairBlockers(blockers)
	if err != nil {
		return nil, err
	}
	if status == "awaiting_run" && len(normalized) != 0 || status != "awaiting_run" && len(normalized) == 0 {
		return nil, errors.New("repair status and blockers disagree")
	}
	return mutateUpgradeRepair(false, func(r *upgradeRepair) error {
		if r.ID != id {
			return errors.New("stale repair request")
		}
		if r.Status == "verified" {
			return errors.New("repair already verified")
		}
		setRepairOutcome(r, status, normalized)
		return nil
	})
}

func verifyUpgradeRepairCycle(scope maintenance.Scope, thread, turn string, c maintenance.Cycle) {
	if c.Result != "completed" || c.Mode != "full" {
		return
	}
	marker, ok := readHeartbeatHostMarker(scope, thread, heartbeatHostAttempt(thread, turn))
	if !ok {
		return
	}
	attempt := marker.OriginAttempt
	if attempt == "" {
		attempt = heartbeatHostAttempt(thread, turn)
	}
	if marker.Cycle != "" && marker.Cycle != c.ID {
		return
	}
	observed, err := maintenance.LifecycleObserved(scope, attempt, "trigger", "observed")
	if err != nil || !observed {
		return
	}
	_, _ = mutateUpgradeRepair(false, func(r *upgradeRepair) error {
		identity, err := watchBindingIdentity()
		home, homeErr := watchstate.CanonicalHome(scope.Home)
		if err != nil || homeErr != nil || identity.AgentID != scope.AgentID || identity.Home != home || identity.Server != scope.Server {
			return errWatchIdentity
		}
		if c.StartedAt < r.CreatedAt || r.SchedulerThread != thread {
			return errors.New("unrelated repair acceptance cycle")
		}
		setRepairOutcome(r, "verified", nil)
		return nil
	})
}

func init() {
	status := &cobra.Command{Use: "upgrade-repair", Short: "Read this account's pending automatic Codex upgrade acceptance", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := currentUpgradeRepair(true)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
	}}
	report := &cobra.Command{Use: "upgrade-repair-report", Short: "Record combined remaining upgrade actions without asserting host trust", Args: cobra.NoArgs}
	report.Flags().Bool("stdin", false, "Read the account repair ID, status and fixed blockers from stdin")
	report.RunE = func(cmd *cobra.Command, _ []string) error {
		var input struct {
			ID       string   `json:"id"`
			Status   string   `json:"status"`
			Blockers []string `json:"blockers"`
		}
		use, _ := cmd.Flags().GetBool("stdin")
		if !use {
			return errors.New("--stdin is required")
		}
		d := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 16<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&input); err != nil {
			return err
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return errors.New("unexpected trailing repair JSON")
		}
		r, err := reportUpgradeRepair(input.ID, input.Status, input.Blockers)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
	}
	ack := &cobra.Command{Use: "upgrade-repair-ack", Short: "Acknowledge an actually submitted combined owner-action notice", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		id, _ := cmd.Flags().GetString("id")
		notice, _ := cmd.Flags().GetString("notice")
		return acknowledgeUpgradeRepair(id, notice)
	}}
	ack.Flags().String("id", "", "Current account repair ID")
	ack.Flags().String("notice", "", "Submitted combined notice ID")
	heartbeatCmd.AddCommand(status, report, ack)
}
