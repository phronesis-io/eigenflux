package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cli.eigenflux.ai/internal/client"
)

type captureV2Poster struct {
	path string
	body map[string]interface{}
}

func (poster *captureV2Poster) Post(path string, body interface{}) (*client.APIResponse, error) {
	poster.path = path
	poster.body, _ = body.(map[string]interface{})
	return &client.APIResponse{Data: json.RawMessage(`{"bootstrap_grant":"efbg_auto","nonce":"efn_auto"}`)}, nil
}

func TestProvisionV2TranscriptCoversMutableFields(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	request := provisionV2Request{
		BootstrapGrant: "efbg_test", IdempotencyKey: "provision-test-request", Nonce: "efn_test",
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), IssuedAt: 123,
		AgentName: "Agent", Draft: []byte(`{"network_goal":"test"}`),
		FieldProvenance: map[string]string{"network_goal": "agent_user_context"},
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
	request.Nonce = "efn_test"
	request.FieldProvenance["network_goal"] = "agent_inferred"
	mutated, _ = provisionV2Transcript(request)
	if ed25519.Verify(publicKey, mutated, signature) {
		t.Fatal("CLI provision proof did not cover field provenance")
	}
}

func TestDeriveProvisionFieldProvenanceDefaultsAndPreservesExplicitSource(t *testing.T) {
	draft := map[string]interface{}{
		"identity_card": map[string]interface{}{
			"agent_name": "Atlas", "geo": "CN", "timezone": "", "working_languages": []interface{}{},
		},
		"security_boundary": map[string]interface{}{"recurring_publish": false},
	}
	got, err := deriveProvisionFieldProvenance(draft, map[string]string{
		"identity_card.geo": "agent_user_context",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["identity_card.agent_name"] != "agent_inferred" || got["identity_card.geo"] != "agent_user_context" {
		t.Fatalf("unexpected provenance: %#v", got)
	}
	if _, ok := got["identity_card.timezone"]; ok {
		t.Fatalf("empty timezone must not have provenance: %#v", got)
	}
	if got["security_boundary.recurring_publish"] != "system_generated" {
		t.Fatalf("explicit false security setting must remain attributed: %#v", got)
	}
}

func TestDeriveProvisionFieldProvenanceRejectsUnknownSources(t *testing.T) {
	draft := map[string]interface{}{"identity_card": map[string]interface{}{"geo": "CN"}}
	if _, err := deriveProvisionFieldProvenance(draft, map[string]string{"identity_card.geo": "human_input"}); err == nil {
		t.Fatal("Agent must not claim a human source")
	}
	if _, err := deriveProvisionFieldProvenance(draft, map[string]string{"identity_card.unknown": "agent_inferred"}); err == nil {
		t.Fatal("unknown field path must be rejected")
	}
}

func TestReadProvisionDraftSeparatesProvenanceMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.json")
	if err := os.WriteFile(path, []byte(`{
		"identity_card":{"agent_name":"Atlas","geo":"CN"},
		"field_provenance":{"identity_card.geo":"agent_user_context"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	draft, provenance, err := readProvisionDraft(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]interface{}
	if err := json.Unmarshal(draft, &object); err != nil {
		t.Fatal(err)
	}
	if _, exists := object["field_provenance"]; exists {
		t.Fatalf("provenance metadata leaked into onboarding draft: %s", draft)
	}
	if provenance["identity_card.agent_name"] != "agent_inferred" || provenance["identity_card.geo"] != "agent_user_context" {
		t.Fatalf("unexpected separated provenance: %#v", provenance)
	}
}

func TestDefaultProvisionDraftRequiresHumanConfirmationForAutonomousActions(t *testing.T) {
	var draft struct {
		SecurityBoundary struct {
			RecurringPublish bool `json:"recurring_publish"`
			AutoReplyPM      bool `json:"auto_reply_pm"`
			AutoComment      bool `json:"auto_comment"`
			ShowAddFriend    bool `json:"show_add_friend"`
		} `json:"security_boundary"`
	}
	if err := json.Unmarshal(defaultProvisionDraft("Test Agent"), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.SecurityBoundary.RecurringPublish || draft.SecurityBoundary.AutoReplyPM || draft.SecurityBoundary.AutoComment || !draft.SecurityBoundary.ShowAddFriend {
		t.Fatalf("autonomous actions must default off while the social entry remains visible: %#v", draft.SecurityBoundary)
	}
}

func TestAutomaticRegistrationChallengeBindsRequestToPublicKey(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	poster := &captureV2Poster{}
	grant, nonce, err := requestAutomaticRegistrationChallenge(poster, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if grant != "efbg_auto" || nonce != "efn_auto" {
		t.Fatalf("unexpected challenge grant=%q nonce=%q", grant, nonce)
	}
	if poster.path != "/agent-identities/registration-challenges" {
		t.Fatalf("automatic registration path=%q", poster.path)
	}
	if poster.body["public_key"] != base64.RawURLEncoding.EncodeToString(publicKey) {
		t.Fatal("automatic registration did not bind the canonical public key")
	}
	requestID, _ := poster.body["idempotency_key"].(string)
	if len(requestID) < 16 {
		t.Fatalf("automatic registration idempotency key is too short: %q", requestID)
	}
}

func TestValidateConsoleHandoffURLRequiresCompleteOneTimeLink(t *testing.T) {
	valid := []string{
		"https://www.eigenflux.ai/dashboard/handoff?ticket=efht_test#nonce=nonce_test",
		"http://127.0.0.1:4173/dashboard/handoff?ticket=efht_test#nonce=nonce_test",
	}
	for _, rawURL := range valid {
		if err := validateConsoleHandoffURL(rawURL); err != nil {
			t.Errorf("expected complete handoff URL to pass: %v", err)
		}
	}

	invalid := []string{
		"http://127.0.0.1:4173/dashboard/handoff",
		"http://127.0.0.1:4173/dashboard/handoff?ticket=efht_test",
		"http://127.0.0.1:4173/dashboard/handoff#nonce=nonce_test",
		"/dashboard/handoff?ticket=efht_test#nonce=nonce_test",
		"http://127.0.0.1:4173/dashboard/claim?ticket=efht_test#nonce=nonce_test",
	}
	for _, rawURL := range invalid {
		if err := validateConsoleHandoffURL(rawURL); err == nil {
			t.Errorf("expected incomplete handoff URL to fail: %q", rawURL)
		}
	}
}
