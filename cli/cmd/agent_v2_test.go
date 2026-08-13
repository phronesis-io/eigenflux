package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestProvisionV2TranscriptCoversMutableFields(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	request := provisionV2Request{
		BootstrapGrant: "efbg_test", Nonce: "efn_test",
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), IssuedAt: 123,
		AgentName: "Agent", Draft: []byte(`{"network_goal":"test"}`),
	}
	transcript, err := provisionV2Transcript(request)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, transcript)
	if !ed25519.Verify(publicKey, transcript, signature) {
		t.Fatal("valid CLI provision proof failed")
	}
	request.Nonce = "substituted"
	mutated, _ := provisionV2Transcript(request)
	if ed25519.Verify(publicKey, mutated, signature) {
		t.Fatal("CLI provision proof did not cover nonce")
	}
}
