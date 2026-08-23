package protocol

import (
	"testing"
	"time"
)

func TestSendWithRetryDoesNotRetryPermanentErrors(t *testing.T) {
	t.Parallel()

	calls := 0
	batcher := &Batcher{
		sendFn: func(seq uint64, data []byte) error {
			calls++
			return permanentBatchError{}
		},
	}

	batcher.sendWithRetry([]byte("payload"))

	if calls != 1 {
		t.Fatalf("sendWithRetry() calls = %d, want 1", calls)
	}
}

func TestBatcherFlushesContinuousDataOnMaxDelay(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	level, err := ParseCompressionLevel("")
	if err != nil {
		t.Fatalf("ParseCompressionLevel: %v", err)
	}

	flushedAt := make(chan time.Time, 1)
	batcher := newBatcher(func(seq uint64, data []byte) error {
		select {
		case flushedAt <- time.Now():
		default:
		}
		return nil
	}, key, level, 120*time.Millisecond, 220*time.Millisecond, 1<<20)
	defer batcher.Stop()

	frame := &Frame{
		Type:    TypeData,
		ConnID:  1,
		Payload: []byte("payload"),
	}

	start := time.Now()
	batcher.Add(frame)
	time.Sleep(80 * time.Millisecond)
	batcher.Add(frame)
	time.Sleep(80 * time.Millisecond)
	batcher.Add(frame)

	select {
	case ts := <-flushedAt:
		if got := ts.Sub(start); got > 320*time.Millisecond {
			t.Fatalf("flush after %v, want <= 320ms", got)
		}
	case <-time.After(600 * time.Millisecond):
		t.Fatal("batch did not flush on max delay")
	}
}

func TestBatcherRoundTripsThroughEnvelopeAndZstd(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	sent := make(chan []byte, 1)
	batcher := NewBatcher(func(seq uint64, data []byte) error {
		sent <- data
		return nil
	}, key, zstdTestLevel(t))
	defer batcher.Stop()

	batcher.Add(&Frame{Type: TypeConnect, ConnID: 7, Payload: []byte("connect payload")})

	var sealed []byte
	select {
	case sealed = <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("batch never flushed")
	}

	compressed, ok, err := OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	if !ok {
		t.Fatal("OpenEnvelope: ok=false for a freshly sealed batch")
	}

	frames, err := batcher.DecompressBatch(compressed)
	if err != nil {
		t.Fatalf("DecompressBatch: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if frames[0].ConnID != 7 || string(frames[0].Payload) != "connect payload" {
		t.Fatalf("frame mismatch: %+v", frames[0])
	}
}

func TestAdaptiveIdleTimeoutTierBoundaries(t *testing.T) {
	t.Parallel()

	b := &Batcher{idleTimeout: 250 * time.Millisecond}

	cases := []struct {
		bps  int64
		want time.Duration
	}{
		{0, 250 * time.Millisecond},
		{100 * 1024, 250 * time.Millisecond},      // at the >100KB/s boundary, not above it
		{100*1024 + 1, 15 * time.Millisecond},     // just above -> 15ms tier
		{1 * 1024 * 1024, 15 * time.Millisecond},  // at the >1MB/s boundary, not above it
		{1*1024*1024 + 1, 10 * time.Millisecond},  // just above -> 10ms tier
		{10 * 1024 * 1024, 10 * time.Millisecond}, // at the >10MB/s boundary, not above it
		{10*1024*1024 + 1, 5 * time.Millisecond},  // just above -> 5ms tier
		{100 * 1024 * 1024, 5 * time.Millisecond}, // well above -> still 5ms
	}
	for _, c := range cases {
		b.bytesPerSec.Store(c.bps)
		if got := b.adaptiveIdleTimeout(); got != c.want {
			t.Errorf("adaptiveIdleTimeout() at %d B/s = %v, want %v", c.bps, got, c.want)
		}
	}
}

func TestUpdateBytesPerSecMeasuresAfterOneSecondWindow(t *testing.T) {
	t.Parallel()

	b := &Batcher{}
	b.lastMeasureNS.Store(time.Now().UnixNano())

	// Within the same window: accumulates but does not yet update bytesPerSec.
	b.updateBytesPerSec(1024)
	if got := b.bytesPerSec.Load(); got != 0 {
		t.Fatalf("bytesPerSec updated mid-window: got %d, want 0", got)
	}

	// Force the window to have elapsed, then the next call measures.
	b.lastMeasureNS.Store(time.Now().Add(-2 * time.Second).UnixNano())
	b.bytesSinceMeas.Store(2 * 1024 * 1024)
	b.updateBytesPerSec(0)

	if got := b.bytesPerSec.Load(); got <= 0 {
		t.Fatalf("bytesPerSec after window elapsed = %d, want > 0", got)
	}
}

func zstdTestLevel(t *testing.T) CompressionLevel {
	t.Helper()
	level, err := ParseCompressionLevel("fastest")
	if err != nil {
		t.Fatalf("ParseCompressionLevel: %v", err)
	}
	return level
}

type permanentBatchError struct{}

func (permanentBatchError) Error() string {
	return "permanent"
}

func (permanentBatchError) Permanent() bool {
	return true
}
