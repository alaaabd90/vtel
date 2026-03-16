package tunnel

import (
	"fmt"
	"net"
	"sync"

	"github.com/teltun/teltun/protocol"
	"github.com/teltun/teltun/socks5"
	"github.com/teltun/teltun/telegram"
)

// Client runs the client side: SOCKS5 server + tunnel to Telegram.
type Client struct {
	mux     *Mux
	batcher *protocol.Batcher
	poller  *telegram.Poller
	sender  *telegram.Sender
	api     *telegram.API

	listenAddr string

	// pending CONNECT_ACKs
	pendingMu sync.Mutex
	pending   map[uint32]chan struct{}
}

func NewClient(api *telegram.API, peerBotID, channelID int64, listenAddr string) *Client {
	sender := telegram.NewSender(api, channelID)

	c := &Client{
		api:        api,
		sender:     sender,
		listenAddr: listenAddr,
		pending:    make(map[uint32]chan struct{}),
	}

	c.batcher = protocol.NewBatcher(sender.Send)
	c.mux = NewMux(func(f *protocol.Frame) {
		c.batcher.Add(f)
	})
	c.poller = telegram.NewPoller(api, peerBotID, channelID)

	// Handle CONNECT_ACK: signal the waiting goroutine
	c.mux.onConnectACK = func(connID uint32) {
		c.pendingMu.Lock()
		ch, ok := c.pending[connID]
		if ok {
			close(ch)
			delete(c.pending, connID)
		}
		c.pendingMu.Unlock()
	}

	return c
}

func (c *Client) Run() error {
	// Start receiving batches from Telegram
	go c.poller.Run()
	go c.recvLoop()

	// Start SOCKS5 server
	s := &socks5.Server{
		Addr:    c.listenAddr,
		Handler: c.handleSOCKS,
	}
	fmt.Printf("[client] starting SOCKS5 on %s\n", c.listenAddr)
	return s.ListenAndServe()
}

func (c *Client) recvLoop() {
	for data := range c.poller.RecvChan() {
		frames, err := protocol.DecompressBatch(data)
		if err != nil {
			fmt.Printf("[client] decompress error: %v\n", err)
			continue
		}
		for _, f := range frames {
			c.mux.HandleFrame(f)
		}
	}
}

func (c *Client) handleSOCKS(conn net.Conn, req *socks5.ConnectRequest) {
	// Create a tunnel connection
	tc := c.mux.NewConn()

	// Register pending ACK
	ackCh := make(chan struct{})
	c.pendingMu.Lock()
	c.pending[tc.ID] = ackCh
	c.pendingMu.Unlock()

	// Send CONNECT frame
	cp := &protocol.ConnectPayload{
		AddrType: req.AddrType,
		Addr:     req.Addr,
		Port:     req.Port,
	}
	c.batcher.Add(&protocol.Frame{
		Type:    protocol.TypeConnect,
		ConnID:  tc.ID,
		Payload: cp.Marshal(),
	})

	fmt.Printf("[client] CONNECT %08x -> %s\n", tc.ID, req.String())

	// Wait for ACK
	select {
	case <-ackCh:
		// Success - send SOCKS5 success and start relay
		if err := socks5.SendSuccess(conn); err != nil {
			c.mux.SendClose(tc.ID)
			c.mux.RemoveConn(tc.ID)
			conn.Close()
			return
		}
		c.mux.Relay(conn, tc)
	case <-tc.CloseCh:
		// Connection was rejected
		socks5.SendFailure(conn)
		c.mux.RemoveConn(tc.ID)
	}
}

func (c *Client) Stop() {
	c.batcher.Stop()
	c.poller.Stop()
	c.mux.Stop()
}
