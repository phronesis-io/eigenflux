package dashboardsearch_test

import (
	"bytes"
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

type identity struct {
	id, peer         int64
	token, sessionID string
	offset           int64
}

func loopbackEndpoint(t *testing.T, raw, name string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || u.Scheme != "http" || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		t.Fatalf("%s must be an explicit loopback HTTP endpoint", name)
	}
	return strings.TrimRight(raw, "/")
}

// This tier requires an explicitly selected disposable stack with both migration
// namespaces applied. Fixtures remain in that stack for inspection. SQL creates
// deterministic records; all visibility revocations go through business APIs.
func TestDashboardSearchDeployed(t *testing.T) {
	endpoint := os.Getenv("DASHBOARD_SEARCH_TEST_URL")
	if endpoint == "" {
		t.Skip("DASHBOARD_SEARCH_TEST_URL selects an isolated deployed stack")
	}
	endpoint = loopbackEndpoint(t, endpoint, "DASHBOARD_SEARCH_TEST_URL")
	commissionEndpoint := loopbackEndpoint(t, os.Getenv("DASHBOARD_SEARCH_COMMISSION_URL"), "DASHBOARD_SEARCH_COMMISSION_URL")
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Fatal("EIGENFLUX_TEST_CLI required")
	}
	dsn := os.Getenv("PG_DSN")
	parsedDSN, err := url.Parse(dsn)
	if err != nil || (parsedDSN.Scheme != "postgres" && parsedDSN.Scheme != "postgresql") || (parsedDSN.Hostname() != "127.0.0.1" && parsedDSN.Hostname() != "localhost" && parsedDSN.Hostname() != "::1") {
		t.Fatal("PG_DSN must explicitly select loopback disposable PostgreSQL")
	}
	checks := map[string]bool{}
	evidence := map[string]interface{}{"schema_version": 1, "checks": checks, "passed": false, "limitations": []string{
		"Fixture creation uses SQL in an explicitly selected disposable database.",
		"Visibility revocation uses real Unfriend, CloseConv and DeleteCommission APIs; no production account or payment provider is used.",
		"This tests API and CLI search, not a frontend search page (that page is deferred upstream).",
	}}
	defer func() {
		evidence["passed"] = !t.Failed()
		if path := os.Getenv("DASHBOARD_SEARCH_EVIDENCE_FILE"); path != "" {
			data, err := json.MarshalIndent(evidence, "", "  ")
			if err == nil {
				err = os.WriteFile(path, append(data, '\n'), 0600)
			}
			if err != nil {
				t.Errorf("write evidence: %v", err)
			}
		}
	}()
	db, err := sql.Open("postgres", dsn)
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
	now := time.Now().UnixMilli()
	actors := []identity{
		{base, base + 1, fmt.Sprintf("efv2a_dashboard_search_%d", base), fmt.Sprintf("dashboard-search-%d", base), 0},
		{base + 2, base + 3, fmt.Sprintf("efv2a_dashboard_search_%d", base+2), fmt.Sprintf("dashboard-search-%d", base+2), 1},
	}
	term := fmt.Sprintf("查找_%%! %d", base)
	peerName := fmt.Sprintf("counterparty-only-%d", base)
	pagingTerm := fmt.Sprintf("paging-only-%d", base)
	revokeTerm := func(kind string) string { return fmt.Sprintf("revoke-%s-%d", kind, base) }
	for _, id := range []int64{base, base + 1, base + 2, base + 3} {
		short, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		name := term
		if id == base+1 {
			name += " " + peerName
		}
		run(`INSERT INTO agents(agent_id,short_id,email,agent_name,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5)`, id, short, fmt.Sprintf("dashboard-search-%d@example.test", id), name, now)
	}
	for _, actor := range actors {
		principal := actor.id + 10
		hash := sha256.Sum256([]byte(actor.token))
		run(`INSERT INTO agent_principals(principal_id,agent_id,key_type,public_key,key_fingerprint,status,created_at,last_seen_at) VALUES($1,$2,'ed25519-v1',$3,$4,'active',$5,$5)`, principal, actor.id, make([]byte, 32), fmt.Sprint(actor.id), now)
		run(`INSERT INTO agent_credential_sessions(principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at) VALUES($1,$2,$3,$2,'agent_v2',ARRAY['profile:read','communication:read','communication:write','relations:read','relations:write','trade:write'],$4,$5,$5,$4)`, principal, fmt.Sprint(actor.id), hex.EncodeToString(hash[:]), now, now+3600000)
		run(`INSERT INTO agent_context_revisions(agent_id,revision,compiled_context,schema_version,generated_at) VALUES($1,1,'{}'::jsonb,1,$2)`, actor.id, now)
		run(`INSERT INTO agent_onboarding_v2(agent_id,state,current_step,active_context_revision,completed_at,created_at,updated_at) VALUES($1,'completed',5,1,$2,$2,$2)`, actor.id, now)
		run(`INSERT INTO console_v2_sessions(session_id,session_secret_hash,agent_id,principal_id,csrf_secret_hash,status,issued_at,idle_expires_at,absolute_expires_at,last_seen_at) VALUES($1,$2,$3,$4,$2,'active',$5,$6,$6,$5)`, actor.sessionID, hex.EncodeToString(hash[:]), actor.id, principal, now, now+3600000)
		// Unfriend requires the symmetric relation established by acceptance.
		for _, pair := range [][2]int64{{actor.id, actor.peer}, {actor.peer, actor.id}} {
			run(`INSERT INTO user_relations(from_uid,to_uid,rel_type,remark,created_at) VALUES($1,$2,1,$3,$4)`, pair[0], pair[1], term+" "+revokeTerm("friend"), now)
		}
		id := base + 100 + actor.offset
		run(`INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES($1,$2,$3,$4)`, id, actor.id, term, now)
		run(`INSERT INTO processed_items(item_id,status,updated_at) VALUES($1,3,$2)`, id, now)
		// CloseConv hides broadcast-origin conversations, not friend-only chats.
		run(`INSERT INTO conversations(conv_id,participant_a,participant_b,initiator_id,last_sender_id,origin_type,origin_id,msg_count,status,updated_at,topic_status) VALUES($1,$2,$3,$2,$2,'broadcast',$4,1,0,$5,1)`, base+200+actor.offset, actor.id, actor.peer, id, now)
		run(`INSERT INTO private_messages(msg_id,conv_id,sender_id,receiver_id,content,is_read,created_at) VALUES($1,$2,$3,$4,$5,false,$6)`, base+300+actor.offset, base+200+actor.offset, actor.id, actor.peer, term+" "+revokeTerm("message"), now)
		commission := base + 400 + actor.offset
		run(`INSERT INTO commission_definitions(commission_id,seller_agent_id,created_at,updated_at) VALUES($1,$2,$3,$3)`, commission, actor.id, now)
		run(`INSERT INTO commission_drafts(commission_id,title,price_fen,promised_delivery_ms,created_at,updated_at) VALUES($1,$2,100,3600000,$3,$3)`, commission, term+" "+revokeTerm("service"), now)
		order, workspace, snapshot := base+500+actor.offset, base+600+actor.offset, base+700+actor.offset
		run(`INSERT INTO order_workspaces(workspace_id,order_id,buyer_agent_id,seller_agent_id,lifecycle,version,created_at,updated_at) VALUES($1,$2,$3,$4,'active',1,$5,$5)`, workspace, order, actor.id, actor.peer, now)
		run(`INSERT INTO workspace_snapshots(snapshot_id,workspace_id,order_version,order_state,manifest_sha256,entry_count,total_bytes,sealed_at) VALUES($1,$2,1,'completed',$3,0,0,$4)`, snapshot, workspace, strings.Repeat("a", 64), now)
		run(`INSERT INTO orders(order_id,buyer_agent_id,seller_agent_id,state,setup_state,version,commission_id,commission_revision,frozen_title,frozen_capability_description,frozen_request_spec_text,frozen_delivery_spec_text,gross_amount_fen,commission_fen,seller_net_fen,currency,frozen_promised_delivery_ms,workspace_id,current_snapshot_id,created_at,updated_at) VALUES($1,$2,$3,'completed','ready',1,$4,1,$5,'description','request','delivery',100,20,80,'CNY',3600000,$6,$7,$8,$8)`, order, actor.id, actor.peer, commission, term, workspace, snapshot, now)
	}
	for _, id := range []int64{base + 900, base + 901} {
		run(`INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES($1,$2,$3,$4)`, id, base, pagingTerm, now)
		run(`INSERT INTO processed_items(item_id,status,updated_at) VALUES($1,3,$2)`, id, now)
	}
	// This owned row would match if the query's underscore/percent were SQL
	// wildcards. Correct literal matching must keep it out of the five groups.
	run(`INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES($1,$2,$3,$4)`, base+902, base, strings.Replace(term, "_%!", "xy!", 1), now)
	run(`INSERT INTO processed_items(item_id,status,updated_at) VALUES($1,3,$2)`, base+902, now)
	profileToken := fmt.Sprintf("efv2a_dashboard_profile_%d", base)
	profileHash := sha256.Sum256([]byte(profileToken))
	run(`INSERT INTO agent_credential_sessions(principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at) VALUES($1,$2,$3,$2,'agent_v2',ARRAY['profile:read'],$4,$5,$5,$4)`, base+10, fmt.Sprintf("profile-%d", base), hex.EncodeToString(profileHash[:]), now, now+3600000)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	call := func(actor identity, console bool, method, target string, body interface{}) (int, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, target, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if console {
			req.AddCookie(&http.Cookie{Name: "ef_console_v2", Value: actor.sessionID + "." + actor.token})
		} else if actor.token != "" {
			req.Header.Set("Authorization", "Bearer "+actor.token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if method == http.MethodDelete {
			req.Header.Set("Idempotency-Key", fmt.Sprintf("dashboard-delete-%d", base))
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusTooManyRequests && res.Header.Get("Retry-After") == "" {
			t.Fatal("rate limit response omitted Retry-After")
		}
		payload, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, payload
	}
	request := func(actor identity, console bool, q, kind string, extra url.Values) []dashboardsearch.Group {
		t.Helper()
		query := url.Values{"q": {q}, "type": {kind}}
		for key, values := range extra {
			query[key] = values
		}
		path := "/api/v2/dashboard/search"
		if console {
			path = "/api/v2/console/search"
		}
		status, body := call(actor, console, http.MethodGet, endpoint+path+"?"+query.Encode(), nil)
		var payload struct {
			Code int `json:"code"`
			Data struct {
				Groups []dashboardsearch.Group `json:"groups"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || status != 200 || payload.Code != 0 {
			t.Fatalf("search status=%d code=%d decode=%v body=%s", status, payload.Code, err, body)
		}
		return payload.Data.Groups
	}
	assertOwned := func(actor identity, groups []dashboardsearch.Group) {
		t.Helper()
		want := map[string]int64{"message": base + 300 + actor.offset, "friend": actor.peer, "broadcast": base + 100 + actor.offset, "service": base + 400 + actor.offset, "order": base + 500 + actor.offset}
		if len(groups) != len(want) {
			t.Fatalf("missing categories: %#v", groups)
		}
		for _, g := range groups {
			id, ok := want[g.Type]
			if !ok || g.Error != "" || len(g.Items) != 1 || g.Items[0].ID != strconv.FormatInt(id, 10) {
				t.Fatalf("owner %d unexpected %s: %#v", actor.id, g.Type, g)
			}
			delete(want, g.Type)
		}
		if len(want) != 0 {
			t.Fatalf("duplicate/missing categories: %#v", want)
		}
	}
	for _, actor := range actors {
		for _, console := range []bool{false, true} {
			assertOwned(actor, request(actor, console, term, "all", nil))
			assertOwned(actor, request(actor, console, term, "all", nil))
		}
	}
	checks["two_identities_five_categories_both_auth_entries"] = true
	checks["warm_cache_keeps_owner_isolation"] = true
	checks["literal_metacharacters_do_not_expand_matches"] = true
	// Profile identity is the only source of this text, exercising Profile RPC
	// -> BFF -> Commission's bounded, owner-filtered counterparty match.
	byCounterparty := request(actors[0], false, peerName, "order", nil)
	if len(byCounterparty) != 1 || byCounterparty[0].Error != "" || len(byCounterparty[0].Items) != 1 || byCounterparty[0].Items[0].ID != fmt.Sprint(base+500) {
		t.Fatalf("counterparty-name result: %#v", byCounterparty)
	}
	checks["counterparty_name_uses_profile_and_owner_filter"] = true
	for i, actor := range actors {
		foreign := actors[1-i]
		for kind, id := range map[string]int64{"broadcast": base + 100 + foreign.offset, "message": base + 300 + foreign.offset, "friend": foreign.peer, "service": base + 400 + foreign.offset, "order": base + 500 + foreign.offset} {
			g := request(actor, false, fmt.Sprint(id), kind, nil)
			if len(g) != 1 || g[0].Error != "" || len(g[0].Items) != 0 {
				t.Fatalf("cross-owner %s: %#v", kind, g)
			}
		}
		for _, check := range []struct {
			id     int64
			status int
		}{{base + 400 + actor.offset, 200}, {base + 400 + foreign.offset, 403}} {
			status, body := call(actor, true, http.MethodGet, fmt.Sprintf("%s/api/v2/console/bff/trade/commissions?commission_id=%d&limit=1", endpoint, check.id), nil)
			if status != check.status || (status == 200 && !strings.Contains(string(body), fmt.Sprint(check.id))) {
				t.Fatalf("Console exact service status=%d body=%s", status, body)
			}
		}
	}
	checks["exact_foreign_ids_are_hidden_in_all_categories"] = true
	checks["trusted_trade_bff_rejects_foreign_service"] = true
	for _, path := range []string{"/api/v2/console/search", "/api/v2/dashboard/search"} {
		status, _ := call(identity{}, false, http.MethodGet, endpoint+path+"?q=x", nil)
		if status != 401 {
			t.Fatalf("anonymous %s HTTP %d", path, status)
		}
	}
	for _, wrong := range []struct {
		path    string
		console bool
	}{{"/api/v2/console/search", false}, {"/api/v2/dashboard/search", true}} {
		status, _ := call(actors[0], wrong.console, http.MethodGet, endpoint+wrong.path+"?q=x", nil)
		if status != 401 {
			t.Fatalf("auth entry crossover %s HTTP %d", wrong.path, status)
		}
	}
	checks["auth_entries_require_their_own_credentials"] = true
	limitedActor := actors[0]
	limitedActor.token = profileToken
	groups := request(limitedActor, false, term, "all", nil)
	if len(groups) != 5 {
		t.Fatalf("scope groups: %#v", groups)
	}
	for _, g := range groups {
		if g.Type == "broadcast" {
			if g.Error != "" || len(g.Items) != 1 || g.Items[0].ID != fmt.Sprint(base+100) {
				t.Fatalf("profile-scoped broadcast %#v", g)
			}
		} else if g.Error != "AGENT_SCOPE_REQUIRED" || len(g.Items) != 0 {
			t.Fatalf("scope leak %#v", g)
		}
	}
	checks["category_scopes_are_enforced"] = true
	first := request(actors[0], false, pagingTerm, "broadcast", url.Values{"limit": {"1"}, "status": {"published"}})
	if len(first) != 1 || first[0].Error != "" || len(first[0].Items) != 1 || first[0].Items[0].ID != fmt.Sprint(base+901) || !first[0].HasMore || first[0].NextCursor == "" {
		t.Fatalf("first page %#v", first)
	}
	second := request(actors[0], false, pagingTerm, "broadcast", url.Values{"limit": {"1"}, "status": {"published"}, "cursor": {first[0].NextCursor}})
	if len(second) != 1 || second[0].Error != "" || len(second[0].Items) != 1 || second[0].Items[0].ID != fmt.Sprint(base+900) || second[0].HasMore {
		t.Fatalf("second page %#v", second)
	}
	checks["single_category_cursor_and_status_filter"] = true
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
	for _, actor := range actors {
		home := filepath.Join(t.TempDir(), ".eigenflux")
		requireWrite(filepath.Join(home, "config.json"), map[string]interface{}{"default_server": "test", "servers": []map[string]string{{"name": "test", "endpoint": endpoint}}, "kv": map[string]string{"auto_skill_sync": "false"}})
		requireWrite(filepath.Join(home, "servers", "test", "agent-v2-credentials.json"), map[string]interface{}{"agent_id": fmt.Sprint(actor.id), "access_token": actor.token, "refresh_token": "test-refresh", "expires_at": now + 3600000})
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
			t.Fatalf("CLI JSON: %v %s", err, out)
		}
		assertOwned(actor, cli.Groups)
	}
	checks["native_cli_search_preserves_both_owner_scopes"] = true
	// Each page is warmed immediately before its business mutation and checked
	// inside the documented five-second cache TTL. A cold read is not sufficient.
	for _, mutation := range []struct {
		kind, method, path string
		body               interface{}
		endpoint           string
	}{
		{"friend", http.MethodPost, "/api/v2/relations/friends/unfriend", map[string]string{"to_uid": fmt.Sprint(actors[0].peer)}, endpoint},
		{"message", http.MethodPost, "/api/v2/pm/conversations/close", map[string]string{"conv_id": fmt.Sprint(base + 200)}, endpoint},
		{"service", http.MethodDelete, fmt.Sprintf("/api/v1/commissions/%d", base+400), nil, commissionEndpoint},
	} {
		started := time.Now()
		// This unique query has not been requested earlier in the test, so the
		// first successful read creates a fresh cache entry with its full TTL.
		query := revokeTerm(mutation.kind)
		for i := 0; i < 2; i++ {
			warm := request(actors[0], false, query, mutation.kind, nil)
			if len(warm) != 1 || warm[0].Error != "" || len(warm[0].Items) != 1 {
				t.Fatalf("warm %s: %#v", mutation.kind, warm)
			}
		}
		status, body := call(actors[0], false, mutation.method, mutation.endpoint+mutation.path, mutation.body)
		var response struct {
			Code *int `json:"code"`
		}
		if err := json.Unmarshal(body, &response); err != nil || status != 200 || response.Code == nil || *response.Code != 0 {
			t.Fatalf("business revoke %s status=%d err=%v body=%s", mutation.kind, status, err, body)
		}
		for _, console := range []bool{false, true} {
			g := request(actors[0], console, query, mutation.kind, nil)
			if len(g) != 1 || g[0].Error != "" || len(g[0].Items) != 0 {
				t.Fatalf("cached permission leak after %s: %#v", mutation.kind, g)
			}
		}
		elapsed := time.Since(started)
		if elapsed >= 5*time.Second {
			t.Fatalf("%s check took %s, outside warm-cache proof window", mutation.kind, elapsed)
		}
		checks["business_revoke_"+mutation.kind+"_filters_warm_cache"] = true
		evidence[mutation.kind+"_revoke_latency_ms"] = elapsed.Milliseconds()
	}
	var relations, convStatus int
	var deletedAt sql.NullInt64
	if err := db.QueryRow(`SELECT count(*) FROM user_relations WHERE rel_type=1 AND ((from_uid=$1 AND to_uid=$2) OR (from_uid=$2 AND to_uid=$1))`, base, base+1).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM conversations WHERE conv_id=$1`, base+200).Scan(&convStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT deleted_at FROM commission_definitions WHERE commission_id=$1`, base+400).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	if relations != 0 || convStatus != 2 || !deletedAt.Valid || deletedAt.Int64 <= 0 {
		t.Fatalf("business revocation not durable: relations=%d conv_status=%d deleted=%v", relations, convStatus, deletedAt)
	}
	checks["business_revocations_are_durable"] = true
	assertOwned(actors[1], request(actors[1], false, term, "all", nil))
	checks["revocation_does_not_change_other_owner_results"] = true
	// The deleted listing cannot rewrite the immutable historical order.
	order := request(actors[0], false, term, "order", nil)
	if len(order) != 1 || order[0].Error != "" || len(order[0].Items) != 1 || order[0].Items[0].ID != fmt.Sprint(base+500) {
		t.Fatalf("deleted listing changed historical order: %#v", order)
	}
	checks["deleted_service_preserves_historical_order_search"] = true
	limited := false
	for i := 0; i < 31; i++ {
		status, _ := call(actors[1], false, http.MethodGet, endpoint+"/api/v2/dashboard/search?q=x&type=friend", nil)
		if status == 429 {
			limited = true
			break
		}
		if status != 200 {
			t.Fatalf("rate limit HTTP %d", status)
		}
	}
	if !limited {
		t.Fatal("search burst was not rate limited")
	}
	checks["owner_search_burst_is_rate_limited"] = true
	t.Log("Verified two identities, five private categories, both auth entries, native CLI, real RPC/delegation and business-API revocation inside warm-cache TTL")
}
