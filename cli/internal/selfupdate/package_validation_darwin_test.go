//go:build darwin

package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeMacPackageValidationFailuresStayClosed(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"plist_read", "version", "signature"} {
		t.Run(scenario, func(t *testing.T) {
			root, _, _ := installationFixture(t)
			dir := t.TempDir()
			version := "9.8.7"
			helperVersion := version
			if scenario == "version" {
				helperVersion = "9.8.6"
			}
			entries := append([]packageEntry{{name: "eigenflux", content: string(binary), mode: 0755}}, realCLITestHelper(t, dir, helperVersion, binary)...)
			for i := range entries {
				if scenario == "plist_read" && strings.HasSuffix(entries[i].name, "/Contents/Info.plist") {
					entries[i].content = "not a plist"
				}
				if scenario == "signature" && strings.HasSuffix(entries[i].name, "/_CodeSignature/CodeResources") {
					entries[i].content = "invalid resource seal"
				}
			}
			archive := testPackage(t, entries...)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
			defer server.Close()
			name := "eigenflux-darwin-" + runtime.GOARCH
			manifest := Manifest{Version: version, Artifacts: map[string]Artifact{name: testArtifact(binary)}, Packages: map[string]Artifact{name + ".zip": testArtifact(archive)}}
			_, _, err := StagePackage(context.Background(), Options{Home: dir, CDN: server.URL, Version: "9.8.6"}, manifest, root)
			want := "verify_helper_" + scenario + "_failed"
			if err == nil || failureCode("verify", err) != want {
				t.Fatalf("validation classification: %v, want %s", err, want)
			}
			if scenario == "signature" && !strings.Contains(err.Error(), "exit status") {
				t.Fatalf("missing local tool error: %v", err)
			}
			if _, err := os.Stat(root + ".current.json"); !os.IsNotExist(err) {
				t.Fatal("failed validation activated package")
			}
			if _, err := os.Stat(filepath.Join(root+".versions", version)); !os.IsNotExist(err) {
				t.Fatal("failed validation installed package")
			}
			old, _ := os.ReadFile(root)
			if string(old) != "stable entry" {
				t.Fatal("failed validation changed stable entry")
			}
		})
	}
}
