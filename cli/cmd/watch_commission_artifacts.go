package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/dispatch"
)

const (
	commissionArtifactMaxCount = 128
	commissionArtifactMaxBytes = int64(64 << 20)
)

// validateCommissionArtifacts verifies listed outputs without publishing them.
// Paths are confined to the output directory; hard-link provenance is not
// established by portable filesystem metadata. Digests always cover read bytes.
func validateCommissionArtifacts(directory string, decision dispatch.CommissionFulfillmentDecision) ([]dispatch.CommissionVerifiedArtifact, error) {
	if len(decision.Artifacts) > commissionArtifactMaxCount || (decision.Outcome == "artifacts_ready" && len(decision.Artifacts) == 0) {
		return nil, errors.New("commission_artifact_count_invalid")
	}
	verified := make([]dispatch.CommissionVerifiedArtifact, 0, len(decision.Artifacts))
	if len(decision.Artifacts) == 0 {
		return verified, nil
	}
	logicalPaths, relativePaths := map[string]bool{}, map[string]bool{}
	for _, artifact := range decision.Artifacts {
		if !validCommissionArtifactPath(artifact.LogicalPath) || !validCommissionArtifactPath(artifact.RelativePath) {
			return nil, errors.New("commission_artifact_path_invalid")
		}
		if logicalPaths[artifact.LogicalPath] || relativePaths[artifact.RelativePath] {
			return nil, errors.New("commission_artifact_path_duplicate")
		}
		logicalPaths[artifact.LogicalPath], relativePaths[artifact.RelativePath] = true, true
	}
	if !filepath.IsAbs(directory) {
		return nil, errors.New("commission_artifact_directory_invalid")
	}
	dirInfo, err := os.Lstat(directory)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("commission_artifact_directory_invalid")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("commission_artifact_directory_unavailable")
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(dirInfo, opened) {
		return nil, errors.New("commission_artifact_directory_changed")
	}
	remaining := commissionArtifactMaxBytes
	for _, artifact := range decision.Artifacts {
		info, err := commissionArtifactPathInfo(root, artifact.RelativePath)
		if err != nil {
			return nil, err
		}
		sum, size, err := hashCommissionArtifact(root, artifact.RelativePath, info, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= size
		verified = append(verified, dispatch.CommissionVerifiedArtifact{
			LogicalPath: artifact.LogicalPath, RelativePath: artifact.RelativePath,
			SHA256: sum, ByteSize: size,
		})
	}
	current, err := os.Lstat(directory)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(dirInfo, current) {
		return nil, errors.New("commission_artifact_directory_changed")
	}
	return verified, nil
}

func validCommissionArtifactPath(value string) bool {
	if !utf8.ValidString(value) || !fs.ValidPath(value) || value == "." || strings.ContainsAny(value, "\\:") {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return false
		}
	}
	return true
}

func commissionArtifactPathInfo(root *os.Root, relativePath string) ([]os.FileInfo, error) {
	parts := strings.Split(relativePath, "/")
	infos := make([]os.FileInfo, 0, len(parts))
	for index := range parts {
		info, err := root.Lstat(filepath.Join(parts[:index+1]...))
		if err != nil {
			return nil, errors.New("commission_artifact_file_unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) || (index == len(parts)-1 && !info.Mode().IsRegular()) {
			return nil, errors.New("commission_artifact_file_not_regular")
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func hashCommissionArtifact(root *os.Root, relativePath string, before []os.FileInfo, maxBytes int64) (string, int64, error) {
	file, err := root.Open(filepath.FromSlash(relativePath))
	if err != nil {
		return "", 0, errors.New("commission_artifact_file_unavailable")
	}
	defer file.Close()
	initial := before[len(before)-1]
	opened, err := file.Stat()
	if err != nil || !sameCommissionArtifactFile(initial, opened) {
		return "", 0, errors.New("commission_artifact_file_changed")
	}
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", 0, errors.New("commission_artifact_read_failed")
	}
	if size > maxBytes {
		return "", 0, errors.New("commission_artifact_size_limit")
	}
	finished, err := file.Stat()
	if err != nil || !sameCommissionArtifactFile(initial, finished) || finished.Size() != size {
		return "", 0, errors.New("commission_artifact_file_changed")
	}
	after, err := commissionArtifactPathInfo(root, relativePath)
	if err != nil {
		return "", 0, err
	}
	for index := range before {
		if !os.SameFile(before[index], after[index]) {
			return "", 0, errors.New("commission_artifact_file_changed")
		}
	}
	if !sameCommissionArtifactFile(initial, after[len(after)-1]) {
		return "", 0, errors.New("commission_artifact_file_changed")
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}

func sameCommissionArtifactFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && before.Mode().IsRegular() && after.Mode().IsRegular() &&
		os.SameFile(before, after) && before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}
