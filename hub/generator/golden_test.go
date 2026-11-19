package generator

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"fluxspace.ai/hub/models"
)

type generatorGolden struct {
	Hash []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"hash"`
	Duration []struct {
		Rounds int `json:"rounds"`
		AI     int `json:"ai"`
		Out    int `json:"out"`
	} `json:"duration"`
	Fingerprint struct {
		Out string `json:"out"`
	} `json:"fingerprint"`
}

func loadGeneratorGolden(t *testing.T) generatorGolden {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "golden.json"))
	if err != nil {
		t.Fatalf("golden.json missing (run node hub/testdata/export_golden.js): %v", err)
	}
	var g generatorGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("bad golden.json: %v", err)
	}
	return g
}

func TestHashStrMatchesPrototype(t *testing.T) {
	g := loadGeneratorGolden(t)
	if len(g.Hash) == 0 {
		t.Fatal("no hash samples")
	}
	for _, c := range g.Hash {
		if got := HashStr(c.In); got != c.Out {
			t.Errorf("HashStr(%q) = %q, prototype says %q", summarize(c.In), got, c.Out)
		}
	}
}

func summarize(s string) string {
	if len(s) > 24 {
		return s[:24] + "..."
	}
	return s
}

func TestEstimateMinutesMatchesPrototype(t *testing.T) {
	g := loadGeneratorGolden(t)
	for _, c := range g.Duration {
		if got := EstimateMinutes(c.Rounds, c.AI); got != c.Out {
			t.Errorf("EstimateMinutes(%d,%d) = %d, prototype says %d", c.Rounds, c.AI, got, c.Out)
		}
	}
}

func TestFingerprintMatchesPrototype(t *testing.T) {
	g := loadGeneratorGolden(t)
	// Must match fixedGame in export_golden.js field for field.
	s := &models.Skeleton{
		Type: "deduction", ThemeID: "d1", Title: "T", ThemeText: "TT",
		Rounds: []models.Round{
			{Title: "R1", Reveal: []models.Clue{{Key: "c1"}}, Actions: []models.Action{{ID: "b"}, {ID: "a"}}},
			{Title: "R2", Reveal: nil, Actions: []models.Action{{ID: "c"}}},
		},
	}
	if got := Fingerprint(s); got != g.Fingerprint.Out {
		t.Errorf("Fingerprint = %q, prototype says %q", got, g.Fingerprint.Out)
	}
}

func TestStructuralHashIgnoresNarrative(t *testing.T) {
	a, err := GenerateSkeleton(GenerateOptions{Mode: "coop", CompanionCount: 3}, rand.New(rand.NewSource(7)))
	if err != nil {
		t.Fatal(err)
	}
	b := *a
	b.Title = "A completely different title"
	b.ThemeText = "Different scene text"

	if a.StructuralHash != StructuralHash(&b) {
		t.Error("structural hash changed with narrative-only edits")
	}
	if a.Fingerprint == Fingerprint(&b) {
		t.Error("fingerprint should change when narrative changes")
	}
	if a.ContentHash == ContentHash(&b) {
		t.Error("content hash should change when narrative changes")
	}
}

func TestGenerateSkeletonInvariants(t *testing.T) {
	seen := make(map[string]bool)
	for seed := int64(0); seed < 50; seed++ {
		s, err := GenerateSkeleton(GenerateOptions{Mode: "coop", CompanionCount: 3}, rand.New(rand.NewSource(seed)))
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if s.DedupDegraded {
			t.Errorf("seed %d: unexpected degraded generation", seed)
		}
		if len(s.Rounds) != 5 {
			t.Errorf("seed %d: expected 5 rounds, got %d", seed, len(s.Rounds))
		}
		if s.EstimatedMinutes < TargetMinutesMin {
			t.Errorf("seed %d: duration %d below GDD minimum", seed, s.EstimatedMinutes)
		}
		if s.Fingerprint == "" || s.StructuralHash == "" || s.ContentHash == "" {
			t.Errorf("seed %d: missing hash", seed)
		}
		if len(s.Rounds) > 0 && len(s.Rounds[0].Actions) != 4 {
			t.Errorf("seed %d: expected 4 actions per round, got %d", seed, len(s.Rounds[0].Actions))
		}
		seen[s.Fingerprint] = true
	}
	if len(seen) < 45 {
		t.Errorf("fingerprint diversity too low: %d unique out of 50", len(seen))
	}
}

func TestGenerateSkeletonAvoidsHistoryFingerprint(t *testing.T) {
	s, err := GenerateSkeleton(GenerateOptions{Mode: "coop", CompanionCount: 3}, rand.New(rand.NewSource(11)))
	if err != nil {
		t.Fatal(err)
	}
	again, err := GenerateSkeleton(GenerateOptions{
		Mode: "coop", CompanionCount: 3,
		HistoryFingerprints: []string{s.Fingerprint},
	}, rand.New(rand.NewSource(11)))
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint == s.Fingerprint {
		t.Error("generator reused a blacklisted fingerprint")
	}
}

func TestGenerateSkeletonRejectsInvalidSeatCount(t *testing.T) {
	for _, n := range []int{0, 5, -1} {
		if _, err := GenerateSkeleton(GenerateOptions{CompanionCount: n}, rand.New(rand.NewSource(1))); err == nil {
			t.Errorf("companion count %d should be rejected", n)
		}
	}
}
