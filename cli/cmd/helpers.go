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
	baseURL := strings.TrimRight(srv.Endpoint, "/")
	if commission {
		baseURL, err = srv.CommissionBaseURL()
		if err != nil {
			output.Die(output.ExitUsageError, "%v; set it with 'eigenflux server update --name %s --commission-endpoint <url>'", err, srv.Name)
		}
	}
	baseURL += "/api/v1"
	c := client.New(baseURL, token, version, clientMeta)
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
	if creds, err := auth.LoadCredentials(srv); err == nil && creds.AgentID != "" {
		return srv + "\x00" + creds.AgentID
	}
	return srv
}

func resolveFormat() string {
	return output.ResolveFormat(formatFlag)
}
