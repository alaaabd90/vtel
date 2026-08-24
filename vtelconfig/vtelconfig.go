// Package vtelconfig holds the JSON config shape shared by every vtel
// front end: cmd/vtel (both the running tunnel process and its CLI),
// cmd/vtel-desktop, and (via the same JSON file format, not a Go import)
// the Android app. Extracted out of cmd/vtel so a second consumer
// (the desktop GUI) doesn't have to duplicate the struct definitions,
// validation, or bot-token-verification/LinkSpec-building logic.
package vtelconfig

import (
	"fmt"

	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/telegram"
	"github.com/alaaabd90/vtel/tunnel"
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

	// Secret derives the AEAD key used to encrypt every batch - one shared
	// secret across the pool.
	Secret string `json:"secret"`

	// CompressionLevel is one of "fastest" (default), "default", "better",
	// or "best" - see protocol.ParseCompressionLevel.
	CompressionLevel string `json:"compression_level"`

	// RejectIPv6, client mode only: immediately reject IPv6 literal SOCKS
	// targets instead of attempting them through the tunnel. Default false.
	RejectIPv6 bool `json:"reject_ipv6"`

	// QuietHours, if set, widens the adaptive idle timeout during a daily
	// window instead of pausing sends - see protocol.QuietHoursConfig.
	QuietHours *protocol.QuietHoursConfig `json:"quiet_hours"`

	// Debug enables verbose [debug]-prefixed logging across every vtel
	// package (see vtellog) - per-frame mux tracing, pool pick/health
	// decisions, batch flush stats, DNS cache hits/misses. Off by default;
	// can also be forced on via the VTEL_DEBUG=1 env var regardless of this
	// field.
	Debug bool `json:"debug,omitempty"`

	Links []LinkConfig `json:"links"`
}

// Validate checks required fields and value ranges, filling in the client
// listen-address default. Does not touch the network (no bot-token
// verification) - see BuildLinkSpecs for that.
func Validate(c *Config) error {
	if c.Mode != "client" && c.Mode != "server" {
		return fmt.Errorf("mode must be 'client' or 'server'")
	}
	if len(c.Links) == 0 {
		return fmt.Errorf("at least one link is required")
	}
	if c.Secret == "" {
		return fmt.Errorf("secret is required")
	}
	if _, err := protocol.ParseCompressionLevel(c.CompressionLevel); err != nil {
		return err
	}
	if c.QuietHours != nil {
		if c.QuietHours.StartHour < 0 || c.QuietHours.StartHour > 23 || c.QuietHours.EndHour < 0 || c.QuietHours.EndHour > 23 {
			return fmt.Errorf("quiet_hours.start_hour/end_hour must be 0-23")
		}
	}
	if c.Mode == "client" && c.Listen == "" {
		c.Listen = "127.0.0.1:1080"
	}
	for i, l := range c.Links {
		if l.Token == "" {
			return fmt.Errorf("links[%d].token is required", i)
		}
		if l.PeerBotID == 0 {
			return fmt.Errorf("links[%d].peer_bot_id is required", i)
		}
		if l.ChannelID == 0 {
			return fmt.Errorf("links[%d].channel_id is required", i)
		}
	}
	return nil
}

// BuildLinkSpecs derives the AEAD key and compression level, verifies every
// bot token via a live GetMe() call, and returns the tunnel.LinkSpecs ready
// for tunnel.NewClient/NewServer. progress, if non-nil, is called once per
// link as it's verified (e.g. for a GUI to show "verifying link 2/5...").
func BuildLinkSpecs(c *Config, progress func(index int, botID int64, err error)) ([]tunnel.LinkSpec, error) {
	key, err := protocol.DeriveKey(c.Secret)
	if err != nil {
		return nil, fmt.Errorf("invalid secret: %w", err)
	}
	level, err := protocol.ParseCompressionLevel(c.CompressionLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid compression_level: %w", err)
	}

	specs := make([]tunnel.LinkSpec, 0, len(c.Links))
	for i, lc := range c.Links {
		api := telegram.NewAPI(lc.Token)
		me, err := api.GetMe()
		if progress != nil {
			var id int64
			if me != nil {
				id = me.ID
			}
			progress(i, id, err)
		}
		if err != nil {
			return nil, fmt.Errorf("link %d: verify bot token: %w", i, err)
		}
		specs = append(specs, tunnel.LinkSpec{
			ID:               i,
			API:              api,
			BotID:            me.ID,
			PeerBotID:        lc.PeerBotID,
			ChannelID:        lc.ChannelID,
			Key:              key,
			CompressionLevel: level,
			QuietHours:       c.QuietHours,
		})
	}
	return specs, nil
}
