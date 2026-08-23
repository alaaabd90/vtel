package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	FlushIdleTimeout   = 250 * time.Millisecond
	MaxFlushDelay      = 750 * time.Millisecond
	DataFlushThreshold = 32 * 1024        // Flush active streams before they accumulate too much latency.
	MaxBatchSize       = 48 * 1024 * 1024 // 48MB uncompressed
	MaxCompressedSize  = 19 * 1024 * 1024 // 19MB compressed (Telegram getFile limit is 20MB)

	maxSendRetries      = 5
	initialRetryBackoff = 1 * time.Second
	maxRetryBackoff     = 30 * time.Second

	// StreamPriorityBytes is the per-connection one-way latch threshold
	// ported from gdrive's priority classification (internal/gdrive/mux.go):
	// a stream's TypeData frames are urgent until this many bytes have been
	// sent on it, then permanently demoted to normal. Control frames
	// (CONNECT/CONNECT_ACK/CLOSE/RESET) are always urgent regardless.
	StreamPriorityBytes = 512 * 1024
)

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
	mu        sync.Mutex
	buf       bytes.Buffer
	hasUrgent bool // whether the in-progress batch contains any urgent frame
	seqNum    atomic.Uint64
	sendFn    func(seq uint64, data []byte, urgent bool) error
	key       []byte // AES-256-GCM key from DeriveKey, applied to every flush

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
	activityCh         chan struct{} // signal queued data so timers can be updated
	flushCh            chan struct{} // signal immediate flush
	done               chan struct{}

	// Throughput measurement for adaptiveIdleTimeout, ported from gdrive's
	// muxLane.updateBytesPerSec/adaptiveCorkDelay: a rolling one-second
	// window measured at flush time (event-driven, no separate ticker).
	bytesPerSec    atomic.Int64
	bytesSinceMeas atomic.Int64
	lastMeasureNS  atomic.Int64
}

func NewBatcher(sendFn func(seq uint64, data []byte, urgent bool) error, key []byte, level CompressionLevel) *Batcher {
	return newBatcher(sendFn, key, level, FlushIdleTimeout, MaxFlushDelay, DataFlushThreshold)
}

func newBatcher(sendFn func(seq uint64, data []byte, urgent bool) error, key []byte, level CompressionLevel, idleTimeout, maxFlushDelay time.Duration, dataFlushThreshold int) *Batcher {
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
		dataFlushThreshold: dataFlushThreshold,
		activityCh:         make(chan struct{}, 1),
		flushCh:            make(chan struct{}, 1),
		done:               make(chan struct{}),
		urgentTok:          make(chan struct{}, 1),
		sharedTok:          make(chan struct{}, 1),
	}
	b.urgentTok <- struct{}{}
	b.sharedTok <- struct{}{}
	go b.flushLoop()
	return b
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
func (b *Batcher) Add(f *Frame, urgent bool) {
	data := f.Marshal()
	now := time.Now()

	b.mu.Lock()
	if b.buf.Len() == 0 {
		b.batchStartedAt = now
	}
	b.lastQueuedAt = now
	b.buf.Write(data)
	bufLen := b.buf.Len()
	if urgent {
		b.hasUrgent = true
	}
	b.mu.Unlock()

	shouldFlush := bufLen >= MaxBatchSize
	if f.Type == TypeData && b.dataFlushThreshold > 0 && bufLen >= b.dataFlushThreshold {
		shouldFlush = true
	}

	if shouldFlush || isControlFrame(f.Type) {
		b.triggerFlush()
		return
	}

	b.triggerActivity()
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
		hasData, batchStartedAt, lastQueuedAt := b.batchState()
		if !hasData {
			stopTimer(&idleTimer, &idleCh)
			stopTimer(&maxTimer, &maxCh)
			return
		}

		resetTimer(&idleTimer, &idleCh, time.Until(lastQueuedAt.Add(b.adaptiveIdleTimeout())))
		resetTimer(&maxTimer, &maxCh, time.Until(batchStartedAt.Add(b.maxFlushDelay)))
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
			b.enc.Close()
			return
		}
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
	raw := make([]byte, b.buf.Len())
	copy(raw, b.buf.Bytes())
	b.buf.Reset()
	b.batchStartedAt = time.Time{}
	b.lastQueuedAt = time.Time{}
	urgent := b.hasUrgent
	b.hasUrgent = false
	b.mu.Unlock()

	seq := b.seqNum.Add(1)
	b.updateBytesPerSec(int64(len(raw)))
	go b.flushRaw(seq, raw, urgent)
}

func (b *Batcher) batchState() (hasData bool, batchStartedAt, lastQueuedAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() == 0 {
		return false, time.Time{}, time.Time{}
	}
	return true, b.batchStartedAt, b.lastQueuedAt
}

// adaptiveIdleTimeout ports gdrive's muxLane.adaptiveCorkDelay tiers
// (internal/gdrive/mux.go): under sustained load, a shorter idle debounce
// keeps latency down; near-idle traffic falls back to idleTimeout instead of
// gdrive's own 10ms default. Deliberate deviation: a Drive PUT is cheap
// enough that gdrive can flush an idle trickle every 10ms for free, but a
// Telegram flush is a real API call gated by RateLimiter's ~400ms-1s
// interval - flushing that often while idle would just queue tiny calls
// behind the limiter for no gain. maxFlushDelay and dataFlushThreshold are
// NOT made adaptive; only the idle timeout is.
func (b *Batcher) adaptiveIdleTimeout() time.Duration {
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
		err := b.sendFn(seq, compressed, urgent)
		if err == nil {
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

// Stop flushes any pending batch and stops the flush loop. The encoder is
// closed once that final flush completes; the decoder is left open since
// DecompressBatch may still be in flight on the receive side when Stop is
// called (full shutdown ordering/resource teardown is Stage 7's job).
func (b *Batcher) Stop() {
	close(b.done)
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
