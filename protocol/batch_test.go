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

	flushedAt := make(chan time.Time, 1)
	batcher := newBatcher(func(seq uint64, data []byte) error {
		select {
		case flushedAt <- time.Now():
		default:
		}
		return nil
	}, key, 120*time.Millisecond, 220*time.Millisecond, 1<<20)
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

type permanentBatchError struct{}

func (permanentBatchError) Error() string {
	return "permanent"
}

func (permanentBatchError) Permanent() bool {
	return true
}
