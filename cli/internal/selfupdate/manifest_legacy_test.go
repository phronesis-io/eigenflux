package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestLegacyReleaseSignatureRetainsOriginalWireContract(t *testing.T) {
	// Model the already-shipped verifier, whose signed payload cannot include
	// new fields. The separately published complete manifest must not replace it.
	type legacyManifest struct {
		Version   string              `json:"version"`
		Artifacts map[string]Artifact `json:"artifacts"`
		Signature string              `json:"signature,omitempty"`
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := Manifest{Version: "1.0.33", Artifacts: map[string]Artifact{"eigenflux-darwin-arm64": testArtifact([]byte("CLI"))}}
	legacyVerifies := func(m Manifest) bool {
		data, _ := json.Marshal(m)
		var old legacyManifest
		if err := json.Unmarshal(data, &old); err != nil {
			t.Fatal(err)
		}
		signature, _ := base64.StdEncoding.DecodeString(old.Signature)
		old.Signature = ""
		payload, _ := json.Marshal(old)
		return ed25519.Verify(pub, append([]byte(manifestDomain), payload...), signature)
	}
	if err := Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	if !legacyVerifies(m) {
		t.Fatal("legacy signature format changed")
	}
	m.Packages = map[string]Artifact{"eigenflux-darwin-arm64.zip": testArtifact([]byte("complete package"))}
	if err := Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	if legacyVerifies(m) {
		t.Fatal("complete manifest was mistaken for a legacy signature")
	}
	data, _ := json.Marshal(m)
	if _, err := Verify(data, pub); err != nil {
		t.Fatal(err)
	}
}
