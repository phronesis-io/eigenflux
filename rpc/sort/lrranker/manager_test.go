package lrranker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newIdleManager builds a Manager without starting the reload goroutine, so
// tests can drive tryReload deterministically.
func newIdleManager(path string) *Manager {
	return &Manager{
		cfg:  Config{Enabled: true, ModelPath: path, ReloadInterval: time.Hour},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

func TestDisabledManagerFallsBack(t *testing.T) {
	m := NewManager(Config{Enabled: false})
	defer m.Close()
	if m.Available() {
		t.Fatal("disabled manager should not be available")
	}
	if m.Enabled() {
		t.Fatal("disabled manager should report Enabled()=false")
	}
	if _, ok := m.Score(Input{}); ok {
		t.Fatal("disabled manager Score should return ok=false")
	}
}

func TestManagerLoadsAndScores(t *testing.T) {
	m := newIdleManager("testdata/model.json")
	m.tryReload("test")
	if !m.Available() {
		t.Fatal("manager should be available after loading a valid model")
	}
	if m.ModelVersion() != "lr_20260803_1625_e69f47d" {
		t.Fatalf("unexpected model version %q", m.ModelVersion())
	}
	if _, ok := m.Score(Input{BroadcastType: "info"}); !ok {
		t.Fatal("Score should succeed with a loaded model")
	}
}

func TestManagerHotReloadSwapsVersion(t *testing.T) {
	dir := t.TempDir()
	mustCopy(t, "testdata/model.json", filepath.Join(dir, "va", "model.json"))
	mustCopy(t, "testdata/model_b.json", filepath.Join(dir, "vb", "model.json"))
	current := filepath.Join(dir, "current")
	if err := os.Symlink(filepath.Join(dir, "va"), current); err != nil {
		t.Fatal(err)
	}

	m := newIdleManager(filepath.Join(current, "model.json"))
	m.tryReload("initial")
	if got := m.ModelVersion(); got != "lr_20260803_1625_e69f47d" {
		t.Fatalf("initial version %q", got)
	}

	// Unchanged bundle: reload is a no-op, version stays.
	m.tryReload("noop")
	if got := m.ModelVersion(); got != "lr_20260803_1625_e69f47d" {
		t.Fatalf("version changed on no-op reload: %q", got)
	}

	// Flip the current symlink to model B and reload.
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "vb"), current); err != nil {
		t.Fatal(err)
	}
	m.tryReload("swap")
	if got := m.ModelVersion(); got != "lr_20260731_1454_e69f47d" {
		t.Fatalf("version after swap %q", got)
	}
}

func TestManagerKeepsPreviousOnBrokenModel(t *testing.T) {
	dir := t.TempDir()
	mustCopy(t, "testdata/model.json", filepath.Join(dir, "va", "model.json"))
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"schema_version":1,"model_type":"logistic_regression","feature_contract_version":"WRONG"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(dir, "current")
	if err := os.Symlink(filepath.Join(dir, "va"), current); err != nil {
		t.Fatal(err)
	}

	m := newIdleManager(filepath.Join(current, "model.json"))
	m.tryReload("initial")
	good := m.ModelVersion()
	if good == "" {
		t.Fatal("expected a good model to load")
	}

	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, current); err != nil { // point at broken.json via dir
		t.Fatal(err)
	}
	m.cfg.ModelPath = filepath.Join(dir, "broken.json")
	m.tryReload("broken")
	if !m.Available() || m.ModelVersion() != good {
		t.Fatalf("broken reload should keep previous model %q, got available=%v version=%q", good, m.Available(), m.ModelVersion())
	}
}

func TestLoadModelRejectsBadBundles(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"bad_schema":   `{"schema_version":2,"model_type":"logistic_regression","feature_contract_version":"lr_features_v2","terms":[]}`,
		"bad_type":     `{"schema_version":1,"model_type":"svm","feature_contract_version":"lr_features_v2","terms":[]}`,
		"bad_contract": `{"schema_version":1,"model_type":"logistic_regression","feature_contract_version":"lr_features_v1","terms":[]}`,
		"wrong_terms":  `{"schema_version":1,"model_type":"logistic_regression","feature_contract_version":"lr_features_v2","intercept":0,"terms":[]}`,
		"bad_json":     `{not json`,
	}
	for name, body := range cases {
		p := filepath.Join(dir, name+".json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadModel(p); err == nil {
			t.Errorf("%s: expected LoadModel to reject bundle", name)
		}
	}
}

func mustCopy(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
