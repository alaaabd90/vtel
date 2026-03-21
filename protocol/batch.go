package protocol

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	FlushIdleTimeout  = 2 * time.Second
	MaxBatchSize      = 48 * 1024 * 1024 // 48MB uncompressed
	MaxCompressedSize = 19 * 1024 * 1024 // 19MB compressed (Telegram getFile limit is 20MB)

	maxSendRetries      = 5
	initialRetryBackoff = 1 * time.Second
	maxRetryBackoff     = 30 * time.Second
)

// Batcher collects frames and flushes them as gzip-compressed batches.
type Batcher struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	seqNum atomic.Uint64
	sendFn func(seq uint64, data []byte) error

	idleTimeout  time.Duration
	lastQueuedAt atomic.Int64
	activityCh   chan struct{} // signal queued data so the idle timer can be reset
	flushCh      chan struct{} // signal immediate flush
	done         chan struct{}
}

func NewBatcher(sendFn func(seq uint64, data []byte) error) *Batcher {
	return newBatcher(sendFn, FlushIdleTimeout)
}

func newBatcher(sendFn func(seq uint64, data []byte) error, idleTimeout time.Duration) *Batcher {
	b := &Batcher{
		sendFn:      sendFn,
		idleTimeout: idleTimeout,
		activityCh:  make(chan struct{}, 1),
		flushCh:     make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	go b.flushLoop()
	return b
}

// Add adds a frame to the current batch. DATA frames are flushed after a quiet
// period, while control frames flush immediately so connection setup/teardown
// does not pick up the batching delay.
func (b *Batcher) Add(f *Frame) {
	data := f.Marshal()
	b.mu.Lock()
	b.buf.Write(data)
	shouldFlush := b.buf.Len() >= MaxBatchSize
	b.mu.Unlock()

	b.lastQueuedAt.Store(time.Now().UnixNano())

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
		timer   *time.Timer
		timerCh <-chan time.Time
	)

	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	resetTimer := func() {
		if !b.hasBufferedData() {
			stopTimer()
			timerCh = nil
			return
		}

		deadline := time.Unix(0, b.lastQueuedAt.Load()).Add(b.idleTimeout)
		delay := time.Until(deadline)
		if delay < 0 {
			delay = 0
		}

		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			stopTimer()
			timer.Reset(delay)
		}
		timerCh = timer.C
	}

	defer stopTimer()

	for {
		select {
		case <-b.activityCh:
			resetTimer()
		case <-timerCh:
			b.Flush()
			resetTimer()
		case <-b.flushCh:
			b.Flush()
			resetTimer()
		case <-b.done:
			b.Flush()
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
	b.mu.Unlock()

	b.flushRaw(raw)
}

func (b *Batcher) hasBufferedData() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len() > 0
}

// flushRaw compresses raw bytes and sends, splitting if compressed size exceeds the limit.
func (b *Batcher) flushRaw(raw []byte) {
	compressed, err := gzipCompress(raw)
	if err != nil {
		fmt.Printf("[batcher] compress error: %v\n", err)
		return
	}

	if len(compressed) > MaxCompressedSize {
		// Split raw in half and send as two batches
		mid := len(raw) / 2
		b.flushRaw(raw[:mid])
		b.flushRaw(raw[mid:])
		return
	}

	b.sendWithRetry(compressed)
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

func (b *Batcher) Stop() {
	close(b.done)
}

func isControlFrame(frameType byte) bool {
	return frameType != TypeData
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func isPermanentError(err error) bool {
	var permanent interface{ Permanent() bool }
	return errors.As(err, &permanent) && permanent.Permanent()
}

// DecompressBatch decompresses a gzip batch and returns all frames.
func DecompressBatch(data []byte) ([]*Frame, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip open: %w", err)
	}
	defer r.Close()

	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("gzip read: %w", err)
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
