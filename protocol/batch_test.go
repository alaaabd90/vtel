package protocol

import "testing"

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

type permanentBatchError struct{}

func (permanentBatchError) Error() string {
	return "permanent"
}

func (permanentBatchError) Permanent() bool {
	return true
}
