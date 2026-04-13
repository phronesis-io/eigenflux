package ranker

import (
	"testing"
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"

	"github.com/stretchr/testify/assert"
)

func TestPickExplorationItems_FiltersCorrectly(t *testing.T) {
	now := time.Now()

	candidates := []sortDal.Item{
		{ID: 1, Type: "info", QualityScore: 0.8, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 2, Type: "info", QualityScore: 0.8, UpdatedAt: now.Add(-12 * time.Hour)}, // too old
		{ID: 3, QualityScore: 0.8, UpdatedAt: now.Add(-1 * time.Hour)},                // draft (no Type)
		{ID: 4, Type: "info", QualityScore: 0.1, UpdatedAt: now.Add(-1 * time.Hour)},  // low quality
	}

	seenIDs := map[int64]bool{}
	result := PickExplorationItems(candidates, seenIDs, nil, 1, 6*time.Hour, 0.4)
	assert.Len(t, result, 1)
	assert.Equal(t, int64(1), result[0].ID)
}

func TestPickExplorationItems_ExcludesSeen(t *testing.T) {
	now := time.Now()
	candidates := []sortDal.Item{
		{ID: 1, Type: "info", QualityScore: 0.8, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 2, Type: "info", QualityScore: 0.7, UpdatedAt: now.Add(-2 * time.Hour)},
	}

	seenIDs := map[int64]bool{1: true}
	result := PickExplorationItems(candidates, seenIDs, nil, 1, 6*time.Hour, 0.4)
	assert.Len(t, result, 1)
	assert.Equal(t, int64(2), result[0].ID)
}

func TestPickExplorationItems_ReturnsEmpty(t *testing.T) {
	result := PickExplorationItems(nil, nil, nil, 1, 6*time.Hour, 0.4)
	assert.Empty(t, result)
}

func TestPickExplorationItems_SortsByQuality(t *testing.T) {
	now := time.Now()
	candidates := []sortDal.Item{
		{ID: 1, Type: "info", QualityScore: 0.5, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 2, Type: "info", QualityScore: 0.9, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 3, Type: "info", QualityScore: 0.7, UpdatedAt: now.Add(-1 * time.Hour)},
	}

	result := PickExplorationItems(candidates, nil, nil, 2, 6*time.Hour, 0.4)
	assert.Len(t, result, 2)
	assert.Equal(t, int64(2), result[0].ID) // highest quality first
	assert.Equal(t, int64(3), result[1].ID)
}

func TestPickExplorationItems_ExcludesSeenGroups(t *testing.T) {
	now := time.Now()
	candidates := []sortDal.Item{
		{ID: 1, GroupID: 10, Type: "info", QualityScore: 0.95, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 2, GroupID: 10, Type: "info", QualityScore: 0.90, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 3, GroupID: 20, Type: "info", QualityScore: 0.85, UpdatedAt: now.Add(-1 * time.Hour)},
	}

	result := PickExplorationItems(candidates, nil, map[int64]bool{10: true}, 2, 6*time.Hour, 0.4)
	assert.Len(t, result, 1)
	assert.Equal(t, int64(3), result[0].ID)
}
