package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// configFileName is the file under the config directory that holds saved
// networks and the current selection.
const configFileName = "soph.json"

// Network modes.
const (
	modePrivate = "private"
	modePublic  = "public"
)

// How a public network's endpoint was chosen.
const (
	discoverySignedList       = "signed-list"
	discoveryOperatorSupplied = "operator-supplied"
)

// Network is one saved connection. A name is a local label only; it says
// nothing about enclave membership or authentication.
type Network struct {
	// Endpoint is the normalized base URL of the node commands talk to.
	Endpoint string `json:"endpoint"`
	// Mode is private or public. Private networks were joined through an
	// operator-supplied node. Public networks are expected to be part of
	// the signed-discovery network; see Discovery for how the endpoint
	// was chosen.
	Mode string `json:"mode"`
	// Discovery is set for public networks: signed-list when the endpoint
	// came from a verified root list, operator-supplied when the user
	// named it. An operator-supplied endpoint is never treated as a trust
	// anchor and is not verified against the signed list.
	Discovery string `json:"discovery,omitempty"`
	// Roots holds the verified root addresses (host:port) from the signed
	// list at join time, for signed-list public networks.
	Roots []string `json:"roots,omitempty"`
	// RootsExpire is the signed list's Unix expiration, for signed-list
	// public networks. After it passes the profile must be re-joined.
	RootsExpire int64 `json:"roots_expire,omitempty"`
	// What /v1/health reported at join time.
	NodeID      string    `json:"node_id,omitempty"`
	NodeNetwork string    `json:"node_network,omitempty"`
	Enclave     string    `json:"enclave,omitempty"`
	JoinedAt    time.Time `json:"joined_at"`
}

// Config is the on-disk state.
type Config struct {
	Current  string             `json:"current"`
	Networks map[string]Network `json:"networks"`
}

func newConfig() *Config {
	return &Config{Networks: map[string]Network{}}
}

// Names returns saved network names in sorted order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Networks))
	for n := range c.Networks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolveConfigDir picks the config directory: SOPH_CONFIG_DIR, then
// $XDG_CONFIG_HOME/sopholeth, then $HOME/.config/sopholeth.
func resolveConfigDir(getenv func(string) string) (string, error) {
	if dir := getenv("SOPH_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "sopholeth"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("cannot locate a config directory: set SOPH_CONFIG_DIR")
		}
	}
	return filepath.Join(home, ".config", "sopholeth"), nil
}

// loadConfig reads the config file. A missing file yields an empty config.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newConfig(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := newConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Networks == nil {
		cfg.Networks = map[string]Network{}
	}
	return cfg, nil
}

// saveConfig writes the config atomically with owner-only permissions.
func saveConfig(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".soph-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	cleanup = false
	return nil
}

// validateNetworkName keeps names to something that is safe to print and
// pass on a command line.
func validateNetworkName(name string) error {
	if name == "" {
		return errors.New("network name must not be empty")
	}
	if len(name) > 64 {
		return errors.New("network name must be at most 64 characters")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("network name %q may only contain letters, digits, '-', '_', and '.'", name)
		}
	}
	return nil
}

// defaultNetworkName derives a label from what the user typed when they did
// not supply one: the host portion of the endpoint, sanitized.
func defaultNetworkName(endpoint string) string {
	name := endpoint
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+3:]
	}
	name = strings.TrimSuffix(name, "/")
	// Drop the port so 192.168.0.1:8080 and 192.168.0.1 collapse together.
	if i := strings.LastIndex(name, ":"); i > 0 && !strings.Contains(name[i:], "]") {
		name = name[:i]
	}
	name = strings.Trim(name, "[]")
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "node"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
