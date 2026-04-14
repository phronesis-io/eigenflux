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
	Name           string            `json:"name"`
	Endpoint       string            `json:"endpoint"`
	StreamEndpoint string            `json:"stream_endpoint,omitempty"`
	KV             map[string]string `json:"kv,omitempty"`
}

type Config struct {
	DefaultServer string            `json:"default_server"`
	Servers       []Server          `json:"servers"`
	KV            map[string]string `json:"kv,omitempty"`
}

const homeDirName = ".eigenflux"

var homeDirOverride string

// SetHomeDir sets an explicit home directory override (from --homedir flag).
// Takes precedence over EIGENFLUX_HOME environment variable.
func SetHomeDir(dir string) {
	homeDirOverride = dir
}

func HomeDir() string {
	dir, _ := HomeDirInfo()
	return dir
}

// HomeDirSource describes how the home directory was determined.
type HomeDirSource string

const (
	HomeDirFromFlag    HomeDirSource = "flag"
	HomeDirFromEnv     HomeDirSource = "env"
	HomeDirFromDefault HomeDirSource = "default"
)

// HomeDirInfo returns the resolved home directory and its source.
func HomeDirInfo() (string, HomeDirSource) {
	if homeDirOverride != "" {
		return ensureEigenfluxSuffix(homeDirOverride), HomeDirFromFlag
	}
	if v := os.Getenv("EIGENFLUX_HOME"); v != "" {
		return ensureEigenfluxSuffix(v), HomeDirFromEnv
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, homeDirName), HomeDirFromDefault
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
		return fmt.Errorf("server %q already exists, use 'eigenflux server update' to modify", name)
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
// by replacing http(s) with ws(s).
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
	return strings.TrimRight(u.String(), "/")
}

func (c *Config) serverNames() []string {
	names := make([]string, 0, len(c.Servers))
	for _, s := range c.Servers {
		names = append(names, s.Name)
	}
	return names
}

// ===== KV store =====
//
// `eigenflux config set/get` is a free-form string key-value store backed
// by config.json. Global entries live in Config.KV; per-server entries
// live in Servers[i].KV. Reads with --server check the server map first,
// then fall back to global. Empty value on set deletes the entry.

// GetKV returns the value stored under key in Config.KV (global), or "" if absent.
func (c *Config) GetKV(key string) string {
	return c.KV[key]
}

// SetKV stores value under key in Config.KV (global) and persists the config.
// Passing an empty value deletes the entry.
func (c *Config) SetKV(key, value string) error {
	if value == "" {
		delete(c.KV, key)
	} else {
		if c.KV == nil {
			c.KV = map[string]string{}
		}
		c.KV[key] = value
	}
	return c.Save()
}

// GetServerKV looks up key first in the named server's KV map, then falls
// back to Config.KV. The boolean reports whether the key was found.
func (c *Config) GetServerKV(serverName, key string) (string, bool, error) {
	i := c.findServer(serverName)
	if i < 0 {
		return "", false, fmt.Errorf("server %q not found", serverName)
	}
	if v, ok := c.Servers[i].KV[key]; ok {
		return v, true, nil
	}
	if v, ok := c.KV[key]; ok {
		return v, true, nil
	}
	return "", false, nil
}

// SetServerKV stores value under key in the named server's KV map and persists.
// Passing an empty value deletes the entry.
func (c *Config) SetServerKV(serverName, key, value string) error {
	i := c.findServer(serverName)
	if i < 0 {
		return fmt.Errorf("server %q not found", serverName)
	}
	if value == "" {
		delete(c.Servers[i].KV, key)
	} else {
		if c.Servers[i].KV == nil {
			c.Servers[i].KV = map[string]string{}
		}
		c.Servers[i].KV[key] = value
	}
	return c.Save()
}
