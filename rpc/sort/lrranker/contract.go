// Package lrranker loads a daily-trained logistic-regression ranking model and
// scores feed candidates in-process. It is a faithful Go port of the feature
// construction, standardization and scoring in the eigenflux-ml repo
// (eigenflux/lr_ranker/{contract,features,model}.py); the two implementations
// share the immutable feature contract "lr_features_v2" and a golden fixture so
// that a model trained in Python scores identically here.
package lrranker

// ContractVersion is the only feature contract this scorer understands. A model
// bundle declaring any other version is rejected at load time so a Python-side
// contract change can never be silently mis-scored in Go.
const ContractVersion = "lr_features_v2"

// Fixed enum vocabularies. The last element is always the "other" bucket that
// absorbs unknown values, so a new category on either side never shifts the
// vector dimension. These mirror contract.py exactly. recallSources also keeps
// retired source labels such as "two_tower": removing one would change the
// lr_features_v2 vector shape and make historical replay/model data incompatible.
var (
	broadcastTypes = []string{"supply", "demand", "info", "alert", "other"}
	sourceTypes    = []string{"original", "curated", "forwarded", "other"}
	timelinessVals = []string{"timely", "evergreen", "other"}
	langVals       = []string{"en", "zh", "other"}
	recallSources  = []string{
		"keyword",
		"knn",
		"two_tower", // Retired runtime channel; retained by the immutable feature contract.
		"hot_recall",
		"new_recall",
		"friend",
		"new_ugc_recall",
	}
)

// term kinds, matching the Python FeatureTerm.kind values.
const (
	kindNumeric  = "numeric"
	kindBool     = "bool"
	kindEquals   = "equals"
	kindContains = "contains"
)

const (
	transformIdentity = "identity"
	transformLog1p    = "log1p"
)

// contractTerm is one position in the fixed-dimension feature vector. It carries
// only the structural identity of the term (name/kind/source/transform/value);
// the numeric mean/scale/coefficient live on the loaded model, not here.
type contractTerm struct {
	name      string
	kind      string
	source    string
	transform string
	value     string // for equals/contains terms; "" otherwise
	clipMin   float64
	clipMax   float64
}

// canonicalTerms is the immutable ordering and meaning of lr_features_v2,
// reproduced from contract.py FeatureContract.from_config(). A loaded model's
// terms must match this list structurally (name/kind/source/transform/value in
// order) or the load is rejected — the online guarantee that the Python term
// order and the Go term order can never drift apart.
var canonicalTerms = buildCanonicalTerms()

func buildCanonicalTerms() []contractTerm {
	// Numeric terms: (source, transform, clipMin, clipMax). The vector position
	// name gains a "_log1p" suffix for log1p terms, matching Python.
	type numSpec struct {
		source    string
		transform string
		clipMin   float64
		clipMax   float64
	}
	numeric := []numSpec{
		{"baseline.semantic", transformIdentity, -1, 1},
		{"baseline.keyword", transformIdentity, 0, 1},
		{"baseline.freshness", transformIdentity, 0, 1},
		{"baseline.total", transformIdentity, 0, 1},
		{"content.quality", transformIdentity, 0, 1},
		{"time.age_hours", transformLog1p, 0, 2160},
		{"time.time_to_expire_hours", transformLog1p, 0, 2160},
		{"cross.keyword_intersection", transformLog1p, 0, 100},
		{"cross.keyword_overlap", transformIdentity, 0, 1},
		{"cross.domain_intersection", transformLog1p, 0, 100},
		{"cross.domain_overlap", transformIdentity, 0, 1},
	}
	terms := make([]contractTerm, 0, 42)
	for _, n := range numeric {
		name := n.source
		if n.transform == transformLog1p {
			name = n.source + "_log1p"
		}
		terms = append(terms, contractTerm{
			name:      name,
			kind:      kindNumeric,
			source:    n.source,
			transform: n.transform,
			clipMin:   n.clipMin,
			clipMax:   n.clipMax,
		})
	}

	boolSources := []string{
		"time.has_expire_time",
		"cross.geo_match",
		"availability.is_draft",
		"missing.baseline.semantic",
		"missing.baseline.keyword",
		"missing.baseline.freshness",
		"missing.baseline.total",
		"missing.content.quality",
		"missing.time.created_at",
	}
	for _, s := range boolSources {
		terms = append(terms, contractTerm{name: s, kind: kindBool, source: s, transform: transformIdentity})
	}

	addEnum := func(source string, values []string) {
		for _, v := range values {
			terms = append(terms, contractTerm{name: source + "=" + v, kind: kindEquals, source: source, transform: transformIdentity, value: v})
		}
	}
	addEnum("broadcast_type", broadcastTypes)
	addEnum("source_type", sourceTypes)
	addEnum("timeliness", timelinessVals)
	addEnum("lang", langVals)

	for _, v := range recallSources {
		terms = append(terms, contractTerm{name: "recall_source=" + v, kind: kindContains, source: "recall_source", transform: transformIdentity, value: v})
	}
	return terms
}
