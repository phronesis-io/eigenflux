package dal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeRecallLanes_Dedups(t *testing.T) {
	a := []ServiceDoc{{ServiceID: 1, Score: 0.9}, {ServiceID: 2, Score: 0.5}}
	b := []ServiceDoc{{ServiceID: 2, Score: 0.7}, {ServiceID: 3, Score: 0.4}}
	out := mergeRecallLanes(a, b)
	assert.Len(t, out, 3)
	seen := map[int64]bool{}
	for _, d := range out {
		seen[d.ServiceID] = true
	}
	assert.True(t, seen[1] && seen[2] && seen[3])
}

func TestMergeRecallLanes_EmptyInputs(t *testing.T) {
	assert.Empty(t, mergeRecallLanes(nil, nil))
	a := []ServiceDoc{{ServiceID: 1}}
	assert.Equal(t, a, mergeRecallLanes(a, nil))
	assert.Equal(t, a, mergeRecallLanes(nil, a))
}

func TestAppendUniqueIntent(t *testing.T) {
	s := appendUniqueIntent(nil, "x")
	s = appendUniqueIntent(s, "y")
	s = appendUniqueIntent(s, "x") // dup
	assert.Equal(t, []string{"x", "y"}, s)
}
