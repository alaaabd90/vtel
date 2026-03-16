package main

import (
	"flag"
	"fmt"
	"os"
)

type Config struct {
	Mode      string // "client" or "server"
	Token     string // This bot's token
	PeerBotID int64  // The other bot's user ID
	ChannelID int64  // Private channel chat ID
	ListenAddr string // SOCKS5 listen address (client mode)
}

func ParseConfig() Config {
	var c Config
	flag.StringVar(&c.Mode, "mode", "", "Run mode: client or server")
	flag.StringVar(&c.Token, "token", "", "Telegram bot token")
	flag.Int64Var(&c.PeerBotID, "peer-bot-id", 0, "Peer bot's user ID")
	flag.Int64Var(&c.ChannelID, "channel-id", 0, "Private channel chat ID")
	flag.StringVar(&c.ListenAddr, "listen", "127.0.0.1:1080", "SOCKS5 listen address (client mode)")
	flag.Parse()

	if c.Mode != "client" && c.Mode != "server" {
		fmt.Fprintln(os.Stderr, "Error: -mode must be 'client' or 'server'")
		flag.Usage()
		os.Exit(1)
	}
	if c.Token == "" {
		fmt.Fprintln(os.Stderr, "Error: -token is required")
		os.Exit(1)
	}
	if c.PeerBotID == 0 {
		fmt.Fprintln(os.Stderr, "Error: -peer-bot-id is required")
		os.Exit(1)
	}
	if c.ChannelID == 0 {
		fmt.Fprintln(os.Stderr, "Error: -channel-id is required")
		os.Exit(1)
	}
	return c
}
