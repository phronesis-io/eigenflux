package cmd

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
)

// v2CredentialError identifies failures in credential acquisition so callers
// stop this operation without attempting the same authentication again.
type v2CredentialError struct {
	cause error
}

var ErrV2IdentityUnavailable = errors.New("existing Agent identity requires local recovery")

func (e *v2CredentialError) Error() string { return e.cause.Error() }
func (e *v2CredentialError) Unwrap() error { return e.cause }

func isV2CredentialError(err error) bool {
	var credentialErr *v2CredentialError
	return errors.As(err, &credentialErr)
}

func v2CredentialNeedsLogin(err error) bool {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized
	}
	return isV2IdentityUnavailable(err)
}

func isV2IdentityUnavailable(err error) bool { return errors.Is(err, ErrV2IdentityUnavailable) }

func unavailableV2Identity(err error) error {
	return &v2CredentialError{cause: errors.Join(ErrV2IdentityUnavailable, err)}
}

func wrapV2CredentialError(err error) error {
	if err == nil || isV2CredentialError(err) {
		return err
	}
	return &v2CredentialError{cause: err}
}

func isExpiredV2Refresh(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized && apiErr.ErrorCode == "REFRESH_EXPIRED"
}

// recoverExpiredV2Credentials runs under the credential lock and uses only the
// selected Home's existing key and an Agent-bound recovery grant.
func recoverExpiredV2Credentials(serverName string, v2 v2Poster, previous *auth.V2Credentials, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey) (*auth.V2Credentials, error) {
	grant, nonce, err := requestExistingAgentRecoveryChallenge(v2, publicKey, previous.AgentID)
	if err != nil {
		return nil, fmt.Errorf("same-account Agent authentication failed: %w", err)
	}
	provisioned, err := requestV2Provision(v2, provisionV2Request{
		BootstrapGrant: grant, Nonce: nonce,
		IdempotencyKey: fmt.Sprintf("provision-%x", sha256.Sum256([]byte(grant))),
		PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey), IssuedAt: time.Now().UnixMilli(),
		AgentName: "EigenFlux Agent", ExpectedAgentID: previous.AgentID,
	}, privateKey)
	if err != nil {
		return nil, fmt.Errorf("same-account Agent authentication failed: %w", err)
	}
	if provisioned.AgentID != previous.AgentID || provisioned.PrincipalID == "" ||
		(previous.PrincipalID != "" && provisioned.PrincipalID != previous.PrincipalID) ||
		!provisioned.createdProvided || provisioned.Created {
		return nil, unavailableV2Identity(fmt.Errorf("same-account Agent authentication did not preserve the existing identity; credentials were not replaced"))
	}
	if provisioned.RefreshToken == "" || provisioned.ExpiresAt <= time.Now().UnixMilli() {
		return nil, fmt.Errorf("invalid same-account Agent authentication response")
	}
	recovered := &auth.V2Credentials{
		AgentID: provisioned.AgentID, PrincipalID: provisioned.PrincipalID,
		AccessToken: provisioned.AccessToken, RefreshToken: provisioned.RefreshToken,
		ExpiresAt: provisioned.ExpiresAt, Scopes: provisioned.Scopes,
	}
	if err := auth.SaveV2Credentials(serverName, recovered); err != nil {
		return nil, fmt.Errorf("save recovered Agent credentials: %w", err)
	}
	return recovered, nil
}
