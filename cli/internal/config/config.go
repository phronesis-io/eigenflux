package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Server struct {
	Name           string `json:"name"`
	Endpoint       string `json:"endpoint"`
	StreamEndpoint string `json:"stream_endpoint,omitempty"`
}

type Config struct {
	DefaultServer string   `json:"default_server"`
	Servers       []Server `json:"servers"`
}

const homeDirName = ".eigenflux"

var homeDirOverride string

// SetHomeDir sets an explicit home directory override (from --homedir flag).
// Takes precedence over EIGENFLUX_HOME environment variable.
func SetHomeDir(dir string) {
	homeDirOverride = dir
}

func HomeDir() string {
	if homeDirOverride != "" {
		return ensureEigenfluxSuffix(homeDirOverride)
	}
	if v := os.Getenv("EIGENFLUX_HOME"); v != "" {
		return ensureEigenfluxSuffix(v)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, homeDirName)
}

// ensureEigenfluxSuffix appends .eigenflux if the path doesn't already end with it.
func ensureEigenfluxSuffix(dir string) string {
	if filepath.Base(dir) == homeDirName {
		return dir
	}
	return filepath.Join(dir, homeDirName)
}

func configPath() string {
	return filepath.Join(HomeDir(), "config.json")
}

func Load() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := &Config{
				DefaultServer: "eigenflux",
				Servers: []Server{
					{
						Name:           "eigenflux",
						Endpoint:       "https://www.eigenflux.ai",
						StreamEndpoint: "wss://stream.eigenflux.ai",
					},
				},
			}
			if err := cfg.Save(); err != nil {
				return nil, fmt.Errorf("create default config: %w", err)
			}
			return cfg, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) Save() error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func (c *Config) findServer(name string) int {
	for i, s := range c.Servers {
		if s.Name == name {
			return i
		}
	}
	return -1
}

func (c *Config) AddServer(name, endpoint string) error {
	return c.AddServerFull(name, endpoint, "")
}

func (c *Config) AddServerFull(name, endpoint, streamEndpoint string) error {
	if c.findServer(name) >= 0 {
		return fmt.Errorf("server %q already exists, use 'server update' to modify", name)
	}
	c.Servers = append(c.Servers, Server{Name: name, Endpoint: endpoint, StreamEndpoint: streamEndpoint})
	return c.Save()
}

func (c *Config) RemoveServer(name string) error {
	if name == c.DefaultServer {
		return fmt.Errorf("cannot remove the default server %q, switch to another server first", name)
	}
	i := c.findServer(name)
	if i < 0 {
		return fmt.Errorf("server %q not found", name)
	}
	c.Servers = append(c.Servers[:i], c.Servers[i+1:]...)
	credsDir := filepath.Join(HomeDir(), "servers", name)
	os.RemoveAll(credsDir)
	return c.Save()
}

func (c *Config) SetCurrent(name string) error {
	if c.findServer(name) < 0 {
		return fmt.Errorf("server %q not found", name)
	}
	c.DefaultServer = name
	return c.Save()
}

func (c *Config) GetActive(override string) (*Server, error) {
	name := c.DefaultServer
	if override != "" {
		name = override
	}
	i := c.findServer(name)
	if i < 0 {
		return nil, fmt.Errorf("server %q not found, available: %v", name, c.serverNames())
	}
	return &c.Servers[i], nil
}

func (c *Config) UpdateServer(name, endpoint, streamEndpoint string) error {
	i := c.findServer(name)
	if i < 0 {
		return fmt.Errorf("server %q not found", name)
	}
	if endpoint != "" {
		c.Servers[i].Endpoint = endpoint
	}
	if streamEndpoint != "" {
		c.Servers[i].StreamEndpoint = streamEndpoint
	}
	return c.Save()
}

// WSBaseURL returns the WebSocket base URL for this server.
// If StreamEndpoint is set, use it directly. Otherwise, derive from Endpoint
// by replacing http(s) with ws(s) and prepending "stream." to the host.
func (s *Server) WSBaseURL() string {
	if s.StreamEndpoint != "" {
		return strings.TrimRight(s.StreamEndpoint, "/")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	// For production hosts, prepend "stream.". For localhost/IP, keep as-is.
	host := u.Hostname()
	port := u.Port()
	if !strings.HasPrefix(host, "stream.") && !isLocalhost(host) {
		host = "stream." + host
	}
	if port != "" {
		u.Host = host + ":" + port
	} else {
		u.Host = host
	}
	return strings.TrimRight(u.String(), "/")
}

func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.")
}

func (c *Config) serverNames() []string {
	names := make([]string, 0, len(c.Servers))
	for _, s := range c.Servers {
		names = append(names, s.Name)
	}
	return names
}
