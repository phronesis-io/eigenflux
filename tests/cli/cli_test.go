package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

// testHome is a temporary directory used as EIGENFLUX_HOME for all CLI invocations.
var testHome string

// apiBaseURL is the local API gateway address.
var apiBaseURL string

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
	// unreachable — RunTestMain calls os.Exit, but the pattern is
	// needed for the advisory lock.
}

// runCLI executes the eigenflux binary with the given args, returning stdout, stderr and error.
// EIGENFLUX_HOME is pointed at a temporary directory so tests don't pollute the real config.
func runCLI(t *testing.T, args ...string) (stdout string, stderr string, err error) {
	t.Helper()
	bin, lookErr := exec.LookPath("eigenflux")
	if lookErr != nil {
		t.Fatalf("eigenflux binary not found in PATH: %v", lookErr)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "EIGENFLUX_HOME="+testHome)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	return outBuf.String(), errBuf.String(), runErr
}

// mustRunCLI is like runCLI but fatals on error.
func mustRunCLI(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("eigenflux %s failed: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

// parseJSON parses stdout as JSON object.
func parseJSON(t *testing.T, data string) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("failed to parse JSON: %v\nraw: %s", err, data)
	}
	return result
}

// setup initialises the test env: temp home, server "local" pointing at the running API.
func setup(t *testing.T) {
	t.Helper()
	var err error
	testHome, err = os.MkdirTemp("", "eigenflux-cli-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(testHome) })

	cfg := config.Load()
	apiBaseURL = fmt.Sprintf("http://localhost:%d", cfg.ApiPort)
	wsBaseURL := fmt.Sprintf("ws://localhost:%d", cfg.WSPort)

	// Pre-add a server entry pointing at the running local API + WS.
	mustRunCLI(t, "server", "add", "--name", "local",
		"--endpoint", apiBaseURL,
		"--stream-endpoint", wsBaseURL)
	mustRunCLI(t, "server", "use", "--name", "local")
}

// ---------- Tests ----------

func TestVersion(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	out := mustRunCLI(t, "version")
	v := parseJSON(t, out)

	if _, ok := v["cli_version"]; !ok {
		t.Error("expected cli_version in version output")
	}
	if v["os"] == nil || v["arch"] == nil {
		t.Error("expected os and arch in version output")
	}
	t.Logf("version: %v", v)
}

func TestVersionShort(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	out := mustRunCLI(t, "version", "--short")
	out = strings.TrimSpace(out)
	if out == "" {
		t.Error("expected non-empty short version")
	}
	t.Logf("version --short: %s", out)
}

func TestServerManagement(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	// server list — should show "local" as current
	out := mustRunCLI(t, "server", "list", "--format", "json")
	var servers []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &servers); err != nil {
		t.Fatalf("failed to parse server list: %v\nraw: %s", err, out)
	}
	found := false
	for _, s := range servers {
		if s["name"] == "local" {
			found = true
			if s["current"] != true {
				t.Error("expected local to be current server")
			}
		}
	}
	if !found {
		t.Error("expected 'local' server in list")
	}

	// server add — staging
	mustRunCLI(t, "server", "add", "--name", "staging", "--endpoint", "https://staging.example.com")

	// server update — staging
	mustRunCLI(t, "server", "update", "--name", "staging", "--endpoint", "https://staging2.example.com")

	// server list — should show both
	out = mustRunCLI(t, "server", "list", "--format", "json")
	if err := json.Unmarshal([]byte(out), &servers); err != nil {
		t.Fatalf("failed to parse updated server list: %v", err)
	}
	if len(servers) < 2 {
		t.Fatalf("expected at least 2 servers, got %d", len(servers))
	}

	// server remove — staging
	mustRunCLI(t, "server", "remove", "--name", "staging")

	// Verify removal
	out = mustRunCLI(t, "server", "list", "--format", "json")
	if err := json.Unmarshal([]byte(out), &servers); err != nil {
		t.Fatalf("failed to parse server list after remove: %v", err)
	}
	for _, s := range servers {
		if s["name"] == "staging" {
			t.Error("staging server should have been removed")
		}
	}
}

func TestConfigKV(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	// Global set/get — values are free-form strings.
	mustRunCLI(t, "config", "set", "--key", "recurring_publish", "--value", "true")
	out := mustRunCLI(t, "config", "get", "--key", "recurring_publish")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("expected recurring_publish=\"true\", got %q", strings.TrimSpace(out))
	}

	mustRunCLI(t, "config", "set", "--key", "feed_delivery_preference", "--value", "push urgent signals")

	// Per-server set overrides global on reads with --server; reads without
	// --server still see the global value.
	mustRunCLI(t, "config", "set", "--key", "recurring_publish", "--value", "false", "--server", "local")
	out = mustRunCLI(t, "config", "get", "--key", "recurring_publish", "--server", "local")
	if strings.TrimSpace(out) != "false" {
		t.Errorf("per-server recurring_publish = %q, want \"false\"", strings.TrimSpace(out))
	}
	out = mustRunCLI(t, "config", "get", "--key", "recurring_publish")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("global recurring_publish = %q, want \"true\"", strings.TrimSpace(out))
	}

	// Unset a per-server key → reads with --server fall back to global.
	mustRunCLI(t, "config", "set", "--key", "recurring_publish", "--value", "", "--server", "local")
	out = mustRunCLI(t, "config", "get", "--key", "recurring_publish", "--server", "local")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("after per-server unset, fallback = %q, want \"true\"", strings.TrimSpace(out))
	}

	// show returns {server, server_kv, kv}.
	out = mustRunCLI(t, "config", "show", "--format", "json")
	v := parseJSON(t, out)
	kv, _ := v["kv"].(map[string]interface{})
	if kv["recurring_publish"] != "true" {
		t.Errorf("show.kv.recurring_publish = %v, want \"true\"", kv["recurring_publish"])
	}
	if kv["feed_delivery_preference"] != "push urgent signals" {
		t.Errorf("show.kv.feed_delivery_preference = %v, want \"push urgent signals\"", kv["feed_delivery_preference"])
	}
	t.Logf("config show: %v", v)
}

func TestStats(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	out := mustRunCLI(t, "stats")
	v := parseJSON(t, out)
	// stats should return some object — at minimum code/msg or data
	t.Logf("stats: %v", v)
}

func TestAuthLoginAndVerify(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_auth_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	// Step 1: auth login
	out := mustRunCLI(t, "auth", "login", "--email", email)
	loginResult := parseJSON(t, out)
	t.Logf("auth login: %v", loginResult)

	// Depending on ENABLE_EMAIL_VERIFICATION, we either get access_token directly or challenge_id.
	if token, ok := loginResult["access_token"].(string); ok && token != "" {
		// Direct login (no OTP). Token is already saved by CLI.
		t.Log("Direct login succeeded (OTP disabled)")
	} else {
		challengeID, ok := loginResult["challenge_id"].(string)
		if !ok || challengeID == "" {
			t.Fatalf("expected either access_token or challenge_id, got: %v", loginResult)
		}

		// Step 2: auth verify
		mockOTP := testutil.GetMockOTP(t)
		out = mustRunCLI(t, "auth", "verify", "--challenge-id", challengeID, "--code", mockOTP)
		verifyResult := parseJSON(t, out)
		t.Logf("auth verify: %v", verifyResult)

		if token, ok := verifyResult["access_token"].(string); !ok || token == "" {
			t.Fatalf("expected access_token after verify, got: %v", verifyResult)
		}
	}

	// Verify credentials were saved by checking that an authenticated command works.
	out = mustRunCLI(t, "profile", "show")
	showResult := parseJSON(t, out)
	// profile show returns {profile: {email: ...}, influence: {...}}
	profileObj, _ := showResult["profile"].(map[string]interface{})
	if profileObj == nil || profileObj["email"] == nil {
		t.Errorf("expected email in profile show output after auth, got: %v", showResult)
	}
	t.Logf("profile show after login: %v", showResult)
}

func TestAuthLogout(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_logout_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	loginAndAuth(t, email)

	// Verify profile.json exists in the server directory.
	// HomeDir() auto-appends ".eigenflux" to EIGENFLUX_HOME.
	profilePath := filepath.Join(testHome, ".eigenflux", "servers", "local", "profile.json")
	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("expected profile.json to exist after login: %v", err)
	}

	// Logout.
	mustRunCLI(t, "auth", "logout")

	// Verify credentials file is gone.
	credsPath := filepath.Join(testHome, ".eigenflux", "servers", "local", "credentials.json")
	if _, err := os.Stat(credsPath); !os.IsNotExist(err) {
		t.Fatalf("expected credentials.json to be removed after logout, err=%v", err)
	}

	// Verify subsequent authenticated CLI command returns auth error.
	_, _, err := runCLI(t, "profile", "show")
	if err == nil {
		t.Error("expected profile show to fail after logout")
	}
	t.Log("CLI logout correctly removes credentials and subsequent commands fail")
}

func TestProfileUpdateAndShow(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_profile_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	loginAndAuth(t, email)

	// Update profile
	mustRunCLI(t, "profile", "update", "--name", "CLITestBot", "--bio", "Integration test agent")

	// Show profile — verify update took
	out := mustRunCLI(t, "profile", "show")
	showResult := parseJSON(t, out)
	profileObj, _ := showResult["profile"].(map[string]interface{})
	if profileObj == nil || profileObj["agent_name"] != "CLITestBot" {
		t.Errorf("expected agent_name=CLITestBot, got %v", showResult)
	}
	t.Logf("profile after update: %v", showResult)
}

func TestPublishAndFeed(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_feed_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	loginAndAuth(t, email)

	// Complete profile (required for publishing/feed)
	mustRunCLI(t, "profile", "update", "--name", "FeedTestBot", "--bio", "Interested in technology and AI")

	// Publish an item
	out := mustRunCLI(t, "publish",
		"--content", "Testing CLI publish: An article about distributed systems and Raft consensus algorithm.",
		"--notes", `{"type":"info","domains":["tech"],"summary":"Distributed systems article","source_type":"original"}`,
	)
	pubResult := parseJSON(t, out)
	if pubResult["item_id"] == nil {
		t.Fatalf("expected item_id in publish response, got: %v", pubResult)
	}
	t.Logf("publish result: %v", pubResult)

	// Feed poll — should return without error (may be empty if pipeline hasn't processed yet)
	out = mustRunCLI(t, "feed", "poll", "--limit", "5")
	feedResult := parseJSON(t, out)
	t.Logf("feed poll: items=%v", feedResult["items"])
}

func TestProfileItems(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_items_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	loginAndAuth(t, email)
	mustRunCLI(t, "profile", "update", "--name", "ItemsTestBot", "--bio", "Test")

	// Publish something first
	mustRunCLI(t, "publish",
		"--content", "CLI integration test item for profile items listing.",
		"--notes", `{"type":"info","domains":["test"],"summary":"CLI test item","source_type":"original"}`,
	)

	// List my items
	out := mustRunCLI(t, "profile", "items", "--limit", "10")
	itemsResult := parseJSON(t, out)
	t.Logf("profile items: %v", itemsResult)
}

func TestInstallShRoute(t *testing.T) {
	testutil.WaitForAPI(t)
	if apiBaseURL == "" {
		apiBaseURL = fmt.Sprintf("http://localhost:%d", config.Load().ApiPort)
	}

	// The install.sh should be served at /install.sh
	resp, err := http.Get(apiBaseURL + "/install.sh")
	if err != nil {
		t.Fatalf("failed to fetch /install.sh: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 for /install.sh, got %d", resp.StatusCode)
	}

	bodyStr := string(body)
	if !strings.Contains(bodyStr, "#!/") {
		t.Error("expected install.sh to start with a shebang")
	}
	if !strings.Contains(bodyStr, "eigenflux") {
		t.Error("expected install.sh to mention eigenflux")
	}
	t.Logf("install.sh: %d bytes, OK", len(body))
}

func TestInstallPs1Route(t *testing.T) {
	testutil.WaitForAPI(t)
	if apiBaseURL == "" {
		apiBaseURL = fmt.Sprintf("http://localhost:%d", config.Load().ApiPort)
	}

	resp, err := http.Get(apiBaseURL + "/install.ps1")
	if err != nil {
		t.Fatalf("failed to fetch /install.ps1: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 for /install.ps1, got %d", resp.StatusCode)
	}

	bodyStr := string(body)
	if !strings.Contains(bodyStr, "eigenflux") {
		t.Error("expected install.ps1 to mention eigenflux")
	}
	if !strings.Contains(bodyStr, "Invoke-") {
		t.Error("expected install.ps1 to contain PowerShell cmdlets")
	}
	t.Logf("install.ps1: %d bytes, OK", len(body))
}

func TestUnauthenticatedCommandFails(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	// Without logging in, profile show should fail (exit code != 0).
	_, stderr, err := runCLI(t, "profile", "show")
	if err == nil {
		t.Error("expected profile show to fail without auth")
	}
	t.Logf("unauthenticated profile show stderr: %s", stderr)
}

func TestFeedGetNonexistent(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_feedget_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	loginAndAuth(t, email)
	mustRunCLI(t, "profile", "update", "--name", "FeedGetBot", "--bio", "Test")

	// Get a non-existent item — should fail with non-zero exit
	_, _, err := runCLI(t, "feed", "get", "--item-id", "999999")
	if err == nil {
		t.Error("expected feed get for non-existent item to fail")
	}
}

func TestMsgFetchNoConversations(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_msg_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	loginAndAuth(t, email)
	mustRunCLI(t, "profile", "update", "--name", "MsgTestBot", "--bio", "Test")

	// Fetch messages — should return empty list, not error
	out := mustRunCLI(t, "msg", "fetch", "--limit", "5")
	t.Logf("msg fetch: %s", out)

	// List conversations — should return empty list, not error
	out = mustRunCLI(t, "msg", "conversations", "--limit", "5")
	t.Logf("msg conversations: %s", out)
}

func TestRelationFriendsList(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	email := fmt.Sprintf("cli_rel_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	loginAndAuth(t, email)
	mustRunCLI(t, "profile", "update", "--name", "RelTestBot", "--bio", "Test")

	// Friends list — should return empty, not error
	out := mustRunCLI(t, "relation", "friends", "--limit", "5")
	t.Logf("relation friends: %s", out)

	// Relation list (incoming) — should return empty
	out = mustRunCLI(t, "relation", "list", "--direction", "incoming", "--limit", "5")
	t.Logf("relation list incoming: %s", out)

	// Relation list (outgoing) — should return empty
	out = mustRunCLI(t, "relation", "list", "--direction", "outgoing", "--limit", "5")
	t.Logf("relation list outgoing: %s", out)
}

func TestStreamReceivesPush(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	senderEmail := fmt.Sprintf("cli_stream_send_%d@test.com", time.Now().UnixNano())
	receiverEmail := fmt.Sprintf("cli_stream_recv_%d@test.com", time.Now().UnixNano())
	t.Cleanup(func() {
		testutil.CleanupTestEmails(t, senderEmail, receiverEmail)
	})

	// Register sender via HTTP (simpler).
	sender := testutil.RegisterAgent(t, senderEmail, "StreamSender", "sends messages")
	senderToken := sender["token"].(string)

	// Register receiver via HTTP and save token directly to CLI credentials.
	receiver := testutil.RegisterAgent(t, receiverEmail, "StreamReceiver", "receives messages")
	receiverToken := receiver["token"].(string)
	senderID, _ := strconv.ParseInt(sender["agent_id"].(string), 10, 64)
	receiverID, _ := strconv.ParseInt(receiver["agent_id"].(string), 10, 64)

	// Write receiver credentials to CLI config directory so `eigenflux stream` can use them.
	saveTestCredentials(t, receiverToken)

	// Clean PM data for both agents.
	cleanPMData(t, senderID, receiverID)
	t.Cleanup(func() { cleanPMData(t, senderID, receiverID) })

	// Create a mock item owned by receiver so sender can PM them.
	itemID := int64(990099)
	mockItem(t, itemID, receiverID)
	t.Cleanup(func() { cleanMockItem(t, itemID) })

	// Start `eigenflux stream` as a background process.
	bin, _ := exec.LookPath("eigenflux")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "stream", "--format", "json")
	cmd.Env = append(os.Environ(), "EIGENFLUX_HOME="+testHome)
	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start stream: %v", err)
	}
	defer func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
	}()

	// Give the stream command time to connect.
	time.Sleep(1 * time.Second)

	// Sender sends a PM to receiver via API.
	sendResp := testutil.DoPost(t, "/api/v1/pm/send", map[string]interface{}{
		"receiver_id": receiver["agent_id"],
		"item_id":     strconv.FormatInt(itemID, 10),
		"content":     "hello from stream cli test",
	}, senderToken)
	if int(sendResp["code"].(float64)) != 0 {
		t.Fatalf("send PM failed: %v", sendResp["msg"])
	}

	// Wait for the stream to receive the message (up to 10s).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(outBuf.String(), "hello from stream cli test") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	output := outBuf.String()
	if !strings.Contains(output, "hello from stream cli test") {
		t.Fatalf("stream did not receive expected message, got: %s", output)
	}

	// Verify it's valid JSON.
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // stderr lines mixed in
		}
		if msg["type"] == "pm_push" {
			t.Logf("stream received pm_push: %s", line)
			return
		}
	}
	t.Error("stream output did not contain a valid pm_push JSON message")

	_ = receiverToken // used indirectly via saveTestCredentials
}

func TestStreamUnauthenticatedFails(t *testing.T) {
	testutil.WaitForAPI(t)
	setup(t)

	// Without logging in, stream should fail.
	_, stderr, err := runCLI(t, "stream")
	if err == nil {
		t.Error("expected stream to fail without auth")
	}
	t.Logf("unauthenticated stream stderr: %s", stderr)
}

// ---------- Helpers ----------

// saveTestCredentials writes a token directly to the CLI credentials directory
// for the "local" server, bypassing the login flow.
func saveTestCredentials(t *testing.T, token string) {
	t.Helper()
	// HomeDir() auto-appends ".eigenflux" to EIGENFLUX_HOME.
	credsDir := filepath.Join(testHome, ".eigenflux", "servers", "local")
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatalf("failed to create creds dir: %v", err)
	}
	creds := fmt.Sprintf(`{"access_token":%q,"email":"test@test.com","expires_at":%d}`,
		token, time.Now().Add(24*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"), []byte(creds), 0600); err != nil {
		t.Fatalf("failed to write credentials: %v", err)
	}
}

func cleanPMData(t *testing.T, agentIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	rdb := testutil.GetTestRedis()
	for _, id := range agentIDs {
		testutil.TestDB.Exec("DELETE FROM private_messages WHERE sender_id = $1 OR receiver_id = $1", id)
		testutil.TestDB.Exec("DELETE FROM conversations WHERE participant_a = $1 OR participant_b = $1", id)
		rdb.Del(ctx, fmt.Sprintf("pm:fetch:%d", id))
	}
}

func mockItem(t *testing.T, itemID, authorAgentID int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec(
		`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, $3, $4)`,
		itemID, authorAgentID, "cli stream test item", now,
	)
	testutil.TestDB.Exec(
		`INSERT INTO processed_items (item_id, status, broadcast_type, updated_at) VALUES ($1, 3, 'info', $2)`,
		itemID, now,
	)
	rdb := testutil.GetTestRedis()
	rdb.Del(context.Background(), fmt.Sprintf("pm:itemowner:%d", itemID))
}

func cleanMockItem(t *testing.T, itemID int64) {
	t.Helper()
	testutil.TestDB.Exec("DELETE FROM processed_items WHERE item_id = $1", itemID)
	testutil.TestDB.Exec("DELETE FROM raw_items WHERE item_id = $1", itemID)
	rdb := testutil.GetTestRedis()
	rdb.Del(context.Background(), fmt.Sprintf("pm:itemowner:%d", itemID))
}

// loginAndAuth performs the full auth login+verify flow via CLI and ensures credentials are saved.
func loginAndAuth(t *testing.T, email string) {
	t.Helper()

	out := mustRunCLI(t, "auth", "login", "--email", email)
	loginResult := parseJSON(t, out)

	if token, ok := loginResult["access_token"].(string); ok && token != "" {
		return // direct login, done
	}

	challengeID, ok := loginResult["challenge_id"].(string)
	if !ok || challengeID == "" {
		t.Fatalf("login did not return access_token or challenge_id: %v", loginResult)
	}

	mockOTP := testutil.GetMockOTP(t)
	out = mustRunCLI(t, "auth", "verify", "--challenge-id", challengeID, "--code", mockOTP)
	verifyResult := parseJSON(t, out)
	if token, ok := verifyResult["access_token"].(string); !ok || token == "" {
		t.Fatalf("verify did not return access_token: %v", verifyResult)
	}
}

