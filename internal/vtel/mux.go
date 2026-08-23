package vtel

import (
	"context"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// mux.go is the core stream multiplexer: it turns SOCKS5-accepted
// connections (client role) or dial-out connections to real destinations
// (exit role) into batched, encrypted frame traffic over the bot pool, and
// back again. Deliberately far simpler than gdrive's mux.go (no adaptive
// pacing, no quota backpressure, no priority-payload coalescing, no
// per-lane keys) - see the vtel v1 plan for why.
const (
	// maxBatchBytes caps one flush's total payload size, comfortably under
	// Telegram's 50MB bot-document ceiling while keeping gzip memory and
	// upload latency reasonable.
	maxBatchBytes = 4 * 1024 * 1024

	// flushDelay is a simple Nagle-style debounce: once the outbound
	// buffer goes empty->non-empty, a flush fires after this delay (or
	// sooner if maxBatchBytes is hit first). One tier, unlike gdrive's
	// interactive/medium/bulk coalescing - v1 accepts up to ~flushDelay
	// of added latency on every frame, SOCKS handshake included.
	flushDelay = 150 * time.Millisecond

	// flushPollInterval is how often runFlushLoop checks whether a flush
	// is due. A plain poll instead of precise per-buffer timers - simpler
	// to get race-free on a first pass, and the added granularity is
	// negligible next to flushDelay itself.
	flushPollInterval = 20 * time.Millisecond

	// maxRecvPending bounds each stream's out-of-order buffer (a safety
	// valve against unbounded memory growth if frames arrive wildly out
	// of order or a peer misbehaves) - not a hard protocol limit.
	maxRecvPending = 256

	uploadTimeout = 60 * time.Second

	readBufSize = 32 * 1024
)

// vtelMux is role-agnostic aside from dialExit (nil on the client role,
// required on the exit role) - the same stream-registry/framing/batching
// machinery runs both directions, distinguished only by sendDir/recvDir
// (see protocol.go's dirClientToExit/dirExitToClient) and by who calls
// openClientStream (client only) vs who receives frameOpen (exit only).
type vtelMux struct {
	role     string // "client" | "exit", logging only
	bots     []*bot
	chatID   int64
	key      []byte
	sendDir  byte
	recvDir  byte
	logger   *log.Logger
	dialExit func(ctx context.Context, target string) (net.Conn, error)

	streamsMu sync.Mutex
	streams   map[uint64]*muxStream
	opening   map[uint64]struct{} // claim-once guard while dialExit is in flight (exit role)

	outMu    sync.Mutex
	outBuf   []frame
	outBytes int

	nextBot atomic.Uint64

	// Testability seams: nil in production (real bot methods are used
	// directly), overridable in tests so mux.go's stream/ordering/dedup
	// logic can be proven correct against an in-process fake transport
	// without live bot tokens - see mux_test.go.
	uploadFn     func(ctx context.Context, b *bot, chatID int64, data []byte) error
	getUpdatesFn func(ctx context.Context, b *bot, offset int64) ([]tgUpdate, error)
	downloadFn   func(ctx context.Context, b *bot, fileID string) ([]byte, error)
}

// muxStream tracks one logical stream: a local net.Conn (the SOCKS-accepted
// conn on the client role, the dialed target conn on the exit role) plus
// enough state to deliver inbound frames in order exactly once, even
// though delivery is neither ordered nor deduplicated by the transport
// itself (concurrent multi-bot uploads reorder batches on the wire, and
// every bot admin of the shared chat sees every post, so the same batch
// arrives once per polling bot).
type muxStream struct {
	id   uint64
	conn net.Conn

	sendSeq atomic.Uint64

	recvMu       sync.Mutex
	recvExpected uint64
	recvPending  map[uint64]frame

	stateMu        sync.Mutex
	localSendDone  bool
	remoteRecvDone bool

	closeOnce sync.Once
	fullyDone chan struct{}
}

func newMuxStream(id uint64, conn net.Conn) *muxStream {
	return &muxStream{
		id:          id,
		conn:        conn,
		recvPending: make(map[uint64]frame),
		fullyDone:   make(chan struct{}),
	}
}

func (s *muxStream) nextSendSeq() uint64 {
	return s.sendSeq.Add(1) - 1 // first call returns 0
}

func newVtelMux(role string, bots []*bot, chatID int64, key []byte, sendDir, recvDir byte, dialExit func(context.Context, string) (net.Conn, error), logger *log.Logger) *vtelMux {
	return &vtelMux{
		role:     role,
		bots:     bots,
		chatID:   chatID,
		key:      key,
		sendDir:  sendDir,
		recvDir:  recvDir,
		logger:   logger,
		dialExit: dialExit,
		streams:  make(map[uint64]*muxStream),
		opening:  make(map[uint64]struct{}),
	}
}

func (m *vtelMux) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Printf(format, args...)
	}
}

// start spawns the outbound flush loop and one inbound poll loop per bot
// token (Telegram's update_id numbering is per-bot, so offsets must be
// tracked per-bot - see runPollLoop). Both roles run both loops: the
// client polls for exit->client responses just as the exit polls for
// client->exit requests.
func (m *vtelMux) start(ctx context.Context) {
	go m.runFlushLoop(ctx)
	for _, b := range m.bots {
		go m.runPollLoop(ctx, b)
	}
}

// closeAll forcibly finalizes every open stream - used on shutdown (the
// exit role has no other natural place to release dialed connections
// since it has no SOCKS server whose own conn.Close() would do it).
func (m *vtelMux) closeAll() {
	m.streamsMu.Lock()
	streams := make([]*muxStream, 0, len(m.streams))
	for _, st := range m.streams {
		streams = append(streams, st)
	}
	m.streamsMu.Unlock()
	for _, st := range streams {
		m.finalizeStream(st)
	}
}

// openClientStream is the client role's SOCKSHandler (see socksserver.go):
// called once per accepted+handshaked SOCKS5 CONNECT. Registers a stream,
// sends a frameOpen, relays local reads out as frameData/frameFIN, and
// blocks until the stream is fully finished in both directions - conn is
// owned by the caller (SOCKSServer.handle), which closes it once this
// returns.
func (m *vtelMux) openClientStream(ctx context.Context, target string, conn net.Conn) error {
	id, err := randomStreamID()
	if err != nil {
		return err
	}

	st := newMuxStream(id, conn)
	m.streamsMu.Lock()
	m.streams[id] = st
	m.streamsMu.Unlock()

	m.enqueueFrame(frame{Kind: frameOpen, StreamID: id, Seq: st.nextSendSeq(), Payload: encodeOpenPayload(target, nil)})

	go m.runLocalReadLoop(st)

	select {
	case <-st.fullyDone:
	case <-ctx.Done():
		m.finalizeStream(st)
	}
	return nil
}

// runLocalReadLoop reads from a stream's local conn (the SOCKS conn on
// the client role, the dialed target conn on the exit role) and turns
// each chunk into an outbound frameData, sending a closing frameFIN once
// the conn hits EOF or errors. Symmetric for both roles - the only
// difference is which conn a muxStream wraps.
func (m *vtelMux) runLocalReadLoop(st *muxStream) {
	buf := make([]byte, readBufSize)
	for {
		n, err := st.conn.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			m.enqueueFrame(frame{Kind: frameData, StreamID: st.id, Seq: st.nextSendSeq(), Payload: payload})
		}
		if err != nil {
			break
		}
	}
	m.enqueueFrame(frame{Kind: frameFIN, StreamID: st.id, Seq: st.nextSendSeq()})
	m.markLocalDone(st)
}

func (m *vtelMux) markLocalDone(st *muxStream) {
	st.stateMu.Lock()
	st.localSendDone = true
	done := st.localSendDone && st.remoteRecvDone
	st.stateMu.Unlock()
	if done {
		m.finalizeStream(st)
	}
}

func (m *vtelMux) markRemoteDone(st *muxStream) {
	st.stateMu.Lock()
	st.remoteRecvDone = true
	done := st.localSendDone && st.remoteRecvDone
	st.stateMu.Unlock()
	if done {
		m.finalizeStream(st)
		return
	}
	// The peer has no more data to relay to us, i.e. no more frameData
	// will ever arrive for this stream - so we're done writing to
	// st.conn. Half-close it if it's a real duplex socket (TCPConn's
	// CloseWrite), so the real local peer (e.g. the exit role's dialed
	// destination) sees EOF instead of hanging - not finalizing yet
	// since our own local read side may still have data to send the
	// other way.
	closeWriteIfSupported(st.conn)
}

func closeWriteIfSupported(conn net.Conn) {
	type writeCloser interface{ CloseWrite() error }
	if wc, ok := conn.(writeCloser); ok {
		wc.CloseWrite()
	}
}

func (m *vtelMux) finalizeStream(st *muxStream) {
	st.closeOnce.Do(func() {
		st.conn.Close()
		m.streamsMu.Lock()
		delete(m.streams, st.id)
		m.streamsMu.Unlock()
		close(st.fullyDone)
	})
}

// ---- outbound: enqueue + coalesce + flush ----

func (m *vtelMux) enqueueFrame(fr frame) {
	m.outMu.Lock()
	m.outBuf = append(m.outBuf, fr)
	m.outBytes += len(fr.Payload)
	m.outMu.Unlock()
}

func (m *vtelMux) runFlushLoop(ctx context.Context) {
	ticker := time.NewTicker(flushPollInterval)
	defer ticker.Stop()
	var pendingSince time.Time

	for {
		select {
		case <-ctx.Done():
			m.flushNow(context.Background()) // best-effort final flush (lets a closing FIN go out)
			return
		case <-ticker.C:
			m.outMu.Lock()
			empty := len(m.outBuf) == 0
			bytes := m.outBytes
			m.outMu.Unlock()

			if empty {
				pendingSince = time.Time{}
				continue
			}
			if pendingSince.IsZero() {
				pendingSince = time.Now()
			}
			if time.Since(pendingSince) >= flushDelay || bytes >= maxBatchBytes {
				m.flushNow(ctx)
				pendingSince = time.Time{}
			}
		}
	}
}

func (m *vtelMux) flushNow(ctx context.Context) error {
	m.outMu.Lock()
	if len(m.outBuf) == 0 {
		m.outMu.Unlock()
		return nil
	}
	frames := m.outBuf
	m.outBuf = nil
	m.outBytes = 0
	m.outMu.Unlock()

	batch, err := encodeBatch(m.sendDir, frames)
	if err != nil {
		m.logf("vtel: encodeBatch: %v", err)
		return err
	}
	sealed, err := sealEnvelope(m.key, batch)
	if err != nil {
		m.logf("vtel: sealEnvelope: %v", err)
		return err
	}

	b := m.pickBot()
	go func() {
		uploadCtx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
		defer cancel()
		if err := m.upload(uploadCtx, b, sealed); err != nil {
			m.logf("vtel: upload via @%s: %v", b.username, err)
		}
	}()
	return nil
}

// pickBot round-robins across the bot pool at flush time - spreads the
// actual rate-limited operation (sendDocument) evenly regardless of how
// traffic is distributed across streams, and lets one busy stream benefit
// from multiple lanes over its lifetime instead of being pinned to a
// single bot's throughput ceiling (which sticky-per-stream assignment
// would do).
func (m *vtelMux) pickBot() *bot {
	idx := m.nextBot.Add(1) - 1
	return m.bots[idx%uint64(len(m.bots))]
}

func (m *vtelMux) upload(ctx context.Context, b *bot, data []byte) error {
	if m.uploadFn != nil {
		return m.uploadFn(ctx, b, m.chatID, data)
	}
	return b.sendDocument(ctx, m.chatID, "batch.bin", data)
}

// ---- inbound: per-bot poll + dispatch ----

// runPollLoop long-polls one bot's getUpdates forever, tracking its own
// offset (Telegram's update_id is per-bot, not shared across the pool).
// Each matched document is dispatched to its own goroutine rather than
// blocking the poll loop on downloadFile - acceptable for v1 since
// Telegram's own flood limiting caps arrival frequency well below
// anything that would cause goroutine pressure.
func (m *vtelMux) runPollLoop(ctx context.Context, b *bot) {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := m.getUpdates(ctx, b, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.logf("vtel: getUpdates via @%s: %v", b.username, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			doc := documentFromUpdate(u)
			if doc == nil {
				continue
			}
			go m.handleDocument(ctx, b, doc.FileID)
		}
	}
}

func documentFromUpdate(u tgUpdate) *tgDocument {
	if u.ChannelPost != nil && u.ChannelPost.Document != nil {
		return u.ChannelPost.Document
	}
	if u.Message != nil && u.Message.Document != nil {
		return u.Message.Document
	}
	return nil
}

func (m *vtelMux) getUpdates(ctx context.Context, b *bot, offset int64) ([]tgUpdate, error) {
	if m.getUpdatesFn != nil {
		return m.getUpdatesFn(ctx, b, offset)
	}
	return b.getUpdates(ctx, offset)
}

func (m *vtelMux) handleDocument(ctx context.Context, b *bot, fileID string) {
	data, err := m.downloadFile(ctx, b, fileID)
	if err != nil {
		m.logf("vtel: downloadFile via @%s: %v", b.username, err)
		return
	}

	plaintext, ok, err := openEnvelope(m.key, data)
	if err != nil {
		m.logf("vtel: openEnvelope: %v", err)
		return
	}
	if !ok {
		return // wrong key, tampered, or genuinely not a vtel envelope (e.g. a human posted something) - skip silently
	}

	frames, dir, ok, err := decodeBatch(plaintext)
	if err != nil {
		m.logf("vtel: decodeBatch: %v", err)
		return
	}
	if !ok || dir != m.recvDir {
		return // not ours, or the other direction's traffic (e.g. our own upload echoed back)
	}

	for _, fr := range frames {
		m.handleFrame(ctx, fr)
	}
}

func (m *vtelMux) downloadFile(ctx context.Context, b *bot, fileID string) ([]byte, error) {
	if m.downloadFn != nil {
		return m.downloadFn(ctx, b, fileID)
	}
	return b.downloadFile(ctx, fileID)
}

// handleFrame routes one already-decoded, already-direction-filtered
// frame to its stream (or, for frameOpen on the exit role, to the
// dial-and-register path).
func (m *vtelMux) handleFrame(ctx context.Context, fr frame) {
	if fr.Kind == frameOpen {
		m.handleOpenFrame(ctx, fr)
		return
	}

	m.streamsMu.Lock()
	st := m.streams[fr.StreamID]
	m.streamsMu.Unlock()
	if st == nil {
		// Unknown or already-finalized stream - drop silently. Routine,
		// not an error: a duplicate FIN/RST for a stream that another
		// bot's delivery already finalized lands here on every close,
		// given every admin bot re-delivers the same post (see
		// handleOpenFrame's doc comment) - also covers post-restart
		// traffic referencing a stream this process never had.
		return
	}
	m.deliver(st, fr)
}

// deliver applies Seq-based ordering and dedup: frames are handed to
// processInOrder only in strict contiguous order, buffering (up to
// maxRecvPending) anything that arrives early and dropping anything at or
// behind recvExpected outright. This one mechanism absorbs both problems
// concurrent multi-bot upload/poll produces - wire reordering, and every
// admin bot re-delivering the same post - without needing separate
// handling for either.
func (m *vtelMux) deliver(st *muxStream, fr frame) {
	st.recvMu.Lock()
	defer st.recvMu.Unlock()

	if fr.Seq < st.recvExpected {
		return // duplicate delivery or stale - drop
	}
	if fr.Seq != st.recvExpected {
		if len(st.recvPending) < maxRecvPending {
			st.recvPending[fr.Seq] = fr
		}
		return
	}
	if st.conn == nil {
		// Next-in-line but the exit role's dial for this stream's OPEN
		// hasn't completed and called attachConn yet - buffer rather
		// than touch a nil conn. In practice recvExpected only advances
		// past 0 in the same critical section attachConn sets conn in
		// (see attachConn), so this should be unreachable, but it's a
		// cheap guard against a nil deref if that invariant ever slips.
		if len(st.recvPending) < maxRecvPending {
			st.recvPending[fr.Seq] = fr
		}
		return
	}

	m.processInOrder(st, fr)
	st.recvExpected++
	m.drainPendingLocked(st)
}

// drainPendingLocked delivers any buffered frames that are now
// contiguous with recvExpected. Callers must hold st.recvMu and must
// only call this once st.conn is attached (see deliver and attachConn).
func (m *vtelMux) drainPendingLocked(st *muxStream) {
	for {
		next, ok := st.recvPending[st.recvExpected]
		if !ok {
			break
		}
		delete(st.recvPending, st.recvExpected)
		m.processInOrder(st, next)
		st.recvExpected++
	}
}

// attachConn is called once the exit role's dial for a just-claimed
// stream completes, connecting the placeholder muxStream registered by
// handleOpenFrame (before dialing) to its real local conn, and draining
// any DATA/FIN/RST frames that arrived - and were buffered by deliver -
// while the dial was still in flight.
func (m *vtelMux) attachConn(st *muxStream, conn net.Conn) {
	st.recvMu.Lock()
	st.conn = conn
	st.recvExpected = 1 // the OPEN frame itself (always Seq 0) is now considered processed
	m.drainPendingLocked(st)
	st.recvMu.Unlock()
}

func (m *vtelMux) processInOrder(st *muxStream, fr frame) {
	switch fr.Kind {
	case frameData:
		if _, err := st.conn.Write(fr.Payload); err != nil {
			m.logf("vtel: stream %d: write to local conn: %v", st.id, err)
		}
	case frameFIN:
		m.markRemoteDone(st)
	case frameRST:
		m.finalizeStream(st)
	}
}

// handleOpenFrame is the exit role's inbound-OPEN path. Dedups via a
// claim-once guard (opening, then streams itself) since every admin bot
// of the shared chat sees every post, so the same frameOpen can arrive
// once per polling bot; a second OPEN for an already-registered or
// already-dialing stream ID is a silent no-op.
//
// The stream is registered (with no conn attached yet) BEFORE dialing,
// not after - dialing takes real time, and a second bot's concurrent
// delivery of the very same batch (OPEN + this stream's first DATA frame
// together, which coalescing makes routine) would otherwise find no
// stream yet and drop that DATA frame as "unknown stream". Registering
// first means deliver() sees the stream immediately and buffers any
// frames that outrace the dial via its normal out-of-order handling
// (see deliver/drainPendingLocked/attachConn) instead of dropping them.
func (m *vtelMux) handleOpenFrame(ctx context.Context, fr frame) {
	if m.dialExit == nil {
		return // client role should never see a frameOpen given direction filtering; defensive no-op if it somehow does
	}

	m.streamsMu.Lock()
	if _, exists := m.streams[fr.StreamID]; exists {
		m.streamsMu.Unlock()
		return
	}
	if _, opening := m.opening[fr.StreamID]; opening {
		m.streamsMu.Unlock()
		return
	}
	m.opening[fr.StreamID] = struct{}{}

	target, initial, err := decodeOpenPayload(fr.Payload)
	if err != nil {
		delete(m.opening, fr.StreamID)
		m.streamsMu.Unlock()
		m.logf("vtel: stream %d: decodeOpenPayload: %v", fr.StreamID, err)
		return
	}

	st := newMuxStream(fr.StreamID, nil)
	m.streams[fr.StreamID] = st
	delete(m.opening, fr.StreamID)
	m.streamsMu.Unlock()

	conn, dialErr := m.dialExit(ctx, target)
	if dialErr != nil {
		m.streamsMu.Lock()
		delete(m.streams, fr.StreamID)
		m.streamsMu.Unlock()
		m.logf("vtel: stream %d: dial %s: %v", fr.StreamID, target, dialErr)
		m.enqueueFrame(frame{Kind: frameRST, StreamID: fr.StreamID, Seq: 0})
		return
	}

	if len(initial) > 0 {
		if _, err := conn.Write(initial); err != nil {
			m.logf("vtel: stream %d: write initial bytes: %v", fr.StreamID, err)
		}
	}

	m.attachConn(st, conn)
	m.logf("vtel[%s]: stream %d: dialed %s ok", m.role, fr.StreamID, target)
	go m.runLocalReadLoop(st)
}
