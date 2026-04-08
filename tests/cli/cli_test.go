package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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

	// Pre-add a server entry pointing at the running local API.
	mustRunCLI(t, "server", "add", "--name", "local", "--endpoint", apiBaseURL)
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
	if _, ok := v["skill_version"]; !ok {
		t.Error("expected skill_version in version output")
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

// ---------- Helpers ----------

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

