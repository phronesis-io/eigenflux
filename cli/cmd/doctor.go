package cmd

import (
	"fmt"
	"os"

	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

type doctorReport struct {
	CLIVersion    string              `json:"cli_version"`
	LatestVersion string              `json:"latest_version"`
	Outdated      bool                `json:"outdated"`
	SkillsDir     string              `json:"skills_dir"`
	HostDetected  string              `json:"host_detected"`
	Writable      bool                `json:"writable"`
	Stale         bool                `json:"stale"`
	Skills        []skills.LocalSkill `json:"skills"`
	Hint          string              `json:"hint,omitempty"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose CLI + skills health (version drift, sha_match, writability)",
	Long: `Report the CLI version against the latest published release, the resolved
skills directory and its per-skill sha_match, and whether the skills dir is
writable. Use this to confirm 'skills sync' landed and that no skill was
silently corrupted or hand-modified.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		host, _ := cmd.Flags().GetString("host")

		rep := doctorReport{CLIVersion: version}
		rep.LatestVersion = skills.FetchLatestVersion(cdnBase(), nil)
		rep.Outdated = rep.LatestVersion != "" && rep.LatestVersion != version

		dir, list, _, err := skills.ListLocal("", host)
		rep.SkillsDir = dir
		rep.Skills = list
		rep.HostDetected = host
		if rep.HostDetected == "" {
			rep.HostDetected = "auto"
		}
		rep.Writable = isWritableDir(dir)
		if err == nil {
			rep.Stale = skillsStale(dir)
		}
		if rep.Outdated {
			rep.Hint = "skills follow the CLI version — upgrade the CLI to get newer skills: curl -fsSL https://www.eigenflux.ai/install.sh | sh"
		}

		exit := output.ExitSuccess
		anyModified := false
		for _, s := range rep.Skills {
			if !s.SHAMatch {
				anyModified = true
			}
		}
		if !rep.Writable || anyModified {
			exit = output.ExitError // hard issue: dir not writable, or a skill drifted from its hash
		}

		if resolveFormat() == "table" {
			printDoctorTable(rep)
		} else {
			output.PrintData(rep, resolveFormat())
		}
		if exit != output.ExitSuccess {
			os.Exit(exit)
		}
		return nil
	},
}

func printDoctorTable(rep doctorReport) {
	fmt.Printf("CLI:        %s", rep.CLIVersion)
	if rep.LatestVersion != "" {
		if rep.Outdated {
			fmt.Printf("  (latest %s — OUTDATED)", rep.LatestVersion)
		} else {
			fmt.Printf("  (latest)")
		}
	}
	fmt.Println()
	fmt.Printf("Skills dir: %s (host=%s, writable=%t, stale=%t)\n", rep.SkillsDir, rep.HostDetected, rep.Writable, rep.Stale)
	if len(rep.Skills) == 0 {
		fmt.Println("  (no skills installed — run 'eigenflux skills sync')")
	}
	for _, s := range rep.Skills {
		flag := "ok"
		if !s.SHAMatch {
			flag = "MODIFIED"
		}
		ver := s.DisplayVersion
		if ver == "" {
			ver = "-"
		}
		fmt.Printf("  %-18s %-8s %s\n", s.Name, ver, flag)
	}
	if rep.Hint != "" {
		fmt.Printf("\n%s\n", rep.Hint)
	}
}

func isWritableDir(dir string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".ef-doctor-*.tmp")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func skillsStale(dir string) bool {
	_, err := os.Stat(dir + string(os.PathSeparator) + skills.StaleMarkerName)
	return err == nil
}

func init() {
	doctorCmd.Flags().String("host", "", "openclaw|claude-code|codex|terminal")
	rootCmd.AddCommand(doctorCmd)
}
