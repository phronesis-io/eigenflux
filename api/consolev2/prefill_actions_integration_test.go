package consolev2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/gorm"
)

func testPrefillActions(t *testing.T, db *gorm.DB, h *server.Hertz, ids *fixedIDGenerator, agentID int64, sourceID, token, cookie, csrf string) {
	t.Helper()
	headers := []ut.Header{{Key: "Cookie", Value: cookie}, {Key: "X-CSRF-Token", Value: csrf}}
	agentHeaders := []ut.Header{{Key: "Authorization", Value: "Bearer " + token}}
	var revision int64
	if err := db.Raw(`SELECT active_revision FROM agent_context_heads WHERE agent_id = ?`, agentID).Scan(&revision).Error; err != nil {
		t.Fatal(err)
	}
	// Keep the original open item for the surrounding projection regression.
	for _, action := range []string{"ask_agent_summarize", "not_interested", "dismiss"} {
		id, _ := ids.NextID()
		itemID := fmt.Sprint(id)
		flag := action
		if flag == "dismiss" {
			flag = "not_interested"
		}
		if err := db.Exec(`INSERT INTO agent_attention_items SELECT (jsonb_populate_record(NULL::agent_attention_items,
  to_jsonb(item) || jsonb_build_object('attention_id', CAST(? AS bigint), 'client_item_id', CAST(? AS text),
   'actions_snapshot', jsonb_build_array(jsonb_build_object('action_key','choose','kind','preset','flag',CAST(? AS text),'appearance','primary'))))).*
  FROM agent_attention_items item WHERE item.agent_id = ? AND item.attention_id = ?`, id, "prefill-test-"+itemID, flag, agentID, sourceID).Error; err != nil {
			t.Fatal(err)
		}
		path := "/api/v2/console/attention-items/" + itemID + "/"
		if action == "dismiss" {
			for _, replay := range []bool{false, true} {
				status, payload, _ := performJSON(t, h, "POST", path+"dismiss", map[string]interface{}{"expected_item_revision": 1}, headers...)
				if status != http.StatusOK || responseData(t, payload)["idempotent_replay"] != replay {
					t.Fatalf("prefill dismiss status=%d payload=%#v", status, payload)
				}
			}
			continue
		}
		req := respondAttentionRequest{ActionKey: "choose", ExpectedItemRevision: 1, IdempotencyKey: "prefill-response-" + itemID}
		wrongRevision := req
		wrongRevision.ExpectedItemRevision = 2
		status, payload, _ := performJSON(t, h, "POST", path+"respond", wrongRevision, headers...)
		if status != http.StatusConflict || responseErrorCode(t, payload) != "ATTENTION_RESPONSE_CONFLICT" {
			t.Fatalf("prefill stale revision status=%d payload=%#v", status, payload)
		}
		var expiresAt int64
		if err := db.Raw(`SELECT expires_at FROM agent_attention_items WHERE attention_id = ?`, id).Scan(&expiresAt).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`UPDATE agent_attention_items SET generated_at = 1, expires_at = 2 WHERE attention_id = ?`, id).Error; err != nil {
			t.Fatal(err)
		}
		status, payload, _ = performJSON(t, h, "POST", path+"respond", req, headers...)
		if err := db.Exec(`UPDATE agent_attention_items SET expires_at = ? WHERE attention_id = ?`, expiresAt, id).Error; err != nil {
			t.Fatal(err)
		}
		if status != http.StatusConflict {
			t.Fatalf("expired prefill status=%d payload=%#v", status, payload)
		}
		// A completed onboarding row alone is insufficient without an active context.
		if err := db.Exec(`UPDATE agent_context_heads SET active_revision = NULL WHERE agent_id = ?`, agentID).Error; err != nil {
			t.Fatal(err)
		}
		status, payload, _ = performJSON(t, h, "POST", path+"respond", req, headers...)
		if err := db.Exec(`UPDATE agent_context_heads SET active_revision = ? WHERE agent_id = ?`, revision, agentID).Error; err != nil {
			t.Fatal(err)
		}
		if status != http.StatusConflict || responseErrorCode(t, payload) != "ATTENTION_CONTEXT_STALE" {
			t.Fatalf("prefill without context status=%d payload=%#v", status, payload)
		}
		status, payload, _ = performJSON(t, h, "POST", path+"respond", req, headers...)
		if status != http.StatusAccepted {
			t.Fatalf("prefill response status=%d payload=%#v", status, payload)
		}
		commandID := responseData(t, payload)["command_id"].(string)
		status, payload, _ = performJSON(t, h, "POST", path+"respond", req, headers...)
		if status != http.StatusOK || responseData(t, payload)["replay"] != true || responseData(t, payload)["command_id"] != commandID {
			t.Fatalf("prefill replay status=%d payload=%#v", status, payload)
		}
		duplicate := req
		duplicate.IdempotencyKey += "-duplicate"
		status, payload, _ = performJSON(t, h, "POST", path+"respond", duplicate, headers...)
		if status != http.StatusConflict {
			t.Fatalf("duplicate prefill choice status=%d payload=%#v", status, payload)
		}
		var frozenRevision int64
		if err := db.Raw(`SELECT required_context_revision FROM agent_commands WHERE command_id = ?`, commandID).Scan(&frozenRevision).Error; err != nil || frozenRevision != revision {
			t.Fatalf("prefill context=%d expected=%d err=%v", frozenRevision, revision, err)
		}
		status, payload, _ = performJSON(t, h, "POST", "/api/v2/runtime/heartbeat", runtimeHeartbeatRequest{RuntimeInstanceID: "prefill-runtime", Capabilities: []string{"commands"}, AppliedContextRevision: &revision}, agentHeaders...)
		if status != http.StatusOK {
			t.Fatalf("prefill heartbeat status=%d payload=%#v", status, payload)
		}
		status, payload, _ = performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/claim", claimAgentCommandRequest{RuntimeInstanceID: "prefill-runtime", AppliedContextRevision: revision}, agentHeaders...)
		if status != http.StatusOK {
			t.Fatalf("prefill claim status=%d payload=%#v", status, payload)
		}
		claim := responseData(t, payload)
		status, payload, _ = performJSON(t, h, "POST", "/api/v2/agent-commands/"+commandID+"/complete", completeAgentCommandRequest{RuntimeInstanceID: "prefill-runtime", ClaimEpoch: int64(claim["claim_epoch"].(float64)), ClaimToken: claim["claim_token"].(string), Status: "completed", Result: json.RawMessage(`{"summary":"Reviewed the initial signal.","related_entities":[]}`)}, agentHeaders...)
		if status != http.StatusOK {
			t.Fatalf("prefill complete status=%d payload=%#v", status, payload)
		}
		status, payload, _ = performJSON(t, h, "POST", path+"respond", req, headers...)
		data := responseData(t, payload)
		if status != http.StatusOK || data["status"] != "acted" || data["command_status"] != "completed" || data["item_revision"] != float64(3) {
			t.Fatalf("completed prefill replay status=%d payload=%#v", status, payload)
		}
		var phase string
		if err := db.Raw(`SELECT attention_phase FROM agent_attention_items WHERE attention_id = ?`, id).Scan(&phase).Error; err != nil || phase != "prefill" {
			t.Fatalf("prefill provenance changed phase=%q err=%v", phase, err)
		}
	}
}
