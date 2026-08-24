package tunnel

import (
	"fmt"
	"testing"

	"github.com/alaaabd90/vtel/faketelegram"
	"github.com/alaaabd90/vtel/protocol"
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

func TestLinkStatusesEmptyForNoLinks(t *testing.T) {
	c := NewClient(nil, "127.0.0.1:0", false)
	defer c.Stop()

	statuses := c.LinkStatuses()
	if len(statuses) != 0 {
		t.Fatalf("got %d statuses, want 0", len(statuses))
	}
}
