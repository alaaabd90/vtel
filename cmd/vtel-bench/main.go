// Command vtel-bench benchmarks a real tunnel.Client/tunnel.Server pair
// against faketelegram (in-memory, no live bot tokens or network access
// needed - see cmd/smoketest for the same pattern used for correctness
// testing rather than benchmarking). Reports:
//   - aggregate throughput vs. link count
//   - per-op CONNECT-to-first-byte latency (p50/p90/p99) vs. concurrent
//     stream count
//   - 429 (rate-limit) frequency per link
//
// All numbers here are relative to faketelegram's in-memory transport, not
// real Telegram API latency or rate limiting - useful for comparing vtel
// builds against each other (regression signal), not for predicting
// real-world throughput. See the README's Honest Limits section.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alaaabd90/vtel/faketelegram"
	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/telegram"
	"github.com/alaaabd90/vtel/tunnel"
)

const benchChannelID int64 = -100987654321

func main() {
	linksFlag := flag.String("links", "1,5,10,20", "comma-separated link counts for the throughput sweep")
	concurrencyFlag := flag.String("concurrency", "1,5,10,20,50", "comma-separated concurrency levels for the scaling sweep")
	payloadSize := flag.Int("payload", 256*1024, "bytes echoed per stream in the throughput sweep")
	scalingLinks := flag.Int("scaling-links", 10, "link count used for the scaling sweep")
	opsPerLevel := flag.Int("ops", 30, "operations per concurrency level in the scaling sweep")
	liveConfig := flag.String("live-config", "", "path to a real vtel client config.json to bench against live Telegram instead of faketelegram (requires real bot tokens; not exercised in this build's own CI)")
	flag.Parse()

	if *liveConfig != "" {
		fmt.Fprintln(os.Stderr, "-live-config is accepted but not yet wired to a running harness - "+
			"there is no live-mode implementation in this build. Use it as a marker for future work, "+
			"not a working flag. Falling back to faketelegram.")
	}

	linkCounts := parseIntList(*linksFlag)
	concurrencies := parseIntList(*concurrencyFlag)

	var throughputResults []throughputResult
	for _, n := range linkCounts {
		fmt.Printf("[bench] throughput sweep: %d link(s)...\n", n)
		throughputResults = append(throughputResults, runThroughput(n, *payloadSize))
	}

	var scalingResults []scalingResult
	var lastCounts map[int]int64
	for _, c := range concurrencies {
		fmt.Printf("[bench] scaling sweep: concurrency %d over %d link(s)...\n", c, *scalingLinks)
		r, counts := runScaling(*scalingLinks, c, *opsPerLevel)
		scalingResults = append(scalingResults, r)
		lastCounts = counts
	}

	printThroughputReport(throughputResults)
	printScalingReport(scalingResults)
	printRetry429Report(lastCounts)
	printFinalNotes()
}

func parseIntList(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad integer %q: %v\n", p, err)
			os.Exit(1)
		}
		out = append(out, n)
	}
	return out
}

// harness is one client+server pair wired against faketelegram with N links
// each, plus a local TCP echo target to CONNECT through.
type harness struct {
	client     *tunnel.Client
	listenAddr string
	echoAddr   string
	stop       func()
}

func newHarness(linkCount int) *harness {
	ft := faketelegram.New(benchChannelID)
	fakeServer := ft.Start()

	echoAddr, stopEcho := startEchoServer()

	key, err := protocol.DeriveKey("bench-secret")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	level, err := protocol.ParseCompressionLevel("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var clientSpecs, serverSpecs []tunnel.LinkSpec
	for i := 0; i < linkCount; i++ {
		clientAPI := telegram.NewAPIWithHost(fmt.Sprintf("bench-client-%d", i), fakeServer.URL)
		serverAPI := telegram.NewAPIWithHost(fmt.Sprintf("bench-server-%d", i), fakeServer.URL)
		clientMe, err := clientAPI.GetMe()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		serverMe, err := serverAPI.GetMe()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		clientSpecs = append(clientSpecs, tunnel.LinkSpec{
			ID: i, API: clientAPI, BotID: clientMe.ID, PeerBotID: serverMe.ID,
			ChannelID: benchChannelID, Key: key, CompressionLevel: level,
		})
		serverSpecs = append(serverSpecs, tunnel.LinkSpec{
			ID: i, API: serverAPI, BotID: serverMe.ID, PeerBotID: clientMe.ID,
			ChannelID: benchChannelID, Key: key, CompressionLevel: level,
		})
	}

	listenAddr := freeTCPAddr()
	client := tunnel.NewClient(clientSpecs, listenAddr, false)
	server := tunnel.NewServer(serverSpecs)

	go server.Run()
	go client.Run()
	time.Sleep(300 * time.Millisecond) // let both sides start listening/polling

	return &harness{
		client:     client,
		listenAddr: listenAddr,
		echoAddr:   echoAddr,
		stop: func() {
			client.Stop()
			server.Stop()
			fakeServer.Close()
			stopEcho()
		},
	}
}

func runThroughput(linkCount, payloadSize int) throughputResult {
	h := newHarness(linkCount)
	defer h.stop()

	concurrency := linkCount
	if concurrency < 1 {
		concurrency = 1
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var totalBytes int64
	start := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := echoRoundTrip(h.listenAddr, h.echoAddr, payloadSize)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[bench] stream error: %v\n", err)
				return
			}
			mu.Lock()
			totalBytes += int64(n) * 2 // written + read back
			mu.Unlock()
		}()
	}
	wg.Wait()

	return throughputResult{links: linkCount, totalBytes: totalBytes, elapsed: time.Since(start)}
}

func runScaling(linkCount, concurrency, opsPerLevel int) (scalingResult, map[int]int64) {
	h := newHarness(linkCount)
	defer h.stop()

	opCh := make(chan struct{}, opsPerLevel)
	for i := 0; i < opsPerLevel; i++ {
		opCh <- struct{}{}
	}
	close(opCh)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var latencies []time.Duration
	start := time.Now()

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range opCh {
				lat, err := timedEchoOp(h.listenAddr, h.echoAddr, 1024)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[bench] op error: %v\n", err)
					continue
				}
				mu.Lock()
				latencies = append(latencies, lat)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	counts := h.client.Retry429Counts()
	return scalingResult{concurrency: concurrency, ops: len(latencies), elapsed: elapsed, latencies: latencies}, counts
}

// echoRoundTrip writes payloadSize random bytes through a fresh SOCKS5
// CONNECT and reads them back, returning payloadSize on success.
func echoRoundTrip(proxyAddr, targetAddr string, payloadSize int) (int, error) {
	conn, err := socksConnect(proxyAddr, targetAddr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	payload := make([]byte, payloadSize)
	rand.Read(payload) // avoid trivially-compressible all-zero data skewing throughput numbers

	if _, err := conn.Write(payload); err != nil {
		return 0, err
	}

	got := make([]byte, payloadSize)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		return 0, err
	}
	return payloadSize, nil
}

// timedEchoOp measures CONNECT-to-first-byte latency: time from starting the
// SOCKS5 handshake to the first byte of the echoed response arriving. Reads
// exactly payloadSize bytes total (not until EOF): the echo target never
// closes the connection on its own, so draining to EOF would hang forever.
func timedEchoOp(proxyAddr, targetAddr string, payloadSize int) (time.Duration, error) {
	start := time.Now()
	conn, err := socksConnect(proxyAddr, targetAddr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	payload := make([]byte, payloadSize)
	rand.Read(payload)
	if _, err := conn.Write(payload); err != nil {
		return 0, err
	}

	got := make([]byte, payloadSize)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(conn, got[:1]); err != nil {
		return 0, err
	}
	latency := time.Since(start)

	if payloadSize > 1 {
		if _, err := io.ReadFull(conn, got[1:]); err != nil {
			return 0, err
		}
	}
	return latency, nil
}

func freeTCPAddr() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func startEchoServer() (addr string, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
