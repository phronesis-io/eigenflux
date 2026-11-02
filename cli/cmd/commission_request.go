package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/requestcontract"
)

const maxRequestDocumentBytes = 1 << 20

// The server remains authoritative for JSON Schema rules and the admitted bytes.
// Local checks stop path and document mistakes before transferring materials.
func validateOrderRequestMaterials(text, schema string, files []materialFile) error {
	if err := requestcontract.ValidateSpec(text, schema); err != nil {
		return err
	}
	structured, err := requestcontract.Structured(schema)
	if err != nil || !structured {
		return err
	}
	var request *materialFile
	alias := false
	for i := range files {
		if files[i].LogicalPath == "inputs/request.json" {
			alias = true
		}
		if files[i].LogicalPath == requestcontract.Path {
			request = &files[i]
		}
	}
	if request == nil {
		if alias {
			return fmt.Errorf("structured requests must use %s; correct the contract and material path", requestcontract.Path)
		}
		// Optional structured fields may accept an absent document as {}. Let the
		// server evaluate that against the complete frozen schema.
		return nil
	}
	data, err := readRequestMaterial(*request)
	if err != nil {
		return err
	}
	return validateRequestDocument(data)
}

func readRequestMaterial(file materialFile) ([]byte, error) {
	if file.ByteSize > maxRequestDocumentBytes {
		return nil, fmt.Errorf("%s exceeds 1 MiB", requestcontract.Path)
	}
	f, err := os.Open(file.LocalPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRequestDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != file.ByteSize || hex.EncodeToString(digest[:]) != file.SHA256 {
		return nil, fmt.Errorf("%s changed after material inspection", requestcontract.Path)
	}
	return data, nil
}

func validateRequestDocument(data []byte) error {
	if len(data) > maxRequestDocumentBytes {
		return fmt.Errorf("%s exceeds 1 MiB", requestcontract.Path)
	}
	var object map[string]json.RawMessage
	if !utf8.Valid(data) || json.Unmarshal(data, &object) != nil || object == nil {
		return fmt.Errorf("%s must contain a UTF-8 JSON object", requestcontract.Path)
	}
	return nil
}

// Give the trial Agent the same request file a buyer uploads, with exact bytes.
func prepareTrialRequestDocument(contract commissionapi.CommissionInput, input []byte, directory string, files []commissionLocalFile) ([]commissionLocalFile, error) {
	structured, err := requestcontract.Structured(contract.RequestSpecSchema)
	if err != nil || !structured {
		return files, err
	}
	if err := validateRequestDocument(input); err != nil {
		return nil, fmt.Errorf("commission_trial_request_invalid: %w", err)
	}
	var supplied *commissionLocalFile
	alias := false
	for i := range files {
		file := &files[i]
		if file.LogicalPath == "inputs/request.json" {
			alias = true
		}
		if file.LogicalPath == requestcontract.Path {
			supplied = file
		}
	}
	if supplied != nil {
		data, err := readRequestMaterial(materialFile{LocalPath: supplied.Path, ByteSize: supplied.ByteSize, SHA256: supplied.SHA256})
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(data, input) {
			return nil, fmt.Errorf("commission_trial_request_mismatch: --trial-input must match %s byte for byte", requestcontract.Path)
		}
		return files, nil
	}
	if alias {
		return nil, fmt.Errorf("commission_trial_request_path_invalid: use %s", requestcontract.Path)
	}
	if len(files) >= commissionArtifactMaxCount {
		return nil, fmt.Errorf("commission_trial_materials_limit")
	}
	total := int64(len(input))
	for _, file := range files {
		if file.ByteSize < 0 || file.ByteSize > commissionArtifactMaxBytes-total {
			return nil, fmt.Errorf("commission_trial_materials_limit")
		}
		total += file.ByteSize
	}
	target := filepath.Join(directory, filepath.FromSlash(requestcontract.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(target, input, 0400); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(input)
	files = append(files, commissionLocalFile{LogicalPath: requestcontract.Path, Path: target, ByteSize: int64(len(input)), SHA256: hex.EncodeToString(digest[:])})
	return files, nil
}
