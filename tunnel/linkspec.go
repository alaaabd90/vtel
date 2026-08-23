package tunnel

import (
	"github.com/alaaabd90/vtel/pool"
	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/telegram"
)

// LinkSpec describes one bot/channel pair to build a runtime link from.
// ID must be unique within a Client/Server's set of links.
type LinkSpec struct {
	ID               int
	API              *telegram.API
	BotID            int64
	PeerBotID        int64
	ChannelID        int64
	Key              []byte                    // AES-256-GCM key from protocol.DeriveKey, shared across the pool
	CompressionLevel protocol.CompressionLevel // from protocol.ParseCompressionLevel, shared across the pool
}

// linkRuntime bundles the transport objects for one link. It is looked up by
// pool.Link.ID; the pool package itself only sees the embedded *pool.Link for
// health/load tracking, keeping pool decoupled from tunnel/protocol/telegram.
type linkRuntime struct {
	link    *pool.Link
	api     *telegram.API
	sender  *telegram.Sender
	poller  *telegram.Poller
	mux     *Mux
	batcher *protocol.Batcher
	key     []byte
}

func newLinkRuntime(spec LinkSpec) *linkRuntime {
	sender := telegram.NewSender(spec.API, spec.BotID, spec.ChannelID)
	lr := &linkRuntime{
		link: &pool.Link{
			ID:        spec.ID,
			BotID:     spec.BotID,
			PeerBotID: spec.PeerBotID,
			ChannelID: spec.ChannelID,
		},
		api:    spec.API,
		sender: sender,
		poller: telegram.NewPoller(spec.API, spec.PeerBotID, spec.ChannelID),
		key:    spec.Key,
	}
	lr.batcher = protocol.NewBatcher(sender.Send, spec.Key, spec.CompressionLevel)
	lr.mux = NewMux(func(f *protocol.Frame, urgent bool) {
		lr.batcher.Add(f, urgent)
	})
	return lr
}

func buildPool(specs []LinkSpec, build func(LinkSpec) *linkRuntime) (map[int]*linkRuntime, *pool.Pool) {
	links := make(map[int]*linkRuntime, len(specs))
	poolLinks := make([]*pool.Link, 0, len(specs))
	for _, spec := range specs {
		lr := build(spec)
		links[spec.ID] = lr
		poolLinks = append(poolLinks, lr.link)
	}
	return links, pool.NewPool(poolLinks)
}
