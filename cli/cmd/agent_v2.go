package cmd

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var agentV2Cmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage the stable Agent V2 identity",
}

func defaultProvisionDraft(agentName string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"identity_card":{"agent_name":%q,"bio":""},"security_boundary":{"recurring_publish":false,"auto_reply_pm":false,"auto_comment":false,"show_add_friend":true},"network_goal":"","intent_actions":[]}`, agentName))
}

var provisionDraftFieldPaths = []string{
	"identity_card.agent_name", "identity_card.bio", "identity_card.agent_description",
	"identity_card.human_description", "identity_card.working_languages", "identity_card.seeking",
	"identity_card.offering", "identity_card.geo", "identity_card.timezone",
	"identity_card.agent_status", "identity_card.human_status", "identity_card.interests_negative",
	"security_boundary.recurring_publish", "security_boundary.auto_reply_pm",
	"security_boundary.auto_comment", "security_boundary.show_add_friend",
	"network_goal", "intent_actions",
}

func provisionDraftPathValue(root map[string]interface{}, path string) (interface{}, bool) {
	var current interface{} = root
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func meaningfulProvisionDraftValue(value interface{}, exists bool) bool {
	if !exists || value == nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []interface{}:
		return len(typed) > 0
	case map[string]interface{}:
		return len(typed) > 0
	default:
		return true
	}
}

func deriveProvisionFieldProvenance(draft map[string]interface{}, supplied map[string]string) (map[string]string, error) {
	known := make(map[string]struct{}, len(provisionDraftFieldPaths))
	for _, path := range provisionDraftFieldPaths {
		known[path] = struct{}{}
	}
	for path, source := range supplied {
		if _, ok := known[path]; !ok || (source != "agent_inferred" && source != "agent_user_context" && source != "system_generated") {
			return nil, fmt.Errorf("invalid field_provenance for %s", path)
		}
	}
	result := map[string]string{}
	for _, path := range provisionDraftFieldPaths {
		value, exists := provisionDraftPathValue(draft, path)
		if !meaningfulProvisionDraftValue(value, exists) {
			continue
		}
		source := supplied[path]
		if source == "" {
			source = "agent_inferred"
			if strings.HasPrefix(path, "security_boundary.") {
				source = "system_generated"
			}
		}
		result[path] = source
	}
	return result, nil
}

func readProvisionDraft(path string) (json.RawMessage, map[string]string, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, nil, err
	}
	if len(data) > 64<<10 {
		return nil, nil, fmt.Errorf("onboarding draft must not exceed 64KB")
	}
	var object map[string]interface{}
	if json.Unmarshal(data, &object) != nil {
		return nil, nil, fmt.Errorf("--draft-file must contain a JSON object")
	}
	supplied := map[string]string{}
	if raw, ok := object["field_provenance"]; ok {
		encoded, _ := json.Marshal(raw)
		if json.Unmarshal(encoded, &supplied) != nil {
			return nil, nil, fmt.Errorf("field_provenance must map field paths to Agent source values")
		}
		delete(object, "field_provenance")
	}
	provenance, err := deriveProvisionFieldProvenance(object, supplied)
	if err != nil {
		return nil, nil, err
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, nil, err
	}
	return normalized, provenance, nil
}

var agentV2InitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create or read this installation's stable Ed25519 identity",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		publicKey, _, created, err := auth.LoadOrCreateIdentity(server.Name)
		if err != nil {
			return err
		}
		homeDir, homeSource := config.HomeDirInfo()
		output.PrintData(map[string]interface{}{
			"server": server.Name, "created": created,
			"home":            homeDir,
			"home_source":     homeSource,
			"key_type":        "ed25519-v1",
			"public_key":      base64.RawURLEncoding.EncodeToString(publicKey),
			"key_fingerprint": auth.IdentityFingerprint(publicKey),
		}, resolveFormat())
		return nil
	},
}

type provisionV2Request struct {
	BootstrapGrant  string            `json:"bootstrap_grant"`
	IdempotencyKey  string            `json:"idempotency_key"`
	Nonce           string            `json:"nonce"`
	PublicKey       string            `json:"public_key"`
	IssuedAt        int64             `json:"issued_at"`
	AgentName       string            `json:"agent_name"`
	Signature       string            `json:"signature"`
	Draft           json.RawMessage   `json:"onboarding_draft,omitempty"`
	FieldProvenance map[string]string `json:"field_provenance,omitempty"`
}

type provisionV2Proof struct {
	BootstrapGrant  string            `json:"bootstrap_grant"`
	IdempotencyKey  string            `json:"idempotency_key"`
	Nonce           string            `json:"nonce"`
	PublicKey       string            `json:"public_key"`
	IssuedAt        int64             `json:"issued_at"`
	AgentName       string            `json:"agent_name"`
	Draft           json.RawMessage   `json:"onboarding_draft,omitempty"`
	FieldProvenance map[string]string `json:"field_provenance,omitempty"`
}

type v2Poster interface {
	Post(path string, body interface{}) (*client.APIResponse, error)
}

func requestAutomaticRegistrationChallenge(v2 v2Poster, publicKey ed25519.PublicKey) (string, string, error) {
	requestNonce, err := newBrowserNonce()
	if err != nil {
		return "", "", err
	}
	response, err := v2.Post("/agent-identities/registration-challenges", map[string]interface{}{
		"public_key":      base64.RawURLEncoding.EncodeToString(publicKey),
		"idempotency_key": "registration-" + requestNonce,
	})
	if err != nil {
		return "", "", err
	}
	var challenge struct {
		BootstrapGrant string `json:"bootstrap_grant"`
		Nonce          string `json:"nonce"`
	}
	if json.Unmarshal(response.Data, &challenge) != nil || challenge.BootstrapGrant == "" || challenge.Nonce == "" {
		return "", "", fmt.Errorf("invalid automatic Agent registration challenge")
	}
	return challenge.BootstrapGrant, challenge.Nonce, nil
}

type v2Poster interface {
	Post(path string, body interface{}) (*client.APIResponse, error)
}

func requestAutomaticRegistrationChallenge(v2 v2Poster, publicKey ed25519.PublicKey) (string, string, error) {
	requestNonce, err := newBrowserNonce()
	if err != nil {
		return "", "", err
	}
	response, err := v2.Post("/agent-identities/registration-challenges", map[string]interface{}{
		"public_key":      base64.RawURLEncoding.EncodeToString(publicKey),
		"idempotency_key": "registration-" + requestNonce,
	})
	if err != nil {
		return "", "", err
	}
	var challenge struct {
		BootstrapGrant string `json:"bootstrap_grant"`
		Nonce          string `json:"nonce"`
	}
	if json.Unmarshal(response.Data, &challenge) != nil || challenge.BootstrapGrant == "" || challenge.Nonce == "" {
		return "", "", fmt.Errorf("invalid automatic Agent registration challenge")
	}
	return challenge.BootstrapGrant, challenge.Nonce, nil
}

func provisionV2Transcript(request provisionV2Request) ([]byte, error) {
	payload, err := json.Marshal(provisionV2Proof{
		BootstrapGrant: request.BootstrapGrant, Nonce: request.Nonce,
		IdempotencyKey: request.IdempotencyKey,
		PublicKey:      request.PublicKey, IssuedAt: request.IssuedAt,
		AgentName: request.AgentName, Draft: request.Draft, FieldProvenance: request.FieldProvenance,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	return []byte(fmt.Sprintf("EF-AUTH-V2\x00POST\n/api/v2/agent-identities/provision\n%x", digest)), nil
}

func validateConsoleHandoffURL(rawURL string) error {
	handoffURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (handoffURL.Scheme != "http" && handoffURL.Scheme != "https") || handoffURL.Host == "" {
		return fmt.Errorf("invalid Console V2 handoff response")
	}
	if handoffURL.Path != "/dashboard/handoff" || handoffURL.Query().Get("ticket") == "" {
		return fmt.Errorf("invalid Console V2 handoff response")
	}
	fragment, err := url.ParseQuery(handoffURL.Fragment)
	if err != nil || fragment.Get("nonce") == "" {
		return fmt.Errorf("invalid Console V2 handoff response")
	}
	return nil
}

var agentV2ProvisionCmd = &cobra.Command{
	Use:   "provision",
	Short: "Provision or recover the Agent bound to this installation key",
	RunE: func(cmd *cobra.Command, _ []string) error {
		grant, _ := cmd.Flags().GetString("bootstrap-grant")
		nonce, _ := cmd.Flags().GetString("nonce")
		if grant == "" {
			grant = os.Getenv("EIGENFLUX_BOOTSTRAP_GRANT")
		}
		if nonce == "" {
			nonce = os.Getenv("EIGENFLUX_BOOTSTRAP_NONCE")
		}
		if (grant == "") != (nonce == "") {
			return fmt.Errorf("--bootstrap-grant and --nonce must be provided together")
		}
		agentName, _ := cmd.Flags().GetString("agent-name")
		draftFile, _ := cmd.Flags().GetString("draft-file")
		noHandoff, _ := cmd.Flags().GetBool("no-handoff")
		if strings.TrimSpace(agentName) == "" {
			agentName = "EigenFlux Agent"
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		publicKey, privateKey, _, err := auth.LoadOrCreateIdentity(server.Name)
		if err != nil {
			return err
		}
		v2 := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", "", version, clientMeta)
		if grant == "" {
			grant, nonce, err = requestAutomaticRegistrationChallenge(v2, publicKey)
			if err != nil {
				return err
			}
		}
		draft := defaultProvisionDraft(agentName)
		var draftObject map[string]interface{}
		_ = json.Unmarshal(draft, &draftObject)
		fieldProvenance, err := deriveProvisionFieldProvenance(draftObject, nil)
		if err != nil {
			return err
		}
		if draftFile != "" {
			draft, fieldProvenance, err = readProvisionDraft(draftFile)
			if err != nil {
				return err
			}
		}
		request := provisionV2Request{
			BootstrapGrant: grant, Nonce: nonce,
			IdempotencyKey: fmt.Sprintf("provision-%x", sha256.Sum256([]byte(grant))),
			PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey),
			IssuedAt:       time.Now().UnixMilli(), AgentName: agentName, Draft: draft,
			FieldProvenance: fieldProvenance,
		}
		transcript, err := provisionV2Transcript(request)
		if err != nil {
			return err
		}
		request.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
		response, err := v2.Post("/agent-identities/provision", request)
		if err != nil {
			return err
		}
		var provisioned struct {
			AgentID      string   `json:"agent_id"`
			PrincipalID  string   `json:"principal_id"`
			Created      bool     `json:"created"`
			AccessToken  string   `json:"access_token"`
			RefreshToken string   `json:"refresh_token"`
			ExpiresAt    int64    `json:"expires_at"`
			Scopes       []string `json:"scopes"`
			NextStep     int16    `json:"next_step"`
		}
		if json.Unmarshal(response.Data, &provisioned) != nil || provisioned.AgentID == "" || provisioned.AccessToken == "" {
			return fmt.Errorf("invalid Agent V2 provision response")
		}
		if err := auth.SaveV2Credentials(server.Name, &auth.V2Credentials{
			AccessToken: provisioned.AccessToken, RefreshToken: provisioned.RefreshToken,
			AgentID: provisioned.AgentID, PrincipalID: provisioned.PrincipalID,
			ExpiresAt: provisioned.ExpiresAt, Scopes: provisioned.Scopes,
		}); err != nil {
			return err
		}
		result := map[string]interface{}{
			"agent_id": provisioned.AgentID, "created": provisioned.Created,
			"next_step": provisioned.NextStep,
		}
		homeDir, homeSource := config.HomeDirInfo()
		result["home"] = homeDir
		result["home_source"] = homeSource
		if !noHandoff {
			authenticated := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", provisioned.AccessToken, version, clientMeta)
			browserNonce, nonceErr := newBrowserNonce()
			if nonceErr != nil {
				return nonceErr
			}
			handoffResponse, handoffErr := authenticated.Post("/console/handoffs", map[string]interface{}{"browser_nonce": browserNonce})
			if handoffErr != nil {
				return handoffErr
			}
			var handoff struct {
				URL       string `json:"handoff_url"`
				ExpiresAt int64  `json:"expires_at"`
			}
			if json.Unmarshal(handoffResponse.Data, &handoff) != nil || validateConsoleHandoffURL(handoff.URL) != nil {
				return fmt.Errorf("invalid Console V2 handoff response")
			}
			result["console_url"] = handoff.URL
			result["handoff_expires_at"] = handoff.ExpiresAt
		}
		output.PrintData(result, resolveFormat())
		return nil
	},
}

func init() {
	agentV2ProvisionCmd.Flags().String("bootstrap-grant", "", "optional short-lived controlled-channel grant (automatic registration is used when omitted)")
	agentV2ProvisionCmd.Flags().String("nonce", "", "single-use proof nonce paired with --bootstrap-grant")
	agentV2ProvisionCmd.Flags().String("agent-name", "EigenFlux Agent", "Agent name used to prefill onboarding")
	agentV2ProvisionCmd.Flags().String("draft-file", "", "optional onboarding draft JSON file ('-' reads stdin)")
	agentV2ProvisionCmd.Flags().Bool("no-handoff", false, "provision without creating a Console V2 link")
	agentV2Cmd.AddCommand(agentV2InitCmd, agentV2ProvisionCmd)
	rootCmd.AddCommand(agentV2Cmd)
}
