package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestCommissionGenerationReusesOnlyReviewedBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "draft"), 0700); err != nil {
		t.Fatal(err)
	}
	location := filepath.Join(dir, "draft", "song.txt")
	if err := os.WriteFile(location, []byte("chorus"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := snapshotCommissionGenerationArtifacts(dir)
	if err != nil || len(before) != 1 || before[0].RelativePath != "draft/song.txt" {
		t.Fatalf("inventory = %+v, %v", before, err)
	}
	decision := commissionArtifactDecision("draft/song.txt")
	decision.Artifacts[0].LogicalPath = "lyrics.md"
	verified, err := validateRecoveredCommissionArtifacts(dir, decision, before)
	if err != nil || len(verified) != 1 || verified[0].SHA256 != before[0].SHA256 || verified[0].LogicalPath != "lyrics.md" {
		t.Fatalf("recovery = %+v, %v", verified, err)
	}
	t.Run("incomplete contract", func(t *testing.T) {
		d := decision
		d.Outcome = "needs_user"
		if got, err := validateRecoveredCommissionArtifacts(dir, d, before); err == nil || got != nil {
			t.Fatalf("adopted unchecked partial work: %+v %v", got, err)
		}
	})
	t.Run("missing contracted file", func(t *testing.T) {
		d := commissionArtifactDecision("draft/song.txt", "guide.md")
		if got, err := validateRecoveredCommissionArtifacts(dir, d, before); err == nil || got != nil {
			t.Fatalf("adopted incomplete manifest: %+v %v", got, err)
		}
	})
	t.Run("unreviewed file", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "added.txt"), []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := validateRecoveredCommissionArtifacts(dir, commissionArtifactDecision("added.txt"), before); err == nil || got != nil {
			t.Fatalf("adopted unreviewed artifact: %+v %v", got, err)
		}
	})
	t.Run("changed same-length bytes", func(t *testing.T) {
		if err := os.WriteFile(location, []byte("CHORUS"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := validateRecoveredCommissionArtifacts(dir, decision, before); err == nil || got != nil {
			t.Fatalf("adopted changed artifact: %+v %v", got, err)
		}
	})
}

func TestCommissionGenerationInventoryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if files, err := snapshotCommissionGenerationArtifacts(dir); err != nil || len(files) != 0 {
		t.Fatalf("empty attempt: %+v, %v", files, err)
	}
	if files, err := snapshotCommissionGenerationArtifacts(filepath.Join(dir, "missing")); err == nil || files != nil {
		t.Fatalf("missing attempt treated as empty: %+v, %v", files, err)
	}
	t.Run("symlink", func(t *testing.T) {
		tree := t.TempDir()
		if err := os.Symlink(dir, filepath.Join(tree, "link")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if files, err := snapshotCommissionGenerationArtifacts(tree); err == nil || files != nil {
			t.Fatalf("symlink attempt: %+v, %v", files, err)
		}
	})
	t.Run("limit", func(t *testing.T) {
		for i := 0; i <= commissionArtifactMaxCount; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if files, err := snapshotCommissionGenerationArtifacts(dir); err == nil || files != nil {
			t.Fatalf("unbounded candidate list accepted: %v, %v", len(files), err)
		}
	})
}

func TestCommissionGenerationVerificationRetainsContract(t *testing.T) {
	original := dispatch.Request{ID: "job", Kind: "commission_fulfillment", Prompt: "frozen contract and material paths"}
	v := commissionGenerationVerificationPrompt(original)
	if !v.GenerationVerifyOnly || v.ID != original.ID || v.Kind != original.Kind || !strings.HasSuffix(v.Prompt, original.Prompt) {
		t.Fatalf("invalid review request: %+v", v)
	}
}
