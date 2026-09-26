package needs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/api/consolev2"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/need"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const original = `{"schema_version":"need_input.v1","intent_id":"INTENT_ID","intent_version":1,"need_type":"broadcast","target":{"desc":"  请帮我记录这个需求：寻找 PostgreSQL 索引资料。\n保留格式。 ","candidate_needs":["PostgreSQL 索引","数据库性能"]},"constraints":{"lang":["zh"]}}`

type ids struct{ n atomic.Int64 }

func (i *ids) NextID() (int64, error) { return i.n.Add(1), nil }
func hash(s string) string            { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }

type harness struct {
	db                  *gorm.DB
	endpoint            string
	owner, other        int64
	intent, otherIntent int64
	token, otherToken   string
	ids                 *ids
}

func setup(t *testing.T) *harness {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for migrated local PostgreSQL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("local PostgreSQL required", err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	generator := &ids{}
	generator.n.Store(time.Now().UnixMicro() * 100)
	owner, _ := generator.NextID()
	other, _ := generator.NextID()
	h := &harness{db: db, owner: owner, other: other, ids: generator, token: fmt.Sprintf("efv2a_need_test_%d", owner), otherToken: fmt.Sprintf("efv2a_need_test_%d", other)}
	t.Cleanup(func() {
		db.Exec("DELETE FROM agent_settings WHERE agent_id IN ?", []int64{owner, other})
		db.Exec("DELETE FROM agents WHERE agent_id IN ?", []int64{owner, other})
	})
	for _, a := range []struct {
		id    int64
		token string
	}{{owner, h.token}, {other, h.otherToken}} {
		now := time.Now().UnixMilli()
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO agents(agent_id,email,agent_name,created_at,updated_at,identity_state) VALUES (?,?, 'Need test',?,?, 'active')`, []any{a.id, fmt.Sprintf("need-%d@test.com", a.id), now, now}},
			{`INSERT INTO agent_principals(principal_id,agent_id,key_fingerprint,public_key,status,created_at,last_seen_at) VALUES (?,?,?,?,'active',?,?)`, []any{a.id, a.id, fmt.Sprint(a.id), make([]byte, 32), now, now}},
			{`INSERT INTO agent_context_revisions(agent_id,revision,compiled_context,generated_at) VALUES (?,1,'{}'::jsonb,?)`, []any{a.id, now}},
			{`INSERT INTO agent_onboarding_v2(agent_id,state,current_step,active_context_revision,completed_at,created_at,updated_at) VALUES (?,'completed',5,1,?,?,?)`, []any{a.id, now, now, now}},
			{`INSERT INTO agent_credential_sessions(principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at) VALUES (?,?,?,?,'agent_v2',ARRAY['context:read','context:write'],?,?,?,?)`, []any{a.id, fmt.Sprint(a.id), hash(a.token), hash(a.token + "refresh"), now, now + 3600000, now + 7200000, now}},
		}
		for _, s := range statements {
			if err := db.Exec(s.sql, s.args...).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, pair := range []struct {
		owner  int64
		target *int64
	}{{owner, &h.intent}, {other, &h.otherIntent}} {
		if err := db.Raw(`INSERT INTO agent_intent_actions(agent_id,watch_for,trigger_when,action_instruction,action_policy,priority,source,status,version,created_at,updated_at) VALUES (?, 'PostgreSQL', 'useful resources', 'summarize', 'analyze_only', 10, 'human_edit', 'active', 1, 1, 1) RETURNING intent_id`, pair.owner).Scan(pair.target).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO agent_context_heads(agent_id,current_revision,active_revision,updated_at) VALUES (?,1,1,1)`, pair.owner).Error; err != nil {
			t.Fatal(err)
		}
	}
	if endpoint := os.Getenv("NEED_TEST_API_URL"); endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "http" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatal("NEED_TEST_API_URL must be a loopback HTTP origin")
		}
		h.endpoint = endpoint
		return h
	}
	svc, err := consolev2.NewService(db, generator, &config.Config{ConsoleV2BootstrapSecret: "need-test-broker", ConsoleV2OTPPepper: "need-test-pepper", ConsoleV2PublicURL: "http://localhost"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.endpoint = "http://" + listener.Addr().String()
	live := server.New(server.WithListener(listener), server.WithTransport(standard.NewTransporter))
	svc.Register(live)
	stopped := make(chan error, 1)
	go func() { stopped <- live.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = live.Shutdown(ctx)
		_ = listener.Close()
		select {
		case <-stopped:
		case <-ctx.Done():
		}
	})
	return h
}
func (h *harness) request(t *testing.T, method, path, token, key, body string, want int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(method, h.endpoint+"/api/v2"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, res.StatusCode, want, raw)
	}
	if res.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("private response can be cached")
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if data, ok := out["data"].(map[string]any); ok {
		return data
	}
	return out
}
func TestNeedInputHTTPAndPostgres(t *testing.T) {
	h := setup(t)
	original := strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1)
	h.request(t, "POST", "/need-inputs", "", "capture-test-1", original, 401)
	h.request(t, "POST", "/need-inputs", h.token, "", original, 400)
	h.request(t, "POST", "/need-inputs", h.token, "capture-bad-1", strings.Replace(original, "need_input.v1", "need_input.v2", 1), 400)
	h.request(t, "POST", "/need-inputs", h.token, "capture-bad-2", strings.Replace(original, `"target":`, `"agent_id":"42","target":`, 1), 400)
	h.request(t, "POST", "/need-inputs", h.token, "capture-bad-3", original+`{}`, 400)
	h.request(t, "POST", "/need-inputs", h.token, "capture-bad-4", strings.Repeat("x", 32769), 413)
	if err := h.db.Exec("UPDATE agent_onboarding_v2 SET state='in_progress',completed_at=NULL WHERE agent_id=?", h.other).Error; err != nil {
		t.Fatal(err)
	}
	h.request(t, "POST", "/need-inputs", h.otherToken, "capture-incomplete", original, 409)
	h.db.Exec("UPDATE agent_onboarding_v2 SET state='completed',completed_at=? WHERE agent_id=?", time.Now().UnixMilli(), h.other)
	h.db.Exec("UPDATE agent_credential_sessions SET scopes=ARRAY['context:read'] WHERE principal_id=?", h.other)
	h.request(t, "POST", "/need-inputs", h.otherToken, "capture-no-scope", original, 403)
	h.db.Exec("UPDATE agent_credential_sessions SET scopes=ARRAY['context:read','context:write'] WHERE principal_id=?", h.other)
	sources := h.request(t, "GET", "/agent-context/intent-actions", h.token, "", "", 200)
	intents := sources["intent_actions"].([]any)
	if len(intents) != 1 || intents[0].(map[string]any)["version"] != float64(1) || intents[0].(map[string]any)["intent_id"] != fmt.Sprint(h.intent) {
		t.Fatal(sources)
	}
	created := h.request(t, "POST", "/need-inputs", h.token, "capture-test-1", original, 201)
	record := created["need_input"].(map[string]any)
	id := record["need_input_id"].(string)
	if record["status"] != "active" || record["agent_id"] != fmt.Sprint(h.owner) {
		t.Fatal(record)
	}
	if record["eligible"] != true || record["normalized_need"] != nil {
		t.Fatal(record)
	}
	snapshot := record["intent_snapshot"].(map[string]any)
	if snapshot["intent_id"] != fmt.Sprint(h.intent) || snapshot["intent_version"] != float64(1) || snapshot["watch_for"] != "PostgreSQL" {
		t.Fatal(snapshot)
	}
	raw := record["input"].(map[string]any)
	if raw["target"].(map[string]any)["desc"] != "  请帮我记录这个需求：寻找 PostgreSQL 索引资料。\n保留格式。 " {
		t.Fatal("original wording changed")
	}
	retry := h.request(t, "POST", "/need-inputs", h.token, "capture-test-1", original, 200)
	if retry["need_input"].(map[string]any)["need_input_id"] != id || retry["replayed"] != true {
		t.Fatal(retry)
	}
	h.request(t, "POST", "/need-inputs", h.token, "capture-test-1", strings.Replace(original, "数据库性能", "SQL 优化", 1), 409)
	h.request(t, "GET", "/need-inputs/"+id, h.otherToken, "", "", 404)
	h.request(t, "GET", "/need-inputs/"+id, h.token, "", "", 200)
	other := h.request(t, "POST", "/need-inputs", h.otherToken, "capture-test-1", strings.Replace(original, fmt.Sprintf(`"intent_id":"%d"`, h.intent), fmt.Sprintf(`"intent_id":"%d"`, h.otherIntent), 1), 201)
	if other["need_input"].(map[string]any)["need_input_id"] == id {
		t.Fatal("owner key collision")
	}
	h.request(t, "POST", "/need-inputs", h.token, "capture-test-2", original, 201)
	page := h.request(t, "GET", "/need-inputs?limit=1", h.token, "", "", 200)
	if len(page["need_inputs"].([]any)) != 1 || page["next_cursor"] == "" {
		t.Fatal(page)
	}
	next := h.request(t, "GET", "/need-inputs?limit=1&cursor="+page["next_cursor"].(string), h.token, "", "", 200)
	if next["need_inputs"].([]any)[0].(map[string]any)["need_input_id"] != id || next["next_cursor"] != "" {
		t.Fatal(next)
	}
	h.request(t, "GET", "/need-inputs?limit=101", h.token, "", "", 400)
	store := need.Store{DB: h.db, IDs: h.ids}
	var wg sync.WaitGroup
	results := make(chan int64, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, err := store.Create(context.Background(), h.owner, "parallel-capture", []byte(original), time.Now().UnixMilli())
			if err != nil {
				errors <- err
			}
			results <- r.NeedInputID
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	var first int64
	for id := range results {
		if first == 0 {
			first = id
		}
		if id != first {
			t.Fatal("parallel duplicate")
		}
	}
	var count int64
	h.db.Raw("SELECT count(*) FROM need_inputs WHERE agent_id=? AND idempotency_key='parallel-capture'", h.owner).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	// An Intent mutation invalidates new submissions but does not invalidate retry identity.
	h.request(t, "PUT", "/agent-context/intent-actions/"+fmt.Sprint(h.intent), h.token, "", `{"expected_context_revision":1,"idempotency_key":"intent-update-test","watch_for":"Updated PostgreSQL resources","trigger_when":"useful resources","action_instruction":"summarize","action_policy":"analyze_only","priority":10}`, 200)
	compiled := h.request(t, "GET", "/agent-context?if_newer=0", h.token, "", "", 200)
	compiledIntents := compiled["control_context"].(map[string]any)["intent_actions"].([]any)
	if len(compiledIntents) != 1 || compiledIntents[0].(map[string]any)["version"] != float64(2) {
		t.Fatal(compiled)
	}
	h.request(t, "POST", "/need-inputs", h.token, "capture-stale", original, 409)
	h.request(t, "POST", "/need-inputs", h.token, "capture-test-1", original, 200)
	for _, status := range []string{"paused", "deleted"} {
		if err := h.db.Exec("UPDATE agent_intent_actions SET status=? WHERE intent_id=?", status, h.intent).Error; err != nil {
			t.Fatal(err)
		}
		h.request(t, "POST", "/need-inputs", h.token, "capture-"+status, strings.Replace(original, `"intent_version":1`, `"intent_version":2`, 1), 409)
	}
	h.request(t, "POST", "/need-inputs", h.token, "capture-other-intent", strings.Replace(original, fmt.Sprintf(`"intent_id":"%d"`, h.intent), fmt.Sprintf(`"intent_id":"%d"`, h.otherIntent), 1), 409)
	h.db.Exec("DELETE FROM agent_settings WHERE agent_id=?", h.other)
	if err := h.db.Exec("DELETE FROM agents WHERE agent_id=?", h.other).Error; err != nil {
		t.Fatal(err)
	}
	h.db.Raw("SELECT count(*) FROM need_inputs WHERE agent_id=?", h.other).Scan(&count)
	if count != 0 {
		t.Fatal("account deletion retained NeedInputs")
	}
}

func TestNeedInputRevisedFields(t *testing.T) {
	h := setup(t)
	raw := strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1)
	for _, kind := range []string{"broadcast", "agent", "commission"} {
		payload := strings.Replace(raw, `"broadcast"`, `"`+kind+`"`, 1)
		created := h.request(t, "POST", "/need-inputs", h.token, "fields-"+kind, payload, 201)
		record := created["need_input"].(map[string]any)
		if record["input"].(map[string]any)["need_type"] != kind {
			t.Fatal("lost source kind", record)
		}
		if record["eligible"] != true || record["normalized_need"] != nil {
			t.Fatal(record)
		}
	}
	for i, bad := range []string{
		strings.Replace(raw, `"broadcast"`, `"find_info"`, 1),
		strings.Replace(raw, `"desc"`, `"free_text"`, 1),
		strings.Replace(raw, `"candidate_needs"`, `"proposed_intents"`, 1),
		strings.Replace(raw, `"target":`, `"outcome":"obsolete","target":`, 1),
		strings.Replace(raw, `"lang":["zh"]`, `"exclude_authors":["123"]`, 1),
		strings.Replace(raw, `{"lang":["zh"]}`, `"{}"`, 1),
		strings.Replace(raw, `"desc":"`, `"desc":"`+strings.Repeat("a", 201), 1),
	} {
		h.request(t, "POST", "/need-inputs", h.token, fmt.Sprintf("bad-fields-%d", i), bad, 400)
	}
	for i, currency := range []string{"USD", "EUR", "", "cny"} {
		bad := strings.Replace(raw, `"broadcast"`, `"commission"`, 1)
		bad = strings.Replace(bad, `"lang":["zh"]`, `"currency":"`+currency+`"`, 1)
		h.request(t, "POST", "/need-inputs", h.token, fmt.Sprintf("bad-currency-%d", i), bad, 400)
	}
}

func TestNeedInputCLI(t *testing.T) {
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Skip("EIGENFLUX_TEST_CLI required")
	}
	h := setup(t)
	// Exercise the published contract example through the actual CLI and gateway.
	example, err := os.ReadFile("../../contracts/need_input.v2.example.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := need.Decode(example)
	if err != nil {
		t.Fatal("Contract example violates the input contract", err)
	}
	in.IntentID = h.intent
	encoded, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	original := string(encoded)
	home := filepath.Join(t.TempDir(), ".eigenflux")
	dir := filepath.Join(home, "servers", "eigenflux")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, v any) {
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "config.json"), map[string]any{"default_server": "eigenflux", "servers": []any{map[string]any{"name": "eigenflux", "endpoint": h.endpoint}}})
	write(filepath.Join(dir, "agent-v2-credentials.json"), map[string]any{"access_token": h.token, "refresh_token": "test-only-refresh", "agent_id": strconv.FormatInt(h.owner, 10), "expires_at": time.Now().Add(time.Hour).UnixMilli()})
	path := filepath.Join(t.TempDir(), "need.json")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) map[string]any {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		cmd.Env = append(os.Environ(), "EIGENFLUX_SKILLS_BASE_URL=http://127.0.0.1:1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("CLI: %v %s", err, stderr.String())
		}
		var out map[string]any
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("CLI output: %s %v", raw, err)
		}
		return out
	}
	created := run("need", "input", "create", "--file", path, "--idempotency-key", "cli-capture-test")
	id := created["need_input"].(map[string]any)["need_input_id"].(string)
	if run("need", "input", "create", "--file", path, "--idempotency-key", "cli-capture-test")["replayed"] != true {
		t.Fatal("CLI retry was duplicated")
	}
	got := run("need", "input", "get", id)
	var expected map[string]any
	if err := json.Unmarshal(encoded, &expected); err != nil {
		t.Fatal(err)
	}
	actual := got["need_input"].(map[string]any)["input"].(map[string]any)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("CLI lost or changed Need fields: got=%v want=%v", actual, expected)
	}

	if got["need_input"].(map[string]any)["input"].(map[string]any)["target"].(map[string]any)["goal"] != "Diagnose and improve PostgreSQL slow queries" {
		t.Fatal(got)
	}
	input := got["need_input"].(map[string]any)["input"].(map[string]any)
	constraints := input["constraints"].(map[string]any)
	if constraints["currency"] != "CNY" || constraints["budget_max_fen"] != float64(50000) {
		t.Fatal("Contract budget did not round-trip through capture", constraints)
	}
	if len(run("need", "input", "list")["need_inputs"].([]any)) != 1 {
		t.Fatal("CLI list mismatch")
	}
}

func TestNormalizedNeedIntegrityAndEligibility(t *testing.T) {
	h := setup(t)
	store := need.Store{DB: h.db, IDs: h.ids}
	payload := []byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1))
	r, _, err := store.Create(context.Background(), h.owner, "normalized-source", payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.Exec("UPDATE need_inputs SET status='normalized' WHERE need_input_id=?", r.NeedInputID).Error; err != nil {
		t.Fatal(err)
	}
	projection := `{"desc":"PostgreSQL indexes","candidate_needs":["indexes"],"constraints":{}}`
	insert := func(owner, version int64, normalizer string) error {
		id, _ := h.ids.NextID()
		return h.db.Exec(`INSERT INTO normalized_needs(normalized_need_id,need_input_id,agent_id,intent_id,intent_version,schema_version,normalized,normalizer_version,created_at,updated_at) VALUES (?,?,?,?,?,'normalized_need.v1',?::jsonb,?,1,1)`, id, r.NeedInputID, owner, h.intent, version, projection, normalizer).Error
	}
	if insert(h.other, 1, "v1") == nil {
		t.Fatal("cross-owner projection accepted")
	}
	if insert(h.owner, 2, "v1") == nil {
		t.Fatal("wrong source version accepted")
	}
	if insert(h.owner, 1, "") == nil {
		t.Fatal("unversioned normalizer accepted")
	}
	if err := h.db.Exec("UPDATE normalized_needs SET status='superseded' WHERE need_input_id=?", r.NeedInputID).Error; err != nil {
		t.Fatal(err)
	}
	if err := insert(h.owner, 1, "v1"); err != nil {
		t.Fatal(err)
	}
	if insert(h.owner, 1, "v1") == nil {
		t.Fatal("duplicate projection accepted")
	}
	if insert(h.owner, 1, "v2") == nil {
		t.Fatal("two active projections accepted")
	}
	count := func(want int64) {
		t.Helper()
		var got int64
		if err := h.db.Raw(`SELECT count(*) FROM current_normalized_needs WHERE agent_id=?`, h.owner).Scan(&got).Error; err != nil || got != want {
			t.Fatalf("eligible=%d want=%d err=%v", got, want, err)
		}
	}
	execSQL := func(query string, args ...any) {
		t.Helper()
		if err := h.db.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	count(1)
	execSQL("UPDATE normalized_needs SET status='superseded' WHERE need_input_id=?", r.NeedInputID)
	count(0)
	if err := insert(h.owner, 1, "v2"); err != nil {
		t.Fatal(err)
	}
	count(1)
	for _, status := range []string{"paused", "deleted"} {
		execSQL("UPDATE agent_intent_actions SET status=? WHERE intent_id=?", status, h.intent)
		count(0)
	}
	execSQL("UPDATE agent_intent_actions SET status='active', version=2 WHERE intent_id=?", h.intent)
	count(0)
	got, err := store.Get(context.Background(), h.owner, r.NeedInputID)
	if err != nil || got.IntentVersion != 1 {
		t.Fatal("source history was rewritten", err)
	}
	execSQL("DELETE FROM agent_intent_actions WHERE intent_id=?", h.intent)
	var retained int64
	if err := h.db.Raw("SELECT count(*) FROM normalized_needs WHERE need_input_id=?", r.NeedInputID).Scan(&retained).Error; err != nil || retained != 0 {
		t.Fatal("projection cascade failed", retained, err)
	}
	if _, err := store.Get(context.Background(), h.owner, r.NeedInputID); err != need.ErrNotFound {
		t.Fatal("input cascade failed", err)
	}
}

func TestNeedInputWaitsForIntentMutation(t *testing.T) {
	h := setup(t)
	tx := h.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("UPDATE agent_intent_actions SET version=2 WHERE intent_id=?", h.intent).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := (need.Store{DB: h.db, IDs: h.ids}).Create(ctx, h.owner, "concurrent-edit", []byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1)), 1)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("capture bypassed locked intent: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != need.ErrStaleIntent {
		t.Fatal("accepted obsolete input", err)
	}
}
