package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestProvisionV2TranscriptCoversMutableFields(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	request := provisionV2Request{
		BootstrapGrant: "efbg_test", IdempotencyKey: "provision-test-request", Nonce: "efn_test",
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

func TestDefaultProvisionDraftKeepsAutomaticActionsDisabled(t *testing.T) {
	var draft struct {
		SecurityBoundary struct {
			RecurringPublish bool `json:"recurring_publish"`
			AutoReplyPM      bool `json:"auto_reply_pm"`
			AutoComment      bool `json:"auto_comment"`
		} `json:"security_boundary"`
	}
	if err := json.Unmarshal(defaultProvisionDraft("Test Agent"), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.SecurityBoundary.RecurringPublish || draft.SecurityBoundary.AutoReplyPM || draft.SecurityBoundary.AutoComment {
		t.Fatalf("automatic actions must default to disabled: %#v", draft.SecurityBoundary)
	}
}
