package vtel

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// startEchoServer runs a minimal TCP echo listener for dialExitTarget to
// dial against, and returns its address plus a shutdown func.
func startEchoServer(t *testing.T) (addr string, shutdown func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestDialExitTargetEcho(t *testing.T) {
	addr, shutdown := startEchoServer(t)
	defer shutdown()

	conn, err := dialExitTarget(context.Background(), addr)
	if err != nil {
		t.Fatalf("dialExitTarget: %v", err)
	}
	defer conn.Close()

	want := []byte("hello from the exit dialer")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := make([]byte, len(want))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}
}

func TestDialExitTargetUnreachable(t *testing.T) {
	// Nothing listens here; the dial should fail rather than hang past
	// the context we hand it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := dialExitTarget(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("dialExitTarget: expected an error dialing a closed port, got nil")
	}
}
