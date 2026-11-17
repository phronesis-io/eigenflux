package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func saveMaintenance(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

func writeFileAtomic(path string, b []byte) error {
	staged, err := stageAtomicWrite(path, b)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	return os.Rename(staged, path)
}

// Write and sync the replacement beside its destination before committing it.
func stageAtomicWrite(path string, b []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".maintenance-*")
	if err != nil {
		return "", err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
