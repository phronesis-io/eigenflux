package cmd

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
)

func newClient() *client.Client {
	return newClientOptionalAuth(true)
}

func newClientNoAuth() *client.Client {
	return newClientOptionalAuth(false)
}

func newClientOptionalAuth(requireAuth bool) *client.Client {
	return newClientForServerOptionalAuth(serverFlag, requireAuth)
}

func newClientForServer(serverName string) *client.Client {
	return newClientForServerOptionalAuth(serverName, true)
}

func newClientForServerOptionalAuth(serverName string, requireAuth bool) *client.Client {
	srv, err := loadActiveServer(serverName)
	if err != nil {
		output.Die(output.ExitUsageError, "%v", err)
	}
	if requireAuth {
		hasV2, v2Err := auth.HasV2Credentials(srv.Name)
		if v2Err != nil {
			output.Die(output.ExitAuthRequired, "inspect Agent V2 credentials for server %q: %v", srv.Name, v2Err)
		}
		if hasV2 {
			credentials, credentialErr := ensureV2Credentials(srv.Name, srv.Endpoint)
			if credentialErr != nil {
				output.Die(output.ExitAuthRequired, "Agent V2 authentication failed for server %q: %v", srv.Name, credentialErr)
			}
			result := v2ClientForResolvedServer(srv, credentials.AccessToken)
			result.OnUnauthorized = func() (string, error) {
				refreshed, refreshErr := refreshV2Credentials(srv.Name, srv.Endpoint, true)
				if refreshErr != nil {
					return "", refreshErr
				}
				return refreshed.AccessToken, nil
			}
			return result
		}
	}
	return newLegacyClientForResolvedServer(srv, requireAuth)
}

// newStoredCredentialClientForServer builds a client from the stored access
// token without exiting the process and without any credential side effect:
// it never refreshes or rotates a V2 session, never extends a legacy expiry,
// and installs no OnUnauthorized or OnSuccess hook. An expired token is an
// error. It serves best-effort side requests such as health telemetry.
func newStoredCredentialClientForServer(serverName string) (*client.Client, bool, error) {
	srv, err := loadActiveServer(serverName)
	if err != nil {
		return nil, false, err
	}
	hasV2, err := auth.HasV2Credentials(srv.Name)
	if err != nil {
		return nil, false, err
	}
	if hasV2 {
		credentials, err := auth.LoadV2Credentials(srv.Name)
		if err != nil {
			return nil, false, err
		}
		if credentials.AccessToken == "" || credentials.ExpiresAt <= time.Now().UnixMilli() {
			return nil, false, fmt.Errorf("agent v2 access token is expired")
		}
		return v2ClientForResolvedServer(srv, credentials.AccessToken), true, nil
	}
	credentials, err := auth.LoadCredentials(srv.Name)
	if err != nil {
		return nil, false, err
	}
	if credentials.IsExpired() {
		return nil, false, fmt.Errorf("access token is expired")
	}
	return legacyClientForResolvedServer(srv, credentials.AccessToken), false, nil
}

func loadActiveServer(serverName string) (*config.Server, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg.GetActive(serverName)
}

func v2ClientForResolvedServer(srv *config.Server, token string) *client.Client {
	return client.New(strings.TrimRight(srv.Endpoint, "/")+"/api/v2", token, version, clientMetaForServer(srv))
}

func legacyClientForResolvedServer(srv *config.Server, token string) *client.Client {
	return client.New(strings.TrimRight(srv.Endpoint, "/")+"/api/v1", token, version, clientMetaForServer(srv))
}

func newLegacyClientForServer(serverName string) *client.Client {
	srv, err := loadActiveServer(serverName)
	if err != nil {
		output.Die(output.ExitUsageError, "%v", err)
	}
	return newLegacyClientForResolvedServer(srv, true)
}

func newLegacyClientForResolvedServer(srv *config.Server, requireAuth bool) *client.Client {
	token := ""
	if requireAuth {
		creds, err := auth.LoadCredentials(srv.Name)
		if err != nil {
			output.Die(output.ExitAuthRequired, "not logged in to server %q — run 'eigenflux auth login --email <email>' first", srv.Name)
		}
		if creds.IsExpired() {
			output.Die(output.ExitAuthRequired, "token expired for server %q — run 'eigenflux auth login --email <email>'", srv.Name)
		}
		token = creds.AccessToken
	}
	c := legacyClientForResolvedServer(srv, token)
	if requireAuth {
		serverName := srv.Name
		c.OnSuccess = sync.OnceFunc(func() {
			auth.RefreshExpiry(serverName)
		})
	}
	return c
}

func activeServerName() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	srv, err := cfg.GetActive(serverFlag)
	if err != nil {
		return ""
	}
	return srv.Name
}

// activeAgentScope returns a stable per-agent salt used to scope locally
// generated idempotency tokens (e.g. feed event dedup_key). It prefers the
// authenticated agent_id from saved credentials and falls back to the server
// name so the token never collides across agents on different servers.
func activeAgentScope() string {
	srv := activeServerName()
	if creds, err := auth.LoadV2Credentials(srv); err == nil && creds.AgentID != "" {
		return creds.AgentID
	}
	if creds, err := auth.LoadCredentials(srv); err == nil && creds.AgentID != "" {
		return creds.AgentID
	}
	return srv
}

func resolveFormat() string {
	return output.ResolveFormat(formatFlag)
}
