// Package pool provides a health-aware load balancer over a set of Telegram
// bot/channel "links", modeled on gdrive's cmd/gdrive-exit/lb.go. It only
// tracks health and load; the actual transport objects (API, Sender, Poller,
// Mux, Batcher) are owned by the caller and looked up by Link.ID, keeping
// this package decoupled from the tunnel/protocol/telegram packages.
package pool

import (
	"sync/atomic"
	"time"
)

const (
	// UnhealthyThreshold is the number of consecutive failures/stalls before
	// a link is marked unhealthy.
	UnhealthyThreshold = 3
	// UnhealthyCooldown is how long a link stays excluded from selection
	// after crossing UnhealthyThreshold.
	UnhealthyCooldown = 10 * time.Second
)

// Link tracks health and load for one bot/channel pair.
type Link struct {
	ID                          int
	BotID, PeerBotID, ChannelID int64

	activeStreams    atomic.Int64
	consecutiveFails atomic.Int32
	unhealthyUntilNS atomic.Int64
}

// Healthy reports whether the link is not currently in its unhealthy cooldown.
func (l *Link) Healthy(now time.Time) bool {
	return now.UnixNano() >= l.unhealthyUntilNS.Load()
}

// RecordSuccess clears failure state.
func (l *Link) RecordSuccess() {
	l.consecutiveFails.Store(0)
	l.unhealthyUntilNS.Store(0)
}

func (l *Link) recordBad() {
	if l.consecutiveFails.Add(1) >= UnhealthyThreshold {
		l.unhealthyUntilNS.Store(time.Now().Add(UnhealthyCooldown).UnixNano())
	}
}

// RecordFailure counts a hard failure (e.g. CONNECT rejected).
func (l *Link) RecordFailure() { l.recordBad() }

// RecordStall counts a stall (e.g. CONNECT_ACK timeout).
func (l *Link) RecordStall() { l.recordBad() }

// AcquireStream marks one more stream as active on this link.
func (l *Link) AcquireStream() { l.activeStreams.Add(1) }

// ReleaseStream marks a stream as no longer active on this link.
func (l *Link) ReleaseStream() { l.activeStreams.Add(-1) }

// ActiveStreams returns the current active stream count.
func (l *Link) ActiveStreams() int64 { return l.activeStreams.Load() }

// Pool selects a Link for a new stream using least-connections among healthy
// links, degrading gracefully when none are healthy.
type Pool struct {
	links []*Link
}

// NewPool builds a Pool over the given links.
func NewPool(links []*Link) *Pool {
	return &Pool{links: links}
}

// Links returns all links in the pool.
func (p *Pool) Links() []*Link { return p.links }

// AnyHealthy reports whether at least one link is currently healthy.
func (p *Pool) AnyHealthy() bool {
	now := time.Now()
	for _, l := range p.links {
		if l.Healthy(now) {
			return true
		}
	}
	return false
}

// PickLeastConnExcluding returns the healthy link with the fewest active
// streams, excluding any ID present in exclude. If no healthy link remains,
// it falls back to ignoring health (still respecting exclude) so the pool
// degrades gracefully instead of hard-refusing. Returns nil only if every
// link is excluded.
func (p *Pool) PickLeastConnExcluding(exclude map[int]bool) *Link {
	if best := p.pickFrom(exclude, true); best != nil {
		return best
	}
	return p.pickFrom(exclude, false)
}

func (p *Pool) pickFrom(exclude map[int]bool, requireHealthy bool) *Link {
	now := time.Now()
	var best *Link
	for _, l := range p.links {
		if exclude[l.ID] {
			continue
		}
		if requireHealthy && !l.Healthy(now) {
			continue
		}
		if best == nil || l.ActiveStreams() < best.ActiveStreams() {
			best = l
		}
	}
	return best
}
