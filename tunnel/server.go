package tunnel

import (
	"fmt"
	"net"
	"time"

	"github.com/alaaabd90/vtel/protocol"
)

// Server runs the server side: receives tunnel frames from a pool of links,
// makes outbound TCP connections.
type Server struct {
	links map[int]*linkRuntime
}

func NewServer(specs []LinkSpec) *Server {
	s := &Server{}
	s.links, _ = buildPool(specs, s.newServerLink)
	return s
}

func (s *Server) newServerLink(spec LinkSpec) *linkRuntime {
	lr := newLinkRuntime(spec)
	lr.mux.onConnect = func(connID uint32, cp *protocol.ConnectPayload) {
		go s.handleConnect(lr, connID, cp)
	}
	return lr
}

func (s *Server) Run() {
	for _, lr := range s.links {
		go lr.poller.Run()
		go s.recvLoop(lr)
	}

	fmt.Printf("[server] running with %d link(s), waiting for tunnel connections...\n", len(s.links))
	select {} // block forever
}

func (s *Server) recvLoop(lr *linkRuntime) {
	for data := range lr.poller.RecvChan() {
		frames, err := protocol.DecompressBatch(data)
		if err != nil {
			fmt.Printf("[server] link %d decompress error: %v\n", lr.link.ID, err)
			continue
		}
		for _, f := range frames {
			lr.mux.HandleFrame(f)
		}
	}
}

func (s *Server) handleConnect(lr *linkRuntime, connID uint32, cp *protocol.ConnectPayload) {
	target := cp.String()
	fmt.Printf("[server] link %d CONNECT %08x -> %s\n", lr.link.ID, connID, target)

	// Dial the target
	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		fmt.Printf("[server] link %d dial failed %08x -> %s: %v\n", lr.link.ID, connID, target, err)
		lr.mux.SendClose(connID)
		return
	}

	// Register connection and send ACK
	tc := lr.mux.RegisterConn(connID)
	lr.batcher.Add(&protocol.Frame{
		Type:   protocol.TypeConnectACK,
		ConnID: connID,
	})

	// Relay traffic
	lr.mux.Relay(conn, tc)
}

func (s *Server) Stop() {
	for _, lr := range s.links {
		lr.batcher.Stop()
		lr.poller.Stop()
		lr.mux.Stop()
	}
}
