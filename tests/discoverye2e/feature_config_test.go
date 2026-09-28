package discoverye2e

import (
	"context"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	featureconfig "eigenflux_server/configs/featureindex"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func TestFeatureYAMLReloadWithoutServiceRestart(t *testing.T) {
	if os.Getenv("DISCOVERY_E2E") != "1" {
		t.Skip("DISCOVERY_E2E required")
	}
	dir := t.TempDir()
	files, err := fs.Glob(featureconfig.Defaults, "*.yaml")
	require.NoError(t, err)
	for _, name := range files {
		raw, err := fs.ReadFile(featureconfig.Defaults, name)
		require.NoError(t, err)
		if name == "broadcast.item.yaml" {
			raw = []byte(strings.ReplaceAll(string(raw), "  - quality_score\n", ""))
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), raw, 0600))
	}
	t.Setenv("FEATURE_INDEX_CONFIG_DIR", dir)
	t.Setenv("FEATURE_INDEX_RELOAD_INTERVAL", "50ms")
	s := startStack(t)
	ctx := context.Background()
	// The producer already provides quality. The running reader initially does not
	// register that field; adding it to YAML must expose it without a restart.
	_, err = (featureindex.BroadcastIndex{DB: s.db, Redis: mq.RDB}).Load(ctx, []int64{s.item})
	require.NoError(t, err)
	quality := func() float64 {
		result := s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Broadcast}}, "")
		require.Len(t, result.Items, 1)
		s.waitSamples(t, result.ImpressionID, 1)
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=?", result.ImpressionID).Scan(&raw).Error)
		sample := decode[struct {
			Search discovery.Candidate `json:"search"`
		}](t, []byte(raw))
		return sample.Search.Score.Features["quality"]
	}
	require.Zero(t, quality())
	raw, err := fs.ReadFile(featureconfig.Defaults, "broadcast.item.yaml")
	require.NoError(t, err)
	path := filepath.Join(dir, "broadcast.item.yaml")
	require.NoError(t, os.WriteFile(path+".tmp", raw, 0600))
	require.NoError(t, os.Rename(path+".tmp", path))
	require.Eventually(t, func() bool { return math.Abs(quality()-.8) < 1e-6 }, 3*time.Second, 100*time.Millisecond)
}
