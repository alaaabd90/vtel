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

// LinkConfig describes one bot/channel pair, or one real-account/channel
// pair, used as an independent tunnel link (a "lane" in the pool).
type LinkConfig struct {
	// Kind selects the transport: "bot" (default, Bot API via Token/
	// PeerBotID) or "account" (MTProto via a real logged-in user account,
	// Session/PeerUserID). Defaults to "bot" when empty so existing configs
	// keep working unchanged.
	Kind string `json:"kind,omitempty"`

	// Bot-kind fields.
	Token     string `json:"token,omitempty"`
	PeerBotID int64  `json:"peer_bot_id,omitempty"`

	// Account-kind fields. Session is the path to a session file written by
	// `vtel account login`; PeerUserID is the peer account's real Telegram
	// user ID (the account-kind counterpart to PeerBotID, printed by
	// `vtel account login` on the peer side).
	Session    string `json:"session,omitempty"`
	PeerUserID int64  `json:"peer_user_id,omitempty"`

	ChannelID int64 `json:"channel_id"`
}

// IsAccount reports whether this link uses the MTProto/real-account
// transport rather than the Bot API.
func (l LinkConfig) IsAccount() bool {
	return l.Kind == "account"
}

// Config is the JSON-driven configuration for a vtel client or server.
type Config struct {
	Mode   string `json:"mode"`   // "client" or "server"
	Listen string `json:"listen"` // SOCKS5 listen address (client mode)

	// Secret derives the AEAD key used to encrypt every batch - one shared
	// secret across the pool.
	Secret string `json:"secret"`

	// TelegramAPIID/TelegramAPIHash are the MTProto application credentials
	// from https://my.telegram.org, required only if any link is
	// kind:"account". One pair is shared by every account link in the
	// process - unlike bot tokens, these identify the *application*, not
	// an individual account.
	TelegramAPIID   int    `json:"telegram_api_id,omitempty"`
	TelegramAPIHash string `json:"telegram_api_hash,omitempty"`

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
	var needsAPICreds bool
	for i, l := range c.Links {
		if l.IsAccount() {
			needsAPICreds = true
			if l.Session == "" {
				return fmt.Errorf("links[%d].session is required for kind \"account\"", i)
			}
			if l.PeerUserID == 0 {
				return fmt.Errorf("links[%d].peer_user_id is required for kind \"account\"", i)
			}
		} else if l.Kind != "" && l.Kind != "bot" {
			return fmt.Errorf("links[%d].kind must be \"bot\" or \"account\", got %q", i, l.Kind)
		} else {
			if l.Token == "" {
				return fmt.Errorf("links[%d].token is required", i)
			}
			if l.PeerBotID == 0 {
				return fmt.Errorf("links[%d].peer_bot_id is required", i)
			}
		}
		if l.ChannelID == 0 {
			return fmt.Errorf("links[%d].channel_id is required", i)
		}
		// Telegram channel/supergroup chat IDs are always negative (e.g.
		// -1001234567890) - a positive value here is never a legitimate
		// channel ID, it's someone losing the leading "-" (easy to do on a
		// phone's numeric keyboard). Caught here so it fails fast with a
		// clear message instead of surfacing later as an opaque Telegram
		// "chat not found" API error from deep inside the sender.
		if l.ChannelID > 0 {
			return fmt.Errorf("links[%d].channel_id must be negative (Telegram channel/supergroup IDs always start with -100...) - got %d, did you drop the leading minus sign?", i, l.ChannelID)
		}
	}
	if needsAPICreds && (c.TelegramAPIID == 0 || c.TelegramAPIHash == "") {
		return fmt.Errorf("telegram_api_id and telegram_api_hash are required when any link is kind \"account\" (get them from https://my.telegram.org)")
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
		var (
			api       telegram.API
			ownID     int64
			peerID    int64
			verifyErr error
		)
		if lc.IsAccount() {
			acct, err := telegram.NewAccountAPI(c.TelegramAPIID, c.TelegramAPIHash, lc.Session, lc.ChannelID, lc.PeerUserID)
			if err == nil {
				var me *telegram.User
				me, err = acct.GetMe()
				if err == nil {
					ownID = me.ID
				}
			}
			api, peerID, verifyErr = acct, lc.PeerUserID, err
		} else {
			bot := telegram.NewAPI(lc.Token)
			me, err := bot.GetMe()
			if err == nil {
				ownID = me.ID
			}
			api, peerID, verifyErr = bot, lc.PeerBotID, err
		}
		if progress != nil {
			progress(i, ownID, verifyErr)
		}
		if verifyErr != nil {
			return nil, fmt.Errorf("link %d: verify %s link: %w", i, linkKindLabel(lc), verifyErr)
		}
		specs = append(specs, tunnel.LinkSpec{
			ID:               i,
			API:              api,
			BotID:            ownID,
			PeerBotID:        peerID,
			ChannelID:        lc.ChannelID,
			Key:              key,
			CompressionLevel: level,
			QuietHours:       c.QuietHours,
		})
	}
	return specs, nil
}

func linkKindLabel(lc LinkConfig) string {
	if lc.IsAccount() {
		return "account"
	}
	return "bot"
}
