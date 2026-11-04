package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIndependentNotifierManifestAuthenticatesVersion(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := Manifest{Version: "1.0.6", NotifierVersion: "1.0.5", Artifacts: map[string]Artifact{}, Packages: map[string]Artifact{}}
	if err := Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if got, err := Verify(raw, pub); err != nil || got.NotifierVersion != "1.0.5" {
		t.Fatalf("v2 verify: %+v %v", got, err)
	}
	m.NotifierVersion = "1.0.4"
	raw, _ = json.Marshal(m)
	if _, err := Verify(raw, pub); err == nil {
		t.Fatal("tampered notifier version accepted")
	}
}

func TestBridgeManifestRetainsOldCompleteVerifier(t *testing.T) {
	type oldManifest struct {
		Version   string              `json:"version"`
		Artifacts map[string]Artifact `json:"artifacts"`
		Packages  map[string]Artifact `json:"packages,omitempty"`
		Signature string              `json:"signature,omitempty"`
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	for _, notifier := range []string{"", "1.0.5"} {
		m := Manifest{Version: "1.0.5", NotifierVersion: notifier, Artifacts: map[string]Artifact{}, Packages: map[string]Artifact{"eigenflux-darwin-arm64.zip": testArtifact([]byte("bridge"))}}
		if err := Sign(&m, key); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(m)
		var old oldManifest
		_ = json.Unmarshal(raw, &old)
		sig, _ := base64.StdEncoding.DecodeString(old.Signature)
		old.Signature = ""
		payload, _ := json.Marshal(old)
		accepted := ed25519.Verify(pub, append([]byte(manifestDomain), payload...), sig)
		if accepted != (notifier == "") {
			t.Fatalf("old verifier acceptance=%v notifier=%q", accepted, notifier)
		}
	}
}

func TestManifestLaneFallbackOnlyOn404(t *testing.T) {
	for _, code := range []int{200, 404, 403, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			legacy := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cli/v2/latest/package-release.json" {
					w.WriteHeader(code)
					_, _ = w.Write([]byte("v2"))
					return
				}
				if r.URL.Path == "/cli/latest/package-release.json" {
					legacy++
					_, _ = w.Write([]byte("v1"))
					return
				}
				t.Errorf("unexpected URL %s", r.URL.Path)
				http.NotFound(w, r)
			}))
			defer server.Close()
			raw, v2, err := downloadReleaseManifest(context.Background(), Options{CDN: server.URL, Client: server.Client(), Version: "1.0.5"}, "package-release.json")
			if code == 404 {
				if err != nil || v2 || legacy != 1 || string(raw) != "v1" {
					t.Fatalf("legacy fallback: %q %v %v count=%d", raw, v2, err, legacy)
				}
			} else if code == 200 {
				if err != nil || !v2 || legacy != 0 {
					t.Fatalf("v2: %v %v count=%d", v2, err, legacy)
				}
			} else if err == nil || legacy != 0 {
				t.Fatalf("unsafe fallback: err=%v legacy=%d", err, legacy)
			}
		})
	}
}

func TestCheckRejectsLegacyPayloadOnV2Lane(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := Manifest{Version: "1.0.5"}
	_ = Sign(&m, key)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli/v2/latest/release.json" {
			t.Errorf("unexpected fallback %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(m)
	}))
	defer server.Close()
	root, _, _ := installationFixture(t)
	result := Check(context.Background(), Options{Home: t.TempDir(), Executable: root, Version: "1.0.5", Key: pub, CDN: server.URL, CheckNow: true})
	if result.Status != "failed" || result.Reason != "manifest_verify_failed" {
		t.Fatalf("unsigned independent version accepted: %+v", result)
	}
}

func TestLegacyEntryManifestCannotDivergeFromV2(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	current := Manifest{Version: "1.0.6", NotifierVersion: "1.0.5", Artifacts: map[string]Artifact{"eigenflux-darwin-arm64": testArtifact([]byte("CLI"))}}
	for _, scenario := range []string{"matching", "wrong_version", "wrong_binary", "v2_metadata", "complete_metadata", "untrusted"} {
		t.Run(scenario, func(t *testing.T) {
			legacy := Manifest{Version: current.Version, Artifacts: map[string]Artifact{"eigenflux-darwin-arm64": current.Artifacts["eigenflux-darwin-arm64"]}}
			switch scenario {
			case "wrong_version":
				legacy.Version = "1.0.5"
			case "wrong_binary":
				legacy.Artifacts["eigenflux-darwin-arm64"] = testArtifact([]byte("other"))
			case "v2_metadata":
				legacy.NotifierVersion = "1.0.5"
			case "complete_metadata":
				legacy.Packages = map[string]Artifact{"pkg.zip": testArtifact([]byte("pkg"))}
			}
			if err := Sign(&legacy, key); err != nil {
				t.Fatal(err)
			}
			if scenario == "untrusted" {
				legacy.Signature = "invalid"
			}
			raw, _ := json.Marshal(legacy)
			err := validateLegacyEntryManifest(raw, current, pub)
			if (err == nil) != (scenario == "matching") {
				t.Fatalf("legacy entry acceptance: %v", err)
			}
		})
	}
}
