package tunnel

import (
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/teltun/teltun/protocol"
)

// Conn represents a multiplexed connection.
type Conn struct {
	ID       uint32
	DataCh   chan []byte // incoming data for this connection
	CloseCh  chan struct{}
	closed   bool
	mu       sync.Mutex
	lastUsed time.Time
}

func (c *Conn) MarkUsed() {
	c.mu.Lock()
	c.lastUsed = time.Now()
	c.mu.Unlock()
}

func (c *Conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.CloseCh)
	}
}

func (c *Conn) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Mux manages multiplexed connections over the tunnel.
type Mux struct {
	mu    sync.RWMutex
	conns map[uint32]*Conn

	// sendFrame is called to send a frame through the tunnel
	sendFrame func(*protocol.Frame)

	// onConnect is called when a CONNECT frame is received (server-side)
	onConnect func(connID uint32, payload *protocol.ConnectPayload)

	// onConnectACK is called when a CONNECT_ACK is received (client-side)
	onConnectACK func(connID uint32)

	done chan struct{}
}

func NewMux(sendFrame func(*protocol.Frame)) *Mux {
	m := &Mux{
		conns:     make(map[uint32]*Conn),
		sendFrame: sendFrame,
		done:      make(chan struct{}),
	}
	go m.gcLoop()
	return m
}

// NewConn creates and registers a new connection.
func (m *Mux) NewConn() *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()

	var id uint32
	for {
		id = rand.Uint32()
		if _, exists := m.conns[id]; !exists {
			break
		}
	}
	c := &Conn{
		ID:       id,
		DataCh:   make(chan []byte, 512),
		CloseCh:  make(chan struct{}),
		lastUsed: time.Now(),
	}
	m.conns[id] = c
	return c
}

// RegisterConn registers a connection with a specific ID (server-side).
func (m *Mux) RegisterConn(id uint32) *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := &Conn{
		ID:       id,
		DataCh:   make(chan []byte, 512),
		CloseCh:  make(chan struct{}),
		lastUsed: time.Now(),
	}
	m.conns[id] = c
	return c
}

// GetConn returns a connection by ID.
func (m *Mux) GetConn(id uint32) *Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[id]
}

// RemoveConn removes a connection.
func (m *Mux) RemoveConn(id uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conns[id]; ok {
		c.Close()
		delete(m.conns, id)
	}
}

// HandleFrame processes an incoming frame from the tunnel.
func (m *Mux) HandleFrame(f *protocol.Frame) {
	switch f.Type {
	case protocol.TypeConnect:
		if m.onConnect != nil {
			cp, err := protocol.ParseConnectPayload(f.Payload)
			if err != nil {
				fmt.Printf("[mux] bad CONNECT payload: %v\n", err)
				return
			}
			m.onConnect(f.ConnID, cp)
		}
	case protocol.TypeConnectACK:
		if m.onConnectACK != nil {
			m.onConnectACK(f.ConnID)
		}
	case protocol.TypeData:
		c := m.GetConn(f.ConnID)
		if c == nil {
			return
		}
		c.MarkUsed()
		select {
		case c.DataCh <- f.Payload:
		case <-time.After(30 * time.Second):
			fmt.Printf("[mux] conn %08x data channel stalled for 30s, closing\n", f.ConnID)
			c.Close()
		case <-c.CloseCh:
		}
	case protocol.TypeClose:
		c := m.GetConn(f.ConnID)
		if c != nil {
			c.Close()
		}
	case protocol.TypeKeepalive:
		// no-op
	}
}

// SendData sends a DATA frame for a connection.
func (m *Mux) SendData(connID uint32, data []byte) {
	m.sendFrame(&protocol.Frame{
		Type:    protocol.TypeData,
		ConnID:  connID,
		Payload: data,
	})
}

// SendClose sends a CLOSE frame.
func (m *Mux) SendClose(connID uint32) {
	m.sendFrame(&protocol.Frame{
		Type:   protocol.TypeClose,
		ConnID: connID,
	})
}

// gcLoop removes idle connections every minute.
func (m *Mux) gcLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.gc()
		case <-m.done:
			return
		}
	}
}

func (m *Mux) gc() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, c := range m.conns {
		c.mu.Lock()
		idle := now.Sub(c.lastUsed)
		c.mu.Unlock()
		if idle > 5*time.Minute {
			fmt.Printf("[mux] GC: closing idle conn %08x\n", id)
			c.Close()
			delete(m.conns, id)
		}
	}
}


func (m *Mux) Stop() {
	close(m.done)
}

// Relay copies data bidirectionally between a net.Conn and a tunnel Conn.
func (m *Mux) Relay(netConn net.Conn, tunnelConn *Conn) {
	// net -> tunnel
	go func() {
		buf := make([]byte, 128*1024)
		for {
			n, err := netConn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				m.SendData(tunnelConn.ID, data)
				tunnelConn.MarkUsed()
			}
			if err != nil {
				m.SendClose(tunnelConn.ID)
				m.RemoveConn(tunnelConn.ID)
				netConn.Close()
				return
			}
		}
	}()

	// tunnel -> net
	for {
		select {
		case data := <-tunnelConn.DataCh:
			if _, err := netConn.Write(data); err != nil {
				m.SendClose(tunnelConn.ID)
				m.RemoveConn(tunnelConn.ID)
				netConn.Close()
				return
			}
		case <-tunnelConn.CloseCh:
			netConn.Close()
			return
		}
	}
}
