package telegram

import (
	"sync"
	"time"

	"github.com/alaaabd90/vtel/vtellog"
)

const (
	initialInterval = 1 * time.Second
	minInterval     = 400 * time.Millisecond
	maxInterval     = 30 * time.Second
	decreaseStep    = 50 * time.Millisecond
	burstCapacity   = 3.0
)

// RateLimiter implements an adaptive token bucket for Telegram API calls.
type RateLimiter struct {
	mu            sync.Mutex
	interval      time.Duration
	minInterval   time.Duration
	maxInterval   time.Duration
	decreaseStep  time.Duration
	burstCapacity float64
	tokens        float64
	lastRefill    time.Time
}

func NewRateLimiter() *RateLimiter {
	return newRateLimiter(initialInterval, minInterval, maxInterval, decreaseStep, burstCapacity)
}

func newRateLimiter(initial, min, max, decrease time.Duration, burst float64) *RateLimiter {
	return &RateLimiter{
		interval:      initial,
		minInterval:   min,
		maxInterval:   max,
		decreaseStep:  decrease,
		burstCapacity: burst,
		tokens:        burst,
		lastRefill:    time.Now(),
	}
}

// Wait blocks until it's safe to send.
func (rl *RateLimiter) Wait() {
	for {
		rl.mu.Lock()
		now := time.Now()
		rl.refill(now)
		if rl.tokens >= 1 {
			rl.tokens--
			rl.mu.Unlock()
			return
		}

		wait := time.Duration((1 - rl.tokens) * float64(rl.interval))
		rl.mu.Unlock()

		time.Sleep(wait)
	}
}

// RecordSend marks a successful send and decreases the interval - by a
// share of how far it currently sits above minInterval, not a flat step.
// A flat step made recovery drastically slower than escalation: a single
// 429 multiplies interval by 1.5x (RecordRetryAfter), but a handful of
// hits under real load (e.g. two bots sharing one channel, each blind to
// the other's traffic against Telegram's per-chat limit) can chain that
// into several multiplications in quick succession - undoing just one of
// those from a ~1s interval took over a hundred successful sends at a flat
// 50ms/send, leaving every send stuck at several seconds long after the
// actual rate-limiting had passed. Proportional recovery scales with how
// escalated the interval currently is, so it comes back down roughly as
// fast as it went up; decreaseStep still sets a floor so recovery never
// stalls once interval is close to minInterval.
func (rl *RateLimiter) RecordSend() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.refill(time.Now())

	step := (rl.interval - rl.minInterval) / 10
	if step < rl.decreaseStep {
		step = rl.decreaseStep
	}
	rl.interval -= step
	if rl.interval < rl.minInterval {
		rl.interval = rl.minInterval
	}
	vtellog.Debugf("[ratelimit] send ok, interval now %v", rl.interval)
}

// RecordRetryAfter increases the interval on a 429 response.
func (rl *RateLimiter) RecordRetryAfter(retryAfter int) {
	// Sleep for the retry_after duration (outside of lock)
	sleepDur := time.Duration(retryAfter) * time.Second
	time.Sleep(sleepDur)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.interval = rl.interval * 3 / 2
	if rl.interval > rl.maxInterval {
		rl.interval = rl.maxInterval
	}
	rl.tokens = 0
	rl.lastRefill = time.Now()
	vtellog.Debugf("[ratelimit] 429 (retry_after=%ds), interval now %v", retryAfter, rl.interval)
}

func (rl *RateLimiter) refill(now time.Time) {
	if rl.lastRefill.IsZero() {
		rl.lastRefill = now
		return
	}
	if now.Before(rl.lastRefill) {
		rl.lastRefill = now
		return
	}

	rl.tokens += float64(now.Sub(rl.lastRefill)) / float64(rl.interval)
	if rl.tokens > rl.burstCapacity {
		rl.tokens = rl.burstCapacity
	}
	rl.lastRefill = now
}
