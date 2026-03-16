package telegram

import (
	"fmt"
	"time"
)

const (
	senderMaxRetries     = 3
	senderInitialBackoff = 2 * time.Second
)

// Sender sends batches to Telegram with rate limiting.
type Sender struct {
	api       *API
	channelID int64
	limiter   *RateLimiter
}

func NewSender(api *API, channelID int64) *Sender {
	return &Sender{
		api:       api,
		channelID: channelID,
		limiter:   NewRateLimiter(),
	}
}

// Send sends a compressed batch as a document. Handles rate limiting and retries.
func (s *Sender) Send(seq uint64, data []byte) error {
	filename := fmt.Sprintf("b_%012d.bin.gz", seq)
	backoff := senderInitialBackoff

	for attempt := 0; ; attempt++ {
		s.limiter.Wait()

		retryAfter, err := s.api.SendDocument(s.channelID, filename, data)
		if err == nil {
			s.limiter.RecordSend()
			return nil
		}

		if retryAfter > 0 {
			// 429: always retry, no attempt limit
			fmt.Printf("[sender] rate limited, retry after %ds\n", retryAfter)
			s.limiter.RecordRetryAfter(retryAfter)
			continue
		}

		// Transient error: retry up to senderMaxRetries
		s.limiter.RecordSend()
		if attempt >= senderMaxRetries {
			return err
		}
		fmt.Printf("[sender] transient error (attempt %d/%d): %v\n", attempt+1, senderMaxRetries, err)
		time.Sleep(backoff)
		backoff *= 2
	}
}
