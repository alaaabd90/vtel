package telegram

import (
	"sync"
	"time"
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

// RecordSend marks a successful send and slowly decreases the interval.
func (rl *RateLimiter) RecordSend() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.refill(time.Now())

	rl.interval -= rl.decreaseStep
	if rl.interval < rl.minInterval {
		rl.interval = rl.minInterval
	}
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
