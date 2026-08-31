package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const targetRegistryFile = "skills-target.json"

// TargetRegistration binds one stable EigenFlux Home to the directory its
// current Agent host actually loads. It removes directory guessing from every
// later heartbeat while keeping separate Agent Homes isolated.
type TargetRegistration struct {
	Path      string `json:"path"`
	Host      string `json:"host,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

func ReadTargetRegistration(home string) (*TargetRegistration, error) {
	data, err := os.ReadFile(filepath.Join(home, targetRegistryFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var target TargetRegistration
	if err := json.Unmarshal(data, &target); err != nil {
		return nil, fmt.Errorf("read skills target: %w", err)
	}
	if !filepath.IsAbs(target.Path) || strings.TrimSpace(target.Path) == "" {
		return nil, fmt.Errorf("registered skills target is not absolute")
	}
	return &target, nil
}

func RegisterTarget(home, path, host string) (*TargetRegistration, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("skills target path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, err
	}
	target := &TargetRegistration{Path: filepath.Clean(abs), Host: strings.TrimSpace(host), UpdatedAt: time.Now().UnixMilli()}
	data, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, dirPerm); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(home, ".skills-target-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmpPath, manifestFilePerm); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpPath, filepath.Join(home, targetRegistryFile)); err != nil {
		return nil, err
	}
	return target, nil
}
