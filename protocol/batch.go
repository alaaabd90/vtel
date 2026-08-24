package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/alaaabd90/vtel/vtellog"
)

const (
	FlushIdleTimeout = 250 * time.Millisecond
	MaxFlushDelay    = 750 * time.Millisecond

	// controlCoalesceWindow bounds how long a CONNECT/CLOSE/RESET frame
	// waits for siblings before flushing, replacing what used to be an
	// unconditional immediate flush (isControlFrame in Add). Found via live
	// device testing: a phone routes every background app's own connection
	// attempts through one SOCKS5 link, so a burst of near-simultaneous
	// CONNECTs (a page load, an app sync hitting several endpoints at once)
	// each firing its own separate Telegram sendMessage call was enough to
	// trip Telegram's flood protection far harder than the per-chat rate
	// limit alone (retry_after climbing into the tens of seconds). A short
	// coalescing window lets frames arriving within it merge into one send;
	// a lone control frame still flushes fast enough that the extra delay
	// is imperceptible for real interactive use. Widened from an initial
	// 20ms after further live testing: a real burst (a page load, an app
	// sync) spreads its connections over tens of milliseconds, not one
	// instant, so 20ms caught less of it than intended.
	controlCoalesceWindow = 60 * time.Millisecond
	DataFlushThreshold    = 32 * 1024        // Flush active streams before they accumulate too much latency.
	MaxBatchSize          = 48 * 1024 * 1024 // 48MB uncompressed
	MaxCompressedSize     = 19 * 1024 * 1024 // 19MB compressed (Telegram getFile limit is 20MB)

	maxSendRetries      = 5
	initialRetryBackoff = 1 * time.Second
	maxRetryBackoff     = 30 * time.Second

	// StreamPriorityBytes is the per-connection one-way latch threshold
	// ported from gdrive's priority classification (internal/gdrive/mux.go):
	// a stream's TypeData frames are urgent until this many bytes have been
	// sent on it, then permanently demoted to normal. Control frames
	// (CONNECT/CONNECT_ACK/CLOSE/RESET) are always urgent regardless.
	StreamPriorityBytes = 512 * 1024

	// batchBufPoolSlots/batchBufCap bound the pooled plaintext-batch-buffer
	// cache (see getBuf/putBuf). batchBufCap is deliberately far below
	// MaxBatchSize: unlike gdrive's single mux (whose pool cap is sized from
	// the deployment's real chunk_size), vtel has one Batcher per link, and
	// with a default 20-link pool, slots*cap*linkCount must stay a modest
	// fraction of total memory. Real flush sizes are realistically tens of
	// KB to low single-digit MB under vtel's adaptive batching tiers; a
	// batch that happens to grow past this cap still works fine, it just
	// isn't pooled afterward.
	batchBufPoolSlots = 4
	batchBufCap       = 4 * 1024 * 1024

	// shutdownFlushTimeout bounds how long Stop waits for the final flush's
	// (possibly still-retrying) send to complete before giving up and
	// closing the encoder anyway.
	shutdownFlushTimeout = 10 * time.Second

	// maxQueuedAndInFlightBytes bounds the combined total of this Batcher's
	// not-yet-flushed buffer plus every byte handed off to a still-running
	// flushRaw goroutine (compressing/sealing/sending, possibly mid-retry).
	// Ported from a confirmed production bug in gdrive
	// (normalLaneBudgetBytes, internal/gdrive/mux.go): before that fix,
	// bytes were "released" from accounting the instant a batch was
	// dequeued for upload, even though the payload stays alive in memory
	// for the whole compress+seal+upload(+retries) lifecycle - heap
	// profiled to reach multiple GB for one busy, fast-producing account.
	// Stage 5 made vtel structurally more exposed to the same failure
	// shape than gdrive's original bug, not less: Flush dispatches each
	// batch's compress+seal+send on its own goroutine, so a link whose
	// real send throughput can't keep up has nothing stopping an unbounded
	// number of these from piling up, each holding its own up-to-
	// MaxBatchSize buffer live in memory while blocked on acquireSlot.
	//
	// Set generously relative to a single batch and the 2-slot concurrency
	// gate's realistic working set: gdrive's own history shows this exact
	// class of bound got mistuned too tight at least once (v1.0.33/34,
	// "restore upload queue depth 8->16 frames to unblock producer") and
	// had to be loosened to stop throttling legitimate throughput. Tune
	// upward, not down, if this ever proves too eager in practice.
	maxQueuedAndInFlightBytes = 4 * MaxBatchSize // 192MB

	// admitPollInterval bounds how often Add polls for budget to free up.
	admitPollInterval = 10 * time.Millisecond
)

// admitTimeout caps how long a single Add call can block waiting for room
// in the combined queued+in-flight budget (see maxQueuedAndInFlightBytes).
// Polling (rather than a channel-based wait) keeps this simple and mirrors
// gdrive's own polling approach to the same problem. On timeout, Add
// reports failure to its caller (see the bool return) so the caller can
// tear the one stuck stream down, rather than either blocking forever or
// silently dropping a frame that would otherwise leave a permanent gap in
// that connection's Seq-ordered stream. A var, not a const, purely so
// tests can shrink it instead of waiting out the real 20s.
var admitTimeout = 20 * time.Second

// CompressionLevel is a zstd encoder level, re-exported so callers outside
// this package don't need to import klauspost/compress/zstd directly.
type CompressionLevel = zstd.EncoderLevel

// ParseCompressionLevel maps a config string to a zstd.EncoderLevel.
// "" and "fastest" match today's gzip.BestSpeed intent - cheap CPU cost per
// flush over squeezing out extra ratio.
func ParseCompressionLevel(s string) (CompressionLevel, error) {
	switch s {
	case "", "fastest":
		return zstd.SpeedFastest, nil
	case "default":
		return zstd.SpeedDefault, nil
	case "better":
		return zstd.SpeedBetterCompression, nil
	case "best":
		return zstd.SpeedBestCompression, nil
	default:
		return 0, fmt.Errorf("unknown compression level %q (want fastest, default, better, or best)", s)
	}
}

// Batcher collects frames and flushes them as zstd-compressed, AES-256-GCM
// sealed batches.
type Batcher struct {
	mu              sync.Mutex
	buf             bytes.Buffer
	hasUrgent       bool // whether the in-progress batch contains any urgent frame
	hasControlFrame bool // whether the in-progress batch contains a CONNECT/CLOSE/RESET - see controlCoalesceWindow
	seqNum          atomic.Uint64
	sendFn          func(seq uint64, data []byte, urgent bool) error
	key             []byte // AES-256-GCM key from DeriveKey, applied to every flush

	// urgentTok/sharedTok are a 2-slot concurrency gate ported from gdrive's
	// urgent/normal priority-reserve concept (internal/gdrive/tunnel.go),
	// right-sized from gdrive's max/4 worker-pool split (meaningless at
	// vtel's realistic 1-2-in-flight-per-link scale): one urgent-reserved
	// slot, one shared slot. Urgent sends may use either (falling back to
	// the shared slot if the reserved one is busy); normal sends only ever
	// use the shared slot, so a slow bulk upload can never fully block
	// urgent traffic on this link. See acquireSlot.
	urgentTok chan struct{}
	sharedTok chan struct{}

	// enc/dec are constructed once and reused across every flush/receive on
	// this Batcher - a fresh zstd encoder per flush is a known real
	// performance trap (each one allocates its own internal tables).
	enc *zstd.Encoder
	dec *zstd.Decoder

	// idleTimeout is the floor adaptiveIdleTimeout falls back to under low
	// throughput; maxFlushDelay is NOT made adaptive (see adaptiveIdleTimeout).
	idleTimeout        time.Duration
	maxFlushDelay      time.Duration
	dataFlushThreshold int
	batchStartedAt     time.Time
	lastQueuedAt       time.Time
	// jitteredMaxFlushDelay is rolled once per batch (at batch start,
	// alongside batchStartedAt) rather than recomputed on every timer
	// update, so the hard-cap deadline stays a fixed target for a given
	// batch instead of flapping around on every new frame's Add call.
	jitteredMaxFlushDelay time.Duration
	quietHours            *QuietHoursConfig
	activityCh            chan struct{} // signal queued data so timers can be updated
	flushCh               chan struct{} // signal immediate flush
	done                  chan struct{}
	stopped               chan struct{} // closed once flushLoop has fully drained after done
	flushWG               sync.WaitGroup

	// Throughput measurement for adaptiveIdleTimeout, ported from gdrive's
	// muxLane.updateBytesPerSec/adaptiveCorkDelay: a rolling one-second
	// window measured at flush time (event-driven, no separate ticker).
	bytesPerSec    atomic.Int64
	bytesSinceMeas atomic.Int64
	lastMeasureNS  atomic.Int64

	// bufPoolCh caches reusable plaintext batch buffers - see getBuf/putBuf.
	bufPoolCh chan *[]byte

	// inFlightBytes tracks bytes handed off to a still-running flushRaw
	// goroutine (not yet fully sent). Combined with the current buf.Len()
	// (the queued-but-not-flushed portion), this is the budget admitBytes
	// enforces - see maxQueuedAndInFlightBytes.
	inFlightBytes atomic.Int64
}

func NewBatcher(sendFn func(seq uint64, data []byte, urgent bool) error, key []byte, level CompressionLevel, quietHours *QuietHoursConfig) *Batcher {
	return newBatcher(sendFn, key, level, quietHours, FlushIdleTimeout, MaxFlushDelay, DataFlushThreshold)
}

func newBatcher(sendFn func(seq uint64, data []byte, urgent bool) error, key []byte, level CompressionLevel, quietHours *QuietHoursConfig, idleTimeout, maxFlushDelay time.Duration, dataFlushThreshold int) *Batcher {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(level))
	if err != nil {
		// Only reachable with an invalid EncoderLevel constant, which this
		// package's own ParseCompressionLevel fully controls - a
		// construction-time misconfiguration, not a runtime condition.
		panic(fmt.Sprintf("protocol: zstd.NewWriter: %v", err))
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		panic(fmt.Sprintf("protocol: zstd.NewReader: %v", err))
	}

	b := &Batcher{
		sendFn:             sendFn,
		key:                key,
		enc:                enc,
		dec:                dec,
		idleTimeout:        idleTimeout,
		maxFlushDelay:      maxFlushDelay,
		quietHours:         quietHours,
		dataFlushThreshold: dataFlushThreshold,
		activityCh:         make(chan struct{}, 1),
		flushCh:            make(chan struct{}, 1),
		done:               make(chan struct{}),
		stopped:            make(chan struct{}),
		urgentTok:          make(chan struct{}, 1),
		sharedTok:          make(chan struct{}, 1),
		bufPoolCh:          make(chan *[]byte, batchBufPoolSlots),
	}
	b.urgentTok <- struct{}{}
	b.sharedTok <- struct{}{}
	go b.flushLoop()
	return b
}

// getBuf/putBuf reuse the plaintext batch buffers that Flush fills before
// compression, ported from gdrive's getBatchBuf/putBatchBuf
// (internal/gdrive/mux.go) - avoiding the repeated grow+copy that dominated
// CPU/allocation profiles there under load. Deliberately a plain buffered
// channel, not a sync.Pool: sync.Pool drops everything it holds at the
// start of every GC cycle, so under real load Get() falls through to New()
// on the majority of calls; a channel isn't subject to that GC-driven
// eviction, so buffers genuinely get reused.
func (b *Batcher) getBuf() *[]byte {
	select {
	case buf := <-b.bufPoolCh:
		return buf
	default:
		buf := make([]byte, 0, batchBufCap)
		return &buf
	}
}

// putBuf returns buf to the pool, dropping it instead if it grew past
// batchBufCap (a batch larger than usual) rather than pooling an
// oversized buffer indefinitely - gdrive's v1.0.65 fix.
func (b *Batcher) putBuf(buf *[]byte) {
	if cap(*buf) > batchBufCap {
		return
	}
	*buf = (*buf)[:0]
	select {
	case b.bufPoolCh <- buf:
	default:
		// Pool full; drop it and let GC reclaim rather than blocking the caller.
	}
}

// acquireSlot blocks until a send slot is available for this Batcher's
// 2-slot concurrency gate (see the urgentTok/sharedTok field doc), returning
// a func to release it.
func (b *Batcher) acquireSlot(urgent bool) func() {
	if urgent {
		select {
		case <-b.urgentTok:
			return func() { b.urgentTok <- struct{}{} }
		default:
		}
	}
	<-b.sharedTok
	return func() { b.sharedTok <- struct{}{} }
}

// Add adds a frame to the current batch. DATA frames are flushed after a quiet
// period, while control frames flush immediately so connection setup/teardown
// does not pick up the batching delay. urgent marks whether this frame should
// be eligible for the reserved concurrency slot at send time (see acquireSlot) -
// callers classify control frames as always urgent and TypeData frames via
// each stream's one-way priority latch (see tunnel.Conn).
//
// Add blocks (bounded by admitTimeout) until there is room in the combined
// queued+in-flight byte budget (see maxQueuedAndInFlightBytes), and reports
// false if that timeout is reached without the frame being admitted - the
// caller (Mux.SendData) is responsible for tearing down that one stream
// rather than leaving a silent gap in its Seq-ordered stream.
func (b *Batcher) Add(f *Frame, urgent bool) bool {
	data := f.Marshal()

	if !b.admitBytes(len(data)) {
		fmt.Printf("[batcher] Add: admission timed out after %v (budget full), rejecting frame type=0x%02x connID=%08x\n",
			admitTimeout, f.Type, f.ConnID)
		return false
	}

	now := time.Now()

	b.mu.Lock()
	if b.buf.Len() == 0 {
		b.batchStartedAt = now
		// Rolled once per batch, not recomputed on every timer update, so
		// the hard-cap deadline is a fixed target for this batch instead of
		// flapping around on every new frame's Add call.
		b.jitteredMaxFlushDelay = jitter(b.maxFlushDelay)
	}
	b.lastQueuedAt = now
	b.buf.Write(data)
	bufLen := b.buf.Len()
	if urgent {
		b.hasUrgent = true
	}
	if isControlFrame(f.Type) {
		b.hasControlFrame = true
	}
	b.mu.Unlock()

	shouldFlush := bufLen >= MaxBatchSize
	if f.Type == TypeData && b.dataFlushThreshold > 0 && bufLen >= b.dataFlushThreshold {
		shouldFlush = true
	}

	if shouldFlush {
		b.triggerFlush()
		return true
	}

	// Control frames no longer force an immediate flush here - see
	// controlCoalesceWindow's doc comment. adaptiveIdleTimeout applies that
	// short window automatically once hasControlFrame is set, via the same
	// activity-triggered timer path DATA frames already use.
	b.triggerActivity()
	return true
}

// admitBytes blocks (polling every admitPollInterval) until there is room
// for n more bytes in the combined queued+in-flight budget, or admitTimeout
// elapses. Returns false on timeout.
func (b *Batcher) admitBytes(n int) bool {
	deadline := time.Now().Add(admitTimeout)
	for {
		b.mu.Lock()
		queued := int64(b.buf.Len())
		b.mu.Unlock()
		if queued+b.inFlightBytes.Load()+int64(n) <= maxQueuedAndInFlightBytes {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(admitPollInterval)
	}
}

func (b *Batcher) triggerFlush() {
	select {
	case b.flushCh <- struct{}{}:
	default:
	}
}

func (b *Batcher) triggerActivity() {
	select {
	case b.activityCh <- struct{}{}:
	default:
	}
}

func (b *Batcher) flushLoop() {
	var (
		idleTimer *time.Timer
		idleCh    <-chan time.Time
		maxTimer  *time.Timer
		maxCh     <-chan time.Time
	)

	stopTimer := func(timer **time.Timer, ch *<-chan time.Time) {
		if *timer == nil {
			return
		}
		if !(*timer).Stop() {
			select {
			case <-(*timer).C:
			default:
			}
		}
		*ch = nil
	}

	resetTimer := func(timer **time.Timer, ch *<-chan time.Time, delay time.Duration) {
		if delay < 0 {
			delay = 0
		}
		if *timer == nil {
			*timer = time.NewTimer(delay)
		} else {
			stopTimer(timer, ch)
			(*timer).Reset(delay)
		}
		*ch = (*timer).C
	}

	updateTimers := func() {
		hasData, batchStartedAt, lastQueuedAt, maxDelay := b.batchState()
		if !hasData {
			stopTimer(&idleTimer, &idleCh)
			stopTimer(&maxTimer, &maxCh)
			return
		}

		resetTimer(&idleTimer, &idleCh, time.Until(lastQueuedAt.Add(jitter(b.adaptiveIdleTimeout()))))
		resetTimer(&maxTimer, &maxCh, time.Until(batchStartedAt.Add(maxDelay)))
	}

	defer stopTimer(&idleTimer, &idleCh)
	defer stopTimer(&maxTimer, &maxCh)

	for {
		select {
		case <-b.activityCh:
			updateTimers()
		case <-idleCh:
			b.Flush()
			updateTimers()
		case <-maxCh:
			b.Flush()
			updateTimers()
		case <-b.flushCh:
			b.Flush()
			updateTimers()
		case <-b.done:
			b.Flush()
			b.waitFlushesWithTimeout(shutdownFlushTimeout)
			b.enc.Close()
			close(b.stopped)
			return
		}
	}
}

// waitFlushesWithTimeout blocks until every in-flight flushRaw goroutine
// (tracked via flushWG) has finished, or timeout elapses - whichever comes
// first. Bounded so a stuck/slow-retrying send can't hang shutdown forever.
func (b *Batcher) waitFlushesWithTimeout(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		b.flushWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Println("[batcher] shutdown: timed out waiting for in-flight flushes")
	}
}

// Flush compresses and sends the current batch. The batch's sequence number
// is minted here, synchronously with the flushLoop goroutine, so it reflects
// batch-creation order even though the actual compress/seal/send work below
// runs concurrently on its own goroutine (see acquireSlot) - the poller on
// the receiving end sorts by this seq before dispatching frames, so
// creation-order sequencing is the only ordering guarantee that actually
// matters; out-of-order arrival/completion of the network sends themselves
// is fine by design.
func (b *Batcher) Flush() {
	b.mu.Lock()
	if b.buf.Len() == 0 {
		b.mu.Unlock()
		return
	}
	bufPtr := b.getBuf()
	*bufPtr = append((*bufPtr)[:0], b.buf.Bytes()...)
	raw := *bufPtr
	b.buf.Reset()
	b.batchStartedAt = time.Time{}
	b.lastQueuedAt = time.Time{}
	urgent := b.hasUrgent
	b.hasUrgent = false
	b.hasControlFrame = false
	b.mu.Unlock()

	seq := b.seqNum.Add(1)
	b.updateBytesPerSec(int64(len(raw)))
	b.inFlightBytes.Add(int64(len(raw)))
	vtellog.Debugf("[batcher] flush seq=%d raw=%d bytes urgent=%v", seq, len(raw), urgent)
	b.flushWG.Add(1)
	go func() {
		defer b.flushWG.Done()
		b.flushRaw(seq, raw, urgent)
		b.putBuf(bufPtr)
		b.inFlightBytes.Add(-int64(len(raw)))
	}()
}

func (b *Batcher) batchState() (hasData bool, batchStartedAt, lastQueuedAt time.Time, jitteredMaxFlushDelay time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() == 0 {
		return false, time.Time{}, time.Time{}, 0
	}
	return true, b.batchStartedAt, b.lastQueuedAt, b.jitteredMaxFlushDelay
}

// adaptiveIdleTimeout ports gdrive's muxLane.adaptiveCorkDelay tiers
// (internal/gdrive/mux.go): under sustained load, a shorter idle debounce
// keeps latency down; near-idle traffic falls back to idleTimeout instead of
// gdrive's own 10ms default. Deliberate deviation: a Drive PUT is cheap
// enough that gdrive can flush an idle trickle every 10ms for free, but a
// Telegram flush is a real API call gated by RateLimiter's ~400ms-1s
// interval - flushing that often while idle would just queue tiny calls
// behind the limiter for no gain. maxFlushDelay and dataFlushThreshold are
// NOT made adaptive; only the idle timeout is. During a configured quiet
// hours window (Stage 8), the result is further multiplied by
// QuietHoursMultiplier rather than pausing sends entirely.
func (b *Batcher) adaptiveIdleTimeout() time.Duration {
	base := b.baseAdaptiveIdleTimeout()
	if b.quietHours.Active(time.Now()) {
		return time.Duration(float64(base) * QuietHoursMultiplier)
	}
	return base
}

func (b *Batcher) baseAdaptiveIdleTimeout() time.Duration {
	tier := func() time.Duration {
		switch bps := b.bytesPerSec.Load(); {
		case bps > 10*1024*1024:
			return 5 * time.Millisecond
		case bps > 1*1024*1024:
			return 10 * time.Millisecond
		case bps > 100*1024:
			return 15 * time.Millisecond
		default:
			return b.idleTimeout
		}
	}()

	b.mu.Lock()
	hasControl := b.hasControlFrame
	b.mu.Unlock()
	if hasControl && controlCoalesceWindow < tier {
		return controlCoalesceWindow
	}
	return tier
}

// BytesPerSec returns the current throughput measurement (see
// updateBytesPerSec) - exposed for pool.Link's load-balancer tiebreak signal.
func (b *Batcher) BytesPerSec() int64 {
	return b.bytesPerSec.Load()
}

// updateBytesPerSec is a direct port of gdrive's muxLane.updateBytesPerSec:
// a rolling one-second measurement window, updated at flush time rather
// than via a separate ticker goroutine.
func (b *Batcher) updateBytesPerSec(newBytes int64) {
	now := time.Now().UnixNano()
	b.bytesSinceMeas.Add(newBytes)
	last := b.lastMeasureNS.Load()
	if interval := now - last; interval >= int64(time.Second) {
		if b.lastMeasureNS.CompareAndSwap(last, now) {
			measured := b.bytesSinceMeas.Swap(0)
			if interval > 0 {
				b.bytesPerSec.Store(measured * int64(time.Second) / interval)
			}
		}
	}
}

// flushRaw compresses raw bytes and sends, splitting if compressed size
// exceeds the limit. Runs on its own goroutine per Flush call (or per split
// half); the actual network send only proceeds once acquireSlot grants a
// concurrency slot, so a slow bulk send here can never block flushLoop from
// starting the next batch.
func (b *Batcher) flushRaw(seq uint64, raw []byte, urgent bool) {
	compressed := b.enc.EncodeAll(raw, nil)

	if len(compressed) > MaxCompressedSize {
		// Split raw in half and send as two batches. The first half keeps
		// this batch's seq; the second mints a fresh one - both are rare
		// (only once a single batch alone exceeds ~19MB compressed), so
		// their ordering relative to unrelated concurrently-flushing
		// batches is not load-bearing the way the common case's
		// creation-order seq is.
		mid := len(raw) / 2
		b.flushRaw(seq, raw[:mid], urgent)
		b.flushRaw(b.seqNum.Add(1), raw[mid:], urgent)
		return
	}

	sealed, err := SealEnvelope(b.key, compressed)
	if err != nil {
		fmt.Printf("[batcher] envelope seal error: %v\n", err)
		return
	}

	release := b.acquireSlot(urgent)
	defer release()
	b.sendWithRetry(seq, sealed, urgent)
}

// sendWithRetry sends a compressed batch with exponential backoff retries.
func (b *Batcher) sendWithRetry(seq uint64, compressed []byte, urgent bool) {
	backoff := initialRetryBackoff

	for attempt := 0; attempt <= maxSendRetries; attempt++ {
		sendStart := time.Now()
		err := b.sendFn(seq, compressed, urgent)
		if err == nil {
			vtellog.Debugf("[batcher] sent seq=%d compressed=%d bytes in %v (attempt %d)", seq, len(compressed), time.Since(sendStart), attempt+1)
			return
		}
		if isPermanentError(err) {
			fmt.Printf("[batcher] DROPPED batch seq=%d due to permanent error: %v\n", seq, err)
			return
		}
		fmt.Printf("[batcher] send error (seq=%d, attempt=%d/%d): %v\n", seq, attempt+1, maxSendRetries+1, err)
		if attempt < maxSendRetries {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxRetryBackoff {
				backoff = maxRetryBackoff
			}
		}
	}
	fmt.Printf("[batcher] DROPPED batch seq=%d after %d retries\n", seq, maxSendRetries+1)
}

// Stop flushes any pending batch, waits (up to shutdownFlushTimeout) for
// that final flush's send to complete, and closes the encoder - blocking
// until the flush loop has fully drained rather than merely signaling it to
// do so, so a caller sequencing Stop after a peer-notification step (see
// Mux.CloseAllNotify) can rely on those frames having actually been sent
// once Stop returns. The decoder is left open since DecompressBatch may
// still be in flight on the receive side when Stop is called.
func (b *Batcher) Stop() {
	close(b.done)
	<-b.stopped
}

func isControlFrame(frameType byte) bool {
	return frameType != TypeData
}

func isPermanentError(err error) bool {
	var permanent interface{ Permanent() bool }
	return errors.As(err, &permanent) && permanent.Permanent()
}

// DecompressBatch decompresses a zstd batch (using this Batcher's shared
// decoder) and returns all frames.
func (b *Batcher) DecompressBatch(data []byte) ([]*Frame, error) {
	raw, err := b.dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("zstd decode: %w", err)
	}

	var frames []*Frame
	reader := bytes.NewReader(raw)
	for reader.Len() > 0 {
		f, err := ReadFrame(reader)
		if err != nil {
			return nil, fmt.Errorf("read frame: %w", err)
		}
		frames = append(frames, f)
	}
	return frames, nil
}
