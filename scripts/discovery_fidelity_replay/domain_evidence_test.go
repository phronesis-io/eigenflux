package main

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDomainEvidenceCannotRefreshFrozenSource(t *testing.T) {
	dir := t.TempDir()
	item := itemEvidence{ItemID: 1, Content: "原文", Notes: "notes", Summary: "summary", Keywords: "keywords", Lang: "zh", ExpireTime: "2027-01-01"}
	write := func(rows []itemEvidence) {
		t.Helper()
		raw, err := json.Marshal(rows)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "retrieval-domain-source.json"), raw, 0600))
	}
	row := item
	row.Domains = "AI agents"
	write([]itemEvidence{row})
	got := bindDomainEvidence(dir, []itemEvidence{item})
	require.Equal(t, "AI agents", got[0].Domains)
	require.Empty(t, item.Domains)
	for _, change := range []func(*itemEvidence){
		func(x *itemEvidence) { x.Content = "edited" }, func(x *itemEvidence) { x.Notes = "edited" },
		func(x *itemEvidence) { x.Summary = "edited" }, func(x *itemEvidence) { x.Keywords = "edited" },
		func(x *itemEvidence) { x.Lang = "en" }, func(x *itemEvidence) { x.ExpireTime = "" },
	} {
		bad := row
		change(&bad)
		write([]itemEvidence{bad})
		require.Panics(t, func() { bindDomainEvidence(dir, []itemEvidence{item}) })
	}
	write(nil)
	require.Panics(t, func() { bindDomainEvidence(dir, []itemEvidence{item}) })
	write([]itemEvidence{row, row})
	require.Panics(t, func() { bindDomainEvidence(dir, []itemEvidence{item}) })
}

func TestLexicalRepairCannotReplaceBaselineGate(t *testing.T) {
	t.Setenv("FIDELITY_LEXICAL_REPAIR", "true")
	t.Setenv("FIDELITY_BASELINE_ONLY", "true")
	require.Panics(t, func() { replayVariant() })
	t.Setenv("FIDELITY_BASELINE_ONLY", "")
	t.Setenv("FIDELITY_UGC_OWNERSHIP", "true")
	t.Setenv("FIDELITY_UGC_METADATA", "true")
	t.Setenv("FIDELITY_UGC_PAIRED", "true")
	_, _, variant := replayVariant()
	require.Equal(t, "ugc-ownership-paired-lexical", variant)
}
