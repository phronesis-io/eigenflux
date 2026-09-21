package discovery

import "reflect"

func emptyFilters(f Filters) bool { return reflect.DeepEqual(f, Filters{}) }

// IntersectFilters narrows legacy request filters over a saved Need without
// changing that Need or dropping either side of an explicit constraint.
func IntersectFilters(a, b Filters) (Filters, error) {
	for _, p := range [][2]*string{{&a.Category, &b.Category}, {&a.Subtype, &b.Subtype}, {&a.TaxonomyVersion, &b.TaxonomyVersion}, {&a.Currency, &b.Currency}} {
		if *p[1] != "" {
			if *p[0] != "" && *p[0] != *p[1] {
				return a, Invalid("filters", "conflicting_constraints")
			}
			*p[0] = *p[1]
		}
	}
	low := func(x, y *int64) *int64 {
		if x == nil {
			return y
		}
		if y == nil || *x <= *y {
			return x
		}
		return y
	}
	high := func(x, y *int64) *int64 {
		if x == nil {
			return y
		}
		if y == nil || *x >= *y {
			return x
		}
		return y
	}
	a.BudgetMaxFen = low(a.BudgetMaxFen, b.BudgetMaxFen)
	a.MaxPriceFen = low(a.MaxPriceFen, b.MaxPriceFen)
	a.MinPriceFen = high(a.MinPriceFen, b.MinPriceFen)
	a.MinDurationMS = high(a.MinDurationMS, b.MinDurationMS)
	a.MaxDurationMS = low(a.MaxDurationMS, b.MaxDurationMS)
	a.DeadlineMS = low(a.DeadlineMS, b.DeadlineMS)
	both := func(x, y []string) ([]string, error) {
		if len(x) == 0 {
			return y, nil
		}
		if len(y) == 0 {
			return x, nil
		}
		out := []string{}
		for _, v := range x {
			for _, w := range y {
				if v == w {
					out = appendUnique(out, v)
				}
			}
		}
		if len(out) == 0 {
			return nil, Invalid("filters", "conflicting_constraints")
		}
		return out, nil
	}
	var err error
	a.Lang, err = both(a.Lang, b.Lang)
	if err != nil {
		return a, err
	}
	a.ProviderRegion, err = both(a.ProviderRegion, b.ProviderRegion)
	if err != nil {
		return a, err
	}
	a.Intents, err = both(a.Intents, b.Intents)
	if err != nil {
		return a, err
	}
	a.ExcludeAuthors = unique(append(append([]string{}, a.ExcludeAuthors...), b.ExcludeAuthors...))
	a.ExcludeTerms = unique(append(append([]string{}, a.ExcludeTerms...), b.ExcludeTerms...))
	return a, nil
}
