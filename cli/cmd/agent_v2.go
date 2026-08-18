package cmd

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

func readProvisionDraft(path string) (json.RawMessage, error) {
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
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, fmt.Errorf("onboarding draft must not exceed 64KB")
	}
	var object map[string]interface{}
	if json.Unmarshal(data, &object) != nil {
		return nil, fmt.Errorf("--draft-file must contain a JSON object")
	}
	return data, nil
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
		output.PrintData(map[string]interface{}{
			"server": server.Name, "created": created,
			"key_type":        "ed25519-v1",
			"public_key":      base64.RawURLEncoding.EncodeToString(publicKey),
			"key_fingerprint": auth.IdentityFingerprint(publicKey),
		}, resolveFormat())
		return nil
	},
}

type provisionV2Request struct {
	BootstrapGrant string          `json:"bootstrap_grant"`
	IdempotencyKey string          `json:"idempotency_key"`
	Nonce          string          `json:"nonce"`
	PublicKey      string          `json:"public_key"`
	IssuedAt       int64           `json:"issued_at"`
	AgentName      string          `json:"agent_name"`
	Signature      string          `json:"signature"`
	Draft          json.RawMessage `json:"onboarding_draft,omitempty"`
}

type provisionV2Proof struct {
	BootstrapGrant string          `json:"bootstrap_grant"`
	IdempotencyKey string          `json:"idempotency_key"`
	Nonce          string          `json:"nonce"`
	PublicKey      string          `json:"public_key"`
	IssuedAt       int64           `json:"issued_at"`
	AgentName      string          `json:"agent_name"`
	Draft          json.RawMessage `json:"onboarding_draft,omitempty"`
}

func provisionV2Transcript(request provisionV2Request) ([]byte, error) {
	payload, err := json.Marshal(provisionV2Proof{
		BootstrapGrant: request.BootstrapGrant, Nonce: request.Nonce,
		IdempotencyKey: request.IdempotencyKey,
		PublicKey:      request.PublicKey, IssuedAt: request.IssuedAt,
		AgentName: request.AgentName, Draft: request.Draft,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	return []byte(fmt.Sprintf("EF-AUTH-V2\x00POST\n/api/v2/agent-identities/provision\n%x", digest)), nil
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
		agentName, _ := cmd.Flags().GetString("agent-name")
		draftFile, _ := cmd.Flags().GetString("draft-file")
		noHandoff, _ := cmd.Flags().GetBool("no-handoff")
		if grant == "" || nonce == "" {
			return fmt.Errorf("--bootstrap-grant and --nonce are required")
		}
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
		draft := defaultProvisionDraft(agentName)
		if draftFile != "" {
			draft, err = readProvisionDraft(draftFile)
			if err != nil {
				return err
			}
		}
		request := provisionV2Request{
			BootstrapGrant: grant, Nonce: nonce,
			IdempotencyKey: fmt.Sprintf("provision-%x", sha256.Sum256([]byte(grant))),
			PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey),
			IssuedAt:       time.Now().UnixMilli(), AgentName: agentName, Draft: draft,
		}
		transcript, err := provisionV2Transcript(request)
		if err != nil {
			return err
		}
		request.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
		v2 := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", "", version, clientMeta)
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
			if json.Unmarshal(handoffResponse.Data, &handoff) != nil || handoff.URL == "" {
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
	agentV2ProvisionCmd.Flags().String("bootstrap-grant", "", "short-lived grant issued by the controlled installation broker (or EIGENFLUX_BOOTSTRAP_GRANT)")
	agentV2ProvisionCmd.Flags().String("nonce", "", "single-use proof nonce issued with the bootstrap grant (or EIGENFLUX_BOOTSTRAP_NONCE)")
	agentV2ProvisionCmd.Flags().String("agent-name", "EigenFlux Agent", "Agent name used to prefill onboarding")
	agentV2ProvisionCmd.Flags().String("draft-file", "", "optional onboarding draft JSON file ('-' reads stdin)")
	agentV2ProvisionCmd.Flags().Bool("no-handoff", false, "provision without creating a Console V2 link")
	agentV2Cmd.AddCommand(agentV2InitCmd, agentV2ProvisionCmd)
	rootCmd.AddCommand(agentV2Cmd)
}
