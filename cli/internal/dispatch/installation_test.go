package dispatch

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"cli.eigenflux.ai/internal/selfupdate"
)

func TestWorkBuddySelfBridgeRecognizesOnlyItsOwnInstallation(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(dir, "eigenflux")
	name := "eigenflux"
	if runtime.GOOS == "windows" {
		root += ".exe"
		name += ".exe"
	}
	current := filepath.Join(root+".versions", "1.0.3", name)
	previous := filepath.Join(root+".versions", "1.0.2", name)
	foreign := filepath.Join(dir, "third-party")
	for _, path := range []string{root, current, previous, foreign} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("CLI"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(selfupdate.InstallRootEnv, root)
	for _, path := range []string{root, current, previous} {
		if !sameCLIInstallation(path, current) {
			t.Fatalf("own bridge was not recognized: %s", path)
		}
	}
	if sameCLIInstallation(foreign, current) {
		t.Fatal("third-party command was remapped to our privileged bridge")
	}
	t.Setenv(selfupdate.InstallRootEnv, foreign)
	if sameCLIInstallation(root, current) {
		t.Fatal("unvalidated environment widened the installation")
	}
}
