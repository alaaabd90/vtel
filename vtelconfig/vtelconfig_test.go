package vtelconfig

import (
	"testing"

	"github.com/alaaabd90/vtel/protocol"
)

func validConfig() Config {
	return Config{
		Mode:   "client",
		Secret: "test-secret",
		Links: []LinkConfig{
			{Token: "tok", PeerBotID: 1, ChannelID: -100},
		},
	}
}

func TestValidateAcceptsMinimalValidConfig(t *testing.T) {
	c := validConfig()
	if err := Validate(&c); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if c.Listen != "127.0.0.1:1080" {
		t.Errorf("Listen default = %q, want 127.0.0.1:1080", c.Listen)
	}
}

func TestValidateRejectsBadMode(t *testing.T) {
	c := validConfig()
	c.Mode = "bogus"
	if err := Validate(&c); err == nil {
		t.Fatal("Validate() = nil, want error for bad mode")
	}
}

func TestValidateRejectsNoLinks(t *testing.T) {
	c := validConfig()
	c.Links = nil
	if err := Validate(&c); err == nil {
		t.Fatal("Validate() = nil, want error for no links")
	}
}

func TestValidateRejectsMissingSecret(t *testing.T) {
	c := validConfig()
	c.Secret = ""
	if err := Validate(&c); err == nil {
		t.Fatal("Validate() = nil, want error for missing secret")
	}
}

func TestValidateRejectsBadCompressionLevel(t *testing.T) {
	c := validConfig()
	c.CompressionLevel = "ludicrous"
	if err := Validate(&c); err == nil {
		t.Fatal("Validate() = nil, want error for bad compression level")
	}
}

func TestValidateRejectsBadQuietHours(t *testing.T) {
	c := validConfig()
	c.QuietHours = &protocol.QuietHoursConfig{StartHour: 25, EndHour: 5}
	if err := Validate(&c); err == nil {
		t.Fatal("Validate() = nil, want error for out-of-range quiet_hours.start_hour")
	}
}

func TestValidateAcceptsGoodQuietHours(t *testing.T) {
	c := validConfig()
	c.QuietHours = &protocol.QuietHoursConfig{StartHour: 23, EndHour: 6, Timezone: "UTC"}
	if err := Validate(&c); err != nil {
		t.Fatalf("Validate() = %v, want nil for a valid quiet_hours", err)
	}
}

func TestValidateRejectsIncompleteLink(t *testing.T) {
	c := validConfig()
	c.Links[0].PeerBotID = 0
	if err := Validate(&c); err == nil {
		t.Fatal("Validate() = nil, want error for missing peer_bot_id")
	}
}

// BuildLinkSpecs always calls telegram.NewAPI (the real api.telegram.org
// host) internally - it has no seam to point at a fake server, so these
// tests only cover the paths that fail before/without a real network call:
// invalid secret up front, and the shape of a real GetMe failure (an
// obviously-fake token against the real API) including that progress is
// still invoked with the error.
func TestBuildLinkSpecsRejectsInvalidSecret(t *testing.T) {
	c := validConfig()
	c.Secret = ""
	if _, err := BuildLinkSpecs(&c, nil); err == nil {
		t.Fatal("BuildLinkSpecs() = nil error, want error for empty secret")
	}
}

func TestBuildLinkSpecsRejectsInvalidCompressionLevel(t *testing.T) {
	c := validConfig()
	c.CompressionLevel = "ludicrous"
	if _, err := BuildLinkSpecs(&c, nil); err == nil {
		t.Fatal("BuildLinkSpecs() = nil error, want error for invalid compression_level")
	}
}
