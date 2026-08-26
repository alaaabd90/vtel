package telegram

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSenderUsesTextAtExactMessageLimit(t *testing.T) {
	t.Parallel()

	var messageCalls, documentCalls int
	api := &BotAPI{
		token: "test",
		client: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/sendMessage"):
					messageCalls++
				case strings.HasSuffix(req.URL.Path, "/sendDocument"):
					documentCalls++
				default:
					t.Fatalf("unexpected request path: %s", req.URL.Path)
				}
				return okAPIResponse(), nil
			}),
		},
	}

	sender := NewSender(api, 1234567890, 42)
	data := bytes.Repeat([]byte("x"), 3054)

	if err := sender.Send(1, data, false); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if messageCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", messageCalls)
	}
	if documentCalls != 0 {
		t.Fatalf("sendDocument calls = %d, want 0", documentCalls)
	}
}

func TestSenderFallsBackToDocumentWhenEncodedTextIsTooLong(t *testing.T) {
	t.Parallel()

	var messageCalls, documentCalls int
	api := &BotAPI{
		token: "test",
		client: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/sendMessage"):
					messageCalls++
				case strings.HasSuffix(req.URL.Path, "/sendDocument"):
					documentCalls++
				default:
					t.Fatalf("unexpected request path: %s", req.URL.Path)
				}
				return okAPIResponse(), nil
			}),
		},
	}

	sender := NewSender(api, 1234567890, 42)
	data := bytes.Repeat([]byte("x"), 3055)

	if err := sender.Send(1, data, false); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if messageCalls != 0 {
		t.Fatalf("sendMessage calls = %d, want 0", messageCalls)
	}
	if documentCalls != 1 {
		t.Fatalf("sendDocument calls = %d, want 1", documentCalls)
	}
}

func TestRotatingFilenamePreservesPrefixAndSortableSeq(t *testing.T) {
	t.Parallel()

	for _, seq := range []uint64{0, 1, 5, 999999999999} {
		name := rotatingFilename(1234567890, seq)
		wantPrefix := "1234567890_"
		if !strings.HasPrefix(name, wantPrefix) {
			t.Fatalf("rotatingFilename(seq=%d) = %q, missing prefix %q", seq, name, wantPrefix)
		}
		if !strings.HasSuffix(name, ".bin.zst") {
			t.Fatalf("rotatingFilename(seq=%d) = %q, missing .bin.zst suffix", seq, name)
		}
	}
}

func TestRotatingFilenameSortsCorrectlyBySeq(t *testing.T) {
	t.Parallel()

	// Different rotating base names must not break ordering by the
	// fixed-width zero-padded seq field that precedes them, since
	// poller.go's sortKey comparison depends on this.
	a := strings.TrimSuffix(rotatingFilename(1, 5), ".bin.zst")
	b := strings.TrimSuffix(rotatingFilename(1, 6), ".bin.zst")
	if !(a < b) {
		t.Fatalf("sortKey for seq=5 (%q) should sort before seq=6 (%q)", a, b)
	}
}

func TestSendRetryDoesNotRetryPermanentErrors(t *testing.T) {
	t.Parallel()

	sender := &Sender{limiter: NewRateLimiter()}
	calls := 0

	err := sender.sendRetry(32, func() (int, error) {
		calls++
		return 0, permanentTestError{}
	})

	if err == nil {
		t.Fatal("sendRetry() error = nil, want non-nil")
	}
	if calls != 1 {
		t.Fatalf("sendRetry() calls = %d, want 1", calls)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type permanentTestError struct{}

func (permanentTestError) Error() string {
	return "permanent"
}

func (permanentTestError) Permanent() bool {
	return true
}

func okAPIResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`)),
	}
}
