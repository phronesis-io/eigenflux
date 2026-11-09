package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/skills"
)

func upgradeRepairReleaseFixture(t *testing.T) (string, func(string, bool)) {
	t.Helper()
	dir := t.TempDir()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := skills.VerifyPublicKeyBase64
	skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { skills.VerifyPublicKeyBase64 = oldKey })
	var sequence uint64
	write := func(displayVersion string, includeRepair bool) {
		t.Helper()
		root := filepath.Join(dir, "ef-onboarding")
		if err := os.MkdirAll(filepath.Join(root, "references"), 0700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("---\nname: ef-onboarding\nmetadata:\n  version: %q\n---\nOnboarding rules.\n", displayVersion)
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "references", "upgrade-repair.md")
		if includeRepair {
			if err := os.WriteFile(path, []byte("Perform scoped repair under existing authority.\n"), 0600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		m, err := skills.GenerateManifest(dir, "1.0.23", "1.0.23", []string{"ef-onboarding"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Skills) != 1 || m.Skills[0].DisplayVersion != displayVersion {
			t.Fatalf("fixture lost signed capability version: %+v", m.Skills)
		}
		sequence++
		m.Sequence = sequence
		m.TarSHA256 = strings.Repeat("a", 64)
		if err := skills.SignManifest(m, key); err != nil {
			t.Fatal(err)
		}
		if err := skills.WriteManifestAtomic(dir, m); err != nil {
			t.Fatal(err)
		}
	}
	return dir, write
}

func TestUpgradeRepairWaitsQuietlyForCompanionSkillsRelease(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	dir, write := upgradeRepairReleaseFixture(t)
	write("0.3.39", false)
	r, err := currentUpgradeRepair(true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readUpgradeRepairRules(dir, true)
	var gap *upgradeSkillsError
	if !errors.As(err, &gap) || gap.Reason != "skills_release_pending" {
		t.Fatalf("valid old release treated as corruption: %v", err)
	}
	state, err := recordUpgradeSkillsFailure(r.ID, err)
	if err != nil || state.Status != "pending" || state.NextAttempt == 0 || state.Notification != "" {
		t.Fatalf("release window forced owner action: %+v %v", state, err)
	}
	write("0.3.40", true)
	data, err := readUpgradeRepairRules(dir, true)
	if err != nil || !strings.Contains(string(data), "scoped repair") {
		t.Fatalf("companion release could not resume automatically: %q %v", data, err)
	}
	state, err = currentUpgradeRepair(false)
	if err != nil || state.ID != r.ID || state.Status != "pending" {
		t.Fatalf("valid publication lost request: %+v %v", state, err)
	}
}

func TestUpgradeRepairOldReleaseRespectsDisabledAutomaticSync(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	dir, write := upgradeRepairReleaseFixture(t)
	write("0.3.39", false)
	r, _ := currentUpgradeRepair(true)
	_, cause := readUpgradeRepairRules(dir, false)
	state, err := recordUpgradeSkillsFailure(r.ID, cause)
	if err != nil || state.Status != "deferred" || state.NextAttempt != 0 || state.Notification != "required" {
		t.Fatalf("disabled sync claimed automatic recovery: %+v %v", state, err)
	}
}

func TestUpgradeRepairReleaseGapNeverHidesCorruptionOrNewContractFailure(t *testing.T) {
	for _, scenario := range []string{"new-contract-missing", "new-file-deleted", "old-file-modified", "bad-signature"} {
		t.Run(scenario, func(t *testing.T) {
			runtimeTestConfig(t, "https://repair.example.invalid", true)
			dir, write := upgradeRepairReleaseFixture(t)
			switch scenario {
			case "new-contract-missing":
				write("0.3.40", false)
			case "new-file-deleted":
				write("0.3.40", true)
				if err := os.Remove(filepath.Join(dir, "ef-onboarding", "references", "upgrade-repair.md")); err != nil {
					t.Fatal(err)
				}
			case "old-file-modified":
				write("0.3.39", false)
				if err := os.WriteFile(filepath.Join(dir, "ef-onboarding", "SKILL.md"), []byte("modified"), 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-signature":
				write("0.3.39", false)
				m, _ := skills.ReadLocalManifest(dir)
				m.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
				if err := skills.WriteManifestAtomic(dir, m); err != nil {
					t.Fatal(err)
				}
			}
			r, _ := currentUpgradeRepair(true)
			_, cause := readUpgradeRepairRules(dir, true)
			if cause == nil {
				t.Fatal("invalid signed installation executed")
			}
			state, err := recordUpgradeSkillsFailure(r.ID, cause)
			if err != nil || state.Status != "blocked" || state.Notification != "required" {
				t.Fatalf("corruption downgraded to release gap: %+v %v", state, err)
			}
		})
	}
}

func TestUpgradeRepairConcurrentSignedWriterRetainsQuietRetry(t *testing.T) {
	runtimeTestConfig(t, "https://repair.example.invalid", true)
	r, _ := currentUpgradeRepair(true)
	state, err := recordUpgradeSkillsFailure(r.ID, &upgradeSkillsError{Reason: "skills_sync_in_progress", Err: skills.ErrSyncInProgress})
	if err != nil || state.Status != "pending" || state.Notification != "" || state.NextAttempt == 0 {
		t.Fatalf("writer contention requested owner intervention: %+v %v", state, err)
	}
}
