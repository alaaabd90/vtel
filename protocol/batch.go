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
	mu     sync.Mutex
	buf    bytes.Buffer
	seqNum atomic.Uint64
	sendFn func(seq uint64, data []byte) error
	key    []byte // AES-256-GCM key from DeriveKey, applied to every flush

	// enc/dec are constructed once and reused across every flush/receive on
	// this Batcher - a fresh zstd encoder per flush is a known real
	// performance trap (each one allocates its own internal tables).
	enc *zstd.Encoder
	dec *zstd.Decoder

	idleTimeout        time.Duration
	maxFlushDelay      time.Duration
	dataFlushThreshold int
	batchStartedAt     time.Time
	lastQueuedAt       time.Time
	activityCh         chan struct{} // signal queued data so timers can be updated
	flushCh            chan struct{} // signal immediate flush
	done               chan struct{}
}

func NewBatcher(sendFn func(seq uint64, data []byte) error, key []byte, level CompressionLevel) *Batcher {
	return newBatcher(sendFn, key, level, FlushIdleTimeout, MaxFlushDelay, DataFlushThreshold)
}

func newBatcher(sendFn func(seq uint64, data []byte) error, key []byte, level CompressionLevel, idleTimeout, maxFlushDelay time.Duration, dataFlushThreshold int) *Batcher {
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
	}
	go b.flushLoop()
	return b
}

// Add adds a frame to the current batch. DATA frames are flushed after a quiet
// period, while control frames flush immediately so connection setup/teardown
// does not pick up the batching delay.
func (b *Batcher) Add(f *Frame) {
	data := f.Marshal()
	now := time.Now()

	b.mu.Lock()
	if b.buf.Len() == 0 {
		b.batchStartedAt = now
	}
	b.lastQueuedAt = now
	b.buf.Write(data)
	bufLen := b.buf.Len()
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

		resetTimer(&idleTimer, &idleCh, time.Until(lastQueuedAt.Add(b.idleTimeout)))
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

// Flush compresses and sends the current batch.
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
	b.mu.Unlock()

	b.flushRaw(raw)
}

func (b *Batcher) batchState() (hasData bool, batchStartedAt, lastQueuedAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() == 0 {
		return false, time.Time{}, time.Time{}
	}
	return true, b.batchStartedAt, b.lastQueuedAt
}

// flushRaw compresses raw bytes and sends, splitting if compressed size exceeds the limit.
func (b *Batcher) flushRaw(raw []byte) {
	compressed := b.enc.EncodeAll(raw, nil)

	if len(compressed) > MaxCompressedSize {
		// Split raw in half and send as two batches
		mid := len(raw) / 2
		b.flushRaw(raw[:mid])
		b.flushRaw(raw[mid:])
		return
	}

	sealed, err := SealEnvelope(b.key, compressed)
	if err != nil {
		fmt.Printf("[batcher] envelope seal error: %v\n", err)
		return
	}

	b.sendWithRetry(sealed)
}

// sendWithRetry sends a compressed batch with exponential backoff retries.
func (b *Batcher) sendWithRetry(compressed []byte) {
	seq := b.seqNum.Add(1)
	backoff := initialRetryBackoff

	for attempt := 0; attempt <= maxSendRetries; attempt++ {
		err := b.sendFn(seq, compressed)
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
