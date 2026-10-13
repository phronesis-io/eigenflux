package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
)

func TestConsoleHandoffCapabilitiesRequireExplicitRecoveryEntry(t *testing.T) {
	if got := consoleHandoffCapabilities(false); !reflect.DeepEqual(got, []string{"account_recovery_v1"}) {
		t.Fatalf("ordinary handoff capabilities = %#v", got)
	}
	if got := consoleHandoffCapabilities(true); !reflect.DeepEqual(got, []string{"account_recovery_v1", "account_recovery_entry_v1"}) {
		t.Fatalf("recovery handoff capabilities = %#v", got)
	}
}

func TestCLIAccountSwitchUsesDedicatedCapability(t *testing.T) {
	if got := cliAccountSwitchCapabilities(); !reflect.DeepEqual(got, []string{"account_switch_v1"}) {
		t.Fatalf("account switch capabilities = %#v", got)
	}
}

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
		AgentName: "Agent", ExpectedAgentID: "42", Draft: []byte(`{"network_goal":"test"}`),
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
	request.FieldProvenance["network_goal"] = "agent_user_context"
	request.ExpectedAgentID = "43"
	mutated, _ = provisionV2Transcript(request)
	if ed25519.Verify(publicKey, mutated, signature) {
		t.Fatal("CLI provision proof did not cover expected_agent_id")
	}
	request.ExpectedAgentID = "42"
	request.Ref = "EF-Bili1234"
	mutated, _ = provisionV2Transcript(request)
	if ed25519.Verify(publicKey, mutated, signature) {
		t.Fatal("CLI provision proof did not cover ref")
	}
}

func TestProvisionV2TranscriptWithoutRefPreservesCompatibility(t *testing.T) {
	request := provisionV2Request{
		BootstrapGrant: "grant", IdempotencyKey: "request", Nonce: "nonce", PublicKey: "key",
		IssuedAt: 123, AgentName: "Agent", ExpectedAgentID: "42", Draft: []byte(`{"network_goal":"test"}`),
		FieldProvenance: map[string]string{"network_goal": "agent_user_context"},
	}
	legacyProof := `{"bootstrap_grant":"grant","idempotency_key":"request","nonce":"nonce","public_key":"key","issued_at":123,"agent_name":"Agent","expected_agent_id":"42","onboarding_draft":{"network_goal":"test"},"field_provenance":{"network_goal":"agent_user_context"}}`
	expected := fmt.Sprintf("EF-AUTH-V2\x00POST\n/api/v2/agent-identities/provision\n%x", sha256.Sum256([]byte(legacyProof)))
	actual, err := provisionV2Transcript(request)
	if err != nil || string(actual) != expected {
		t.Fatalf("empty ref changed the existing signature transcript: %q, %v", actual, err)
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

type legacyUpgradePoster struct {
	captureV2Poster
}

func (poster *legacyUpgradePoster) Post(path string, body interface{}) (*client.APIResponse, error) {
	poster.path = path
	poster.body, _ = body.(map[string]interface{})
	return &client.APIResponse{Data: json.RawMessage(`{"bootstrap_grant":"efbg_upgrade","nonce":"efn_upgrade","agent_id":"42","identity_preserved":true}`)}, nil
}

func TestLegacyUpgradeChallengeRequiresSubjectBoundIdentity(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	poster := &legacyUpgradePoster{}
	grant, nonce, agentID, err := requestLegacyAgentUpgradeChallenge(poster, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if grant != "efbg_upgrade" || nonce != "efn_upgrade" || agentID != "42" {
		t.Fatalf("unexpected legacy upgrade challenge grant=%q nonce=%q agent=%q", grant, nonce, agentID)
	}
	if poster.path != "/console/agent-upgrade-challenges" {
		t.Fatalf("legacy upgrade path=%q", poster.path)
	}
	if poster.body["public_key"] != base64.RawURLEncoding.EncodeToString(publicKey) {
		t.Fatal("legacy upgrade challenge did not bind the stable public key")
	}
}

func TestProvisionCommandExposesFailClosedExistingAgentMode(t *testing.T) {
	flag := agentV2ProvisionCmd.Flags().Lookup("require-existing-agent")
	if flag == nil || flag.DefValue != "false" {
		t.Fatal("agent provision must expose an opt-in fail-closed existing-Agent mode")
	}
}

func TestStaleLegacyCredentialsRequireExplicitAccountRecovery(t *testing.T) {
	tests := []struct {
		name        string
		credentials *auth.Credentials
	}{
		{name: "expired", credentials: &auth.Credentials{AccessToken: "at_expired", AgentID: "42", ExpiresAt: 1}},
		{name: "incomplete", credentials: &auth.Credentials{AgentID: "42"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usable, err := usableLegacyCredentialsForProvision(test.credentials, false, "staging")
			if err == nil || usable != nil || !strings.Contains(err.Error(), "eigenflux agent provision") || !strings.Contains(err.Error(), "--recover-account") {
				t.Fatalf("ordinary provision must stop with the explicit recovery command: usable=%#v err=%v", usable, err)
			}

			usable, err = usableLegacyCredentialsForProvision(test.credentials, true, "staging")
			if err != nil || usable != nil {
				t.Fatalf("explicit recovery must ignore stale legacy proof: usable=%#v err=%v", usable, err)
			}
		})
	}
}

func TestActiveLegacyCredentialsStillUseInPlaceUpgrade(t *testing.T) {
	credentials := &auth.Credentials{AccessToken: "at_active", AgentID: "42"}
	usable, err := usableLegacyCredentialsForProvision(credentials, true, "staging")
	if err != nil || usable != credentials {
		t.Fatalf("active legacy credentials must remain identity proof: usable=%#v err=%v", usable, err)
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

func TestProvisionDraftInputSources(t *testing.T) {
	payload := `{"identity_card":{"agent_name":"Atlas ' $HOME 中文","geo":"SG"},"field_provenance":{"identity_card.geo":"agent_user_context"}}`
	path := filepath.Join(t.TempDir(), "draft.json")
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	want, provenance, err := readProvisionDraft(path)
	if err != nil {
		t.Fatal(err)
	}
	newCommand := func() *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().String("draft-json", "", "")
		cmd.Flags().String("draft-file", "", "")
		cmd.Flags().String("agent-name", "", "")
		return cmd
	}
	for _, source := range []string{"literal", "file", "stdin"} {
		t.Run(source, func(t *testing.T) {
			cmd := newCommand()
			switch source {
			case "literal":
				_ = cmd.Flags().Set("draft-json", payload)
			case "file":
				_ = cmd.Flags().Set("draft-file", path)
			case "stdin":
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				old := os.Stdin
				os.Stdin = f
				defer func() { os.Stdin = old }()
				_ = cmd.Flags().Set("draft-file", "-")
			}
			got, meta, err := provisionDraftInput(cmd)
			if err != nil || string(got) != string(want) || !reflect.DeepEqual(meta, provenance) {
				t.Fatalf("input %s changed draft or provenance: %s %#v %v", source, got, meta, err)
			}
		})
	}
	cmd := newCommand()
	_ = cmd.Flags().Set("agent-name", "Custom Agent")
	got, _, err := provisionDraftInput(cmd)
	if err != nil || !strings.Contains(string(got), "Custom Agent") {
		t.Fatalf("default draft: %s %v", got, err)
	}
	_ = cmd.Flags().Set("draft-json", payload)
	_ = cmd.Flags().Set("draft-file", "")
	if _, _, err := provisionDraftInput(cmd); err == nil {
		t.Fatal("conflicting input flags accepted")
	}
}

func TestProvisionLiteralDraftValidation(t *testing.T) {
	for _, payload := range []string{"", "null", "[]", "{", `{ } { }`, strings.Repeat(" ", 64<<10) + "{}", `{"identity_card":{"geo":"SG"},"field_provenance":{"identity_card.geo":"human_input"}}`} {
		cmd := &cobra.Command{}
		cmd.Flags().String("draft-json", "", "")
		cmd.Flags().String("draft-file", "", "")
		_ = cmd.Flags().Set("draft-json", payload)
		if _, _, err := provisionDraftInput(cmd); err == nil {
			t.Fatalf("invalid literal accepted (length %d)", len(payload))
		}
	}
}

func TestProvisionLiteralDraftReachesSignedRequest(t *testing.T) {
	const payload = `{"identity_card":{"agent_name":"Atlas ' $HOME 中文","geo":"SG"},"field_provenance":{"identity_card.geo":"agent_user_context"}}`
	var received provisionV2Request
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/agent-identities/registration-challenges":
			fmt.Fprint(w, `{"code":0,"data":{"bootstrap_grant":"test-grant","nonce":"test-nonce"}}`)
		case "/api/v2/agent-identities/provision":
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"code":0,"data":{"agent_id":"agent-1","access_token":"test-new-token","refresh_token":"test-refresh","expires_at":4102444800000,"created":false}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	_, serverName := runtimeTestConfig(t, server.URL, true)
	cmd := &cobra.Command{}
	cmd.Flags().String("draft-json", "", "")
	cmd.Flags().String("draft-file", "", "")
	cmd.Flags().String("mode", "skill", "")
	cmd.Flags().String("runtime-name", "codex", "")
	cmd.Flags().Bool("no-handoff", true, "")
	_ = cmd.Flags().Set("draft-json", payload)
	if err := agentV2ProvisionCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	want, meta, err := parseProvisionDraft([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || string(received.Draft) != string(want) || !reflect.DeepEqual(received.FieldProvenance, meta) || received.ExpectedAgentID != "agent-1" {
		t.Fatalf("literal input lost draft/provenance/identity: calls=%d request=%+v", calls, received)
	}
	pub, err := base64.RawURLEncoding.DecodeString(received.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(received.Signature)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := provisionV2Transcript(received)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, transcript, sig) {
		t.Fatal("draft request signature invalid")
	}
	credentials, err := auth.LoadV2Credentials(serverName)
	if err != nil || credentials.AgentID != "agent-1" || credentials.AccessToken != "test-new-token" {
		t.Fatalf("credentials not retained: %v", err)
	}
	_ = cmd.Flags().Set("draft-json", "null")
	if err := agentV2ProvisionCmd.RunE(cmd, nil); err == nil {
		t.Fatal("invalid input accepted")
	}
	if calls != 2 {
		t.Fatal("invalid input made an external request")
	}
}
