package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/tests/testutil"
	"github.com/stretchr/testify/require"
)

// Build the real CLI into build/ and set EIGENFLUX_TEST_CLI to opt into the
// subprocess acceptance test. The root and CLI remain independent Go modules.
func TestCLIFeedbackProcessRecovery(t *testing.T) {
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Skip("set EIGENFLUX_TEST_CLI to the built CLI for process-recovery acceptance")
	}
	token, agent, item, _ := feedbackMetadataFixture(t)
	upstream, err := url.Parse(testutil.BaseURL)
	require.NoError(t, err)
	accepted, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.Request.URL.Path == "/api/v1/items/feedback" && requests.Add(1) == 1 {
			close(accepted)
			<-release
		}
		return nil
	}
	proxy.ErrorLog = nil
	server := httptest.NewServer(proxy)
	defer server.Close()
	root := filepath.Join(t.TempDir(), ".eigenflux")
	dir := filepath.Join(root, "servers", "local")
	require.NoError(t, os.MkdirAll(dir, 0700))
	write := func(path string, value any) {
		data, e := json.Marshal(value)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(path, data, 0600))
	}
	write(filepath.Join(root, "config.json"), map[string]any{"default_server": "local", "servers": []any{map[string]any{"name": "local", "endpoint": server.URL}}, "kv": map[string]string{"auto_skill_sync": "false"}})
	write(filepath.Join(dir, "credentials.json"), map[string]any{"agent_id": strconv.FormatInt(agent, 10), "access_token": token})
	args := []string{"--homedir", root, "--server", "local", "--format", "json"}
	report := fmt.Sprintf(`[{"item_id":%q,"impression_id":"process-recovery","score":2,"surfaced":false}]`, strconv.FormatInt(item, 10))
	command := exec.Command(binary, append(append([]string{}, args...), "feed", "feedback", "--items", report)...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	require.NoError(t, command.Start())
	defer func() { _ = command.Process.Kill() }()
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("CLI did not reach the real feedback API")
	}
	// Stop after the server accepts the durable report but before the CLI sees
	// its receipt, then restart from the same Home with its retained queue.
	require.NoError(t, command.Process.Kill())
	_ = command.Wait()
	close(release)
	waitForStreamDrained(t, itemstats.StreamName, itemstats.GroupName)
	retry := exec.Command(binary, append(append([]string{}, args...), "feed", "event", "flush")...)
	result, err := retry.CombinedOutput()
	require.NoError(t, err, string(result))
	require.Eventually(t, func() bool {
		var n int
		return testutil.TestDB.QueryRow(`SELECT count(*) FROM feedback_logs WHERE agent_id=$1 AND item_id=$2`, agent, item).Scan(&n) == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond)
	waitForStreamDrained(t, itemstats.StreamName, itemstats.GroupName)
	var n, scoreCount int
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT count(*) FROM feedback_logs WHERE agent_id=$1 AND item_id=$2`, agent, item).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT score_2_count FROM item_stats WHERE item_id=$1`, item).Scan(&scoreCount))
	require.Equal(t, 1, scoreCount)
	var key, contract, source string
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT client_key,contract_version,impression_source FROM feedback_logs WHERE agent_id=$1 AND item_id=$2`, agent, item).Scan(&key, &contract, &source))
	require.Len(t, key, 32)
	require.Equal(t, "feedback.v2", contract)
	require.Equal(t, "explicit", source)
	raw, err := os.ReadFile(filepath.Join(dir, "data", "feed-reports.json"))
	require.NoError(t, err)
	var state struct {
		Entries map[string]struct {
			ClientKey string `json:"client_key"`
			ScoredAt  int64  `json:"scored_at"`
			Delivery  string `json:"delivery"`
		}
	}
	require.NoError(t, json.Unmarshal(raw, &state))
	require.Len(t, state.Entries, 1)
	for _, entry := range state.Entries {
		require.Equal(t, key, entry.ClientKey)
		require.Positive(t, entry.ScoredAt)
		require.Contains(t, []string{"accepted", "duplicate"}, entry.Delivery)
	}
	queue, err := os.ReadFile(filepath.Join(dir, "data", "events", "queue.json"))
	require.NoError(t, err)
	var pending struct{ Events []any }
	require.NoError(t, json.Unmarshal(queue, &pending))
	require.Empty(t, pending.Events)
	require.GreaterOrEqual(t, requests.Load(), int32(2))
}
