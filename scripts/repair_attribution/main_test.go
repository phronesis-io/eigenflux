package main

import (
	itemdal "eigenflux_server/rpc/item/dal"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRepairRejectsConcurrentChangesAndNeverReactivates(t *testing.T) {
	p := plan{ID: 1, Author: 2, Group: 3, URL: "https://example.test", Expiry: "2020-01-01T00:00:00Z", Before: textFields{"raw", "bad", "坏"}, After: textFields{"source signature", "good", "好"}}
	r := itemdal.RawItem{ItemID: 1, AuthorAgentID: 2, RawURL: p.URL, RawContent: "raw"}
	s := itemdal.ProcessedItem{ItemID: 1, GroupID: 3, Status: 3, ExpireTime: p.Expiry, Summary: "bad", SummaryZh: "坏"}
	need, e := validate(p, r, s, time.Now())
	require.NoError(t, e)
	require.True(t, need)
	s.Summary = "concurrent edit"
	_, e = validate(p, r, s, time.Now())
	require.Error(t, e)
	r.RawContent = p.After.Content
	s.Summary = p.After.Summary
	s.SummaryZh = p.After.SummaryZh
	need, e = validate(p, r, s, time.Now())
	require.NoError(t, e)
	require.False(t, need)
	s.Status = 4
	_, e = validate(p, r, s, time.Now())
	require.Error(t, e)
	s.Status = 3
	_, e = validate(p, r, s, time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC))
	require.Error(t, e)
}
