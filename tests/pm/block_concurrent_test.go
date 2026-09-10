package pm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestBlockUser_ConcurrentDuplicateReturnsClientError(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"concurrent_block_a@test.com", "concurrent_block_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, emails[0], "Concurrent Block A", "bio")
	agentB := testutil.RegisterAgent(t, emails[1], "Concurrent Block B", "bio")
	uidA := testutil.MustID(t, agentA["agent_id"], "agent_id")
	uidB := testutil.MustID(t, agentB["agent_id"], "agent_id")
	defer cleanRelationsData(t, uidA, uidB)

	type blockResult struct {
		remark string
		status int
		code   int
		msg    string
		err    error
	}
	start := make(chan struct{})
	results := make(chan blockResult, 2)
	client := &http.Client{Timeout: 15 * time.Second}
	for _, remark := range []string{"first contender", "second contender"} {
		body, err := json.Marshal(map[string]string{
			"to_uid": agentB["agent_id"].(string),
			"remark": remark,
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, testutil.BaseURL+"/api/v1/relations/block", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+agentA["token"].(string))
		go func(remark string, req *http.Request) {
			<-start
			result := blockResult{remark: remark}
			defer func() { results <- result }()
			resp, err := client.Do(req)
			if err != nil {
				result.err = err
				return
			}
			defer resp.Body.Close()
			result.status = resp.StatusCode
			var payload struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				result.err = fmt.Errorf("decode block response: %w", err)
				return
			}
			result.code, result.msg = payload.Code, payload.Msg
		}(remark, req)
	}
	close(start)

	responses := []blockResult{<-results, <-results}
	successes, conflicts := 0, 0
	winningRemark := ""
	for _, result := range responses {
		if result.err != nil {
			t.Fatalf("%s failed: %v", result.remark, result.err)
		}
		if result.status != http.StatusOK {
			t.Fatalf("%s HTTP status=%d, want 200; code=%d msg=%q", result.remark, result.status, result.code, result.msg)
		}
		switch result.code {
		case 0:
			successes++
			winningRemark = result.remark
		case 409:
			conflicts++
			if result.msg != "already blocked" {
				t.Fatalf("duplicate block message=%q, want fixed client message", result.msg)
			}
		default:
			t.Fatalf("%s code=%d msg=%q, want success or duplicate-block client error", result.remark, result.code, result.msg)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent blocks: successes=%d conflicts=%d, want one of each", successes, conflicts)
	}

	var count int
	var storedRemark string
	err := testutil.TestDB.QueryRow(
		"SELECT COUNT(*), MIN(remark) FROM user_relations WHERE from_uid = $1 AND to_uid = $2 AND rel_type = 2",
		uidA, uidB,
	).Scan(&count, &storedRemark)
	if err != nil {
		t.Fatalf("read persisted block: %v", err)
	}
	if count != 1 || storedRemark != winningRemark {
		t.Fatalf("persisted blocks=%d remark=%q, want one block with winning remark %q", count, storedRemark, winningRemark)
	}
}
