package rank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type itemPayload struct{ Title string }

func TestBasicCandidate_InterfaceMethods(t *testing.T) {
	src := &itemPayload{Title: "hello"}
	feat := map[string]float64{"freshness": 0.8, "semantic": 0.62}
	c := NewCandidate(42, CandidateItem, 0.75, feat, src)

	var iface Candidate = c
	assert.Equal(t, int64(42), iface.ID())
	assert.Equal(t, CandidateItem, iface.Type())
	assert.InDelta(t, 0.75, iface.Score(), 1e-9)
	assert.Equal(t, feat, iface.Features())
	assert.Equal(t, "item:42", iface.Fingerprint())

	recovered, ok := iface.Source().(*itemPayload)
	require.True(t, ok, "Source must round-trip the original typed payload")
	assert.Equal(t, "hello", recovered.Title)
}

func TestBasicCandidate_FingerprintByType(t *testing.T) {
	tests := []struct {
		name string
		t    CandidateType
		id   int64
		want string
	}{
		{name: "item", t: CandidateItem, id: 1, want: "item:1"},
		{name: "other", t: CandidateType("other"), id: 1, want: "other:1"},
		{name: "large_id", t: CandidateItem, id: 9_999_999_999, want: "item:9999999999"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCandidate(tc.id, tc.t, 0, nil, nil)
			assert.Equal(t, tc.want, c.Fingerprint())
		})
	}
}

func TestBasicCandidate_SameIDDifferentTypeNotEqual(t *testing.T) {
	a := NewCandidate(7, CandidateItem, 0, nil, nil)
	b := NewCandidate(7, CandidateType("other"), 0, nil, nil)
	assert.NotEqual(t, a.Fingerprint(), b.Fingerprint(), "type must disambiguate the fingerprint")
}

func TestBasicCandidate_SetScore(t *testing.T) {
	c := NewCandidate(1, CandidateItem, 0.5, nil, nil)
	c.SetScore(0.9)
	assert.InDelta(t, 0.9, c.Score(), 1e-9)
}

func TestBasicCandidate_Reasons(t *testing.T) {
	c := NewCandidate(1, CandidateItem, 0, nil, nil)
	assert.Empty(t, c.Reasons())

	c.AddReason("normalize:minmax")
	c.AddReason("slot:3")
	assert.Equal(t, []string{"normalize:minmax", "slot:3"}, c.Reasons())
}

func TestBasicCandidate_NilFeatures(t *testing.T) {
	c := NewCandidate(1, CandidateItem, 0, nil, nil)
	assert.Nil(t, c.Features(), "nil features are passed through unchanged")
}
