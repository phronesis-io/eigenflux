package agti_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"eigenflux_server/tests/testutil"
)

// End-to-end flow of the AgentRapport quiz (/api/v1/agti/*): question pickup,
// commit-reveal locking, human submit idempotency and the shareable result.
// Requires the local stack (scripts/local/start_local.sh) like the other e2e
// suites. Session creation is IP rate limited (10/min); this test creates a
// single session, so a fresh gateway start is enough headroom.
func TestAgentRapportFlow(t *testing.T) {
	testutil.WaitForAPI(t)

	// --- start a session ---
	newResp := testutil.DoPost(t, "/api/v1/agti/quiz/new", map[string]string{}, "")
	if int(newResp["code"].(float64)) != 0 {
		t.Fatalf("quiz/new failed: %v", newResp)
	}
	data := newResp["data"].(map[string]interface{})
	sid := data["session_id"].(string)
	questions := data["questions"].([]interface{})
	if len(questions) != 10 {
		t.Fatalf("expected 10 questions, got %d", len(questions))
	}
	q0 := questions[0].(map[string]interface{})
	opt0 := q0["options"].([]interface{})[0].(map[string]interface{})
	if _, leaked := opt0["energy"]; leaked {
		t.Fatal("scoring metadata (energy) leaked to client")
	}

	// Build answer sets: agent picks first option, human differs on 3 questions.
	agentAns := map[string]string{}
	humanAns := map[string]string{}
	for i, qi := range questions {
		q := qi.(map[string]interface{})
		opts := q["options"].([]interface{})
		first := opts[0].(map[string]interface{})["key"].(string)
		agentAns[q["id"].(string)] = first
		if i < 3 && len(opts) > 1 {
			humanAns[q["id"].(string)] = opts[1].(map[string]interface{})["key"].(string)
		} else {
			humanAns[q["id"].(string)] = first
		}
	}

	quizPath := fmt.Sprintf("/api/v1/agti/quiz/%s", sid)

	// --- commit-reveal: human cannot submit before the agent locks ---
	early := testutil.DoPost(t, quizPath+"/human", map[string]interface{}{"answers": humanAns}, "")
	if int(early["code"].(float64)) != 409 {
		t.Fatalf("human submit before agent lock should be 409, got %v", early)
	}

	// --- agent locks ---
	lock := testutil.DoPost(t, quizPath+"/agent", map[string]interface{}{"answers": agentAns}, "")
	if int(lock["code"].(float64)) != 0 {
		t.Fatalf("agent submit failed: %v", lock)
	}
	humanURL := lock["data"].(map[string]interface{})["human_url"].(string)
	if !strings.Contains(humanURL, "/agti/q/"+sid) {
		t.Fatalf("unexpected human_url: %s", humanURL)
	}

	// --- locked: second agent submit is rejected ---
	relock := testutil.DoPost(t, quizPath+"/agent", map[string]interface{}{"answers": agentAns}, "")
	if int(relock["code"].(float64)) != 409 {
		t.Fatalf("second agent submit should be 409, got %v", relock)
	}

	// --- human page state: flags only, never the agent's answers ---
	state := testutil.DoGet(t, quizPath, "")
	stateData := state["data"].(map[string]interface{})
	if stateData["agent_submitted"] != true || stateData["human_submitted"] != false {
		t.Fatalf("unexpected state flags: %v", stateData)
	}

	// --- human submits; retry must be idempotent ---
	submit := testutil.DoPost(t, quizPath+"/human", map[string]interface{}{"answers": humanAns}, "")
	if int(submit["code"].(float64)) != 0 {
		t.Fatalf("human submit failed: %v", submit)
	}
	rid := submit["data"].(map[string]interface{})["result_id"].(string)
	retry := testutil.DoPost(t, quizPath+"/human", map[string]interface{}{"answers": agentAns}, "")
	if got := retry["data"].(map[string]interface{})["result_id"].(string); got != rid {
		t.Fatalf("human resubmit must return the same result, got %s want %s", got, rid)
	}

	// --- shareable result ---
	result := testutil.DoGet(t, "/api/v1/agti/result/"+rid, "")
	rd := result["data"].(map[string]interface{})
	typeInfo := rd["type"].(map[string]interface{})
	if typeInfo["code"] == "" || rd["agent_view"] == "" {
		t.Fatalf("result payload incomplete: %v", rd)
	}
	if int(rd["match"].(float64)) != 7 || int(rd["total"].(float64)) != 10 {
		t.Fatalf("expected match 7/10, got %v/%v", rd["match"], rd["total"])
	}

	// --- type gallery: 10 types, desc stays server-side until the result ---
	types := testutil.DoGet(t, "/api/v1/agti/types", "")
	list := types["data"].(map[string]interface{})["types"].([]interface{})
	if len(list) != 10 {
		t.Fatalf("expected 10 types, got %d", len(list))
	}
	if _, leaked := list[0].(map[string]interface{})["desc"]; leaked {
		t.Fatal("type desc leaked in gallery list")
	}
}

func TestAgentRapportNotFound(t *testing.T) {
	testutil.WaitForAPI(t)
	missQuiz := testutil.DoGet(t, "/api/v1/agti/quiz/deadbeef00000000", "")
	if int(missQuiz["code"].(float64)) != 404 {
		t.Fatalf("missing session should be 404, got %v", missQuiz)
	}
	missResult := testutil.DoGet(t, "/api/v1/agti/result/deadbeef00000000", "")
	if int(missResult["code"].(float64)) != 404 {
		t.Fatalf("missing result should be 404, got %v", missResult)
	}
}

func TestAgentRapportSkillsDoc(t *testing.T) {
	testutil.WaitForAPI(t)
	resp, err := http.Get(testutil.BaseURL + "/agti/skills")
	if err != nil {
		t.Fatalf("GET /agti/skills failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("skills doc status %d", resp.StatusCode)
	}
	doc := string(body)
	if !strings.Contains(doc, "/api/v1/agti/quiz/new") {
		t.Fatal("skills doc missing quiz/new endpoint")
	}
	if strings.Contains(doc, "{{") {
		t.Fatal("skills doc has unrendered template variables")
	}
}
