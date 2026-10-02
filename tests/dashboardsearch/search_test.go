package dashboardsearch_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/dashboardsearch"
	_ "github.com/lib/pq"
)

// This tier requires an explicitly selected disposable stack with both migration
// namespaces applied. Fixtures remain in that stack for browser inspection.
func TestDashboardSearchDeployed(t *testing.T) {
	endpoint := os.Getenv("DASHBOARD_SEARCH_TEST_URL")
	if endpoint == "" {
		t.Skip("DASHBOARD_SEARCH_TEST_URL selects an isolated deployed stack")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("loopback test endpoint required")
	}
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Fatal("EIGENFLUX_TEST_CLI required")
	}
	db, err := sql.Open("postgres", os.Getenv("PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	run := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UnixMicro()
	owner, peer, other := base, base+1, base+2
	now := time.Now().UnixMilli()
	term := fmt.Sprintf("查找_%%! %d", base)
	for _, id := range []int64{owner, peer, other} {
		short, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		run(`INSERT INTO agents(agent_id,short_id,email,agent_name,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5)`, id, short, fmt.Sprintf("dashboard-search-%d@example.test", id), term, now)
	}
	token := fmt.Sprintf("efv2a_dashboard_search_%d", base)
	hash := sha256.Sum256([]byte(token))
	run(`INSERT INTO agent_principals(principal_id,agent_id,key_type,public_key,key_fingerprint,status,created_at,last_seen_at) VALUES($1,$2,'ed25519-v1',$3,$4,'active',$5,$5)`, base+10, owner, make([]byte, 32), fmt.Sprint(base), now)
	run(`INSERT INTO agent_credential_sessions(principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at) VALUES($1,$2,$3,$2,'agent_v2',ARRAY['profile:read','communication:read','relations:read','trade:write'],$4,$5,$5,$4)`, base+10, fmt.Sprint(base), hex.EncodeToString(hash[:]), now, now+3600000)
	run(`INSERT INTO agent_context_revisions(agent_id,revision,compiled_context,schema_version,generated_at) VALUES($1,1,'{}'::jsonb,1,$2)`, owner, now)
	run(`INSERT INTO agent_onboarding_v2(agent_id,state,current_step,active_context_revision,completed_at,created_at,updated_at) VALUES($1,'completed',5,1,$2,$2,$2)`, owner, now)
	sessionID := fmt.Sprintf("dashboard-search-%d", base)
	run(`INSERT INTO console_v2_sessions(session_id,session_secret_hash,agent_id,principal_id,csrf_secret_hash,status,issued_at,idle_expires_at,absolute_expires_at,last_seen_at) VALUES($1,$2,$3,$4,$2,'active',$5,$6,$6,$5)`, sessionID, hex.EncodeToString(hash[:]), owner, base+10, now, now+3600000)
	run(`INSERT INTO user_relations(from_uid,to_uid,rel_type,remark,created_at) VALUES($1,$2,1,$3,$4)`, owner, peer, term, now)
	for i, author := range []int64{owner, other} {
		id := base + 100 + int64(i)
		run(`INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES($1,$2,$3,$4)`, id, author, term, now)
		run(`INSERT INTO processed_items(item_id,status,updated_at) VALUES($1,3,$2)`, id, now)
		run(`INSERT INTO conversations(conv_id,participant_a,participant_b,initiator_id,last_sender_id,origin_type,origin_id,msg_count,status,updated_at,topic_status) VALUES($1,$2,$3,$2,$2,'friend',0,1,0,$4,1)`, base+200+int64(i), author, peer, now)
		run(`INSERT INTO private_messages(msg_id,conv_id,sender_id,receiver_id,content,is_read,created_at) VALUES($1,$2,$3,$4,$5,false,$6)`, base+300+int64(i), base+200+int64(i), author, peer, term, now)
		commission := base + 400 + int64(i)
		run(`INSERT INTO commission_definitions(commission_id,seller_agent_id,created_at,updated_at) VALUES($1,$2,$3,$3)`, commission, author, now)
		run(`INSERT INTO commission_drafts(commission_id,title,price_fen,promised_delivery_ms,created_at,updated_at) VALUES($1,$2,100,3600000,$3,$3)`, commission, term, now)
		order, workspace, snapshot := base+500+int64(i), base+600+int64(i), base+700+int64(i)
		run(`INSERT INTO order_workspaces(workspace_id,order_id,buyer_agent_id,seller_agent_id,lifecycle,version,created_at,updated_at) VALUES($1,$2,$3,$4,'active',1,$5,$5)`, workspace, order, author, peer, now)
		run(`INSERT INTO workspace_snapshots(snapshot_id,workspace_id,order_version,order_state,manifest_sha256,entry_count,total_bytes,sealed_at) VALUES($1,$2,1,'completed',$3,0,0,$4)`, snapshot, workspace, strings.Repeat("a", 64), now)
		run(`INSERT INTO orders(order_id,buyer_agent_id,seller_agent_id,state,setup_state,version,commission_id,commission_revision,frozen_title,frozen_capability_description,frozen_request_spec_text,frozen_delivery_spec_text,gross_amount_fen,commission_fen,seller_net_fen,currency,frozen_promised_delivery_ms,workspace_id,current_snapshot_id,created_at,updated_at) VALUES($1,$2,$3,'completed','ready',1,$4,1,$5,'description','request','delivery',100,20,80,'CNY',3600000,$6,$7,$8,$8)`, order, author, peer, commission, term, workspace, snapshot, now)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	request := func(q, kind string) []dashboardsearch.Group {
		t.Helper()
		target := endpoint + "/api/v2/dashboard/search?" + url.Values{"q": {q}, "type": {kind}}.Encode()
		req, _ := http.NewRequest("GET", target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var payload struct {
			Data struct {
				Groups []dashboardsearch.Group `json:"groups"`
			} `json:"data"`
		}
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("HTTP %d", res.StatusCode)
		}
		return payload.Data.Groups
	}
	groups := request(term, "all")
	if len(groups) != 5 {
		t.Fatalf("groups=%#v", groups)
	}
	for _, g := range groups {
		if g.Error != "" || len(g.Items) != 1 {
			t.Fatalf("%s: %#v", g.Type, g)
		}
	}
	for kind, id := range map[string]int64{"broadcast": base + 101, "message": base + 301, "service": base + 401, "order": base + 501} {
		g := request(strconv.FormatInt(id, 10), kind)
		if len(g) != 1 || g[0].Error != "" || len(g[0].Items) != 0 {
			t.Fatalf("cross-owner result: %#v", g)
		}
	}
	for _, check := range []struct {
		path   string
		status int
		want   string
	}{
		{"/api/v2/console/search?" + url.Values{"q": {term}}.Encode(), 200, `"type":"order"`},
		{fmt.Sprintf("/api/v2/console/bff/trade/commissions?commission_id=%d&limit=1", base+400), 200, fmt.Sprint(base + 400)},
		{fmt.Sprintf("/api/v2/console/bff/trade/commissions?commission_id=%d&limit=1", base+401), 403, ""},
	} {
		req, _ := http.NewRequest("GET", endpoint+check.path, nil)
		req.AddCookie(&http.Cookie{Name: "ef_console_v2", Value: sessionID + "." + token})
		res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != check.status || !strings.Contains(string(body), check.want) {
			t.Fatalf("Console read %s: status=%d body=%s err=%v", check.path, res.StatusCode, body, err)
		}
	}
	home := filepath.Join(t.TempDir(), ".eigenflux")
	requireWrite := func(path string, v interface{}) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	requireWrite(filepath.Join(home, "config.json"), map[string]interface{}{"default_server": "test", "servers": []map[string]string{{"name": "test", "endpoint": endpoint}}, "kv": map[string]string{"auto_skill_sync": "false"}})
	requireWrite(filepath.Join(home, "servers", "test", "agent-v2-credentials.json"), map[string]interface{}{"agent_id": fmt.Sprint(owner), "access_token": token, "refresh_token": "test-refresh", "expires_at": now + 3600000})
	cmd := exec.Command(binary, "--homedir", home, "--server", "test", "dashboard", "search", term, "--format", "json")
	cmd.Env = append(os.Environ(), "EIGENFLUX_SKILLS_DIR="+filepath.Join(t.TempDir(), "skills"), "EIGENFLUX_SKILLS_BASE_URL=http://127.0.0.1:1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI: %v %s", err, out)
	}
	var cli struct {
		Groups []dashboardsearch.Group `json:"groups"`
	}
	if err := json.Unmarshal(out, &cli); err != nil {
		t.Fatalf("JSON: %v %s", err, out)
	}
	if len(cli.Groups) != 5 {
		t.Fatalf("CLI groups=%#v", cli.Groups)
	}
	t.Log("Verified five categories across HTTP, trusted Commission delegation, RPC, PostgreSQL and the CLI; foreign records excluded")
}
