package consumer

import (
	"testing"
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"

	"github.com/stretchr/testify/assert"
)

// --- assignDefaultGroupID tests ---

func TestAssignDefaultGroupID_WithSimilar(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50},
		{ID: 200, GroupID: 60},
	}
	assert.Equal(t, int64(50), assignDefaultGroupID(1, similar))
}

func TestAssignDefaultGroupID_Empty(t *testing.T) {
	assert.Equal(t, int64(1), assignDefaultGroupID(1, nil))
}

// --- resolveGroupID tests ---

func TestResolveGroupID_Info_KeepsExistingGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "info", similar, time.Now())
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Info_NoSimilar_ReturnsItemID(t *testing.T) {
	got := resolveGroupID(1, 111, "info", nil, time.Now())
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Demand_SameAuthor_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 111, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar, time.Now())
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Demand_DifferentAuthor_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar, time.Now())
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Demand_SameAuthorNotFirst(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: time.Now()},
		{ID: 200, GroupID: 60, AuthorAgentID: 111, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar, time.Now())
	assert.Equal(t, int64(60), got, "should find same-author match at index 1")
}

func TestResolveGroupID_Demand_AllDifferentAuthors(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: time.Now()},
		{ID: 200, GroupID: 60, AuthorAgentID: 888, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar, time.Now())
	assert.Equal(t, int64(1), got, "no same-author match, should ungroup")
}

func TestResolveGroupID_Supply_SameAuthor_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 111, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "supply", similar, time.Now())
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Supply_DifferentAuthor_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "supply", similar, time.Now())
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Supply_SameAuthorNotFirst(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
		{ID: 200, GroupID: 60, AuthorAgentID: 111, Score: 0.75, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "supply", similar, time.Now())
	assert.Equal(t, int64(60), got, "should find same-author match at index 1")
}

func TestResolveGroupID_Alert_HighSimRecent_KeepsGroup(t *testing.T) {
	now := time.Now()
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: now.Add(-1 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar, now)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Alert_LowSim_Ungroups(t *testing.T) {
	now := time.Now()
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.75, CreatedAt: now.Add(-1 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar, now)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Alert_OldItem_Ungroups(t *testing.T) {
	now := time.Now()
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: now.Add(-8 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar, now)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Alert_ExactThreshold_KeepsGroup(t *testing.T) {
	now := time.Now()
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: now.Add(-1 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar, now)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Alert_QualifyingItemNotFirst(t *testing.T) {
	now := time.Now()
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: now.Add(-8 * time.Hour)}, // too old
		{ID: 200, GroupID: 60, AuthorAgentID: 999, Score: 0.88, CreatedAt: now.Add(-2 * time.Hour)}, // qualifies
	}
	got := resolveGroupID(1, 111, "alert", similar, now)
	assert.Equal(t, int64(60), got, "should find qualifying match at index 1")
}

func TestResolveGroupID_Alert_NoneQualify(t *testing.T) {
	now := time.Now()
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: now.Add(-8 * time.Hour)}, // too old
		{ID: 200, GroupID: 60, AuthorAgentID: 999, Score: 0.75, CreatedAt: now.Add(-1 * time.Hour)}, // too low sim
	}
	got := resolveGroupID(1, 111, "alert", similar, now)
	assert.Equal(t, int64(1), got, "no qualifying alert match, should ungroup")
}

func TestResolveGroupID_UnknownType_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "", similar, time.Now())
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Demand_NoSimilar_ReturnsItemID(t *testing.T) {
	got := resolveGroupID(1, 111, "demand", nil, time.Now())
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Demand_SameAuthor_ZeroAuthorID_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 0, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar, time.Now())
	assert.Equal(t, int64(1), got, "legacy items with zero author_agent_id should be treated as different author")
}
