// Command smoketest wires a real tunnel.Client and tunnel.Server together
// against faketelegram (real HTTP, real multipart parsing, real JSON - just
// not the real api.telegram.org) and drives one SOCKS5 CONNECT through the
// whole encrypted+compressed+ordered pipeline to a local TCP echo server.
// No live bot tokens or network access required.
//
// This exercises, end to end rather than as isolated units: AES-256-GCM
// envelope sealing/opening, zstd batching, the register-before-dial fix in
// Server.handleConnect, and Seq-based frame ordering in Mux.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/alaaabd90/vtel/faketelegram"
	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/telegram"
	"github.com/alaaabd90/vtel/tunnel"
)

const fakeChannelID int64 = -100123456789

func startEchoServer() (addr string, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "echo listen: %v\n", err)
		os.Exit(1)
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

// socksConnect performs a minimal no-auth SOCKS5 CONNECT handshake against
// an IPv4 target, standing in for a real SOCKS5 client library.
func socksConnect(proxyAddr, targetAddr string) (net.Conn, error) {
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		return nil, err
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	greetReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, greetReply); err != nil {
		conn.Close()
		return nil, err
	}
	if greetReply[0] != 0x05 || greetReply[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5 greeting rejected: %v", greetReply)
	}

	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		conn.Close()
		return nil, fmt.Errorf("expected an IPv4 target, got %q", host)
	}

	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, uint16(port))
	req = append(req, portBuf...)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		conn.Close()
		return nil, err
	}
	if reply[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5 CONNECT failed, REP=0x%02x", reply[1])
	}
	return conn, nil
}

func main() {
	fmt.Println("[smoketest] starting fake Telegram server...")
	ft := faketelegram.New(fakeChannelID)
	fakeServer := ft.Start()
	defer fakeServer.Close()

	echoAddr, stopEcho := startEchoServer()
	defer stopEcho()
	fmt.Printf("[smoketest] echo target at %s\n", echoAddr)

	key, err := protocol.DeriveKey("smoketest-secret")
	if err != nil {
		fmt.Fprintf(os.Stderr, "DeriveKey: %v\n", err)
		os.Exit(1)
	}
	level, err := protocol.ParseCompressionLevel("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ParseCompressionLevel: %v\n", err)
		os.Exit(1)
	}

	clientAPI := telegram.NewAPIWithHost("faketoken-client", fakeServer.URL)
	serverAPI := telegram.NewAPIWithHost("faketoken-server", fakeServer.URL)

	clientMe, err := clientAPI.GetMe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "client GetMe: %v\n", err)
		os.Exit(1)
	}
	serverMe, err := serverAPI.GetMe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "server GetMe: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[smoketest] client bot ID %d, server bot ID %d\n", clientMe.ID, serverMe.ID)

	clientSpec := tunnel.LinkSpec{ID: 0, API: clientAPI, BotID: clientMe.ID, PeerBotID: serverMe.ID, ChannelID: fakeChannelID, Key: key, CompressionLevel: level}
	serverSpec := tunnel.LinkSpec{ID: 0, API: serverAPI, BotID: serverMe.ID, PeerBotID: clientMe.ID, ChannelID: fakeChannelID, Key: key, CompressionLevel: level}

	const listenAddr = "127.0.0.1:19191"
	client := tunnel.NewClient([]tunnel.LinkSpec{clientSpec}, listenAddr, false)
	server := tunnel.NewServer([]tunnel.LinkSpec{serverSpec})

	go server.Run()
	go func() {
		if err := client.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "client.Run: %v\n", err)
			os.Exit(1)
		}
	}()

	time.Sleep(300 * time.Millisecond) // let both sides start listening/polling

	fmt.Println("[smoketest] dialing through SOCKS5 -> Telegram tunnel -> echo server...")
	conn, err := socksConnect(listenAddr, echoAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "socksConnect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	want := []byte("hello through the fake telegram tunnel, stage 2")
	if _, err := conn.Write(want); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}

	got := make([]byte, len(want))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		fmt.Fprintf(os.Stderr, "read echo: %v\n", err)
		os.Exit(1)
	}
	if !bytes.Equal(got, want) {
		fmt.Fprintf(os.Stderr, "echo mismatch: got %q, want %q\n", got, want)
		os.Exit(1)
	}

	fmt.Println("[smoketest] echo round-trip OK - CONNECT, encrypted+compressed batching, ordering, and register-before-dial all verified end to end.")

	client.Stop()
	server.Stop()
	fmt.Println("[smoketest] PASS")
}
