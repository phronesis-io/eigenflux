package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestReceiptSignatureBindsIndependentVersionSourceTeamAndArtifacts(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	r := receipt{Schema: 1, Version: "1.2.3", Source: strings.Repeat("a", 64), TeamID: "ABCDEFGHIJ", Validation: "codesign_stapler_gatekeeper", Artifacts: map[string]artifact{"amd64": {strings.Repeat("b", 64), 100}, "arm64": {strings.Repeat("c", 64), 200}}}
	if e := validate(r); e != nil {
		t.Fatal(e)
	}
	sig := ed25519.Sign(key, payload(r))
	r.Signature = base64.StdEncoding.EncodeToString(sig)
	if !ed25519.Verify(pub, payload(r), sig) {
		t.Fatal("roundtrip")
	}
	for _, change := range []func(*receipt){func(r *receipt) { r.Version = "1.2.4" }, func(r *receipt) { r.Source = strings.Repeat("d", 64) }, func(r *receipt) { r.TeamID = "OTHERTEAM1" }, func(r *receipt) {
		r.Artifacts = map[string]artifact{"amd64": {strings.Repeat("e", 64), 100}, "arm64": {strings.Repeat("c", 64), 200}}
	}} {
		c := r
		change(&c)
		if ed25519.Verify(pub, payload(c), sig) {
			t.Fatal("tamper accepted")
		}
	}
	r.Artifacts["extra"] = artifact{strings.Repeat("f", 64), 3}
	if validate(r) == nil {
		t.Fatal("extra architecture accepted")
	}
}
