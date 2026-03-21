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
	api := &API{
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

	if err := sender.Send(1, data); err != nil {
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
	api := &API{
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

	if err := sender.Send(1, data); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if messageCalls != 0 {
		t.Fatalf("sendMessage calls = %d, want 0", messageCalls)
	}
	if documentCalls != 1 {
		t.Fatalf("sendDocument calls = %d, want 1", documentCalls)
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
