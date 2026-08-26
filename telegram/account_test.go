package telegram

import (
	"context"
	"testing"
	"time"
)

func newTestAccountAPI() *AccountAPI {
	return &AccountAPI{
		ctx:      context.Background(),
		notifyCh: make(chan struct{}),
		docCache: make(map[string][]byte),
	}
}

func TestAccountAPIGetUpdatesReturnsAlreadyBufferedItems(t *testing.T) {
	t.Parallel()

	a := newTestAccountAPI()
	a.push(Update{UpdateID: 5, ChannelPost: &ChannelPost{Text: "hi"}})

	got, err := a.GetUpdates(0, 1)
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if len(got) != 1 || got[0].UpdateID != 5 {
		t.Fatalf("GetUpdates() = %+v, want one update with UpdateID=5", got)
	}
}

func TestAccountAPIGetUpdatesDropsStaleItemsBelowOffset(t *testing.T) {
	t.Parallel()

	a := newTestAccountAPI()
	a.push(Update{UpdateID: 1, ChannelPost: &ChannelPost{Text: "old"}})
	a.push(Update{UpdateID: 9, ChannelPost: &ChannelPost{Text: "new"}})

	got, err := a.GetUpdates(5, 1)
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if len(got) != 1 || got[0].UpdateID != 9 {
		t.Fatalf("GetUpdates(offset=5) = %+v, want only UpdateID=9", got)
	}

	// The stale item must also be gone from the internal buffer, not just
	// excluded from this one response - a later GetUpdates with a lower
	// offset than 5 (which shouldn't normally happen, but proves the
	// buffer was actually trimmed) must not resurrect it.
	got2, err := a.GetUpdates(0, 0)
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	for _, u := range got2 {
		if u.UpdateID == 1 {
			t.Fatalf("stale UpdateID=1 resurfaced after being dropped: %+v", got2)
		}
	}
}

func TestAccountAPIGetUpdatesBlocksUntilPush(t *testing.T) {
	t.Parallel()

	a := newTestAccountAPI()
	go func() {
		time.Sleep(20 * time.Millisecond)
		a.push(Update{UpdateID: 42, ChannelPost: &ChannelPost{Text: "later"}})
	}()

	start := time.Now()
	got, err := a.GetUpdates(0, 2)
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if len(got) != 1 || got[0].UpdateID != 42 {
		t.Fatalf("GetUpdates() = %+v, want one update with UpdateID=42", got)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("GetUpdates() took %v, want it to wake on push well before the 2s timeout", elapsed)
	}
}

func TestAccountAPIGetUpdatesTimesOutWithNoData(t *testing.T) {
	t.Parallel()

	a := newTestAccountAPI()
	start := time.Now()
	got, err := a.GetUpdates(0, 0) // timeout=0: should return immediately, not hang
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if got != nil {
		t.Fatalf("GetUpdates() = %+v, want nil", got)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("GetUpdates(timeout=0) took %v, want near-immediate return", elapsed)
	}
}

func TestAccountAPIDownloadFileConsumesCacheOnce(t *testing.T) {
	t.Parallel()

	a := newTestAccountAPI()
	a.docCache["f1"] = []byte("payload")

	data, err := a.DownloadFile("f1")
	if err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("DownloadFile() = %q, want %q", data, "payload")
	}

	if _, err := a.DownloadFile("f1"); err == nil {
		t.Fatal("DownloadFile() second call error = nil, want an error (cache entry should be consumed)")
	}
}

func TestRawChannelIDConversion(t *testing.T) {
	t.Parallel()

	got := rawChannelID(-1001234567890)
	want := int64(1234567890)
	if got != want {
		t.Fatalf("rawChannelID(-1001234567890) = %d, want %d", got, want)
	}
}
