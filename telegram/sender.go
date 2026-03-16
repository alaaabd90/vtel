package telegram

import (
	"fmt"
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

	for {
		s.limiter.Wait()

		retryAfter, err := s.api.SendDocument(s.channelID, filename, data)
		if err != nil {
			if retryAfter > 0 {
				fmt.Printf("[sender] rate limited, retry after %ds\n", retryAfter)
				s.limiter.RecordRetryAfter(retryAfter)
				continue
			}
			// Non-rate-limit error, still record and return
			s.limiter.RecordSend()
			return err
		}

		s.limiter.RecordSend()
		return nil
	}
}
