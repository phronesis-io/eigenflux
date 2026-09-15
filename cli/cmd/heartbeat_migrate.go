package cmd

import (
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	"cli.eigenflux.ai/internal/output"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type migrationRecord struct {
	ID       string                  `json:"plan_id"`
	Home     string                  `json:"home"`
	Server   string                  `json:"server"`
	Host     string                  `json:"host"`
	Created  time.Time               `json:"created_at"`
	Verified time.Time               `json:"verified_at,omitempty"`
	Plan     heartbeatmigration.Plan `json:"plan"`
}

func heartbeatLauncher(home, server, mode string) string {
	s := "eigenflux --homedir " + shellQuote(home)
	if server != "" {
		s += " --server " + shellQuote(server)
	}
	s += " heartbeat plan --format agent"
	if mode != "" {
		s = "EIGENFLUX_MODE=" + shellQuote(mode) + " " + s
	}
	return s
}

func readMaintenanceInput(cmd *cobra.Command, target interface{}) error {
	ok, _ := cmd.Flags().GetBool("stdin")
	if !ok {
		return fmt.Errorf("--stdin is required")
	}
	b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 2<<20))
	if err != nil {
		return err
	}
	if len(b) >= 2<<20 {
		return fmt.Errorf("scheduler input too large")
	}
	return json.Unmarshal(b, target)
}

func maintenancePath(name string) string {
	server := sha256.Sum256([]byte(activeServerName()))
	return filepath.Join(config.HomeDir(), "maintenance", hex.EncodeToString(server[:8])+"-"+name+".json")
}

func migrationPendingPath(host string) string {
	scope := sha256.Sum256([]byte(host))
	return maintenancePath("migration-pending-" + hex.EncodeToString(scope[:8]))
}

func migrationReceiptPath(host string) string {
	scope := sha256.Sum256([]byte(host))
	return maintenancePath("scheduler-" + hex.EncodeToString(scope[:8]))
}

func saveMaintenance(path string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".maintenance-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func init() {
	migrate := &cobra.Command{Use: "migrate", Short: "Plan and verify an in-place host scheduler migration"}
	plan := &cobra.Command{Use: "plan", Args: cobra.NoArgs, Short: "Read native scheduler inventory on stdin and propose prompt-only changes", RunE: func(cmd *cobra.Command, _ []string) error {
		var in heartbeatmigration.Inventory
		if err := readMaintenanceInput(cmd, &in); err != nil {
			return err
		}
		meta := clientMetaForServerName(activeServerName())
		if meta.Mode == "plugin" {
			return fmt.Errorf("plugin owns scheduling; native task migration is not applicable")
		}
		home, server := config.HomeDir(), activeServerName()
		if server == "" {
			return fmt.Errorf("explicit configured server required")
		}
		p, err := heartbeatmigration.Build(in, home, server, heartbeatLauncher(home, server, "skill"))
		if err != nil {
			return err
		}
		token := make([]byte, 16)
		if _, err = rand.Read(token); err != nil {
			return err
		}
		r := migrationRecord{ID: hex.EncodeToString(token), Home: home, Server: server, Host: meta.Host, Created: time.Now(), Plan: p}
		if p.Status == "update" {
			if err = saveMaintenance(migrationPendingPath(meta.Host), r); err != nil {
				return err
			}
		}
		output.PrintData(r, resolveFormat())
		return nil
	}}
	verify := &cobra.Command{Use: "verify", Args: cobra.NoArgs, Short: "Validate a fresh native scheduler readback and persist the migration receipt", RunE: func(cmd *cobra.Command, _ []string) error {
		var in struct {
			PlanID string `json:"plan_id"`
			heartbeatmigration.Inventory
		}
		if err := readMaintenanceInput(cmd, &in); err != nil {
			return err
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(in.PlanID) {
			return fmt.Errorf("invalid plan ID")
		}
		meta := clientMetaForServerName(activeServerName())
		path := migrationPendingPath(meta.Host)
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var r migrationRecord
		pending := err == nil
		if pending {
			if err = json.Unmarshal(b, &r); err != nil {
				return err
			}
			pending = r.ID == in.PlanID
		}
		if !pending {
			b, err = os.ReadFile(migrationReceiptPath(meta.Host))
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &r); err != nil {
				return err
			}
			if r.Verified.IsZero() {
				return fmt.Errorf("migration receipt is not verified")
			}
		}
		if r.ID != in.PlanID {
			return fmt.Errorf("migration plan ID mismatch")
		}
		if r.Home != config.HomeDir() || r.Server != activeServerName() || r.Host != meta.Host || meta.Mode == "plugin" {
			return fmt.Errorf("migration identity/runtime mismatch")
		}
		if time.Since(r.Created) > time.Hour || time.Since(r.Created) < 0 {
			return fmt.Errorf("migration plan expired; read scheduler again")
		}
		if err = heartbeatmigration.Verify(r.Plan, in.Inventory); err != nil {
			return err
		}
		r.Verified = time.Now()
		if err = saveMaintenance(migrationReceiptPath(meta.Host), r); err != nil {
			return err
		}
		// Retain one expiring plan snapshot. Deleting it here could remove a
		// newer plan written concurrently; only a new update plan replaces it.
		output.PrintData(map[string]interface{}{"status": "verified", "migration_version": heartbeatmigration.Version, "task_id": r.Plan.TaskID}, resolveFormat())
		return nil
	}}
	plan.Flags().Bool("stdin", false, "read complete inventory from stdin")
	verify.Flags().Bool("stdin", false, "read plan_id and fresh inventory from stdin")
	migrate.AddCommand(plan, verify)
	heartbeatCmd.AddCommand(migrate)
}
