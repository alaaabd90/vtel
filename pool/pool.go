// Package pool provides a health-aware load balancer over a set of Telegram
// bot/channel "links", modeled on gdrive's cmd/gdrive-exit/lb.go. It only
// tracks health and load; the actual transport objects (API, Sender, Poller,
// Mux, Batcher) are owned by the caller and looked up by Link.ID, keeping
// this package decoupled from the tunnel/protocol/telegram packages.
package pool

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/alaaabd90/vtel/vtellog"
)

const (
	// UnhealthyThreshold is the number of consecutive failures/stalls before
	// a link is marked unhealthy.
	UnhealthyThreshold = 3
	// UnhealthyCooldown is how long a link stays excluded from selection
	// after crossing UnhealthyThreshold.
	UnhealthyCooldown = 10 * time.Second
	// WarmupInterval is how often RunWarmup re-probes each link after its
	// initial immediate call, matching gdrive's ConnectionWarmerStore cadence.
	WarmupInterval = 90 * time.Second
)

// Link tracks health and load for one bot/channel pair.
type Link struct {
	ID                          int
	BotID, PeerBotID, ChannelID int64

	// WarmFn, if set, is called periodically by Pool.RunWarmup to keep this
	// link's underlying connection warm and probe reachability. Optional -
	// a nil WarmFn is simply skipped.
	WarmFn func() error

	// ThroughputBytesPerSec, if set, reports this link's current measured
	// throughput (bytes/sec) - consulted by pickFrom as a tiebreak when
	// multiple links are tied on ActiveStreams. Optional; nil means "no
	// throughput signal", falling back to pure least-connections for ties
	// involving this link.
	//
	// Ported from gdrive's historical bytesScore load-balancer tiebreak
	// (cmd/gdrive-exit/lb.go, v1.0.91): pure connection-count selection
	// degenerates to round-robin once load fans out evenly across
	// upstreams, since every upstream ends up with the same connection
	// count even when their actual throughput differs. gdrive later
	// reverted this - not because the tiebreak itself was wrong (it fixed
	// a real, tested fan-out bug), but because it got bundled with much
	// heavier adaptive bandwidth-cap/burst-controller machinery that
	// caused real regressions. This port is deliberately narrow: it's a
	// tiebreak among links already tied on ActiveStreams, nothing more -
	// no caps, no controllers.
	ThroughputBytesPerSec func() int64

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
	if l.consecutiveFails.Load() > 0 || l.unhealthyUntilNS.Load() > 0 {
		vtellog.Debugf("[pool] link %d recovered", l.ID)
	}
	l.consecutiveFails.Store(0)
	l.unhealthyUntilNS.Store(0)
}

func (l *Link) recordBad() {
	n := l.consecutiveFails.Add(1)
	vtellog.Debugf("[pool] link %d failure #%d", l.ID, n)
	if n >= UnhealthyThreshold {
		l.unhealthyUntilNS.Store(time.Now().Add(UnhealthyCooldown).UnixNano())
		vtellog.Debugf("[pool] link %d marked UNHEALTHY for %v", l.ID, UnhealthyCooldown)
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
		vtellog.Debugf("[pool] picked link %d (healthy, active=%d)", best.ID, best.ActiveStreams())
		return best
	}
	best := p.pickFrom(exclude, false)
	if best != nil {
		vtellog.Debugf("[pool] picked link %d (degraded: no healthy links available)", best.ID)
	} else {
		vtellog.Debugf("[pool] no link available (all excluded)")
	}
	return best
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
		if best == nil || betterCandidate(l, best) {
			best = l
		}
	}
	return best
}

// betterCandidate reports whether l should be preferred over cur: primarily
// by ActiveStreams (least-connections), falling back to
// ThroughputBytesPerSec as a tiebreak when both are tied - see
// Link.ThroughputBytesPerSec's doc comment for why.
func betterCandidate(l, cur *Link) bool {
	lActive, curActive := l.ActiveStreams(), cur.ActiveStreams()
	if lActive != curActive {
		return lActive < curActive
	}
	if l.ThroughputBytesPerSec == nil || cur.ThroughputBytesPerSec == nil {
		return false // no throughput signal on at least one side; keep the earlier (stable) pick
	}
	return l.ThroughputBytesPerSec() < cur.ThroughputBytesPerSec()
}

// RunWarmup starts one background goroutine per link that has a WarmFn set:
// an immediate call, then a call every WarmupInterval, ported from gdrive's
// ConnectionWarmerStore pattern - important once a pool of N links (Stage 1)
// all share telegram.sharedTransport, so an idle link's connection doesn't
// silently drop from the pool between bursts of real traffic. A warmup
// failure/success also feeds the link's health tracking (RecordFailure/
// RecordSuccess), so a genuinely unreachable bot gets excluded from
// selection even before any real stream is attempted on it. Goroutines stop
// when ctx is done.
func (p *Pool) RunWarmup(ctx context.Context) {
	for _, l := range p.links {
		if l.WarmFn == nil {
			continue
		}
		go func(l *Link) {
			l.warmOnce()
			ticker := time.NewTicker(WarmupInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					l.warmOnce()
				}
			}
		}(l)
	}
}

func (l *Link) warmOnce() {
	if err := l.WarmFn(); err != nil {
		l.RecordFailure()
	} else {
		l.RecordSuccess()
	}
}
