package vtel

import "testing"

// tunnel.go is deliberately thin wiring (mux.go's own fake-transport test
// already proves stream/ordering/dedup mechanics, socksserver.go's
// smoke test already proved the SOCKS5 wire protocol) - what's left to
// test here without a live network is NewTunnel's own validation. The
// real end-to-end proof (SOCKSServer.Handler -> mux.openClientStream via
// ServeClient, and ServeExit's dial path) needs live bot tokens per the
// v1 plan's build order, since telegram.go's Bot API host is intentionally
// not overridable for testing.

func TestNewTunnelRequiresAtLeastOneBot(t *testing.T) {
	cfg := &Config{ChatID: 1, Secret: "s"}
	if _, err := NewTunnel(nil, cfg); err == nil {
		t.Fatal("NewTunnel with no bots: expected an error, got nil")
	}
}

func TestNewTunnelRequiresValidSecret(t *testing.T) {
	bots := []*bot{{token: "fake", username: "fake"}}
	cfg := &Config{ChatID: 1, Secret: ""}
	if _, err := NewTunnel(bots, cfg); err == nil {
		t.Fatal("NewTunnel with an empty secret: expected an error, got nil")
	}
}

func TestNewTunnelOK(t *testing.T) {
	bots := []*bot{{token: "fake", username: "fake"}}
	cfg := &Config{ChatID: 1, Secret: "some-secret"}
	tun, err := NewTunnel(bots, cfg)
	if err != nil {
		t.Fatalf("NewTunnel: %v", err)
	}
	if len(tun.Bots) != 1 {
		t.Fatalf("Tunnel.Bots: got %d, want 1", len(tun.Bots))
	}
	if tun.ChatID != cfg.ChatID {
		t.Fatalf("Tunnel.ChatID: got %d, want %d", tun.ChatID, cfg.ChatID)
	}
	if len(tun.key) != keyLen {
		t.Fatalf("Tunnel.key: got %d bytes, want %d", len(tun.key), keyLen)
	}
}
