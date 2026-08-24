package tunnel

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alaaabd90/vtel/faketelegram"
	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/socks5"
	"github.com/alaaabd90/vtel/telegram"
)

func TestLinkStatusesSortedAndHealthyOnFreshClient(t *testing.T) {
	ft := faketelegram.New(-100123)
	server := ft.Start()
	defer server.Close()

	key, err := protocol.DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	level, err := protocol.ParseCompressionLevel("")
	if err != nil {
		t.Fatalf("ParseCompressionLevel: %v", err)
	}

	var specs []LinkSpec
	for i := 0; i < 3; i++ {
		api := telegram.NewAPIWithHost(fmt.Sprintf("tok-%d", i), server.URL)
		me, err := api.GetMe()
		if err != nil {
			t.Fatalf("GetMe: %v", err)
		}
		specs = append(specs, LinkSpec{
			ID: i, API: api, BotID: me.ID, PeerBotID: 999, ChannelID: -100123,
			Key: key, CompressionLevel: level,
		})
	}

	c := NewClient(specs, "127.0.0.1:0", false)
	defer c.Stop()

	statuses := c.LinkStatuses()
	if len(statuses) != 3 {
		t.Fatalf("got %d statuses, want 3", len(statuses))
	}
	for i, s := range statuses {
		if s.ID != i {
			t.Errorf("statuses[%d].ID = %d, want %d (must be sorted by ID)", i, s.ID, i)
		}
		if !s.Healthy {
			t.Errorf("statuses[%d].Healthy = false, want true for a freshly constructed link", i)
		}
		if s.ActiveStreams != 0 {
			t.Errorf("statuses[%d].ActiveStreams = %d, want 0", i, s.ActiveStreams)
		}
	}
}

// TestTryConnectGatesAdmissionPerLink verifies maxInFlightConnectsPerLink:
// found via live device testing that a burst of simultaneous SOCKS5
// CONNECTs used to all fire their own CONNECT frame at once, racing for a
// rate-limited link's tiny send budget - most lost the race and timed out
// having wasted a send slot, while only a lucky few got through. A 3rd
// concurrent attempt on a link admitting only 2 at a time must queue
// (block waiting for a slot) rather than immediately racing too.
func TestTryConnectGatesAdmissionPerLink(t *testing.T) {
	origAttempt, origAdmit := connectAttemptTimeout, connectAdmitTimeout
	connectAttemptTimeout = 300 * time.Millisecond
	connectAdmitTimeout = 2 * time.Second
	defer func() {
		connectAttemptTimeout = origAttempt
		connectAdmitTimeout = origAdmit
	}()

	ft := faketelegram.New(-100999)
	server := ft.Start()
	defer server.Close()

	key, err := protocol.DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	level, err := protocol.ParseCompressionLevel("")
	if err != nil {
		t.Fatalf("ParseCompressionLevel: %v", err)
	}

	api := telegram.NewAPIWithHost("tok-0", server.URL)
	me, err := api.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	specs := []LinkSpec{{
		ID: 0, API: api, BotID: me.ID, PeerBotID: 999, ChannelID: -100999,
		Key: key, CompressionLevel: level,
	}}

	c := NewClient(specs, "127.0.0.1:0", false)
	defer c.Stop()
	lr := c.links[0]

	req := &socks5.ConnectRequest{AddrType: 0x03, Addr: []byte("example.invalid"), Port: 443}

	const attempts = 3 // more than maxInFlightConnectsPerLink (2)
	var wg sync.WaitGroup
	results := make([]bool, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			clientConn, serverConn := net.Pipe()
			defer serverConn.Close()
			results[i] = c.tryConnect(clientConn, req, lr)
		}()
	}

	// Give the goroutines a moment to reach steady state: with no
	// CONNECT_ACK ever sent (this test has no server side), exactly
	// maxInFlightConnectsPerLink should have acquired a slot and posted
	// their CONNECT; the rest must be blocked waiting for one to free.
	time.Sleep(100 * time.Millisecond)
	if inFlight := len(c.connectSem[0]); inFlight != maxInFlightConnectsPerLink {
		t.Fatalf("in-flight admits = %d, want %d (maxInFlightConnectsPerLink) - extra concurrent CONNECTs should queue, not race", inFlight, maxInFlightConnectsPerLink)
	}

	wg.Wait()
	for i, got := range results {
		if got {
			t.Errorf("tryConnect[%d] = true, want false (no CONNECT_ACK was ever sent in this test)", i)
		}
	}

	if inFlight := len(c.connectSem[0]); inFlight != 0 {
		t.Fatalf("in-flight admits after all attempts resolved = %d, want 0 (leaked admission slot)", inFlight)
	}
}

func TestLinkStatusesEmptyForNoLinks(t *testing.T) {
	c := NewClient(nil, "127.0.0.1:0", false)
	defer c.Stop()

	statuses := c.LinkStatuses()
	if len(statuses) != 0 {
		t.Fatalf("got %d statuses, want 0", len(statuses))
	}
}
