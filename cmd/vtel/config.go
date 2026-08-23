package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

// LinkConfig describes one bot/channel pair used as an independent tunnel
// link (a "lane" in the pool).
type LinkConfig struct {
	Token     string `json:"token"`
	PeerBotID int64  `json:"peer_bot_id"`
	ChannelID int64  `json:"channel_id"`
}

// Config is the JSON-driven configuration for a vtel client or server.
type Config struct {
	Mode   string `json:"mode"`   // "client" or "server"
	Listen string `json:"listen"` // SOCKS5 listen address (client mode)

	// Secret derives the AEAD key used to encrypt every batch (added here
	// for the envelope encryption layer; one shared secret across the pool).
	Secret string `json:"secret"`

	Links []LinkConfig `json:"links"`
}

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

	if c.Mode != "client" && c.Mode != "server" {
		fmt.Fprintln(os.Stderr, "Error: mode must be 'client' or 'server'")
		os.Exit(1)
	}
	if len(c.Links) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one link is required")
		os.Exit(1)
	}
	if c.Mode == "client" && c.Listen == "" {
		c.Listen = "127.0.0.1:1080"
	}
	for i, l := range c.Links {
		if l.Token == "" {
			fmt.Fprintf(os.Stderr, "Error: links[%d].token is required\n", i)
			os.Exit(1)
		}
		if l.PeerBotID == 0 {
			fmt.Fprintf(os.Stderr, "Error: links[%d].peer_bot_id is required\n", i)
			os.Exit(1)
		}
		if l.ChannelID == 0 {
			fmt.Fprintf(os.Stderr, "Error: links[%d].channel_id is required\n", i)
			os.Exit(1)
		}
	}
	return c
}
