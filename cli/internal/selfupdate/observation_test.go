package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestObservationDistinguishesActualCheckAndSiblingAdoption(t *testing.T) {
	o, _, _, _ := fixture(t)
	started := 0
	var progress []Progress
	o.OnAttempt = func() { started++ }
	o.OnProgress = func(p Progress) { progress = append(progress, p) }
	r := Check(context.Background(), o)
	if !r.Attempted || r.Phase != "adoption" || r.TargetVersion != "0.0.48" || started != 1 {
		t.Fatalf("installation observation: %+v started=%d", r, started)
	}
	want := []Progress{{Phase: "check"}, {Phase: "download", TargetVersion: "0.0.48"}, {Phase: "verify", TargetVersion: "0.0.48"}, {Phase: "probe", TargetVersion: "0.0.48"}, {Phase: "install", TargetVersion: "0.0.48"}, {Phase: "adoption", TargetVersion: "0.0.48"}}
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("update progress: got %+v want %+v", progress, want)
	}
	progress = nil
	r = Check(context.Background(), o)
	if r.Attempted || r.Phase != "adoption" || started != 1 || r.TargetVersion != "" || !reflect.DeepEqual(progress, []Progress{{Phase: "adoption"}}) {
		t.Fatalf("sibling adoption counted as install: %+v progress=%+v", r, progress)
	}
	o.Version = "0.0.48"
	progress = nil
	r = Check(context.Background(), o)
	if r.Attempted || r.Status != "deferred" || r.Reason != "check_interval" || r.NextCheckAt != o.Now.Add(24*time.Hour).Format(time.RFC3339) || started != 1 || len(progress) != 0 {
		t.Fatalf("throttle counted as attempt: %+v progress=%+v", r, progress)
	}
}

func TestObservationRetainsFailurePhaseAndTrustedTarget(t *testing.T) {
	for _, tc := range []struct {
		name, phase, target string
		rolledBack          bool
	}{
		{name: "signature", phase: "check"},
		{name: "download", phase: "download", target: "0.0.48"},
		{name: "checksum", phase: "verify", target: "0.0.48"},
		{name: "staged-probe", phase: "probe", target: "0.0.48"},
		{name: "installed-probe", phase: "adoption", target: "0.0.48", rolledBack: true},
		{name: "restore-failed", phase: "adoption", target: "0.0.48"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, m, bin, _ := fixture(t)
			var progress []Progress
			o.OnProgress = func(p Progress) { progress = append(progress, p) }
			switch tc.name {
			case "signature":
				m.Version = "0.0.49"
			case "download":
				o.Client = &http.Client{Transport: artifactFailureTransport{}}
			case "checksum":
				*bin = []byte(strings.Repeat("x", len(*bin)))
			case "staged-probe":
				*bin = script("wrong")
				resignFixture(t, &o, m, *bin)
			case "installed-probe", "restore-failed":
				action := "exit 1"
				if tc.name == "restore-failed" {
					action = "rm \"$0.previous\"; exit 1"
				}
				*bin = []byte("#!/bin/sh\ncase \"$0\" in *.eigenflux-update-*) echo 0.0.48;; *) " + action + ";; esac\n")
				resignFixture(t, &o, m, *bin)
			}
			r := Check(context.Background(), o)
			if r.Status != "failed" || r.Reason == "" || r.Phase != tc.phase || !r.Attempted || r.TargetVersion != tc.target || r.RolledBack != tc.rolledBack || r.Version != o.Version {
				t.Fatalf("failure observation: %+v", r)
			}
			if progress[len(progress)-1].Phase != tc.phase {
				t.Fatalf("missing phase start: %+v", progress)
			}
			for _, p := range progress {
				if tc.name == "signature" && p.TargetVersion != "" {
					t.Fatalf("untrusted version observed: %+v", p)
				}
			}
			if r.RolledBack {
				bytes, err := os.ReadFile(o.Executable)
				if err != nil || string(bytes) != string(script(o.Version)) {
					t.Fatalf("reported rollback without old binary: %q %v", bytes, err)
				}
			}
		})
	}
}

func TestObservationPanicsCannotInterruptUpdateOrRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprint(rollback), func(t *testing.T) {
			o, m, bin, _ := fixture(t)
			o.OnAttempt = func() { panic("attempt observer") }
			o.OnProgress = func(Progress) { panic("phase observer") }
			if rollback {
				*bin = []byte("#!/bin/sh\ncase \"$0\" in *.eigenflux-update-*) echo 0.0.48;; *) exit 1;; esac\n")
				resignFixture(t, &o, m, *bin)
			}
			r := Check(context.Background(), o)
			if rollback {
				if r.Status != "failed" || !r.RolledBack {
					t.Fatalf("observer interrupted rollback: %+v", r)
				}
			} else if r.Status != "updated" || r.Version != "0.0.48" {
				t.Fatalf("observer interrupted update: %+v", r)
			}
		})
	}
}

func resignFixture(t *testing.T, o *Options, m *Manifest, bin []byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	o.Key = pub
	sum := sha256.Sum256(bin)
	for k := range m.Artifacts {
		m.Artifacts[k] = Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))}
	}
	if err := Sign(m, key); err != nil {
		t.Fatal(err)
	}
}

type artifactFailureTransport struct{}

func (artifactFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/cli/latest/release.json" {
		return nil, fmt.Errorf("artifact unavailable")
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestSkipAndLockReasons(t *testing.T) {
	o, _, _, count := fixture(t)
	invalid := o
	invalid.Version = "dev"
	if r := Check(context.Background(), invalid); r.Reason != "invalid_version" || r.Status != "skipped" {
		t.Fatalf("invalid version: %+v", r)
	}
	untrusted := o
	untrusted.Key = nil
	if r := Check(context.Background(), untrusted); r.Reason != "trust_key_missing" || r.Status != "unconfigured" {
		t.Fatalf("missing trust root: %+v", r)
	}
	lock, err := os.OpenFile(o.Executable+".update.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if locked, err := tryLock(lock); !locked || err != nil {
		t.Fatal(err)
	}
	defer unlock(lock)
	if r := Check(context.Background(), o); r.Reason != "installation_locked" || r.Status != "busy" || r.Attempted || r.NextCheckAt != "" {
		t.Fatalf("lock: %+v", r)
	}
	if count.Load() != 0 {
		t.Fatal("skipped check contacted CDN")
	}
}
