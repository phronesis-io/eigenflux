package main

import (
	"os"
	"testing"

	profiledal "eigenflux_server/rpc/profile/dal"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestUpsertAgentCardVersionAndNoOpSemantics(t *testing.T) {
	var agentID int64
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw("SELECT agent_id FROM agents ORDER BY agent_id LIMIT 1").Scan(&agentID).Error; err != nil || agentID == 0 {
		t.Skip("requires one agent in the local integration database")
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	const source = int64(8_000_000_000_000_000_000)
	const fence = int64(8_000_000_000_000_000_000)
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":1}`, `{"p":1}`, 1, source, fence); err != nil {
		t.Fatal(err)
	}
	first, err := profiledal.GetAgentCard(tx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":1}`, `{"p":1}`, 1, source, fence+1); err != nil {
		t.Fatal(err)
	}
	noOp, _ := profiledal.GetAgentCard(tx, agentID)
	if noOp.CardVersion != first.CardVersion || noOp.GeneratedAt != first.GeneratedAt {
		t.Fatal("identical upsert changed projection metadata")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":2}`, `{"p":1}`, 1, source, fence+2); err != nil {
		t.Fatal(err)
	}
	changed, _ := profiledal.GetAgentCard(tx, agentID)
	if changed.CardVersion != first.CardVersion+1 {
		t.Fatal("content change did not advance card_version")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":2}`, `{"p":1}`, 1, source+1, fence+3); err != nil {
		t.Fatal(err)
	}
	advanced, _ := profiledal.GetAgentCard(tx, agentID)
	if advanced.SourceVersion != source+1 || advanced.CardVersion != changed.CardVersion {
		t.Fatal("source-only advance changed visible version")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":3}`, `{"p":1}`, 1, source+2, fence+1); err != nil {
		t.Fatalf("newer source with an older fence was rejected: %v", err)
	}
	newerSource, _ := profiledal.GetAgentCard(tx, agentID)
	if newerSource.SourceVersion != source+2 || newerSource.CardVersion != advanced.CardVersion+1 {
		t.Fatal("lexicographically newer source was not accepted")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"stale":true}`, `{"p":1}`, 1, source, fence+2); err == nil {
		t.Fatal("stale different projection was acknowledged")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"stale":true}`, `{"p":1}`, 1, source, fence+1); err == nil {
		t.Fatal("older fence overwrote a newer projection")
	}
	if err := tx.Exec(`UPDATE agent_cards SET public_card = '{"legacy":true}'::jsonb WHERE agent_id = ?`, agentID).Error; err == nil {
		t.Fatal("database trigger accepted a legacy content write without an ordering-key advance")
	}
}
