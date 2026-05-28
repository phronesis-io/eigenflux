package recallsource

// Source is a bitset representing which recall channel(s) contributed an item.
type Source uint8

const (
	Keyword  Source = 1 << iota // 0x01
	KNN                         // 0x02
	TwoTower                    // 0x04
)

func (s Source) Has(flag Source) bool    { return s&flag != 0 }
func (s Source) IsOnly(flag Source) bool { return s == flag }
func (s *Source) Add(flag Source)        { *s |= flag }

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
	return names
}
