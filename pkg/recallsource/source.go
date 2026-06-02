package recallsource

import "context"

// Source is a bitset representing which recall channel(s) contributed an item.
type Source uint8

const (
	Keyword  Source = 1 << iota // 0x01
	KNN                         // 0x02
	TwoTower                    // 0x04
	HotRecall                   // 0x08
	NewRecall                   // 0x10
)

func (s Source) Has(flag Source) bool    { return s&flag != 0 }
func (s Source) IsOnly(flag Source) bool { return s == flag }
func (s *Source) Add(flag Source)        { *s |= flag }

// Candidate represents a single item returned by a recall source.
type Candidate struct {
	ItemID int64
	Score  float64 // 0 when the source provides no precomputed score
	Source Source
}

// RecallSource fetches recall candidates for a given user.
type RecallSource interface {
	Name() string
	SourceFlag() Source
	Recall(ctx context.Context, userID string, limit int) ([]Candidate, error)
}

func Names(s Source) []string {
	var names []string
	if s.Has(Keyword) {
		names = append(names, "keyword")
	}
	if s.Has(KNN) {
		names = append(names, "knn")
	}
	if s.Has(TwoTower) {
		names = append(names, "two_tower")
	}
	if s.Has(HotRecall) {
		names = append(names, "hot_recall")
	}
	if s.Has(NewRecall) {
		names = append(names, "new_recall")
	}
	return names
}
