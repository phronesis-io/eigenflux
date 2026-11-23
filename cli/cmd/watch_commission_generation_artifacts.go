package cmd

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"

	"cli.eigenflux.ai/internal/dispatch"
)

// An interrupted generation has no authoritative logical paths yet. Pin the
// candidate bytes before asking a read-only verifier to map them to the contract.
func snapshotCommissionGenerationArtifacts(directory string) ([]dispatch.CommissionVerifiedArtifact, error) {
	info, err := os.Lstat(directory)
	if err != nil || !filepath.IsAbs(directory) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("commission_artifact_directory_invalid")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("commission_artifact_directory_changed")
	}

	// Read directory entries in bounded batches, including directories with
	// no deliverables. Refuse links, devices and unbounded scratch trees.
	dirs := []string{"."}
	var candidates dispatch.CommissionFulfillmentDecision
	scanDir := func(name string) error {
		dir, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			entries, readErr := dir.ReadDir(64)
			for _, entry := range entries {
				relative := path.Join(name, entry.Name())
				if !validCommissionArtifactPath(relative) || len(relative) > 1024 {
					return errors.New("commission_artifact_path_invalid")
				}
				switch {
				case entry.IsDir():
					dirs = append(dirs, relative)
				case entry.Type().IsRegular():
					candidates.Artifacts = append(candidates.Artifacts, dispatch.CommissionArtifact{LogicalPath: relative, RelativePath: relative})
				default:
					return errors.New("commission_artifact_file_not_regular")
				}
				if len(candidates.Artifacts) > commissionArtifactMaxCount || len(dirs) > 1024 {
					return errors.New("commission_artifact_count_invalid")
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	for i := 0; i < len(dirs); i++ {
		if err := scanDir(dirs[i]); err != nil {
			return nil, err
		}
	}
	verified, err := validateCommissionArtifacts(directory, candidates)
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(directory)
	if err != nil || !os.SameFile(info, current) {
		return nil, errors.New("commission_artifact_directory_changed")
	}
	return verified, nil
}

func commissionGenerationVerificationPrompt(req dispatch.Request) dispatch.Request {
	req.GenerationVerifyOnly = true
	req.Prompt = "EIGENFLUX CLI VERIFICATION-ONLY MODE. Inspect the existing paid Order output_directory in read-only mode. Reuse the supplied fulfillment return protocol, frozen contract, inputs and business acceptance criteria. Read each candidate in existing_relative_paths under output_directory. Map its physical relative_path to the logical_path required for upload by the frozen contract. Check all required deliverables and their actual bytes. Return artifacts_ready only for the complete, unchanged contractual files from the interrupted attempt. Return needs_user if any obligation, file, or check is missing or uncertain. This invocation verifies existing files only; never create, repair, rewrite, or publish artifacts.\n\n" + req.Prompt
	return req
}

// The caller has already checked the receipt identity. Logical paths come from
// the contract-aware verifier; physical paths must identify the same bytes read
// before verification. Delivery will independently freeze and rehash them.
func validateRecoveredCommissionArtifacts(directory string, decision dispatch.CommissionFulfillmentDecision, before []dispatch.CommissionVerifiedArtifact) ([]dispatch.CommissionVerifiedArtifact, error) {
	if decision.Outcome != "artifacts_ready" {
		return nil, errors.New("commission_generation_verification_incomplete")
	}
	current, err := validateCommissionArtifacts(directory, decision)
	if err != nil {
		return nil, err
	}
	pinned := make(map[string]dispatch.CommissionVerifiedArtifact, len(before))
	for _, file := range before {
		pinned[file.RelativePath] = file
	}
	for _, file := range current {
		original, ok := pinned[file.RelativePath]
		if !ok || original.SHA256 != file.SHA256 || original.ByteSize != file.ByteSize {
			return nil, errors.New("commission_artifact_changed")
		}
	}
	return current, nil
}
