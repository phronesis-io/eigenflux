package discoverye2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pkg/es"
	"github.com/stretchr/testify/require"
)

func TestFeedCLISavedBatchesKeepOneExposureSet(t *testing.T) {
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Skip("EIGENFLUX_TEST_CLI required")
	}
	s := startStack(t, map[string]string{"FRIEND_FEED_ENABLED": "true"})
	s.sql(t, "INSERT INTO agent_context_heads(agent_id,current_revision,active_revision,updated_at) VALUES(?,1,1,?)", s.owner, time.Now().UnixMilli())
	s.token = s.seedSession(t, s.owner, "{feed:read,feed:feedback,context:read,context:write,commands:claim}")
	s.sql(t, "INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,1,?)", s.owner, s.author, time.Now().UnixMilli())
	for i := int64(1); i <= 4; i++ {
		s.addSocialNeighbor(t, s.item+i)
	}
	refreshed, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(es.IndexName))
	require.NoError(t, err)
	_ = refreshed.Body.Close()
	require.False(t, refreshed.IsError())
	home := s.socialCLIHome(t, s.owner, s.token)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	writeJSON(t, filepath.Join(home, "servers", "eigenflux", "identity.json"), map[string]any{"key_type": "ed25519-v1", "private_key": base64.RawURLEncoding.EncodeToString(private), "created_at": time.Now().UnixMilli()})
	ctx, cancel := context.WithCancel(context.Background())
	var worker sync.WaitGroup
	worker.Add(1)
	go func() { defer worker.Done(); consumer.NewItemStatsConsumer(s.cfg, nil).Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		worker.Wait()
		s.sql(t, "DELETE FROM feedback_logs WHERE agent_id=?", s.owner)
		s.sql(t, "DELETE FROM item_stats WHERE author_agent_id=?", s.author)
	})
	run := func(args ...string) []byte {
		t.Helper()
		commandCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		command := exec.CommandContext(commandCtx, binary, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		command.Env = append(os.Environ(), "EIGENFLUX_SKILLS_BASE_URL=http://127.0.0.1:1")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		out, err := command.Output()
		require.NoError(t, err, "CLI: %s", stderr.String())
		return out
	}
	type batch struct {
		ImpressionID   string `json:"impression_id"`
		OutputContract string `json:"output_contract"`
		Items          []struct {
			ItemID string `json:"item_id"`
		} `json:"items"`
		LocalBatch struct {
			Source  string `json:"source"`
			Backlog int    `json:"backlog"`
		} `json:"local_batch"`
	}
	first := decode[batch](t, run("feed", "poll", "--batch", "1"))
	require.Len(t, first.Items, 1)
	require.Equal(t, "server", first.LocalBatch.Source)
	require.Equal(t, 4, first.LocalBatch.Backlog)
	require.Eventually(t, func() bool {
		var count int64
		return s.db.Table("replay_logs").Where("agent_id=?", s.owner).Count(&count).Error == nil && count == 5
	}, 10*time.Second, 25*time.Millisecond)
	seen := map[string]bool{}
	current := first
	for i := 0; i < 3; i++ {
		require.Equal(t, first.ImpressionID, current.ImpressionID)
		id := current.Items[0].ItemID
		require.False(t, seen[id])
		seen[id] = true
		// Omit the impression to exercise the handed-exposure attribution path.
		run("feed", "feedback", "--items", fmt.Sprintf(`[{"item_id":%q,"score":0}]`, id))
		if i < 2 {
			current = decode[batch](t, run("feed", "next", "--batch", "1"))
			require.Equal(t, "local", current.LocalBatch.Source)
		}
	}
	local := decode[batch](t, run("feed", "poll", "--batch", "1"))
	require.Equal(t, "local", local.LocalBatch.Source)
	require.Equal(t, first.ImpressionID, local.ImpressionID)
	require.Len(t, local.Items, 1)
	require.False(t, seen[local.Items[0].ItemID])
	agentOutput := string(run("feed", "next", "--batch", "1", "--format", "agent"))
	contract := strings.TrimSpace(first.OutputContract)
	require.NotEmpty(t, contract)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(contract)))[:16]
	contractPath := filepath.Join(home, "servers", "eigenflux", "data", "feed-contracts", digest+".md")
	contractFile, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	require.Equal(t, contract, string(contractFile))
	require.Contains(t, agentOutput, fmt.Sprintf("Read %s (sha256 %s, %d bytes) completely", contractPath, digest, len(contract)))
	require.NotContains(t, agentOutput, contract)
	lines := strings.Split(strings.TrimSpace(agentOutput), "\n")
	rehanded := decode[batch](t, []byte(lines[len(lines)-1]))
	require.Equal(t, local.Items, rehanded.Items)
	require.Equal(t, first.ImpressionID, rehanded.ImpressionID)
	require.Eventually(t, func() bool {
		var count int64
		return s.db.Table("feedback_logs").Where("agent_id=? AND impression_id=? AND impression_source='cli_inferred'", s.owner, first.ImpressionID).Count(&count).Error == nil && count == 3
	}, 10*time.Second, 25*time.Millisecond)
	var exposures []struct {
		ImpressionID string
		ItemID       int64
	}
	require.NoError(t, s.db.Table("replay_logs").Select("impression_id,item_id").Where("agent_id=?", s.owner).Scan(&exposures).Error)
	require.Len(t, exposures, 5)
	for _, e := range exposures {
		require.Equal(t, first.ImpressionID, e.ImpressionID)
	}
	var ledger struct {
		Entries map[string]struct {
			HandedAt *int64 `json:"handed_at"`
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, "servers", "eigenflux", "data", "feed-reports.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &ledger))
	require.Len(t, ledger.Entries, 5)
}
