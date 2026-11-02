package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

type schedulerReportInput struct {
	ObservedAt int64  `json:"observed_at"`
	ErrorCode  string `json:"error_code,omitempty"`
	heartbeatmigration.Inventory
}

func schedulerReadback(in heartbeatmigration.Inventory, home, server, agentID string, observedAt int64) (*maintenance.SchedulerReadback, error) {
	now := time.Now().UnixMilli()
	if observedAt <= 0 || observedAt < now-int64(5*time.Minute/time.Millisecond) || observedAt > now+int64(30*time.Second/time.Millisecond) {
		return nil, fmt.Errorf("fresh scheduler readback within five minutes required")
	}
	if len(in.Tasks) > 512 {
		return nil, fmt.Errorf("scheduler inventory too large")
	}
	p, err := heartbeatmigration.Build(in, home, server, heartbeatLauncher(home, server, "skill"))
	if err != nil {
		return nil, err
	}
	r := &maintenance.SchedulerReadback{ObservedAt: observedAt, State: "missing"}
	if p.TaskID == "" {
		return r, nil
	}
	t := p.Before.Tasks[0]
	if strings.TrimSpace(t.Prompt) == "" || strings.TrimSpace(t.Schedule) == "" {
		return nil, fmt.Errorf("scheduler task lacks an execution prompt or cadence")
	}
	r.State = "unknown"
	switch strings.ToUpper(strings.TrimSpace(t.Status)) {
	case "ACTIVE", "ENABLED":
		r.State = "enabled"
	case "PAUSED":
		r.State = "paused"
	case "DISABLED":
		r.State = "disabled"
	}
	salt := home + "\x00" + server + "\x00" + agentID + "\x00"
	hash := func(kind, value string) string {
		sum := sha256.Sum256([]byte(salt + kind + "\x00" + value))
		return hex.EncodeToString(sum[:])
	}
	r.TaskHash, r.ScheduleHash = hash("task", t.ID), hash("schedule", t.Schedule)
	return r, nil
}

func newSchedulerReportCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "scheduler-report", Short: "Record fresh host-native scheduler readback without changing tasks", Args: cobra.NoArgs}
	cmd.Flags().Bool("stdin", false, "Read observed_at and complete native scheduler inventory from stdin")
	cmd.RunE = func(cmd *cobra.Command, _ []string) (resultErr error) {
		o := newOuterLoopRecorder(cmd, "scheduler", "readback")
		o.code = "readback_invalid"
		defer func() {
			if resultErr != nil {
				o.finish(resultErr)
			}
		}()
		useStdin, _ := cmd.Flags().GetBool("stdin")
		if !useStdin {
			return fmt.Errorf("--stdin is required")
		}
		b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 2<<20))
		if err != nil || len(b) >= 2<<20 {
			return fmt.Errorf("cannot read bounded scheduler inventory")
		}
		var in schedulerReportInput
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err = d.Decode(&in); err != nil {
			return fmt.Errorf("invalid scheduler readback: %w", err)
		}
		if err = d.Decode(&struct{}{}); err != io.EOF {
			return fmt.Errorf("unexpected trailing scheduler JSON")
		}
		if !o.r.authenticated {
			return fmt.Errorf("scheduler receipt requires the existing authenticated Agent")
		}
		if in.ErrorCode != "" {
			switch in.ErrorCode {
			case "scheduler_api_unavailable", "scheduler_readback_denied", "scheduler_readback_failed":
				if in.Complete || len(in.Tasks) > 0 || in.ObservedAt <= 0 || in.ObservedAt < time.Now().Add(-5*time.Minute).UnixMilli() || in.ObservedAt > time.Now().Add(30*time.Second).UnixMilli() {
					return fmt.Errorf("invalid failed scheduler readback")
				}
				o.code = in.ErrorCode
				return fmt.Errorf("native scheduler readback failed: %s", in.ErrorCode)
			default:
				return fmt.Errorf("unsupported scheduler readback error category")
			}
		}
		meta := clientMetaForServerName(activeServerName())
		if meta.Mode == "plugin" {
			return fmt.Errorf("plugin owns scheduling; native readback is not applicable")
		}
		if err = resolveLegacyHeartbeatServers(&in.Inventory, config.HomeDir()); err != nil {
			return err
		}
		receipt, err := schedulerReadback(in.Inventory, config.HomeDir(), o.r.scope.Server, o.r.scope.AgentID, in.ObservedAt)
		if err != nil {
			return err
		}
		o.record("verified", "", receipt)
		if o.r.recordFailed {
			return fmt.Errorf("scheduler observation could not be persisted")
		}
		o.r.Flush()
		output.PrintData(map[string]interface{}{"status": "recorded", "scheduler_state": receipt.State, "observed_at": receipt.ObservedAt, "observation": o.r.Status()}, resolveFormat())
		return nil
	}
	return cmd
}

func init() { heartbeatCmd.AddCommand(newSchedulerReportCommand()) }
