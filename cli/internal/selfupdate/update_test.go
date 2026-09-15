package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func script(version string) []byte { return []byte("#!/bin/sh\nprintf '%s\\n' '" + version + "'\n") }

func fixture(t *testing.T) (Options, *Manifest, *[]byte, *atomic.Int32) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture; Windows is cross-compiled separately")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "eigenflux")
	if err := os.WriteFile(path, script("0.0.47"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := script("0.0.48")
	sum := sha256.Sum256(bin)
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	m := &Manifest{Version: "0.0.48", Artifacts: map[string]Artifact{name: {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))}}}
	if err := Sign(m, key); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path == "/cli/latest/release.json" {
			_ = json.NewEncoder(w).Encode(m)
			return
		}
		if r.URL.Path != "/cli/0.0.48/"+name {
			t.Errorf("non-versioned artifact URL: %s", r.URL.Path)
		}
		_, _ = w.Write(bin)
	}))
	t.Cleanup(srv.Close)
	return Options{Home: t.TempDir(), Executable: path, Version: "0.0.47", CDN: srv.URL, Key: pub, Now: time.Now()}, m, &bin, &count
}

func TestUpdateSignedBinaryAndKeepIdentity(t *testing.T) {
	o, _, _, count := fixture(t)
	identity := filepath.Join(filepath.Dir(o.Executable), "credentials.json")
	if err := os.WriteFile(identity, []byte("legacy-agent"), 0600); err != nil {
		t.Fatal(err)
	}
	r := Check(context.Background(), o)
	if r.Status != "updated" || r.Version != "0.0.48" {
		t.Fatalf("%+v", r)
	}
	old, _ := os.ReadFile(o.Executable + ".previous")
	if string(old) != string(script("0.0.47")) {
		t.Fatal("previous binary lost")
	}
	b, _ := os.ReadFile(identity)
	if string(b) != "legacy-agent" {
		t.Fatal("identity changed")
	}
	o.Version = "0.0.48"
	r = Check(context.Background(), o)
	if r.Status != "deferred" || count.Load() != 2 {
		t.Fatalf("daily check not throttled: %+v requests=%d", r, count.Load())
	}
}

func TestAllVersionProbesUseStableHome(t *testing.T) {
	o, m, bin, _ := fixture(t)
	envHome := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", envHome)
	t.Setenv("EF_EXPECTED_HOME", o.Home)
	log := filepath.Join(t.TempDir(), "probes")
	t.Setenv("EF_PROBE_LOG", log)
	makeScript := func(v string) []byte {
		return []byte("#!/bin/sh\n[ \"$1\" = --homedir ] && [ \"$2\" = \"$EF_EXPECTED_HOME\" ] && [ \"$3\" = version ] && [ \"$4\" = --short ] || exit 41\nprintf 'probe\\n' >> \"$EF_PROBE_LOG\"\necho " + v + "\n")
	}
	if err := os.WriteFile(o.Executable, makeScript(o.Version), 0755); err != nil {
		t.Fatal(err)
	}
	*bin = makeScript(m.Version)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	o.Key = pub
	sum := sha256.Sum256(*bin)
	for k := range m.Artifacts {
		m.Artifacts[k] = Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(*bin))}
	}
	if err := Sign(m, key); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if r := Check(context.Background(), o); r.Status != "updated" {
			t.Fatalf("%+v", r)
		}
	}
	b, err := os.ReadFile(log)
	if err != nil || strings.Count(string(b), "probe\n") != 4 {
		t.Fatalf("expected current, staged, installed and sibling probes: %q %v", b, err)
	}
	if entries, err := os.ReadDir(envHome); err != nil || len(entries) != 0 {
		t.Fatalf("environment Home changed: %v %v", entries, err)
	}
}

func TestUpdateFailurePreservesBinary(t *testing.T) {
	for _, name := range []string{"signature", "checksum", "health", "post-install-health", "offline", "minimum", "rollback", "permission"} {
		t.Run(name, func(t *testing.T) {
			o, m, bin, _ := fixture(t)
			switch name {
			case "signature":
				m.Version = "0.0.49"
			case "checksum":
				*bin = []byte(strings.Repeat("x", len(*bin)))
			case "health", "post-install-health":
				// Sign a valid download that cannot report the declared version.
				pub, key, _ := ed25519.GenerateKey(rand.Reader)
				o.Key = pub
				*bin = script("wrong")
				if name == "post-install-health" {
					*bin = []byte("#!/bin/sh\ncase \"$0\" in *.eigenflux-update-*) echo 0.0.48;; *) exit 1;; esac\n")
				}
				sum := sha256.Sum256(*bin)
				for k := range m.Artifacts {
					m.Artifacts[k] = Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(*bin))}
				}
				_ = Sign(m, key)
			case "offline":
				o.CDN = "http://127.0.0.1:1"
			case "permission":
				if os.Geteuid() == 0 {
					t.Skip("requires non-root permissions")
				}
				dir := filepath.Dir(o.Executable)
				if err := os.Chmod(dir, 0555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
			case "minimum":
				o.Minimum = "0.0.49"
			case "rollback":
				if err := saveState(o.Executable, state{Highest: "0.0.49"}); err != nil {
					t.Fatal(err)
				}
			}
			r := Check(context.Background(), o)
			if r.Status != "failed" || r.Error == "" {
				t.Fatalf("%+v", r)
			}
			b, _ := os.ReadFile(o.Executable)
			if string(b) != string(script("0.0.47")) {
				t.Fatal("failed update changed binary")
			}
		})
	}
}

func TestMinimumBypassesDailyCheckOnce(t *testing.T) {
	o, _, _, count := fixture(t)
	if err := saveState(o.Executable, state{Attempt: o.Now}); err != nil {
		t.Fatal(err)
	}
	o.Minimum = "0.0.49"
	if r := Check(context.Background(), o); r.Status != "failed" {
		t.Fatalf("%+v", r)
	}
	if r := Check(context.Background(), o); r.Status != "deferred" {
		t.Fatalf("%+v", r)
	}
	if count.Load() != 1 {
		t.Fatalf("repeated incompatible update attempts: %d", count.Load())
	}
}

func TestConcurrentUpdaterAndSharedBinary(t *testing.T) {
	o, _, _, count := fixture(t)
	f, err := os.OpenFile(o.Executable+".update.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	locked, err := tryLock(f)
	if !locked || err != nil {
		t.Fatal(err)
	}
	if r := Check(context.Background(), o); r.Status != "busy" {
		t.Fatalf("%+v", r)
	}
	unlock(f)
	if r := Check(context.Background(), o); r.Status != "updated" {
		t.Fatalf("%+v", r)
	}
	if r := Check(context.Background(), o); r.Status != "updated" || r.Version != "0.0.48" {
		t.Fatalf("running old process failed to adopt sibling update: %+v", r)
	}
	if count.Load() != 2 {
		t.Fatalf("duplicate download: %d", count.Load())
	}
}

func TestReleaseVersionOrdering(t *testing.T) {
	for _, v := range []string{"0.0.48", "1.20.300", "99999999999999999999.0.0"} {
		if !ValidVersion(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"dev", "0.0.48-beta", "../0.0.48", "0.00.48"} {
		if ValidVersion(v) {
			t.Fatal(v)
		}
	}
	if Compare("0.0.48", "0.0.48") != 0 || Compare("0.0.49", "0.0.48") != 1 || Compare("0.9.99", "0.10.0") != -1 {
		t.Fatal("incorrect numeric ordering")
	}
}
