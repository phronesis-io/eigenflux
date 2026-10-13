package uninstallscript

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDistributedWrappersMatchEmbeddedAssets(t *testing.T) {
	for name, embedded := range map[string][]byte{"uninstall.sh": POSIX, "uninstall.ps1": Windows} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "static", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, embedded) {
			t.Fatalf("static/%s differs from embedded wrapper", name)
		}
	}
}

func TestInstallPreservesChangedWrapper(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "eigenflux")
	path, digest, err := Install(bin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("owner edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(bin, map[string]string{path: digest}); err == nil {
		t.Fatal("edited wrapper overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "owner edited" {
		t.Fatal("edited wrapper changed")
	}
}
