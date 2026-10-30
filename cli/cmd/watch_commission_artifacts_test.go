package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func commissionArtifactDecision(paths ...string) dispatch.CommissionFulfillmentDecision {
	decision := dispatch.CommissionFulfillmentDecision{Outcome: "artifacts_ready"}
	for _, path := range paths {
		decision.Artifacts = append(decision.Artifacts, dispatch.CommissionArtifact{LogicalPath: path, RelativePath: path})
	}
	return decision
}

func TestValidateCommissionArtifactsHashesActualBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "reports"), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("完成的报告\n")
	if err := os.WriteFile(filepath.Join(dir, "reports", "summary.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.bin"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	decision := commissionArtifactDecision("reports/summary.txt", "empty.bin")
	decision.Artifacts[0].LogicalPath = "deliverables/报告.txt"
	got, err := validateCommissionArtifacts(dir, decision)
	if err != nil || len(got) != 2 {
		t.Fatalf("verify artifacts: %+v %v", got, err)
	}
	for index, data := range [][]byte{content, nil} {
		digest := sha256.Sum256(data)
		if got[index].SHA256 != hex.EncodeToString(digest[:]) || got[index].ByteSize != int64(len(data)) || got[index].LogicalPath != decision.Artifacts[index].LogicalPath || got[index].RelativePath != decision.Artifacts[index].RelativePath {
			t.Fatalf("wrong actual artifact facts: %+v", got[index])
		}
	}
}

func TestValidateCommissionArtifactsRejectsNonCanonicalAndDuplicatePaths(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"", ".", "..", "../secret", "folder/../file", "/etc/passwd", "folder//file", "folder/./file", "folder/", `folder\file`, `C:/secret`, `C:secret`, `//server/share/file`, `\\server\share\file`, "file:stream", "file\x00", "file\n", "file\u0085", "file\u202e", string([]byte{0xff})} {
		for _, field := range []string{"logical", "relative"} {
			decision := commissionArtifactDecision("good.txt")
			if field == "logical" {
				decision.Artifacts[0].LogicalPath = bad
			} else {
				decision.Artifacts[0].RelativePath = bad
			}
			if got, err := validateCommissionArtifacts(dir, decision); err == nil || got != nil || err.Error() != "commission_artifact_path_invalid" {
				t.Fatalf("accepted unsafe %s path %q: %+v %v", field, bad, got, err)
			}
		}
	}
	for _, field := range []string{"logical", "relative"} {
		decision := commissionArtifactDecision("one.txt", "two.txt")
		if field == "logical" {
			decision.Artifacts[1].LogicalPath = decision.Artifacts[0].LogicalPath
		} else {
			decision.Artifacts[1].RelativePath = decision.Artifacts[0].RelativePath
		}
		if got, err := validateCommissionArtifacts(dir, decision); err == nil || got != nil || err.Error() != "commission_artifact_path_duplicate" {
			t.Fatalf("duplicate %s path accepted: %+v %v", field, got, err)
		}
	}
}

func TestValidateCommissionArtifactsRejectsSymlinksAndNonFiles(t *testing.T) {
	for _, mode := range []string{"file-link", "directory-link", "output-link", "directory", "missing"} {
		t.Run(mode, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			decision := commissionArtifactDecision("result.txt")
			var err error
			switch mode {
			case "file-link":
				err = os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "result.txt"))
			case "directory-link":
				err = os.Symlink(outside, filepath.Join(dir, "linked"))
				decision.Artifacts[0].RelativePath = "linked/secret.txt"
			case "output-link":
				link := filepath.Join(dir, "output")
				err = os.Symlink(outside, link)
				dir = link
				decision.Artifacts[0].RelativePath = "secret.txt"
			case "directory":
				err = os.Mkdir(filepath.Join(dir, "result.txt"), 0700)
			}
			if err != nil {
				t.Skipf("fixture unavailable: %v", err)
			}
			got, err := validateCommissionArtifacts(dir, decision)
			if err == nil || got != nil {
				t.Fatalf("unsafe artifact accepted: %+v %v", got, err)
			}
			if strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("artifact error leaked a path: %v", err)
			}
		})
	}
}

func TestValidateCommissionArtifactsBoundsCountAndTotalActualBytes(t *testing.T) {
	dir := t.TempDir()
	if got, err := validateCommissionArtifacts(dir, commissionArtifactDecision()); err == nil || got != nil {
		t.Fatal("artifacts_ready accepted without artifacts")
	}
	if got, err := validateCommissionArtifacts("", dispatch.CommissionFulfillmentDecision{Outcome: "needs_user"}); err != nil || len(got) != 0 {
		t.Fatalf("empty non-ready result rejected: %+v %v", got, err)
	}
	tooMany := commissionArtifactDecision()
	tooMany.Artifacts = make([]dispatch.CommissionArtifact, commissionArtifactMaxCount+1)
	if got, err := validateCommissionArtifacts(dir, tooMany); err == nil || got != nil || err.Error() != "commission_artifact_count_invalid" {
		t.Fatal("oversized artifact list accepted")
	}
	for _, name := range []string{"one.bin", "two.bin"} {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(commissionArtifactMaxBytes / 2); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	decision := commissionArtifactDecision("one.bin", "two.bin")
	if got, err := validateCommissionArtifacts(dir, decision); err != nil || len(got) != 2 || got[0].ByteSize+got[1].ByteSize != commissionArtifactMaxBytes {
		t.Fatalf("exact aggregate byte limit rejected: %+v %v", got, err)
	}
	if err := os.Truncate(filepath.Join(dir, "two.bin"), commissionArtifactMaxBytes/2+1); err != nil {
		t.Fatal(err)
	}
	if got, err := validateCommissionArtifacts(dir, decision); err == nil || got != nil || err.Error() != "commission_artifact_size_limit" {
		t.Fatalf("aggregate byte limit bypassed: %+v %v", got, err)
	}
}

func TestHashCommissionArtifactRejectsReplacementAndActualReadOverflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.txt")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := commissionArtifactPathInfo(root, "result.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(dir, "previous.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hashCommissionArtifact(root, "result.txt", before, 100); err == nil || err.Error() != "commission_artifact_file_changed" {
		t.Fatalf("replacement after inspection accepted: %v", err)
	}
	before, err = commissionArtifactPathInfo(root, "result.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := hashCommissionArtifact(root, "result.txt", before, 4); err == nil || err.Error() != "commission_artifact_size_limit" {
		t.Fatalf("actual streamed byte limit bypassed: %v", err)
	}
}
