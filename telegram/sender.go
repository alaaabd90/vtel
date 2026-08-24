package telegram

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/alaaabd90/vtel/vtellog"
)

const (
	senderMaxRetries     = 3
	senderInitialBackoff = 2 * time.Second
	maxMessageChars      = 4096
)

// filenameBases rotates document filenames among generic-but-plausible base
// names (Stage 8's "look normal" traffic shaping). Deliberately NOT faked
// as real media (e.g. "IMG_1234.jpg" naming a zstd blob): a fake media
// extension on non-media content is itself a worse fingerprint than an
// honest generic name, since the content/extension mismatch is an easy
// signal. Keeps the honest .bin.zst extension; only the base name rotates.
var filenameBases = []string{"backup", "export", "archive"}

// rotatingFilename picks a base name deterministically from seq (so
// retries of the same seq reuse the same filename) while still preserving
// poller.go's parsing contract: the fixed-width zero-padded seq field
// always resolves a sort-key comparison before any trailing content, so
// appending "_{base}" after it doesn't affect ordering.
func rotatingFilename(botID int64, seq uint64) string {
	base := filenameBases[seq%uint64(len(filenameBases))]
	return fmt.Sprintf("%d_%012d_%s.bin.zst", botID, seq, base)
}

// Sender sends batches to Telegram with rate limiting.
type Sender struct {
	api       *API
	botID     int64
	channelID int64
	limiter   *RateLimiter

	retry429Count atomic.Int64 // for cmd/vtel-bench's 429-frequency-per-bot metric
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
		vtellog.Debugf("[sender] bot %d: seq=%d as text message (%d bytes)", s.botID, seq, len(data))
		return s.sendRetry(seq, func() (int, error) {
			return s.api.SendMessage(s.channelID, text)
		})
	}
	vtellog.Debugf("[sender] bot %d: seq=%d as document (%d bytes)", s.botID, seq, len(data))
	return s.sendRetry(seq, func() (int, error) {
		return s.api.SendDocument(s.channelID, rotatingFilename(s.botID, seq), data)
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
			s.retry429Count.Add(1)
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

// Retry429Count returns how many times this Sender has been rate-limited
// (HTTP 429 / retry_after) since construction.
func (s *Sender) Retry429Count() int64 {
	return s.retry429Count.Load()
}

func isPermanentError(err error) bool {
	var permanent interface{ Permanent() bool }
	return errors.As(err, &permanent) && permanent.Permanent()
}
