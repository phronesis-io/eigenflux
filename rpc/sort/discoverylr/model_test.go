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
			if _, err := load(path, Contract); err == nil {
				t.Fatal("accepted invalid model")
			}
		})
	}
}

func ratingFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/discovery_rating_lr_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Model json.RawMessage `json:"model"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rating.json")
	if err = os.WriteFile(path, data.Model, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestContractsAreIsolated(t *testing.T) {
	rating := ratingFixture(t)
	followup, features, _ := fixture(t)
	if _, err := load(rating, RatingContract); err != nil {
		t.Fatalf("rating bundle rejected: %v", err)
	}
	if _, err := load(rating, Contract); err == nil {
		t.Fatal("follow-up scorer accepted a rating bundle")
	}
	if _, err := load(followup, RatingContract); err == nil {
		t.Fatal("rating scorer accepted a follow-up bundle")
	}
	if _, err := load(rating, "unknown"); err == nil {
		t.Fatal("accepted an unknown contract")
	}
	m := NewRating(true, rating, time.Hour)
	defer m.Close()
	if _, ok := m.ScoreBatch([][]float64{features}); ok {
		t.Fatal("rating scorer accepted a follow-up vector")
	}
	v := make([]float64, len(RatingFeatureNames))
	got, ok := m.ScoreBatch([][]float64{v})
	if !ok || got[0].Contract != RatingContract || got[0].ModelVersion == "" {
		t.Fatalf("rating score: %+v %v", got, ok)
	}
}

func TestRatingV2ContractIsolated(t *testing.T) {
	raw, err := os.ReadFile("testdata/discovery_rating_lr_v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Model json.RawMessage `json:"model"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	v2 := filepath.Join(t.TempDir(), "v2.json")
	if err = os.WriteFile(v2, data.Model, 0600); err != nil {
		t.Fatal(err)
	}
	v1 := ratingFixture(t)
	if _, err := load(v2, RatingV2Contract); err != nil {
		t.Fatalf("v2 bundle rejected: %v", err)
	}
	if _, err := load(v2, RatingContract); err == nil {
		t.Fatal("v1 scorer accepted a v2 bundle")
	}
	if _, err := load(v1, RatingV2Contract); err == nil {
		t.Fatal("v2 scorer accepted a v1 bundle")
	}
	m := NewRatingV2(true, v2, time.Hour)
	defer m.Close()
	if _, ok := m.ScoreBatch([][]float64{make([]float64, len(RatingFeatureNames))}); ok {
		t.Fatal("v2 scorer accepted a v1 vector")
	}
	got, ok := m.ScoreBatch([][]float64{make([]float64, len(RatingV2FeatureNames))})
	if !ok || got[0].Contract != RatingV2Contract || !IsRating(got[0].Contract) || IsRating(Contract) {
		t.Fatalf("v2 score: %+v %v", got, ok)
	}
}
