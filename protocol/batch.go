package protocol

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	FlushInterval    = 500 * time.Millisecond
	MaxBatchSize     = 4 * 1024 * 1024 // 4MB uncompressed
)

// Batcher collects frames and flushes them as gzip-compressed batches.
type Batcher struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	seqNum atomic.Uint64
	sendFn func(seq uint64, data []byte) error

	flushCh chan struct{} // signal immediate flush
	done    chan struct{}
}

func NewBatcher(sendFn func(seq uint64, data []byte) error) *Batcher {
	b := &Batcher{
		sendFn:  sendFn,
		flushCh: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go b.flushLoop()
	return b
}

// Add adds a frame to the current batch. Triggers immediate flush for CONNECT/CONNECT_ACK.
func (b *Batcher) Add(f *Frame) {
	data := f.Marshal()
	b.mu.Lock()
	b.buf.Write(data)
	shouldFlush := f.Type == TypeConnect || f.Type == TypeConnectACK || b.buf.Len() >= MaxBatchSize
	b.mu.Unlock()

	if shouldFlush {
		b.triggerFlush()
	}
}

func (b *Batcher) triggerFlush() {
	select {
	case b.flushCh <- struct{}{}:
	default:
	}
}

func (b *Batcher) flushLoop() {
	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			b.Flush()
		case <-b.flushCh:
			b.Flush()
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

	compressed, err := gzipCompress(raw)
	if err != nil {
		fmt.Printf("[batcher] compress error: %v\n", err)
		return
	}

	seq := b.seqNum.Add(1)
	if err := b.sendFn(seq, compressed); err != nil {
		fmt.Printf("[batcher] send error (seq=%d): %v\n", seq, err)
	}
}

func (b *Batcher) Stop() {
	close(b.done)
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
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
