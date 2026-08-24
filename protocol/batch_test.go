package protocol

import (
	"testing"
	"time"
)

func TestSendWithRetryDoesNotRetryPermanentErrors(t *testing.T) {
	t.Parallel()

	calls := 0
	batcher := &Batcher{
		sendFn: func(seq uint64, data []byte, urgent bool) error {
			calls++
			return permanentBatchError{}
		},
	}

	batcher.sendWithRetry(1, []byte("payload"), false)

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
	batcher := newBatcher(func(seq uint64, data []byte, urgent bool) error {
		select {
		case flushedAt <- time.Now():
		default:
		}
		return nil
	}, key, level, nil, 120*time.Millisecond, 220*time.Millisecond, 1<<20)
	defer batcher.Stop()

	frame := &Frame{
		Type:    TypeData,
		ConnID:  1,
		Payload: []byte("payload"),
	}

	start := time.Now()
	batcher.Add(frame, false)
	time.Sleep(80 * time.Millisecond)
	batcher.Add(frame, false)
	time.Sleep(80 * time.Millisecond)
	batcher.Add(frame, false)

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
	batcher := NewBatcher(func(seq uint64, data []byte, urgent bool) error {
		sent <- data
		return nil
	}, key, zstdTestLevel(t), nil)
	defer batcher.Stop()

	batcher.Add(&Frame{Type: TypeConnect, ConnID: 7, Payload: []byte("connect payload")}, true)

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

func TestAdmitBytesSucceedsUnderBudget(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	if !b.admitBytes(1024) {
		t.Fatal("admitBytes(1024) = false under an empty budget, want true")
	}
}

func TestAdmitBytesBlocksThenSucceedsOnceBudgetFrees(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	// Fill the budget so the next admitBytes call must block.
	b.inFlightBytes.Store(maxQueuedAndInFlightBytes)

	freedAt := make(chan time.Time, 1)
	go func() {
		time.Sleep(60 * time.Millisecond)
		b.inFlightBytes.Store(0)
		freedAt <- time.Now()
	}()

	start := time.Now()
	if !b.admitBytes(1024) {
		t.Fatal("admitBytes(1024) = false, want true once budget freed")
	}
	admitted := time.Now()

	select {
	case freed := <-freedAt:
		if admitted.Before(freed) {
			t.Fatalf("admitBytes returned before the budget actually freed (admitted %v before free %v)", admitted.Sub(start), freed.Sub(start))
		}
	default:
		t.Fatal("admitBytes returned before the freeing goroutine ran at all")
	}
}

func TestAddReturnsFalseOnAdmissionTimeout(t *testing.T) {
	// Mutates the package-level admitTimeout var - must not run in parallel
	// with other tests that call Add/admitBytes.
	orig := admitTimeout
	admitTimeout = 30 * time.Millisecond
	defer func() { admitTimeout = orig }()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	b.inFlightBytes.Store(maxQueuedAndInFlightBytes) // never freed in this test

	got := b.Add(&Frame{Type: TypeData, ConnID: 1, Payload: []byte("x")}, false)
	if got {
		t.Fatal("Add() = true with the budget permanently full, want false (admission timeout)")
	}
}

func TestFlushTracksInFlightBytesAndReleasesOnCompletion(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	release := make(chan struct{})
	sent := make(chan struct{})
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error {
		close(sent)
		<-release // hold the flushRaw goroutine open until the test says go
		return nil
	}, key, zstdTestLevel(t), nil)
	defer func() {
		close(release)
		b.Stop()
	}()

	b.Add(&Frame{Type: TypeConnect, ConnID: 1, Payload: []byte("connect payload")}, true)

	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("batch never reached sendFn")
	}

	if b.inFlightBytes.Load() <= 0 {
		t.Fatal("inFlightBytes = 0 while a flushRaw goroutine is still blocked in sendFn, want > 0")
	}
}

func TestGetBufPutBufReusesCapacity(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	buf1 := b.getBuf()
	orig := *buf1
	*buf1 = append(*buf1, []byte("hello")...)
	b.putBuf(buf1)

	buf2 := b.getBuf()
	if len(*buf2) != 0 {
		t.Fatalf("reused buffer len = %d, want 0", len(*buf2))
	}
	if cap(*buf2) != cap(orig) {
		t.Fatalf("expected the pooled buffer's capacity to be reused, got cap %d want %d", cap(*buf2), cap(orig))
	}
}

func TestPutBufDropsOversizedBuffer(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	oversized := make([]byte, 0, batchBufCap+1)
	b.putBuf(&oversized)

	select {
	case <-b.bufPoolCh:
		t.Fatal("oversized buffer was pooled instead of dropped")
	default:
	}
}

func TestStopBlocksUntilFinalFlushCompletes(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	sent := make(chan struct{}, 1)
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error {
		sent <- struct{}{}
		return nil
	}, key, zstdTestLevel(t), nil)

	b.Add(&Frame{Type: TypeConnect, ConnID: 1, Payload: []byte("x")}, true)
	b.Stop() // must not return until the flush above has actually been sent

	select {
	case <-sent:
	default:
		t.Fatal("Stop() returned before the final flush's send completed")
	}
}

func TestAcquireSlotUrgentDoesNotBlockBehindBusySharedSlot(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	releaseShared := b.acquireSlot(false) // occupy the shared slot, never released in this test
	_ = releaseShared

	done := make(chan struct{})
	go func() {
		release := b.acquireSlot(true) // should succeed immediately via the reserved slot
		release()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("urgent acquireSlot blocked behind a busy shared slot")
	}
}

func TestAcquireSlotNormalBlocksUntilSharedSlotFrees(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	releaseShared := b.acquireSlot(false)

	acquired := make(chan struct{})
	go func() {
		release := b.acquireSlot(false) // normal must wait for the shared slot
		close(acquired)
		release()
	}()

	select {
	case <-acquired:
		t.Fatal("normal acquireSlot proceeded while the shared slot was busy")
	case <-time.After(150 * time.Millisecond):
	}

	releaseShared()

	select {
	case <-acquired:
	case <-time.After(1 * time.Second):
		t.Fatal("normal acquireSlot never proceeded after the shared slot freed")
	}
}

func TestAcquireSlotUrgentFallsBackToSharedWhenReservedBusy(t *testing.T) {
	t.Parallel()

	key, err := DeriveKey("test-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	b := NewBatcher(func(seq uint64, data []byte, urgent bool) error { return nil }, key, zstdTestLevel(t), nil)
	defer b.Stop()

	releaseUrgent := b.acquireSlot(true) // occupy the reserved slot
	defer releaseUrgent()

	done := make(chan struct{})
	go func() {
		release := b.acquireSlot(true) // reserved is busy -> falls back to the free shared slot
		release()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("second urgent acquireSlot did not fall back to the shared slot")
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
