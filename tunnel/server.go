package tunnel

import (
	"fmt"
	"net"
	"time"

	"github.com/teltun/teltun/protocol"
	"github.com/teltun/teltun/telegram"
)

// Server runs the server side: receives tunnel frames, makes outbound TCP connections.
type Server struct {
	mux     *Mux
	batcher *protocol.Batcher
	poller  *telegram.Poller
	sender  *telegram.Sender
	api     *telegram.API
}

func NewServer(api *telegram.API, peerBotID, channelID int64) *Server {
	sender := telegram.NewSender(api, channelID)

	s := &Server{
		api:    api,
		sender: sender,
	}

	s.batcher = protocol.NewBatcher(sender.Send)
	s.mux = NewMux(func(f *protocol.Frame) {
		s.batcher.Add(f)
	})
	s.poller = telegram.NewPoller(api, peerBotID, channelID)

	// Handle CONNECT: dial the target and set up relay
	s.mux.onConnect = func(connID uint32, cp *protocol.ConnectPayload) {
		go s.handleConnect(connID, cp)
	}

	return s
}

func (s *Server) Run() {
	go s.poller.Run()
	go s.recvLoop()

	fmt.Println("[server] running, waiting for tunnel connections...")
	select {} // block forever
}

func (s *Server) recvLoop() {
	for data := range s.poller.RecvChan() {
		frames, err := protocol.DecompressBatch(data)
		if err != nil {
			fmt.Printf("[server] decompress error: %v\n", err)
			continue
		}
		for _, f := range frames {
			s.mux.HandleFrame(f)
		}
	}
}

func (s *Server) handleConnect(connID uint32, cp *protocol.ConnectPayload) {
	target := cp.String()
	fmt.Printf("[server] CONNECT %08x -> %s\n", connID, target)

	// Dial the target
	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		fmt.Printf("[server] dial failed %08x -> %s: %v\n", connID, target, err)
		s.mux.SendClose(connID)
		return
	}

	// Register connection and send ACK
	tc := s.mux.RegisterConn(connID)
	s.batcher.Add(&protocol.Frame{
		Type:   protocol.TypeConnectACK,
		ConnID: connID,
	})

	// Relay traffic
	s.mux.Relay(conn, tc)
}

func (s *Server) Stop() {
	s.batcher.Stop()
	s.poller.Stop()
	s.mux.Stop()
}
