package vtel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeTelegram stands in for the shared Telegram channel: a single
// append-only list of "documents" (sealed envelope bytes) that every fake
// bot polling it sees in full, from whatever offset it's tracking -
// exactly mirroring the real "every admin bot sees every channel post"
// behavior the mux's dedup logic is designed against.
type fakeTelegram struct {
	mu    sync.Mutex
	posts [][]byte
}

func (ft *fakeTelegram) upload(data []byte) {
	ft.mu.Lock()
	ft.posts = append(ft.posts, data)
	ft.mu.Unlock()
}

func (ft *fakeTelegram) since(offset int64) [][]byte {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if offset < 0 || int(offset) >= len(ft.posts) {
		return nil
	}
	return append([][]byte(nil), ft.posts[offset:]...)
}

func (ft *fakeTelegram) download(fileID string) ([]byte, error) {
	idx, err := strconv.ParseInt(fileID, 10, 64)
	if err != nil {
		return nil, err
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if idx < 0 || int(idx) >= len(ft.posts) {
		return nil, fmt.Errorf("fakeTelegram: no such post %d", idx)
	}
	return ft.posts[idx], nil
}

// fakeGetUpdates mirrors bot.getUpdates' offset contract (offset = last
// seen update_id + 1) against a fakeTelegram, encoding each post's index
// as both its UpdateID and its Document.FileID.
func fakeGetUpdates(ft *fakeTelegram) func(ctx context.Context, b *bot, offset int64) ([]tgUpdate, error) {
	return func(ctx context.Context, b *bot, offset int64) ([]tgUpdate, error) {
		posts := ft.since(offset)
		if len(posts) == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
			return nil, nil
		}
		updates := make([]tgUpdate, 0, len(posts))
		for i := range posts {
			idx := offset + int64(i)
			updates = append(updates, tgUpdate{
				UpdateID: idx,
				ChannelPost: &tgMessage{
					MessageID: idx,
					Document:  &tgDocument{FileID: strconv.FormatInt(idx, 10)},
				},
			})
		}
		return updates, nil
	}
}

// wireFakeMux points a vtelMux's upload/getUpdates/download seams at a
// shared fakeTelegram - no live bot tokens or network calls involved.
func wireFakeMux(m *vtelMux, ft *fakeTelegram) {
	m.uploadFn = func(ctx context.Context, b *bot, chatID int64, data []byte) error {
		ft.upload(data)
		return nil
	}
	m.getUpdatesFn = fakeGetUpdates(ft)
	m.downloadFn = func(ctx context.Context, b *bot, fileID string) ([]byte, error) {
		return ft.download(fileID)
	}
}

// TestMuxEndToEndEcho proves the whole client<->exit round trip - stream
// registration, OPEN dedup, Seq-based ordering/dedup across a two-bot
// pool where every bot sees every post (fakeGetUpdates), coalesced
// flush/poll, and clean stream teardown via FIN propagation - entirely
// through the fake transport and a real dialExitTarget against a local
// TCP echo server. No live Telegram involved.
func TestMuxEndToEndEcho(t *testing.T) {
	ft := &fakeTelegram{}
	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	// Two bots, shared by both roles (matches config.go's doc comment:
	// the same bot pool is known to both sides) - this is what makes the
	// dedup path (every bot sees every post) actually exercised here,
	// not just structurally possible.
	bots := []*bot{
		{token: "fakeA", username: "fakeA"},
		{token: "fakeB", username: "fakeB"},
	}

	echoAddr, shutdownEcho := startEchoServer(t)
	defer shutdownEcho()

	clientMux := newVtelMux("client", bots, 1, key, dirClientToExit, dirExitToClient, nil, nil)
	wireFakeMux(clientMux, ft)

	exitMux := newVtelMux("exit", bots, 1, key, dirExitToClient, dirClientToExit, dialExitTarget, nil)
	wireFakeMux(exitMux, ft)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientMux.start(ctx)
	exitMux.start(ctx)

	// Stand in for a SOCKS5-accepted connection with an in-process pipe:
	// clientSide is what openClientStream treats as "the local conn",
	// testSide is what the test drives as if it were the SOCKS client.
	clientSide, testSide := net.Pipe()

	openDone := make(chan error, 1)
	go func() {
		openDone <- clientMux.openClientStream(ctx, echoAddr, clientSide)
	}()

	want := []byte("hello through the fake telegram mux")
	if _, err := testSide.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := make([]byte, len(want))
	testSide.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(testSide, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}

	// Dedup check: with two bots both independently re-delivering every
	// post, a broken Seq-dedup would show up here as extra duplicate
	// bytes becoming readable shortly after the expected echo.
	extra := make([]byte, 1)
	testSide.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if n, err := testSide.Read(extra); err == nil && n > 0 {
		t.Fatalf("received unexpected extra byte after the full echo (dedup likely failed): %q", extra[:n])
	}

	// Close the SOCKS-side conn, same as SOCKSServer.handle's own defer
	// conn.Close() would once a real client disconnects - this should
	// propagate a FIN all the way through the exit's dialed echo
	// connection and back, and openClientStream should return cleanly.
	testSide.Close()

	select {
	case err := <-openDone:
		if err != nil {
			t.Fatalf("openClientStream returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openClientStream did not return after the local conn closed")
	}
}

// TestMuxOpenDedupDoesNotDoubleDial proves the exit role's claim-once
// guard: delivering the exact same frameOpen twice (simulating two bots
// both reporting the same post) must dial the target exactly once, not
// twice.
func TestMuxOpenDedupDoesNotDoubleDial(t *testing.T) {
	key, _ := DeriveKey("test-secret")
	bots := []*bot{{token: "fakeA", username: "fakeA"}}

	var dialCount int
	var mu sync.Mutex
	dialed := make(chan net.Conn, 4)

	fakeDial := func(ctx context.Context, target string) (net.Conn, error) {
		mu.Lock()
		dialCount++
		mu.Unlock()
		client, server := net.Pipe()
		dialed <- server
		return client, nil
	}

	exitMux := newVtelMux("exit", bots, 1, key, dirExitToClient, dirClientToExit, fakeDial, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fr := frame{Kind: frameOpen, StreamID: 42, Seq: 0, Payload: encodeOpenPayload("example.invalid:80", nil)}
	exitMux.handleFrame(ctx, fr)
	exitMux.handleFrame(ctx, fr) // duplicate, as if a second bot re-delivered the same post

	select {
	case <-dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("dialExit was never called")
	}
	// Give a genuinely duplicate dial a moment to happen if the guard is broken.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if dialCount != 1 {
		t.Fatalf("dialExit called %d times for a duplicated OPEN, want 1", dialCount)
	}
}

// TestMuxDeliverOutOfOrder proves frames arriving out of Seq order are
// buffered and delivered in order, not dropped or applied out of
// sequence.
func TestMuxDeliverOutOfOrder(t *testing.T) {
	key, _ := DeriveKey("test-secret")
	bots := []*bot{{token: "fakeA", username: "fakeA"}}
	m := newVtelMux("client", bots, 1, key, dirClientToExit, dirExitToClient, nil, nil)

	serverSide, testSide := net.Pipe()
	st := newMuxStream(7, serverSide)
	m.streamsMu.Lock()
	m.streams[7] = st
	m.streamsMu.Unlock()

	readAll := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 3)
		testSide.SetReadDeadline(time.Now().Add(5 * time.Second))
		io.ReadFull(testSide, buf)
		readAll <- buf
	}()

	// Deliver Seq 2, then 1, then 0 - out of order. Only after Seq 0
	// arrives should anything be written to the conn, and it must come
	// out in order "A","B","C".
	m.deliver(st, frame{Kind: frameData, StreamID: 7, Seq: 2, Payload: []byte("C")})
	m.deliver(st, frame{Kind: frameData, StreamID: 7, Seq: 1, Payload: []byte("B")})
	m.deliver(st, frame{Kind: frameData, StreamID: 7, Seq: 0, Payload: []byte("A")})

	select {
	case got := <-readAll:
		if string(got) != "ABC" {
			t.Fatalf("out-of-order delivery: got %q, want %q", got, "ABC")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never received the reordered frames")
	}
}
