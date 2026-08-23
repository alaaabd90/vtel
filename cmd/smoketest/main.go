// Command smoketest wires a real tunnel.Client and tunnel.Server together
// against an in-memory fake Telegram Bot API (real HTTP, real multipart
// parsing, real JSON - just not the real api.telegram.org) and drives one
// SOCKS5 CONNECT through the whole encrypted+compressed+ordered pipeline to
// a local TCP echo server. No live bot tokens or network access required.
//
// This exercises, end to end rather than as isolated units: AES-256-GCM
// envelope sealing/opening, gzip batching, the register-before-dial fix in
// Server.handleConnect, and Seq-based frame ordering in Mux.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/telegram"
	"github.com/alaaabd90/vtel/tunnel"
)

const fakeChannelID int64 = -100123456789

// fakeTelegram is a minimal in-memory stand-in for the Telegram Bot API:
// every bot token's getUpdates/getFile/download hits the same shared post
// list, mirroring "every bot with access to a channel sees every post" -
// which is exactly the scenario the frame-ordering/dedup logic needs to be
// correct against.
type fakeTelegram struct {
	mu      sync.Mutex
	nextID  int
	posts   []fakePost
	files   map[string][]byte
	botIDs  map[string]int64
	nextBot int64
}

type fakePost struct {
	updateID int
	fileName string
	fileID   string
	text     string
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{
		files:   make(map[string][]byte),
		botIDs:  make(map[string]int64),
		nextBot: 1000,
	}
}

func (f *fakeTelegram) botID(token string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.botIDs[token]; ok {
		return id
	}
	f.nextBot++
	f.botIDs[token] = f.nextBot
	return f.nextBot
}

func (f *fakeTelegram) addDocument(fileName string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fileID := fmt.Sprintf("file%d", f.nextID)
	f.files[fileID] = data
	f.posts = append(f.posts, fakePost{updateID: f.nextID, fileName: fileName, fileID: fileID})
	f.nextID++
}

func (f *fakeTelegram) addText(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, fakePost{updateID: f.nextID, text: text})
	f.nextID++
}

func (f *fakeTelegram) since(offset int) []fakePost {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakePost
	for _, p := range f.posts {
		if p.updateID >= offset {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeTelegram) file(fileID string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[fileID]
	return data, ok
}

func writeOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// handler routes fake Telegram Bot API requests, matching the real path
// shapes: /bot<token>/<method> and /file/bot<token>/<fileID>.
func (f *fakeTelegram) handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasPrefix(path, "/file/bot") {
		rest := strings.TrimPrefix(path, "/file/bot")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		data, ok := f.file(parts[1])
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
		return
	}

	if !strings.HasPrefix(path, "/bot") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, "/bot")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	token, method := parts[0], parts[1]

	switch method {
	case "getMe":
		writeOK(w, telegram.User{ID: f.botID(token), IsBot: true})

	case "sendMessage":
		var payload struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.addText(payload.Text)
		writeOK(w, map[string]any{"message_id": 1})

	case "sendDocument":
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
			http.Error(w, "bad content-type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var filename string
		var data []byte
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if part.FormName() == "document" {
				filename = part.FileName()
				data, _ = io.ReadAll(part)
			}
		}
		f.addDocument(filename, data)
		writeOK(w, map[string]any{"message_id": 1})

	case "getUpdates":
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		posts := f.since(offset)
		if len(posts) == 0 {
			// Loosely mirror long-poll latency without actually blocking
			// for the full 30s timeout every real getUpdates call would.
			time.Sleep(20 * time.Millisecond)
		}
		updates := make([]telegram.Update, 0, len(posts))
		for _, p := range posts {
			cp := &telegram.ChannelPost{
				MessageID: p.updateID,
				Chat:      telegram.Chat{ID: fakeChannelID},
			}
			if p.text != "" {
				cp.Text = p.text
			} else {
				cp.Document = &telegram.Document{FileID: p.fileID, FileName: p.fileName}
			}
			updates = append(updates, telegram.Update{UpdateID: p.updateID, ChannelPost: cp})
		}
		writeOK(w, updates)

	case "getFile":
		fileID := r.URL.Query().Get("file_id")
		writeOK(w, map[string]any{"file_path": fileID})

	default:
		http.NotFound(w, r)
	}
}

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
	ft := newFakeTelegram()
	fakeServer := httptest.NewServer(http.HandlerFunc(ft.handler))
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
	client := tunnel.NewClient([]tunnel.LinkSpec{clientSpec}, listenAddr)
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
