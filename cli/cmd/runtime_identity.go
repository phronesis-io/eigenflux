package cmd

import (
	"fmt"
	"strings"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

const runtimeHostKey = "runtime_host"
const runtimeModeKey = "runtime_mode"

func runtimeProduct(host string) string {
	return strings.SplitN(host, "/", 2)[0]
}

func normalizedRuntimeHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "terminal" {
		return "", nil
	}
	parts := strings.SplitN(host, "/", 2)
	version := ""
	if len(parts) == 2 {
		version = parts[1]
		if version == "" {
			return "", fmt.Errorf("runtime host version must not be empty after '/'")
		}
	}
	return reportedRuntimeHost(parts[0], version)
}

// Resolve per server. Process evidence takes precedence; an unrecognized shell
// inherits the installation identity established during setup. Mode never comes
// from the host name, channel, model, MCP availability, or a server response.
func resolveRuntimeMeta(saved profilestate.RuntimeState, meta client.Meta) (client.Meta, error) {
	host, err := normalizedRuntimeHost(meta.Host)
	if err != nil {
		return meta, fmt.Errorf("invalid runtime host: %w", err)
	}
	mode := strings.TrimSpace(meta.Mode)
	if mode != "" && mode != "plugin" && mode != "skill" {
		return meta, fmt.Errorf("runtime mode must be plugin or skill")
	}
	savedHost, err := normalizedRuntimeHost(saved.Host)
	if err != nil {
		savedHost = ""
	}
	if host == "" {
		host = savedHost
	} else if !meta.HostExplicit && host == runtimeProduct(savedHost) {
		// A process marker may reveal the product without exposing its version.
		host = savedHost
	}
	if mode == "" && (savedHost == "" || runtimeProduct(host) == runtimeProduct(savedHost)) {
		mode = saved.Mode
		if mode != "plugin" && mode != "skill" {
			mode = ""
		}
	}
	meta.Host, meta.Mode, meta.CLIVersion = host, mode, version
	return meta, nil
}

func clientMetaForServer(server *config.Server) client.Meta {
	saved, loadErr := profilestate.LoadRuntime(config.HomeDir(), server.Name)
	meta, err := resolveRuntimeMeta(saved, clientMeta)
	if err != nil || loadErr != nil {
		// Ordinary operations remain available with malformed optional metadata.
		meta = clientMeta
		meta.Host, meta.Mode = "", ""
	}
	return meta
}

func clientMetaForServerName(serverName string) client.Meta {
	if cfg, err := config.Load(); err == nil {
		if server, err := cfg.GetActive(serverName); err == nil {
			return clientMetaForServer(server)
		}
	}
	return client.Meta{CLIVersion: version}
}

// Explicit setup/settings values become durable installation intent even when
// the network is unavailable. Only successful HTTP reports update report stamps.
func configureRuntimeIdentity(cfg *config.Config, serverName, mode, name, runtimeVersion string) (client.Meta, error) {
	meta, _, err := configureRuntimeReportIdentity(cfg, serverName, mode, name, runtimeVersion)
	return meta, err
}

func configureRuntimeReportIdentity(cfg *config.Config, serverName, mode, name, runtimeVersion string) (client.Meta, profilestate.RuntimeState, error) {
	if mode != "" && mode != "plugin" && mode != "skill" {
		return client.Meta{}, profilestate.RuntimeState{}, fmt.Errorf("--mode must be plugin or skill")
	}
	host, err := reportedRuntimeHost(name, runtimeVersion)
	if err != nil {
		return client.Meta{}, profilestate.RuntimeState{}, err
	}
	server, err := cfg.GetActive(serverName)
	if err != nil {
		return client.Meta{}, profilestate.RuntimeState{}, err
	}
	base := clientMeta
	if host != "" {
		base.Host = host
	}
	if mode != "" {
		base.Mode = mode
	}
	var meta client.Meta
	state, err := profilestate.UpdateRuntime(config.HomeDir(), server.Name, func(saved *profilestate.RuntimeState) (bool, error) {
		var err error
		meta, err = resolveRuntimeMeta(*saved, base)
		if err != nil {
			return false, err
		}
		if host != "" {
			// An explicit bare --runtime-name intentionally clears a previous version.
			meta.Host = host
		}
		if saved.Host == meta.Host && saved.Mode == meta.Mode {
			return false, nil
		}
		saved.Host, saved.Mode = meta.Host, meta.Mode
		saved.Revision++
		return true, nil
	})
	return meta, state, err
}
