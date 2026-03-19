package telegram

import (
	"encoding/base64"
	"fmt"
	"time"
)

const (
	senderMaxRetries     = 3
	senderInitialBackoff = 2 * time.Second
	textThreshold        = 3 * 1024 // 3KB; send as text message below this, document above
)

// Sender sends batches to Telegram with rate limiting.
type Sender struct {
	api       *API
	botID     int64
	channelID int64
	limiter   *RateLimiter
}

func NewSender(api *API, botID int64, channelID int64) *Sender {
	return &Sender{
		api:       api,
		botID:     botID,
		channelID: channelID,
		limiter:   NewRateLimiter(),
	}
}

// Send sends a compressed batch. Small batches (<3.8KB) go as text messages to
// avoid the document upload overhead; larger ones go as documents.
func (s *Sender) Send(seq uint64, data []byte) error {
	if len(data) < textThreshold {
		return s.sendRetry(seq, func() (int, error) {
			// Format: "{botID}_{seq:012d}\n{base64data}"
			text := fmt.Sprintf("%d_%012d\n%s", s.botID, seq, base64.StdEncoding.EncodeToString(data))
			return s.api.SendMessage(s.channelID, text)
		})
	}
	return s.sendRetry(seq, func() (int, error) {
		filename := fmt.Sprintf("%d_%012d.bin.gz", s.botID, seq)
		return s.api.SendDocument(s.channelID, filename, data)
	})
}

// sendRetry runs sendFn with rate limiting, retrying on 429 and transient errors.
func (s *Sender) sendRetry(seq uint64, sendFn func() (retryAfter int, err error)) error {
	backoff := senderInitialBackoff
	for attempt := 0; ; attempt++ {
		s.limiter.Wait()

		retryAfter, err := sendFn()
		if err == nil {
			s.limiter.RecordSend()
			return nil
		}

		if retryAfter > 0 {
			fmt.Printf("[sender] rate limited, retry after %ds\n", retryAfter)
			s.limiter.RecordRetryAfter(retryAfter)
			continue
		}

		s.limiter.RecordSend()
		if attempt >= senderMaxRetries {
			return err
		}
		fmt.Printf("[sender] transient error (seq=%d, attempt %d/%d): %v\n", seq, attempt+1, senderMaxRetries, err)
		time.Sleep(backoff)
		backoff *= 2
	}
}
