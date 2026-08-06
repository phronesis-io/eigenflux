package recallsource

import "context"

// Source is a persisted compatibility contract, not only an in-process enum.
// Replay records store both this numeric bitset and the labels returned by
// Names. NEVER delete, reuse, or renumber a bit after release. When a recall
// channel is retired, remove its runtime registration and implementation but
// keep its Source constant and Names mapping so historical replay remains
// unambiguous.
type Source uint8

const (
	Keyword   Source = 0x01
	KNN       Source = 0x02
	TwoTower  Source = 0x04 // Reserved for decoding historical replay data.
	HotRecall Source = 0x08
	NewRecall Source = 0x10
	Friend    Source = 0x20
	NewUGC    Source = 0x40
	SwingI2I  Source = 0x80
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

// Names decodes the persisted Source bitset. Keep mappings for retired sources;
// downstream replay analysis and LR feature extraction consume these labels.
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
	if s.Has(Friend) {
		names = append(names, "friend")
	}
	if s.Has(NewUGC) {
		names = append(names, "new_ugc_recall")
	}
	if s.Has(SwingI2I) {
		names = append(names, "swing_i2i")
	}
	return names
}
