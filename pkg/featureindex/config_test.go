package featureindex

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	featureconfig "eigenflux_server/configs/featureindex"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func configFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	files, err := fs.Glob(featureconfig.Defaults, "*.yaml")
	require.NoError(t, err)
	for _, name := range files {
		raw, err := fs.ReadFile(featureconfig.Defaults, name)
		require.NoError(t, err)
		out[name] = &fstest.MapFile{Data: raw}
	}
	return out
}

func TestHotReloadChangesActualForwardProjection(t *testing.T) {
	files := configFiles(t)
	registry, err := NewRegistry(files)
	require.NoError(t, err)
	redisClient := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { redisClient.Close() })
	f := Forward{Redis: redisClient, Namespace: "broadcast:v1", Registry: registry}
	ctx := context.Background()
	value := map[string]any{"item_id": 7, "version": 1, "new_scalar": .8}
	require.NoError(t, f.Put(ctx, 7, "item", 1, value))
	require.NotContains(t, redisClient.HGet(ctx, f.Key(7, "item"), "data").Val(), "new_scalar")
	base := string(files["broadcast.item.yaml"].Data)
	files["broadcast.item.yaml"].Data = []byte(base + "  - new_scalar\n")
	changed, err := registry.Reload(files)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, f.Put(ctx, 7, "item", 1, value)) // Same source revision can rematerialize added fields.
	got, err := f.Get(ctx, []int64{7}, "item")
	require.NoError(t, err)
	require.Contains(t, string(got[7]), `"new_scalar":0.8`)
	files["broadcast.item.yaml"].Data = []byte(base)
	_, err = registry.Reload(files)
	require.NoError(t, err)
	got, err = f.Get(ctx, []int64{7}, "item")
	require.NoError(t, err)
	require.NotContains(t, string(got[7]), "new_scalar", "removed fields are hidden even on old stored payloads")
}

func TestInvalidReloadKeepsWholeLastGoodSnapshot(t *testing.T) {
	for name, change := range map[string]func(fstest.MapFS){
		"syntax":       func(f fstest.MapFS) { f["agent.card.yaml"].Data = []byte("fields: [") },
		"missing view": func(f fstest.MapFS) { delete(f, "agent.card.yaml") },
		"unknown key": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = append(f["agent.card.yaml"].Data, []byte("typo: true\n")...)
		},
		"duplicate field": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = append(f["agent.card.yaml"].Data, []byte("  - agent_id\n")...)
		},
		"embedding": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = append(f["agent.card.yaml"].Data, []byte("  - embedding\n")...)
		},
		"identity omitted": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = []byte(strings.ReplaceAll(string(f["agent.card.yaml"].Data), "  - agent_id\n", ""))
		},
		"identity changed": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = []byte(strings.ReplaceAll(string(f["agent.card.yaml"].Data), "id_field: agent_id", "id_field: version"))
		},
		"invalid TTL": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = []byte(strings.ReplaceAll(string(f["agent.card.yaml"].Data), "ttl: 0s", "ttl: -1s"))
		},
		"retention shorter than freshness": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = []byte(strings.ReplaceAll(string(f["agent.card.yaml"].Data), "ttl: 0s", "ttl: 200h"))
		},
		"unbounded loader": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = []byte(strings.ReplaceAll(string(f["agent.card.yaml"].Data), "batch_size: 100", "batch_size: 1001"))
		},
		"second document": func(f fstest.MapFS) {
			f["agent.card.yaml"].Data = append(f["agent.card.yaml"].Data, []byte("---\nname: other\n")...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			files := configFiles(t)
			registry, err := NewRegistry(files)
			require.NoError(t, err)
			before := registry.Definitions()
			files["broadcast.item.yaml"].Data = append(files["broadcast.item.yaml"].Data, []byte("  - new_scalar\n")...)
			change(files)
			changed, err := registry.Reload(files)
			require.Error(t, err)
			require.False(t, changed)
			require.Equal(t, before, registry.Definitions())
		})
	}
}

func TestConfigPollingAndShutdown(t *testing.T) {
	files := configFiles(t)
	registry, err := NewRegistry(files)
	require.NoError(t, err)
	dir := t.TempDir()
	for name, file := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), file.Data, 0600))
	}
	stop, err := registry.Start(context.Background(), dir, 10*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(stop)
	path := filepath.Join(dir, "broadcast.item.yaml")
	require.NoError(t, os.WriteFile(path+".tmp", append(files["broadcast.item.yaml"].Data, []byte("  - polled_field\n")...), 0600))
	require.NoError(t, os.Rename(path+".tmp", path))
	require.Eventually(t, func() bool {
		view, err := registry.resolve("broadcast:v1", "item")
		return err == nil && strings.Contains(strings.Join(view.Fields, ","), "polled_field")
	}, time.Second, 10*time.Millisecond)
	stop()
	before := registry.Definitions()
	require.NoError(t, os.WriteFile(path, files["broadcast.item.yaml"].Data, 0600))
	require.Equal(t, before, registry.Definitions())
	_, err = registry.Start(context.Background(), t.TempDir(), time.Second)
	require.Error(t, err)
}

func TestConcurrentSnapshotReads(t *testing.T) {
	files := configFiles(t)
	registry, err := NewRegistry(files)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				d, err := registry.resolve("broadcast:v1", "item")
				require.NoError(t, err)
				require.Contains(t, d.Fields, "item_id")
				registry.Definitions()
			}
		}()
	}
	base := string(files["broadcast.item.yaml"].Data)
	for i := 0; i < 20; i++ {
		text := base
		if i%2 == 0 {
			text += "  - new_scalar\n"
		}
		files["broadcast.item.yaml"].Data = []byte(text)
		_, err := registry.Reload(files)
		require.NoError(t, err)
	}
	wg.Wait()
}
