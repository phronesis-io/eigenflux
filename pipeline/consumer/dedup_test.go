package consumer

import (
	"testing"
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"

	"github.com/stretchr/testify/assert"
)

func TestResolveGroupID_Info_KeepsExistingGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "info", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Info_NoSimilar_ReturnsItemID(t *testing.T) {
	got := resolveGroupID(1, 111, "info", nil)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Demand_SameAuthor_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 111, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Demand_DifferentAuthor_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Supply_SameAuthor_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 111, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "supply", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Supply_DifferentAuthor_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.80, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "supply", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Alert_HighSimRecent_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: time.Now().Add(-1 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Alert_LowSim_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.75, CreatedAt: time.Now().Add(-1 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Alert_OldItem_Ungroups(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.90, CreatedAt: time.Now().Add(-8 * time.Hour)},
	}
	got := resolveGroupID(1, 111, "alert", similar)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_UnknownType_KeepsGroup(t *testing.T) {
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 999, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "", similar)
	assert.Equal(t, int64(50), got)
}

func TestResolveGroupID_Demand_NoSimilar_ReturnsItemID(t *testing.T) {
	got := resolveGroupID(1, 111, "demand", nil)
	assert.Equal(t, int64(1), got)
}

func TestResolveGroupID_Demand_SameAuthor_ZeroAuthorID_Ungroups(t *testing.T) {
	// Legacy data: similar item has no author_agent_id (zero value)
	similar := []sortDal.Item{
		{ID: 100, GroupID: 50, AuthorAgentID: 0, Score: 0.85, CreatedAt: time.Now()},
	}
	got := resolveGroupID(1, 111, "demand", similar)
	assert.Equal(t, int64(1), got, "legacy items with zero author_agent_id should be treated as different author")
}
