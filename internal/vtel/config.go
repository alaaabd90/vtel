package vtel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config is the on-disk config for both the client and exit binaries. Both
// roles load the SAME file: every bot in Bots must be known to both sides,
// since a lane's bot token is a shared secret between the client and exit
// process, not something one side owns exclusively (see protocol.go's frame
// Dir byte for how direction is told apart on a lane whose bot identity is
// shared by both ends).
type Config struct {
	// Bots is the fleet of bot tokens that make up the lane pool. One token
	// = one lane. More lanes = more aggregate throughput, since Telegram's
	// flood limits are tracked per bot, not per channel (see README).
	Bots []string `json:"bots"`

	// ChatID is the private channel (or supergroup) all bots in Bots are
	// admins of. Use a negative supergroup/channel ID as returned by
	// getChat, e.g. -1001234567890.
	ChatID int64 `json:"chat_id"`

	// Listen is the local SOCKS5 listen address, client role only.
	// e.g. "127.0.0.1:1080".
	Listen string `json:"listen,omitempty"`

	// Secret is the shared passphrase both sides derive the AES-256-GCM
	// envelope key from (see envelope.go's DeriveKey). Unlike gdrive's
	// Drive storage, Telegram (and anyone holding one of the Bots tokens)
	// can otherwise read uploaded document content in plaintext, so this
	// is required, not optional.
	Secret string `json:"secret"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if len(c.Bots) == 0 {
		return errors.New("config: at least one bot token is required in \"bots\"")
	}
	for i, tok := range c.Bots {
		if strings.TrimSpace(tok) == "" {
			return fmt.Errorf("config: bots[%d] is empty", i)
		}
	}
	if c.ChatID == 0 {
		return errors.New("config: \"chat_id\" is required")
	}
	if strings.TrimSpace(c.Secret) == "" {
		return errors.New("config: \"secret\" is required")
	}
	return nil
}
