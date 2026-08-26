package cmd

import (
	"strings"
	"sync"

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
	return newClientForOrigin(serverName, requireAuth, false)
}

func newCommissionClient() *client.Client {
	return newClientForOrigin(serverFlag, true, true)
}

func newClientForOrigin(serverName string, requireAuth, commission bool) *client.Client {
	cfg, err := config.Load()
	if err != nil {
		output.Die(output.ExitUsageError, "load config: %v", err)
	}
	srv, err := cfg.GetActive(serverName)
	if err != nil {
		output.Die(output.ExitUsageError, "%v", err)
	}
	token := ""
	usingV2 := false
	if requireAuth {
		if commission {
			if credentials, credentialErr := auth.LoadV2Credentials(srv.Name); credentialErr == nil && supportsCommissionV2(credentials.Scopes) {
				if refreshed, refreshErr := ensureV2Credentials(srv.Name, srv.Endpoint); refreshErr == nil {
					credentials = refreshed
				}
				token = credentials.AccessToken
				usingV2 = true
			}
		}
		if token == "" {
			creds, credentialErr := auth.LoadCredentials(srv.Name)
			if credentialErr != nil {
				output.Die(output.ExitAuthRequired, "no usable Agent identity for server %q — run 'eigenflux agent provision' or use the explicit legacy login route", srv.Name)
			}
			if creds.IsExpired() {
				output.Die(output.ExitAuthRequired, "legacy token expired for server %q — refresh the Agent identity or use the explicit legacy login route", srv.Name)
			}
			token = creds.AccessToken
		}
	}
	baseURL := strings.TrimRight(srv.Endpoint, "/")
	if commission {
		baseURL, err = srv.CommissionBaseURL()
		if err != nil {
			output.Die(output.ExitUsageError, "%v; set it with 'eigenflux server update --name %s --commission-endpoint <url>'", err, srv.Name)
		}
	}
	baseURL += "/api/v1"
	c := client.New(baseURL, token, version, clientMeta)
	if requireAuth && !usingV2 {
		serverName := srv.Name
		c.OnSuccess = sync.OnceFunc(func() {
			auth.RefreshExpiry(serverName)
		})
	}
	return c
}

func supportsCommissionV2(scopes []string) bool {
	for _, scope := range scopes {
		if strings.HasPrefix(strings.TrimSpace(scope), "commission:") {
			return true
		}
	}
	return false
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
	if credentials, err := auth.LoadV2Credentials(srv); err == nil && credentials.AgentID != "" {
		return srv + "\x00" + credentials.AgentID
	}
	if creds, err := auth.LoadCredentials(srv); err == nil && creds.AgentID != "" {
		return srv + "\x00" + creds.AgentID
	}
	return srv
}

func resolveFormat() string {
	return output.ResolveFormat(formatFlag)
}
