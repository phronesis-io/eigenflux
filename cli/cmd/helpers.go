package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
)

const skillVersion = "0.0.6"

func newClient() *client.Client {
	return newClientOptionalAuth(true)
}

func newClientNoAuth() *client.Client {
	return newClientOptionalAuth(false)
}

func newClientOptionalAuth(requireAuth bool) *client.Client {
	cfg, err := config.Load()
	if err != nil {
		output.Die(output.ExitUsageError, "load config: %v", err)
	}
	srv, err := cfg.GetActive(serverFlag)
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
	return client.New(srv.Endpoint+"/api/v1", token, skillVersion)
}

func resolveFormat() string {
	return output.ResolveFormat(formatFlag)
}
