package tunnel

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/alaaabd90/vtel/pool"
	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/socks5"
	"github.com/alaaabd90/vtel/vtellog"
)

// connectAttemptTimeout is the per-link deadline for a CONNECT_ACK. Shorter
// than teltun's original single-shot 30s so a stalled/unhealthy link can be
// retried on another one within a reasonable total time.
const connectAttemptTimeout = 5 * time.Second

// maxConnectAttempts bounds how many links a single SOCKS CONNECT will try
// before giving up.
const maxConnectAttempts = 3

// Client runs the client side: SOCKS5 server + a pool of tunnel links to Telegram.
type Client struct {
	pool  *pool.Pool
	links map[int]*linkRuntime

	listenAddr string
	rejectIPv6 bool

	// pending CONNECT_ACKs, keyed by tunnel conn ID (shared across all
	// links' muxes; each mux only signals for conn IDs it generated).
	pendingMu sync.Mutex
	pending   map[uint32]chan struct{}

	warmupCancel context.CancelFunc
}

func NewClient(specs []LinkSpec, listenAddr string, rejectIPv6 bool) *Client {
	c := &Client{
		listenAddr: listenAddr,
		rejectIPv6: rejectIPv6,
		pending:    make(map[uint32]chan struct{}),
	}
	c.links, c.pool = buildPool(specs, c.newClientLink)
	return c
}

func (c *Client) newClientLink(spec LinkSpec) *linkRuntime {
	lr := newLinkRuntime(spec)
	lr.mux.onConnectACK = func(connID uint32) {
		c.pendingMu.Lock()
		ch, ok := c.pending[connID]
		if ok {
			close(ch)
			delete(c.pending, connID)
		}
		c.pendingMu.Unlock()
	}
	return lr
}

func (c *Client) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.warmupCancel = cancel
	c.pool.RunWarmup(ctx)

	for _, lr := range c.links {
		go lr.poller.Run()
		go c.recvLoop(lr)
	}

	s := &socks5.Server{
		Addr:       c.listenAddr,
		Handler:    c.handleSOCKS,
		RejectIPv6: c.rejectIPv6,
	}
	fmt.Printf("[client] starting SOCKS5 on %s (%d link(s))\n", c.listenAddr, len(c.links))
	return s.ListenAndServe()
}

func (c *Client) recvLoop(lr *linkRuntime) {
	for data := range lr.poller.RecvChan() {
		compressed, ok, err := protocol.OpenEnvelope(lr.key, data)
		if err != nil {
			fmt.Printf("[client] link %d envelope open error: %v\n", lr.link.ID, err)
			continue
		}
		if !ok {
			continue // wrong key, tampered, or not a vtel envelope - skip silently
		}
		frames, err := lr.batcher.DecompressBatch(compressed)
		if err != nil {
			fmt.Printf("[client] link %d decompress error: %v\n", lr.link.ID, err)
			continue
		}
		vtellog.Debugf("[client] link %d recv batch: %d frame(s), %d compressed bytes", lr.link.ID, len(frames), len(compressed))
		for _, f := range frames {
			lr.mux.HandleFrame(f)
		}
	}
}

func (c *Client) handleSOCKS(conn net.Conn, req *socks5.ConnectRequest) {
	vtellog.Debugf("[client] SOCKS5 CONNECT request: %s", req.String())
	excluded := make(map[int]bool)
	for attempt := 1; attempt <= maxConnectAttempts; attempt++ {
		link := c.pool.PickLeastConnExcluding(excluded)
		if link == nil {
			break
		}
		lr := c.links[link.ID]
		if c.tryConnect(conn, req, lr) {
			return
		}
		excluded[link.ID] = true
	}
	socks5.SendFailure(conn)
	conn.Close()
}

// tryConnect attempts a CONNECT over one link. It returns true once the SOCKS
// connection has been terminally handled (success-and-relayed, or a local
// failure not worth retrying on another link); false means the caller should
// retry on a different link.
func (c *Client) tryConnect(conn net.Conn, req *socks5.ConnectRequest, lr *linkRuntime) bool {
	lr.link.AcquireStream()
	tc := lr.mux.NewConn()

	ackCh := make(chan struct{})
	c.pendingMu.Lock()
	c.pending[tc.ID] = ackCh
	c.pendingMu.Unlock()

	cp := &protocol.ConnectPayload{
		AddrType: req.AddrType,
		Addr:     req.Addr,
		Port:     req.Port,
	}
	lr.batcher.Add(&protocol.Frame{
		Type:    protocol.TypeConnect,
		ConnID:  tc.ID,
		Payload: cp.Marshal(),
	}, true)

	fmt.Printf("[client] link %d CONNECT %08x -> %s\n", lr.link.ID, tc.ID, req.String())

	select {
	case <-ackCh:
		lr.link.RecordSuccess()
		if err := socks5.SendSuccess(conn); err != nil {
			lr.mux.SendClose(tc.ID)
			lr.mux.RemoveConn(tc.ID)
			lr.link.ReleaseStream()
			conn.Close()
			return true
		}
		vtellog.Debugf("[client] link %d conn %08x: relaying", lr.link.ID, tc.ID)
		lr.mux.Relay(conn, tc)
		vtellog.Debugf("[client] link %d conn %08x: relay ended", lr.link.ID, tc.ID)
		lr.link.ReleaseStream()
		return true
	case <-tc.CloseCh:
		// Connection was rejected by the server on this link; worth
		// retrying on another one.
		lr.link.RecordFailure()
		c.pendingMu.Lock()
		delete(c.pending, tc.ID)
		c.pendingMu.Unlock()
		lr.mux.RemoveConn(tc.ID)
		lr.link.ReleaseStream()
		return false
	case <-time.After(connectAttemptTimeout):
		fmt.Printf("[client] link %d CONNECT_ACK timeout for %08x\n", lr.link.ID, tc.ID)
		lr.link.RecordStall()
		c.pendingMu.Lock()
		delete(c.pending, tc.ID)
		c.pendingMu.Unlock()
		lr.mux.SendClose(tc.ID)
		lr.mux.RemoveConn(tc.ID)
		lr.link.ReleaseStream()
		return false
	}
}

func (c *Client) Stop() {
	if c.warmupCancel != nil {
		c.warmupCancel()
	}
	for _, lr := range c.links {
		lr.mux.CloseAllNotify()
		lr.batcher.Stop() // blocks until the resulting TypeClose frames are flushed
		lr.poller.Stop()
		lr.mux.Stop()
	}
}

// Retry429Counts returns, per link ID, how many times that link's Sender
// has been rate-limited since construction - used by cmd/vtel-bench's
// 429-frequency-per-bot metric.
func (c *Client) Retry429Counts() map[int]int64 {
	counts := make(map[int]int64, len(c.links))
	for id, lr := range c.links {
		counts[id] = lr.sender.Retry429Count()
	}
	return counts
}

// LinkStatus is a snapshot of one link's health/load, for UIs (the desktop/
// mobile apps) that want to show real pool state rather than a fabricated
// "connected" indicator.
type LinkStatus struct {
	ID            int
	Healthy       bool
	ActiveStreams int64
	BytesPerSec   int64
}

// LinkStatuses returns a point-in-time snapshot of every link's health/load,
// sorted by ID.
func (c *Client) LinkStatuses() []LinkStatus {
	now := time.Now()
	out := make([]LinkStatus, 0, len(c.links))
	for id, lr := range c.links {
		out = append(out, LinkStatus{
			ID:            id,
			Healthy:       lr.link.Healthy(now),
			ActiveStreams: lr.link.ActiveStreams(),
			BytesPerSec:   lr.batcher.BytesPerSec(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
