package needs_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestNeedInputMigrationPreservesHistory(t *testing.T) {
	h := setup(t)
	tx := h.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := tx.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	schema := fmt.Sprintf("need_migration_%d", h.owner)
	exec("CREATE SCHEMA " + schema)
	exec("SET LOCAL search_path TO " + schema)
	exec("CREATE TABLE agents(agent_id bigint PRIMARY KEY)")
	exec("CREATE TABLE agent_intent_actions(agent_id bigint, intent_id bigint, version bigint, status text, UNIQUE(agent_id,intent_id))")
	read := func(name string) (string, string) {
		t.Helper()
		raw, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		up, down, ok := strings.Cut(string(raw), "-- +goose Down")
		if !ok {
			t.Fatal("missing migration boundary")
		}
		return up, down
	}
	old, _ := read("000105_need_inputs_normalized.sql")
	exec(old)
	exec("INSERT INTO agents VALUES (1)")
	exec("INSERT INTO agent_intent_actions VALUES (1,2,1,'active')")
	input := `{"schema_version":"need_input.v1","intent_id":"2","intent_version":1,"need_type":"broadcast","target":{"desc":"Find SQL references","candidate_needs":["SQL"]}}`
	exec(`INSERT INTO need_inputs VALUES (3,1,2,1,'need_input.v1',?::jsonb,'{"watch_for":"original"}', 'normalized','original-key','unchanged-hash',1,1)`, input)
	exec(`INSERT INTO normalized_needs(normalized_need_id,need_input_id,agent_id,intent_id,intent_version,schema_version,normalized,normalizer_version,created_at,updated_at) VALUES (4,3,1,2,1,'normalized_need.v1','{"desc":"SQL","candidate_needs":["SQL"]}','basic.v1',1,1)`)
	var before, after string
	if err := tx.Raw("SELECT row_to_json(i)::text FROM need_inputs i WHERE need_input_id=3").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	up, down := read("000106_need_input_v2.sql")
	exec(up)
	if err := tx.Raw("SELECT row_to_json(i)::text FROM need_inputs i WHERE need_input_id=3").Scan(&after).Error; err != nil || before != after {
		t.Fatal("migration changed historical input", err)
	}
	count := func(query string, want int64) {
		t.Helper()
		var got int64
		if err := tx.Raw(query).Scan(&got).Error; err != nil || got != want {
			t.Fatal(query, got, err)
		}
	}
	count("SELECT count(*) FROM current_need_inputs", 1)
	count("SELECT count(*) FROM normalized_needs WHERE normalizer_version='basic.v1' AND normalized->>'desc'='SQL'", 1)
	exec(down)
	exec(up)
	exec(`INSERT INTO need_inputs VALUES (5,1,2,1,'need_input.v2','{"schema_version":"need_input.v2","intent_id":"2","intent_version":1,"need_type":"agent","target":{"goal":"Find a reviewer"}}','{}','active','v2-input-key','new-hash',2,2)`)
	exec("SAVEPOINT downgrade_guard")
	if err := tx.Exec(down).Error; err == nil || !strings.Contains(err.Error(), "Cannot downgrade") {
		t.Fatal("lossy downgrade accepted", err)
	}
	exec("ROLLBACK TO SAVEPOINT downgrade_guard")
	count("SELECT count(*) FROM current_need_inputs", 2)
	count("SELECT count(*) FROM normalized_needs", 1)
	exec("UPDATE agent_intent_actions SET version=2")
	count("SELECT count(*) FROM current_need_inputs", 0)
}
