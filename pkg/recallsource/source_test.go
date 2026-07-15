package recallsource

import (
	"testing"
)

func TestSourceBits(t *testing.T) {
	var s Source

	s.Add(Keyword)
	if !s.Has(Keyword) {
		t.Error("expected Has(Keyword)")
	}
	if s.Has(KNN) {
		t.Error("should not Has(KNN)")
	}
	if !s.IsOnly(Keyword) {
		t.Error("expected IsOnly(Keyword)")
	}

	s.Add(TwoTower)
	if s.IsOnly(Keyword) {
		t.Error("should not be IsOnly(Keyword) after adding TwoTower")
	}
	if !s.Has(Keyword) || !s.Has(TwoTower) {
		t.Error("expected both Keyword and TwoTower")
	}

	if s != Keyword|TwoTower {
		t.Errorf("expected %d, got %d", Keyword|TwoTower, s)
	}
}

func TestSourceNames(t *testing.T) {
	tests := []struct {
		source Source
		want   []string
	}{
		{0, nil},
		{Keyword, []string{"keyword"}},
		{KNN, []string{"knn"}},
		{TwoTower, []string{"two_tower"}},
		{Keyword | KNN, []string{"keyword", "knn"}},
		{Keyword | KNN | TwoTower, []string{"keyword", "knn", "two_tower"}},
		{NewUGC, []string{"new_ugc_recall"}},
		{Keyword | NewUGC, []string{"keyword", "new_ugc_recall"}},
	}

	for _, tt := range tests {
		got := Names(tt.source)
		if len(got) != len(tt.want) {
			t.Errorf("Names(%d) = %v, want %v", tt.source, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("Names(%d)[%d] = %q, want %q", tt.source, i, got[i], tt.want[i])
			}
		}
	}
}

func TestSourceBitValues(t *testing.T) {
	if Keyword != 1 {
		t.Errorf("Keyword = %d, want 1", Keyword)
	}
	if KNN != 2 {
		t.Errorf("KNN = %d, want 2", KNN)
	}
	if TwoTower != 4 {
		t.Errorf("TwoTower = %d, want 4", TwoTower)
	}
	if NewUGC != 0x40 {
		t.Errorf("NewUGC = %d, want 64", NewUGC)
	}
}
