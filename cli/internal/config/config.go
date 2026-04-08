package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Server struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

type Config struct {
	CurrentServer string            `json:"current_server"`
	Servers       map[string]Server `json:"servers"`
}

func HomeDir() string {
	if v := os.Getenv("EIGENFLUX_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eigenflux")
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
				CurrentServer: "default",
				Servers: map[string]Server{
					"default": {
						Name:     "default",
						Endpoint: "https://www.eigenflux.ai",
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

func (c *Config) AddServer(name, endpoint string) error {
	if _, exists := c.Servers[name]; exists {
		return fmt.Errorf("server %q already exists, use 'server update' to modify", name)
	}
	c.Servers[name] = Server{Name: name, Endpoint: endpoint}
	return c.Save()
}

func (c *Config) RemoveServer(name string) error {
	if name == c.CurrentServer {
		return fmt.Errorf("cannot remove the current server %q, switch to another server first", name)
	}
	if _, exists := c.Servers[name]; !exists {
		return fmt.Errorf("server %q not found", name)
	}
	delete(c.Servers, name)
	credsDir := filepath.Join(HomeDir(), "servers", name)
	os.RemoveAll(credsDir)
	return c.Save()
}

func (c *Config) SetCurrent(name string) error {
	if _, exists := c.Servers[name]; !exists {
		return fmt.Errorf("server %q not found", name)
	}
	c.CurrentServer = name
	return c.Save()
}

func (c *Config) GetActive(override string) (*Server, error) {
	name := c.CurrentServer
	if override != "" {
		name = override
	}
	srv, ok := c.Servers[name]
	if !ok {
		return nil, fmt.Errorf("server %q not found, available: %v", name, c.serverNames())
	}
	return &srv, nil
}

func (c *Config) UpdateServer(name, endpoint string) error {
	srv, ok := c.Servers[name]
	if !ok {
		return fmt.Errorf("server %q not found", name)
	}
	if endpoint != "" {
		srv.Endpoint = endpoint
	}
	c.Servers[name] = srv
	return c.Save()
}

func (c *Config) serverNames() []string {
	names := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		names = append(names, n)
	}
	return names
}
