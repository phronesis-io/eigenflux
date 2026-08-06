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
		{HotRecall, []string{"hot_recall"}},
		{NewRecall, []string{"new_recall"}},
		{Friend, []string{"friend"}},
		{SwingI2I, []string{"swing_i2i"}},
		{Keyword | KNN, []string{"keyword", "knn"}},
		{Keyword | KNN | TwoTower | HotRecall, []string{"keyword", "knn", "two_tower", "hot_recall"}},
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

func TestSourceBitValuesAreReplayContract(t *testing.T) {
	tests := []struct {
		name string
		got  Source
		want Source
	}{
		{"Keyword", Keyword, 0x01},
		{"KNN", KNN, 0x02},
		{"TwoTower", TwoTower, 0x04},
		{"HotRecall", HotRecall, 0x08},
		{"NewRecall", NewRecall, 0x10},
		{"Friend", Friend, 0x20},
		{"NewUGC", NewUGC, 0x40},
		{"SwingI2I", SwingI2I, 0x80},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}
