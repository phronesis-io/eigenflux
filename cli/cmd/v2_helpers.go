package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
)

func newBrowserNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func newV2ClientForServer(serverName string, requireAuth bool) (*client.Client, *config.Server, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	server, err := cfg.GetActive(serverName)
	if err != nil {
		return nil, nil, err
	}
	token := ""
	if requireAuth {
		credentials, credentialErr := ensureV2Credentials(server.Name, server.Endpoint)
		if credentialErr != nil {
			return nil, nil, credentialErr
		}
		token = credentials.AccessToken
	}
	return client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", token, version, clientMeta), server, nil
}

func ensureV2Credentials(serverName, endpoint string) (*auth.V2Credentials, error) {
	var credentials *auth.V2Credentials
	err := auth.WithV2CredentialsLock(serverName, 35*time.Second, func() error {
		var refreshErr error
		credentials, refreshErr = ensureV2CredentialsUnlocked(serverName, endpoint)
		return refreshErr
	})
	return credentials, err
}

func ensureV2CredentialsUnlocked(serverName, endpoint string) (*auth.V2Credentials, error) {
	credentials, err := auth.LoadV2Credentials(serverName)
	if err != nil {
		return nil, fmt.Errorf("Agent V2 is not provisioned for server %q — run 'eigenflux agent init' and then 'eigenflux agent provision': %w", serverName, err)
	}
	if credentials.ExpiresAt > time.Now().Add(30*time.Second).UnixMilli() {
		return credentials, nil
	}
	publicKey, privateKey, _, err := auth.LoadOrCreateIdentity(serverName)
	if err != nil {
		return nil, err
	}
	unauthenticated := client.New(strings.TrimRight(endpoint, "/")+"/api/v2", "", version, clientMeta)
	challengeResponse, err := unauthenticated.Post("/agent-sessions/refresh-challenges", map[string]interface{}{
		"refresh_token": credentials.RefreshToken, "rotation_request_id": refreshRotationRequestID(credentials.RefreshToken),
	})
	if err != nil {
		return nil, err
	}
	var challenge struct {
		Nonce    string `json:"nonce"`
		IssuedAt int64  `json:"issued_at"`
	}
	if json.Unmarshal(challengeResponse.Data, &challenge) != nil || challenge.Nonce == "" {
		return nil, fmt.Errorf("invalid Agent V2 refresh challenge")
	}
	request := refreshV2Request{
		RefreshToken:      credentials.RefreshToken,
		RotationRequestID: refreshRotationRequestID(credentials.RefreshToken),
		Nonce:             challenge.Nonce,
		PublicKey:         base64.RawURLEncoding.EncodeToString(publicKey),
		IssuedAt:          challenge.IssuedAt,
	}
	transcript, err := refreshV2Transcript(request)
	if err != nil {
		return nil, err
	}
	request.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
	refreshResponse, err := unauthenticated.Post("/agent-sessions/refresh", request)
	if err != nil {
		return nil, err
	}
	var refreshed struct {
		PrincipalID  string   `json:"principal_id"`
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresAt    int64    `json:"expires_at"`
		Scopes       []string `json:"scopes"`
	}
	if json.Unmarshal(refreshResponse.Data, &refreshed) != nil || refreshed.AccessToken == "" || refreshed.RefreshToken == "" {
		return nil, fmt.Errorf("invalid Agent V2 refresh response")
	}
	credentials.AccessToken = refreshed.AccessToken
	credentials.RefreshToken = refreshed.RefreshToken
	credentials.PrincipalID = refreshed.PrincipalID
	credentials.ExpiresAt = refreshed.ExpiresAt
	credentials.Scopes = refreshed.Scopes
	if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

type refreshV2Request struct {
	RefreshToken      string `json:"refresh_token"`
	RotationRequestID string `json:"rotation_request_id"`
	Nonce             string `json:"nonce"`
	PublicKey         string `json:"public_key"`
	IssuedAt          int64  `json:"issued_at"`
	Signature         string `json:"signature"`
}

type refreshV2Proof struct {
	RefreshToken      string `json:"refresh_token"`
	RotationRequestID string `json:"rotation_request_id"`
	Nonce             string `json:"nonce"`
	PublicKey         string `json:"public_key"`
	IssuedAt          int64  `json:"issued_at"`
}

func refreshV2Transcript(request refreshV2Request) ([]byte, error) {
	payload, err := json.Marshal(refreshV2Proof{
		RefreshToken: request.RefreshToken, RotationRequestID: request.RotationRequestID, Nonce: request.Nonce,
		PublicKey: request.PublicKey, IssuedAt: request.IssuedAt,
	})
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("EF-AUTH-V2-REFRESH\x00POST\n/api/v2/agent-sessions/refresh\n%s", sha256HexCLI(payload))), nil
}

func refreshRotationRequestID(refreshToken string) string {
	digest := sha256.Sum256([]byte("eigenflux-refresh-v2\x00" + refreshToken))
	return fmt.Sprintf("refresh-%x", digest)
}

func sha256HexCLI(value []byte) string {
	return fmt.Sprintf("%x", sha256Sum(value))
}

func sha256Sum(value []byte) [32]byte {
	return sha256.Sum256(value)
}
