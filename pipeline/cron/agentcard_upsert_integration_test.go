package main

import (
	"context"
	"os"
	"testing"

	"eigenflux_server/pkg/agentcard"
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
	if changed.PublicCardVersion != first.PublicCardVersion+1 {
		t.Fatal("public content change did not advance public_card_version")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":2}`, `{"p":2}`, 1, source, fence+3); err != nil {
		t.Fatal(err)
	}
	privateChanged, _ := profiledal.GetAgentCard(tx, agentID)
	if privateChanged.CardVersion != changed.CardVersion+1 {
		t.Fatal("private content change did not advance card_version")
	}
	if privateChanged.PublicCardVersion != changed.PublicCardVersion || privateChanged.PublicCardGeneratedAt != changed.PublicCardGeneratedAt {
		t.Fatal("private content change advanced public projection metadata")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":2}`, `{"p":2}`, 1, source+1, fence+4); err != nil {
		t.Fatal(err)
	}
	advanced, _ := profiledal.GetAgentCard(tx, agentID)
	if advanced.SourceVersion != source+1 || advanced.CardVersion != privateChanged.CardVersion {
		t.Fatal("source-only advance changed visible version")
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":3}`, `{"p":2}`, 1, source+2, fence+1); err != nil {
		t.Fatalf("newer source with an older fence was rejected: %v", err)
	}
	newerSource, _ := profiledal.GetAgentCard(tx, agentID)
	if newerSource.SourceVersion != source+2 || newerSource.CardVersion != advanced.CardVersion+1 {
		t.Fatal("lexicographically newer source was not accepted")
	}
	cards, err := profiledal.GetAgentCards(tx, []int64{agentID, agentID, 0, -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[agentID] == nil || cards[agentID].PrivateCard != "" {
		t.Fatal("batch card projection must deduplicate IDs and exclude private card data")
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

func TestRebuildFenceSequenceIsMonotonicAcrossConnections(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connA, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()
	var a1, b1, a2 int64
	if err := connA.QueryRowContext(ctx, `SELECT nextval('agent_card_rebuild_fence_seq')`).Scan(&a1); err != nil {
		t.Fatal(err)
	}
	if err := connB.QueryRowContext(ctx, `SELECT nextval('agent_card_rebuild_fence_seq')`).Scan(&b1); err != nil {
		t.Fatal(err)
	}
	if err := connA.QueryRowContext(ctx, `SELECT nextval('agent_card_rebuild_fence_seq')`).Scan(&a2); err != nil {
		t.Fatal(err)
	}
	if !(a1 < b1 && b1 < a2) {
		t.Fatalf("sequence is not globally monotonic: A=%d B=%d A=%d", a1, b1, a2)
	}
}

func TestInfluenceRollupTracksFactMutations(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	const agentID, otherID, itemID, movedItemID = int64(9_100_001), int64(9_100_002), int64(9_200_001), int64(9_200_002)
	if err := tx.Exec(`INSERT INTO agents(agent_id,short_id,email,agent_name,created_at,updated_at) VALUES
		(?, 'RoLlA', 'rollup-a@test.local', 'rollup-a', 1, 1), (?, 'RoLlB', 'rollup-b@test.local', 'rollup-b', 1, 1)`, agentID, otherID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES
		(?,?,'x',1),(?,?,'moved',1)`, itemID, agentID, movedItemID, agentID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO item_stats(item_id,author_agent_id,consumed_count,score_1_count,score_2_count,total_score,created_at,updated_at)
		VALUES (?,?,7,2,3,8,1,1)`, itemID, agentID).Error; err != nil {
		t.Fatal(err)
	}
	assertRollup(t, tx, agentID, 8, 1, 7)
	if err := tx.Exec(`UPDATE item_stats SET item_id=? WHERE item_id=?`, movedItemID, itemID).Error; err != nil {
		t.Fatal(err)
	}
	assertRollup(t, tx, agentID, 8, 1, 7)
	if err := tx.Exec(`UPDATE item_stats SET author_agent_id=?, consumed_count=9, score_2_count=4, total_score=10 WHERE item_id=?`, otherID, movedItemID).Error; err != nil {
		t.Fatal(err)
	}
	assertRollup(t, tx, agentID, 0, 0, 0)
	assertRollup(t, tx, otherID, 10, 1, 9)
	if err := tx.Exec(`DELETE FROM item_stats WHERE item_id=?`, movedItemID).Error; err != nil {
		t.Fatal(err)
	}
	assertRollup(t, tx, otherID, 0, 0, 0)
}

func TestInfluenceBackfillRepairsDirectGooseState(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	const agentID, itemID = int64(9_110_001), int64(9_210_001)
	if err := tx.Exec(`INSERT INTO agents(agent_id,short_id,email,agent_name,created_at,updated_at)
		VALUES (?,'BaCkf','backfill@test.local','backfill',1,1)`, agentID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES (?,?,'x',1)`, itemID, agentID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO item_stats(item_id,author_agent_id,consumed_count,score_1_count,score_2_count,total_score,created_at,updated_at)
		VALUES (?,?,4,3,2,7,1,1)`, itemID, agentID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`UPDATE agent_influence_rollup_meta SET backfill_complete=FALSE`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`DELETE FROM agent_influence_rollups WHERE agent_id=?`, agentID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO agent_influence_rollup_pending(agent_id) VALUES (?) ON CONFLICT DO NOTHING`, agentID).Error; err != nil {
		t.Fatal(err)
	}
	processed, complete, err := agentcard.AdvanceInfluenceRollupBackfill(context.Background(), tx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || !complete {
		t.Fatalf("processed=%d complete=%v, want 1,true", processed, complete)
	}
	assertRollup(t, tx, agentID, 7, 1, 4)
}

func TestRollingDeploymentFenceTrigger(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	const agentID = int64(9_300_001)
	if err := tx.Exec(`INSERT INTO agents(agent_id,short_id,email,agent_name,created_at,updated_at) VALUES (?,'RoLin','rolling@test.local','rolling',1,1)`, agentID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO agent_cards(agent_id,public_card,private_card,schema_version,source_version,card_version,generated_at,rebuild_fence)
		VALUES (?,'{"v":0}','{}',1,1,1,1,0)`, agentID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`UPDATE agent_cards SET public_card='{"v":1}',card_version=2,generated_at=2 WHERE agent_id=?`, agentID).Error; err != nil {
		t.Fatalf("pre-fence rolling writer was rejected: %v", err)
	}
	if err := profiledal.UpsertAgentCardWithFence(tx, agentID, `{"v":2}`, `{}`, 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`UPDATE agent_cards SET card_version=card_version+1,generated_at=3 WHERE agent_id=?`, agentID).Error; err == nil {
		t.Fatal("post-fence legacy metadata write was accepted")
	}
	if err := tx.Exec(`UPDATE agent_cards SET public_card='{"legacy":true}',source_version=source_version+1,card_version=card_version+1,generated_at=4 WHERE agent_id=?`, agentID).Error; err == nil {
		t.Fatal("post-fence legacy writer bypassed the fence with a newer source_version")
	}
}

func assertRollup(t *testing.T, tx *gorm.DB, agentID, score, broadcasts, consumed int64) {
	t.Helper()
	var got struct{ Score, Broadcasts, Consumed int64 }
	if err := tx.Raw(`SELECT COALESCE(SUM(score_1_count)+2*SUM(score_2_count),0)::BIGINT score,
		COALESCE(SUM(broadcast_count),0)::BIGINT broadcasts,
		COALESCE(SUM(consumed_count),0)::BIGINT consumed
		FROM agent_influence_rollups WHERE agent_id=?`, agentID).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.Score != score || got.Broadcasts != broadcasts || got.Consumed != consumed {
		t.Fatalf("agent %d rollup=%+v want score=%d broadcasts=%d consumed=%d", agentID, got, score, broadcasts, consumed)
	}
}
