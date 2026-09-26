package discoverye2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/sort/discovery"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryCLIE2E(t *testing.T) {
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Skip("EIGENFLUX_TEST_CLI required")
	}
	s := startStack(t)
	captured := s.saved(t, "agent")
	home := filepath.Join(t.TempDir(), ".eigenflux")
	dir := filepath.Join(home, "servers", "eigenflux")
	require.NoError(t, os.MkdirAll(dir, 0700))
	write := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
	}
	write(filepath.Join(home, "config.json"), map[string]any{
		"default_server": "eigenflux",
		"servers":        []any{map[string]any{"name": "eigenflux", "endpoint": s.url}},
		"kv":             map[string]string{"auto_skill_sync": "false"},
	})
	write(filepath.Join(dir, "agent-v2-credentials.json"), map[string]any{
		"access_token": s.token, "refresh_token": "test-only-refresh",
		"agent_id": fmt.Sprint(s.owner), "expires_at": time.Now().Add(time.Hour).UnixMilli(),
	})
	filters := filepath.Join(home, "filters.json")
	write(filters, map[string]any{"category": s.category})
	run := func(args ...string) discovery.Response {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		command.Env = append(os.Environ(), "EIGENFLUX_SKILLS_BASE_URL=http://127.0.0.1:1")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		raw, err := command.Output()
		require.NoError(t, err, "CLI: %s", stderr.String())
		return decode[discovery.Response](t, raw)
	}
	found := run("search", "landing page design", "--types", "agent", "--filters", filters)
	require.Len(t, found.Items, 1)
	require.Equal(t, s.author, found.Items[0].Ref.ID)
	require.Zero(t, found.Items[0].NeedID, "query search does not require a captured Need")

	page := run("search", "landing page design", "--filters", filters, "--limit", "1")
	impression := page.ImpressionID
	for i, kind := range discovery.AllKinds {
		require.Len(t, page.Items, 1)
		require.Equal(t, kind, page.Items[0].Ref.Type)
		require.Equal(t, impression, page.ImpressionID)
		require.Equal(t, i < 2, page.HasMore)
		if page.HasMore {
			page = run("search", "landing page design", "--filters", filters, "--limit", "1", "--cursor", page.NextCursor)
		}
	}
	require.Empty(t, page.NextCursor)
	s.waitSamples(t, impression, 3)

	recommended := run("recommend", "--types", "agent", "--limit", "5", "--idempotency-key", "cli-auto-recommend")
	require.Len(t, recommended.Items, 1)
	require.Equal(t, s.author, recommended.Items[0].Ref.ID)
	require.Equal(t, captured.NeedInputID, recommended.Items[0].NeedID, "platform selects the captured Need without CLI IDs")
	require.Equal(t, recommended, run("recommend", "--types", "agent", "--limit", "5", "--idempotency-key", "cli-auto-recommend"))
	require.Eventually(t, func() bool {
		return mq.RDB.SIsMember(context.Background(), fmt.Sprintf("impr:discovery:agent:%d:items", s.owner), fmt.Sprintf("agent:%d", s.author)).Val()
	}, 3*time.Second, 25*time.Millisecond)
	s.saved(t, "broadcast")
	s.saved(t, "commission")
	batch := run("recommend", "--limit", "5", "--idempotency-key", "cli-batch-recommend")
	require.Len(t, batch.Items, 2, "return multiple eligible results without padding to limit")
	require.Equal(t, batch, run("recommend", "--limit", "5", "--idempotency-key", "cli-batch-recommend"))
	s.waitSamples(t, batch.ImpressionID, 2)
}
