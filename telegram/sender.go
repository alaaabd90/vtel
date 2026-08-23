package telegram

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const (
	senderMaxRetries     = 3
	senderInitialBackoff = 2 * time.Second
	maxMessageChars      = 4096
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

// Send sends a compressed batch. Small batches go as text messages when the
// fully encoded payload fits Telegram's message length limit; larger ones go as
// documents to avoid oversized sendMessage requests. urgent classifies the
// batch per Batcher.Add's doc comment; Send doesn't yet treat it
// differently (both paths already go through the same RateLimiter), but it's
// plumbed through so a future traffic-shaping stage (e.g. skipping jitter
// for control-frame batches) has it available without another signature change.
func (s *Sender) Send(seq uint64, data []byte, urgent bool) error {
	if fitsTextBatch(s.botID, seq, data) {
		text := formatTextBatch(s.botID, seq, data)
		return s.sendRetry(seq, func() (int, error) {
			return s.api.SendMessage(s.channelID, text)
		})
	}
	return s.sendRetry(seq, func() (int, error) {
		filename := fmt.Sprintf("%d_%012d.bin.zst", s.botID, seq)
		return s.api.SendDocument(s.channelID, filename, data)
	})
}

func fitsTextBatch(botID int64, seq uint64, data []byte) bool {
	prefixLen := len(fmt.Sprintf("%d_%012d\n", botID, seq))
	return prefixLen+base64.StdEncoding.EncodedLen(len(data)) <= maxMessageChars
}

func formatTextBatch(botID int64, seq uint64, data []byte) string {
	// Format: "{botID}_{seq:012d}\n{base64data}"
	return fmt.Sprintf("%d_%012d\n%s", botID, seq, base64.StdEncoding.EncodeToString(data))
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
		if isPermanentError(err) {
			return err
		}
		if attempt >= senderMaxRetries {
			return err
		}
		fmt.Printf("[sender] transient error (seq=%d, attempt %d/%d): %v\n", seq, attempt+1, senderMaxRetries, err)
		time.Sleep(backoff)
		backoff *= 2
	}
}

func isPermanentError(err error) bool {
	var permanent interface{ Permanent() bool }
	return errors.As(err, &permanent) && permanent.Permanent()
}
