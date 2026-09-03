package capabilities

import (
	"os"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
)

func TestCapabilityCacheIsBoundToAgentAndLanguage(t *testing.T) {
	home := t.TempDir()
	config.SetHomeDir(home)
	t.Cleanup(func() { config.SetHomeDir("") })
	snapshot := Snapshot{OwnerAgentID: "42", ServerURL: "https://example.test", Language: "zh-CN", ETag: `"v1"`, FetchedAt: time.Now().UnixMilli(), MaxAgeSeconds: 300, Registry: []byte(`{"schema_version":1}`)}
	if err := Save("default", snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load("default", "https://example.test", "42", "zh-CN")
	if err != nil || loaded.ETag != `"v1"` {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	if _, err := Load("default", "https://example.test", "99", "zh-CN"); err == nil {
		t.Fatal("cache must reject a different Agent identity")
	}
	if _, err := Load("default", "https://other.test", "42", "zh-CN"); err == nil {
		t.Fatal("cache must reject a different server URL")
	}
	info, err := os.Stat(pathFor("default", "zh-CN"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("cache permissions = %o, want 600", info.Mode().Perm())
	}
}
