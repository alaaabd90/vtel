package vtel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
)

// socksserver.go is a minimal SOCKS5 listener (RFC 1928), CONNECT only -
// no UDP ASSOCIATE/BIND, no IPv6/fake-IP/DNS-over-TLS filtering like
// gdrive's VPN-oriented socksserver.go carries. It has zero dependency on
// the mux/transport layer: SOCKSServer only speaks the SOCKS5 wire
// protocol and, once a client has successfully negotiated a CONNECT to
// some target, hands the raw net.Conn off to Handler and steps out of the
// way. mux.go's openClientStream is the real Handler in tunnel.go; nothing
// here needs to know that exists.

// SOCKSHandler receives a successfully negotiated SOCKS5 CONNECT: target
// is "host:port" (host may be a domain name - it is intentionally NOT
// resolved locally, so resolution happens on the exit side, same as any
// other SOCKS5 proxy). The handler owns conn from the moment it's called;
// SOCKSServer closes it automatically once Handler returns.
type SOCKSHandler func(ctx context.Context, target string, conn net.Conn)

type SOCKSServer struct {
	Listen  string
	Handler SOCKSHandler
	Logger  *log.Logger
}

func (s *SOCKSServer) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}

// Serve opens the listener and accepts connections until ctx is canceled.
func (s *SOCKSServer) Serve(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.Listen)
	if err != nil {
		return fmt.Errorf("vtel: socks5 listen %s: %w", s.Listen, err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("vtel: socks5 accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

func (s *SOCKSServer) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	target, err := socksHandshake(conn)
	if err != nil {
		s.logf("vtel: socks5 handshake from %s: %v", conn.RemoteAddr(), err)
		return
	}

	if err := socksReply(conn, socksRepSucceeded); err != nil {
		s.logf("vtel: socks5 reply to %s: %v", conn.RemoteAddr(), err)
		return
	}

	s.Handler(ctx, target, conn)
}

const (
	socksVersion5 = 0x05

	socksAuthNone         = 0x00
	socksAuthNoAcceptable = 0xFF

	socksCmdConnect = 0x01

	socksAtypIPv4   = 0x01
	socksAtypDomain = 0x03
	socksAtypIPv6   = 0x04

	socksRepSucceeded     = 0x00
	socksRepCmdNotSupport = 0x07
)

// socksHandshake performs the version/method negotiation and reads a
// CONNECT request, returning the "host:port" dial target. Any other
// command (BIND, UDP ASSOCIATE) gets an explicit "command not supported"
// reply and an error - not silently stubbed.
func socksHandshake(conn net.Conn) (target string, err error) {
	// Client hello: VER(1) NMETHODS(1) METHODS(NMETHODS)
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return "", fmt.Errorf("read client hello: %w", err)
	}
	if hdr[0] != socksVersion5 {
		return "", fmt.Errorf("unsupported socks version %d", hdr[0])
	}
	nmethods := int(hdr[1])
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", fmt.Errorf("read methods: %w", err)
	}

	offered := false
	for _, m := range methods {
		if m == socksAuthNone {
			offered = true
			break
		}
	}
	if !offered {
		conn.Write([]byte{socksVersion5, socksAuthNoAcceptable})
		return "", errors.New("client did not offer no-auth")
	}
	if _, err := conn.Write([]byte{socksVersion5, socksAuthNone}); err != nil {
		return "", fmt.Errorf("write method selection: %w", err)
	}

	// Request: VER(1) CMD(1) RSV(1) ATYP(1) DST.ADDR DST.PORT(2)
	reqHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHdr); err != nil {
		return "", fmt.Errorf("read request header: %w", err)
	}
	if reqHdr[0] != socksVersion5 {
		return "", fmt.Errorf("unsupported socks version %d in request", reqHdr[0])
	}
	cmd := reqHdr[1]
	atyp := reqHdr[3]

	host, err := readSocksAddr(conn, atyp)
	if err != nil {
		return "", fmt.Errorf("read address: %w", err)
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return "", fmt.Errorf("read port: %w", err)
	}
	port := binary.BigEndian.Uint16(portBuf[:])

	if cmd != socksCmdConnect {
		socksReply(conn, socksRepCmdNotSupport)
		return "", fmt.Errorf("unsupported command %d (only CONNECT is implemented)", cmd)
	}

	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func readSocksAddr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case socksAtypIPv4:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case socksAtypIPv6:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case socksAtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", err
		}
		buf := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	default:
		return "", fmt.Errorf("unsupported address type %d", atyp)
	}
}

// socksReply writes a reply with an all-zero BND.ADDR/BND.PORT (IPv4
// 0.0.0.0:0) - a standard, curl/browser-compatible choice for a proxy
// that has no genuine local bind address to disclose (the "connection" to
// the real target doesn't exist as a local socket at all on the client
// side - it exists as a mux stream).
func socksReply(conn net.Conn, rep byte) error {
	reply := []byte{socksVersion5, rep, 0x00, socksAtypIPv4, 0, 0, 0, 0, 0, 0}
	_, err := conn.Write(reply)
	return err
}
