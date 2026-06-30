package cmd

import (
	"fmt"
	"os"

	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

// cdnBase resolves the CDN base for skill downloads, honoring EIGENFLUX_CDN_URL
// (the same override install.sh uses) before the public default.
func cdnBase() string {
	if v := os.Getenv("EIGENFLUX_CDN_URL"); v != "" {
		return v
	}
	return skills.CDNDefault
}

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Manage EigenFlux skills (synced from R2, not bundled in plugins)",
	Long: `Manage the EigenFlux skill set installed into this host's skill-load
directory. Skills ship alongside the CLI on R2 and are pulled in by
'eigenflux skills sync' — so updating a skill is a CLI release, not a plugin
republish.

Examples:
  eigenflux skills sync                 # pull latest for this CLI version
  eigenflux skills sync --if-stale --quiet   # startup-hook form (offline-safe)
  eigenflux skills list
  eigenflux skills path`,
}

var skillsSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync skills from R2 (atomic swap + sha256 verify + reconcile)",
	Long: `Download the skills bundle for this CLI version from R2, verify it, and
atomically swap it into the host's skill-load directory. Skills you placed
yourself or hand-edited are preserved; only skills this tool installed are
reconciled away when they leave the manifest.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		into, _ := cmd.Flags().GetString("into")
		host, _ := cmd.Flags().GetString("host")
		ifStale, _ := cmd.Flags().GetBool("if-stale")
		quiet, _ := cmd.Flags().GetBool("quiet")
		res, err := skills.Sync(skills.SyncOptions{
			Into:       into,
			Host:       host,
			IfStale:    ifStale,
			Quiet:      quiet,
			CLIVersion: version,
			CDNBase:    cdnBase(),
		})
		if err != nil {
			// Generic failure (network/IO/checksum) — NOT auth. Using exit 4
			// here would make hooks misread a CDN outage as "re-login needed".
			output.Die(output.ExitError, "skills sync: %v", err)
		}
		if resolveFormat() == "table" {
			fmt.Printf("skills %s -> %s (source=%s, removed=%d, atomic=%t, stale=%t)\n",
				res.CLIVersion, res.SkillsDir, res.Source, len(res.Removed), res.Atomic, res.Stale)
			if len(res.Preserved) > 0 {
				fmt.Printf("  note: %d skill(s) kept on local edits, update skipped: %v\n", len(res.Preserved), res.Preserved)
			}
			return nil
		}
		output.PrintData(res, resolveFormat())
		return nil
	},
}

var skillsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed EigenFlux skills with live sha_match",
	RunE: func(cmd *cobra.Command, _ []string) error {
		into, _ := cmd.Flags().GetString("into")
		host, _ := cmd.Flags().GetString("host")
		dir, list, managed, err := skills.ListLocal(into, host)
		if err != nil {
			output.Die(output.ExitNotFound, "skills list: %v", err)
		}
		if resolveFormat() == "table" {
			fmt.Printf("skills dir: %s (managed=%t)\n", dir, managed)
			if len(list) == 0 {
				fmt.Println("  (none installed)")
				return nil
			}
			for _, s := range list {
				ver := s.DisplayVersion
				if ver == "" {
					ver = "-"
				}
				flag := "ok"
				if !s.SHAMatch {
					flag = "MODIFIED"
				}
				fmt.Printf("  %-18s %-8s %s (%s)\n", s.Name, ver, s.SHA256[:min(8, len(s.SHA256))], flag)
			}
			return nil
		}
		output.PrintData(map[string]any{"skills_dir": dir, "managed": managed, "skills": list}, resolveFormat())
		return nil
	},
}

var skillsPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print this host's real skills-load directory",
	RunE: func(cmd *cobra.Command, _ []string) error {
		into, _ := cmd.Flags().GetString("into")
		host, _ := cmd.Flags().GetString("host")
		dir, err := skills.ResolveSkillsDir(into, host)
		if err != nil {
			output.Die(output.ExitUsageError, "skills path: %v", err)
		}
		fmt.Println(dir)
		return nil
	},
}

var skillsInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install skills from a local bundle directory (offline / dev)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		into, _ := cmd.Flags().GetString("into")
		host, _ := cmd.Flags().GetString("host")
		bundle, _ := cmd.Flags().GetString("from-bundle")
		if bundle == "" {
			output.Die(output.ExitUsageError, "skills install: --from-bundle <dir> required")
		}
		res, err := skills.InstallFromBundle(skills.SyncOptions{
			Into: into, Host: host, BundleDir: bundle, CLIVersion: version,
		})
		if err != nil {
			output.Die(output.ExitUsageError, "skills install: %v", err)
		}
		if resolveFormat() == "table" {
			fmt.Printf("skills installed -> %s (source=%s, atomic=%t)\n", res.SkillsDir, res.Source, res.Atomic)
			if len(res.Preserved) > 0 {
				fmt.Printf("  note: %d skill(s) kept on local edits, update skipped: %v\n", len(res.Preserved), res.Preserved)
			}
			return nil
		}
		output.PrintData(res, resolveFormat())
		return nil
	},
}

func init() {
	skillsSyncCmd.Flags().String("into", "", "explicit skills dir (overrides host detection)")
	skillsSyncCmd.Flags().String("host", "", "openclaw|claude-code|codex|terminal")
	skillsSyncCmd.Flags().Bool("if-stale", false, "skip network when local cli_version already matches")
	skillsSyncCmd.Flags().Bool("quiet", false, "never fail (exit 0); for startup hooks")

	skillsListCmd.Flags().String("into", "", "explicit skills dir")
	skillsListCmd.Flags().String("host", "", "openclaw|claude-code|codex|terminal")

	skillsPathCmd.Flags().String("into", "", "explicit skills dir")
	skillsPathCmd.Flags().String("host", "", "openclaw|claude-code|codex|terminal")

	skillsInstallCmd.Flags().String("into", "", "explicit skills dir")
	skillsInstallCmd.Flags().String("host", "", "openclaw|claude-code|codex|terminal")
	skillsInstallCmd.Flags().String("from-bundle", "", "local skills directory to install from")

	skillsCmd.AddCommand(skillsSyncCmd, skillsListCmd, skillsPathCmd, skillsInstallCmd)
	rootCmd.AddCommand(skillsCmd)
}
