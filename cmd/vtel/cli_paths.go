package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Path/service constants for the CLI subcommands (status/restart/logs/
// install/uninstall/links/config/update) - the systemd-managed deployment
// layout install.sh sets up. The actual tunnel-running path (runTunnel,
// invoked via `vtel -config <path>`) does NOT depend on these; it only
// knows whatever -config points at, exactly as before this CLI existed.
const (
	vtelRoot          = "/root/vtel"
	defaultConfigPath = vtelRoot + "/config.json"
	binaryPath        = "/usr/local/bin/vtel"
	serviceName       = "vtel"
	repoSlug          = "alaaabd90/vtel"
)

// cliConfigPath is the config file the CLI subcommands read/write.
// Overridable via VTEL_CONFIG for local testing without touching /root.
func cliConfigPath() string {
	if v := os.Getenv("VTEL_CONFIG"); v != "" {
		return v
	}
	return defaultConfigPath
}

// loadConfigForCLI reads and parses the config for CLI purposes. Unlike
// ParseConfig (used by runTunnel), this does NOT validate/exit(1) - a CLI
// tool inspecting or editing a config should be able to work with an
// incomplete one (e.g. before any links have been added yet).
func loadConfigForCLI() (Config, error) {
	data, err := os.ReadFile(cliConfigPath())
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func saveConfigForCLI(c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := cliConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
