package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/alaaabd90/vtel/vtelconfig"
)

// LinkConfig and Config are aliases onto vtelconfig's shared definitions
// (also used by cmd/vtel-desktop) - kept as local names purely so the rest
// of this package's CLI code (cli_paths.go, cmd_links.go, cmd_settings.go,
// menu.go) didn't need touching when this moved out to a shared package.
type LinkConfig = vtelconfig.LinkConfig
type Config = vtelconfig.Config

func ParseConfig() Config {
	var path string
	flag.StringVar(&path, "config", "", "Path to JSON config file")
	flag.Parse()

	if path == "" {
		fmt.Fprintln(os.Stderr, "Error: -config is required")
		flag.Usage()
		os.Exit(1)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config %s: %v\n", path, err)
		os.Exit(1)
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config %s: %v\n", path, err)
		os.Exit(1)
	}

	if err := vtelconfig.Validate(&c); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return c
}
