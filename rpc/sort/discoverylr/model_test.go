package discoverylr

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, []float64, float64) {
	t.Helper()
	raw, err := os.ReadFile("testdata/discovery_lr_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Model    json.RawMessage `json:"model"`
		Features []float64       `json:"features"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "model.json")
	if err = os.WriteFile(path, data.Model, 0600); err != nil {
		t.Fatal(err)
	}
	var m model
	if err = json.Unmarshal(data.Model, &m); err != nil {
		t.Fatal(err)
	}
	return path, data.Features, m.SelfTests[0].Probability
}

func TestPythonParityAndAtomicBatch(t *testing.T) {
	path, features, want := fixture(t)
	manager := New(true, path, time.Hour)
	defer manager.Close()
	got, ok := manager.ScoreBatch([][]float64{features})
	if !ok || math.Abs(got[0].Probability-want) > 1e-12 || got[0].Contract != Contract {
		t.Fatalf("scores=%+v ok=%v want=%v", got, ok, want)
	}
	invalid := append([]float64(nil), features...)
	invalid[0] = math.NaN()
	if result, ok := manager.ScoreBatch([][]float64{features, invalid}); ok || result != nil {
		t.Fatal("partially scored invalid batch")
	}
	if _, ok := manager.ScoreBatch([][]float64{{1}}); ok {
		t.Fatal("accepted incorrect feature dimension")
	}
}

func TestBadReloadRetainsPreviousAndDisabledFallsBack(t *testing.T) {
	path, features, want := fixture(t)
	manager := New(true, path, time.Hour)
	defer manager.Close()
	if err := os.WriteFile(path, []byte(`{"feature_contract_version":"lr_features_v2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	manager.reload(path)
	got, ok := manager.ScoreBatch([][]float64{features})
	if !ok || got[0].Probability != want {
		t.Fatal("bad reload displaced previous model")
	}
	disabled := New(false, path, time.Hour)
	defer disabled.Close()
	if _, ok := disabled.ScoreBatch([][]float64{features}); ok {
		t.Fatal("disabled scorer active")
	}
}

func TestRejectFailedPromotionAndSelfTest(t *testing.T) {
	for _, bad := range []string{"promotion", "contract", "self_test", "features"} {
		t.Run(bad, func(t *testing.T) {
			path, _, _ := fixture(t)
			raw, _ := os.ReadFile(path)
			var data map[string]any
			_ = json.Unmarshal(raw, &data)
			switch bad {
			case "promotion":
				data["promotion_passed"] = false
			case "contract":
				data["feature_contract_version"] = "lr_features_v2"
			case "self_test":
				data["intercept"] = 10.0
			case "features":
				data["feature_names"].([]any)[0] = "wrong"
			}
			raw, _ = json.Marshal(data)
			_ = os.WriteFile(path, raw, 0600)
			if _, err := load(path); err == nil {
				t.Fatal("accepted invalid model")
			}
		})
	}
}
