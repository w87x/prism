// Package config holds the bootstrap configuration: the few values needed
// before the database is reachable. Everything else lives in the database and
// is edited from the web UI.
package config

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	Listen         string   `json:"listen"`          // HTTP/WS listen address
	DSN            string   `json:"dsn"`             // Postgres connection string (empty until onboarding)
	DataDir        string   `json:"data_dir"`        // user-visible storage: artifacts, downloads, skills, scratch
	AllowedOrigins []string `json:"allowed_origins"` // extra WS origins (e.g. the Vite dev server)
	AuthToken      string   `json:"auth_token"`      // required when listening beyond loopback
	AllowedHosts   []string `json:"allowed_hosts"`   // extra Host names accepted (e.g. mac.local, a Tailscale name)
	// NoAuth skips the access token even when listening beyond loopback. Only for a network that is already
	// private and trusted (a VPN with just your own devices on it): PRISM can run commands on this machine.
	NoAuth bool `json:"no_auth"`

	mu   sync.Mutex
	path string
	// values as stored in the file: environment overrides are runtime-only and must not be persisted
	file struct{ Listen, DSN, DataDir string }
}

// Home returns the PRISM home directory (PRISM_HOME or ~/.prism).
func Home() string {
	if h := os.Getenv("PRISM_HOME"); h != "" {
		return h
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return ".prism"
	}
	return filepath.Join(u, ".prism")
}

// Load reads the config file, creating defaults when it does not exist.
// Environment overrides: PRISM_LISTEN, PRISM_DSN, PRISM_DATA.
func Load() (*Config, error) {
	home := Home()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	c := &Config{path: filepath.Join(home, "config.json")}
	b, err := os.ReadFile(c.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:7777"
	}
	if c.DataDir == "" {
		c.DataDir = filepath.Join(home, "data")
	}
	c.file.Listen, c.file.DSN, c.file.DataDir = c.Listen, c.DSN, c.DataDir
	if v := os.Getenv("PRISM_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("PRISM_DSN"); v != "" {
		c.DSN = v
	}
	if v := os.Getenv("PRISM_DATA"); v != "" {
		c.DataDir = v
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return nil, err
	}
	return c, nil
}

// Save persists the config atomically with owner-only permissions (it may contain a DSN password).
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := struct {
		Listen         string   `json:"listen"`
		DSN            string   `json:"dsn"`
		DataDir        string   `json:"data_dir"`
		AllowedOrigins []string `json:"allowed_origins"`
		AuthToken      string   `json:"auth_token"`
		AllowedHosts   []string `json:"allowed_hosts"`
		NoAuth         bool     `json:"no_auth,omitempty"`
	}{c.file.Listen, c.file.DSN, c.file.DataDir, c.AllowedOrigins, c.AuthToken, c.AllowedHosts, c.NoAuth}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// SetDSN updates and persists the DSN.
func (c *Config) SetDSN(dsn string) error {
	c.mu.Lock()
	c.DSN, c.file.DSN = dsn, dsn
	c.mu.Unlock()
	return c.Save()
}

// Sub returns (and creates) a subdirectory of the data dir.
func (c *Config) Sub(name string) string {
	p := filepath.Join(c.DataDir, name)
	_ = os.MkdirAll(p, 0o755)
	return p
}

// Exposed reports whether listening on host reaches beyond this machine (anything but a loopback address).
func Exposed(host string) bool {
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
