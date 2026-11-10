package main

import (
	itemdal "eigenflux_server/rpc/item/dal"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestPublishPinsUGCSummaryOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, notes, text, origin string
		enabled                   bool
		code                      int32
	}{
		{"publisher", `{"summary":"  zero_source v0.4; FTC is not expanded  "}`, "  zero_source v0.4; FTC is not expanded  ", "publisher", true, 0},
		{"missing", `{}`, "", "none", true, 0},
		{"blank", `{"summary":"  "}`, "", "none", true, 0},
		{"invalid", `{"summary":42}`, "", "", true, 400},
		{"long", `{"summary":"` + strings.Repeat("x", 281) + `"}`, "", "", true, 400},
		{"disabled", `{"summary":"author"}`, "", "unknown", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newDeleteItemTestDB(t)
			seedPublishAuthor(t, gdb)
			req := publishRequest()
			req.RawNotes = &tc.notes
			svc := &ItemServiceImpl{itemIDGen: publishFixtureID{}, ugcPublisherSummary: tc.enabled}
			resp, err := svc.PublishItem(publishContext(), req)
			require.NoError(t, err)
			require.Equal(t, tc.code, resp.BaseResp.Code)
			if tc.code != 0 {
				assertPublishRowCounts(t, gdb, 0)
				return
			}
			var row itemdal.ProcessedItem
			require.NoError(t, gdb.First(&row, "item_id = ?", resp.ItemId).Error)
			require.Equal(t, tc.text, row.Summary)
			require.Equal(t, tc.origin, row.SummaryOrigin)
		})
	}
}
func TestPublisherSummaryDoesNotChangePGCPolicy(t *testing.T) {
	gdb := newDeleteItemTestDB(t)
	seedPublishAuthor(t, gdb)
	require.NoError(t, gdb.Table("agents").Where("agent_id = ?", 101).Update("email", "source@pgc.eigenflux.one").Error)
	svc := &ItemServiceImpl{itemIDGen: publishFixtureID{}, ugcPublisherSummary: true, pgcEmailSuffixes: []string{"@pgc.eigenflux.one"}}
	req := publishRequest()
	req.RawNotes = strPtr(`{"summary":42}`)
	resp, err := svc.PublishItem(publishContext(), req)
	require.NoError(t, err)
	require.Zero(t, resp.BaseResp.Code)
	var row itemdal.ProcessedItem
	require.NoError(t, gdb.First(&row, "item_id = ?", 201).Error)
	require.Equal(t, "unknown", row.SummaryOrigin)
}
