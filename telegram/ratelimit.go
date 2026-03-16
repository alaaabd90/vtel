package telegram

import (
	"sync"
	"time"
)

const (
	initialInterval = 3 * time.Second
	minInterval     = 1 * time.Second
	maxInterval     = 30 * time.Second
	decreaseStep    = 100 * time.Millisecond // slow decrease on success
)

// RateLimiter implements an adaptive token bucket for Telegram API calls.
type RateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	lastSend time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		interval: initialInterval,
	}
}

// Wait blocks until it's safe to send.
func (rl *RateLimiter) Wait() {
	rl.mu.Lock()
	since := time.Since(rl.lastSend)
	wait := rl.interval - since
	rl.mu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}
}

// RecordSend marks a successful send and slowly decreases the interval.
func (rl *RateLimiter) RecordSend() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.lastSend = time.Now()
	rl.interval -= decreaseStep
	if rl.interval < minInterval {
		rl.interval = minInterval
	}
}

// RecordRetryAfter increases the interval on a 429 response.
func (rl *RateLimiter) RecordRetryAfter(retryAfter int) {
	// Sleep for the retry_after duration (outside of lock)
	sleepDur := time.Duration(retryAfter) * time.Second
	time.Sleep(sleepDur)

	rl.mu.Lock()
	defer rl.mu.Unlock()
	// Increase interval by 50%
	rl.interval = rl.interval * 3 / 2
	if rl.interval > maxInterval {
		rl.interval = maxInterval
	}
	rl.lastSend = time.Now()
}
