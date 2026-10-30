package cmd

import (
	"errors"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/skills"
)

func TestUpgradeSkillsRequiresVerifiedLatestSync(t *testing.T) {
	cases := []struct {
		name   string
		result *skills.SyncResult
		err    error
		reason string
	}{
		{"fresh", &skills.SyncResult{Source: "cli/latest", VerifiedManifest: true}, nil, ""},
		{"unchanged_latest", &skills.SyncResult{Source: "local", VerifiedManifest: true}, nil, ""},
		{"download_kept_local", &skills.SyncResult{Source: "local", Diagnostics: []skills.SyncDiagnostic{{Reason: "download failed"}}}, nil, "skills_latest_unverified"},
		{"offline_kept_local", &skills.SyncResult{Source: "local", NoNetwork: true}, nil, "skills_latest_unavailable"},
		{"rollback_kept_local", &skills.SyncResult{Source: "local", Diagnostics: []skills.SyncDiagnostic{{Reason: "signed manifest rollback rejected"}}}, nil, "skills_rollback_rejected"},
		{"sequence_reused", &skills.SyncResult{Source: "local", Diagnostics: []skills.SyncDiagnostic{{Reason: "signed manifest sequence reused for different content"}}}, nil, "skills_sequence_conflict"},
		{"needs_new_cli", &skills.SyncResult{RequiredCLIVersion: "99.0.0"}, nil, "skills_cli_upgrade_required"},
		{"modified_preserved", &skills.SyncResult{Preserved: []string{"ef-commission"}}, nil, "skills_local_modified"},
		{"provisional", &skills.SyncResult{Stale: true}, nil, "skills_provisional"},
		{"nil_result", nil, nil, "skills_latest_unverified"},
		{"sync_error", nil, errors.New("private URL token=secret"), "skills_sync_failed"},
		{"diagnostics_with_verified_flag", &skills.SyncResult{VerifiedManifest: true, Diagnostics: []skills.SyncDiagnostic{{Reason: "retained"}}}, nil, "skills_latest_unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localChecks := 0
			err := verifyUpgradeWatchRules(true, func() (*skills.SyncResult, error) {
				return tc.result, tc.err
			}, func() error { localChecks++; return nil })
			if tc.reason == "" {
				if err != nil || localChecks != 1 {
					t.Fatalf("expected latest sync and local validation: err=%v checks=%d", err, localChecks)
				}
				return
			}
			var failure *upgradeSkillsError
			if !errors.As(err, &failure) || failure.Reason != tc.reason || err.Error() != tc.reason || localChecks != 0 {
				t.Fatalf("unexpected sync acceptance: err=%v checks=%d", err, localChecks)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatal("lost underlying diagnostic")
			}
		})
	}
}

func TestUpgradeSkillsDisabledSyncValidatesLocallyWithoutNetwork(t *testing.T) {
	for _, valid := range []bool{true, false} {
		checks := 0
		cause := errors.New("private local path")
		err := verifyUpgradeWatchRules(false, func() (*skills.SyncResult, error) {
			t.Fatal("owner-disabled automatic sync accessed network")
			return nil, nil
		}, func() error {
			checks++
			if !valid {
				return cause
			}
			return nil
		})
		if checks != 1 {
			t.Fatalf("local checks = %d", checks)
		}
		if valid && err != nil {
			t.Fatal(err)
		}
		if !valid && (!errors.Is(err, cause) || err.Error() != "skills_local_validation_failed") {
			t.Fatalf("expected bounded local failure, got %v", err)
		}
	}
}

func TestUpgradeSkillsFreshSyncStillVerifiesInstalledFiles(t *testing.T) {
	cause := errors.New("signature mismatch at private path")
	err := verifyUpgradeWatchRules(true, func() (*skills.SyncResult, error) {
		return &skills.SyncResult{VerifiedManifest: true}, nil
	}, func() error { return cause })
	if !errors.Is(err, cause) || err.Error() != "skills_local_validation_failed" || strings.Contains(err.Error(), "private") {
		t.Fatalf("unexpected local acceptance: %v", err)
	}
}
