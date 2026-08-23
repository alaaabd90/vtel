package tunnel

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/alaaabd90/vtel/pool"
	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/socks5"
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

	// pending CONNECT_ACKs, keyed by tunnel conn ID (shared across all
	// links' muxes; each mux only signals for conn IDs it generated).
	pendingMu sync.Mutex
	pending   map[uint32]chan struct{}
}

func NewClient(specs []LinkSpec, listenAddr string) *Client {
	c := &Client{
		listenAddr: listenAddr,
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
	for _, lr := range c.links {
		go lr.poller.Run()
		go c.recvLoop(lr)
	}

	s := &socks5.Server{
		Addr:    c.listenAddr,
		Handler: c.handleSOCKS,
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
		for _, f := range frames {
			lr.mux.HandleFrame(f)
		}
	}
}

func (c *Client) handleSOCKS(conn net.Conn, req *socks5.ConnectRequest) {
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
		lr.mux.Relay(conn, tc)
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
	for _, lr := range c.links {
		lr.batcher.Stop()
		lr.poller.Stop()
		lr.mux.Stop()
	}
}
