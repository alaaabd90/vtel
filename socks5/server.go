package socks5

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// ConnectRequest represents a parsed SOCKS5 CONNECT request.
type ConnectRequest struct {
	AddrType byte   // 0x01=IPv4, 0x03=domain, 0x04=IPv6
	Addr     []byte // raw address bytes
	Port     uint16
}

func (r *ConnectRequest) String() string {
	switch r.AddrType {
	case 0x01:
		return fmt.Sprintf("%d.%d.%d.%d:%d", r.Addr[0], r.Addr[1], r.Addr[2], r.Addr[3], r.Port)
	case 0x03:
		return fmt.Sprintf("%s:%d", string(r.Addr), r.Port)
	case 0x04:
		return fmt.Sprintf("[%x]:%d", r.Addr, r.Port)
	}
	return "unknown"
}

// HandleFunc is called for each SOCKS5 CONNECT request.
type HandleFunc func(conn net.Conn, req *ConnectRequest)

// Server is a minimal SOCKS5 server supporting CONNECT only, no auth.
type Server struct {
	Addr    string
	Handler HandleFunc
}

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Printf("[socks5] listening on %s\n", s.Addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[socks5] panic: %v\n", r)
			conn.Close()
		}
	}()

	// 1. Greeting: client sends version + methods
	buf := make([]byte, 258)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		conn.Close()
		return
	}
	if buf[0] != 0x05 {
		conn.Close()
		return
	}
	nmethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
		conn.Close()
		return
	}

	// 2. Reply: no auth required
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		conn.Close()
		return
	}

	// 3. Request: VER CMD RSV ATYP DST.ADDR DST.PORT
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		conn.Close()
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 { // only CONNECT
		// Send command not supported
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		conn.Close()
		return
	}

	req := &ConnectRequest{AddrType: buf[3]}
	switch req.AddrType {
	case 0x01: // IPv4
		req.Addr = make([]byte, 4)
		if _, err := io.ReadFull(conn, req.Addr); err != nil {
			conn.Close()
			return
		}
	case 0x03: // Domain
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			conn.Close()
			return
		}
		req.Addr = make([]byte, buf[0])
		if _, err := io.ReadFull(conn, req.Addr); err != nil {
			conn.Close()
			return
		}
	case 0x04: // IPv6
		req.Addr = make([]byte, 16)
		if _, err := io.ReadFull(conn, req.Addr); err != nil {
			conn.Close()
			return
		}
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		conn.Close()
		return
	}

	// Read port
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		conn.Close()
		return
	}
	req.Port = binary.BigEndian.Uint16(portBuf[:])

	s.Handler(conn, req)
}

// SendSuccess sends a SOCKS5 success reply.
func SendSuccess(conn net.Conn) error {
	// VER REP RSV ATYP BND.ADDR BND.PORT
	_, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

// SendFailure sends a SOCKS5 general failure reply.
func SendFailure(conn net.Conn) {
	conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	conn.Close()
}
