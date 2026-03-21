package telegram

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsSmallStartupBurst(t *testing.T) {
	t.Parallel()

	rl := newRateLimiter(80*time.Millisecond, 20*time.Millisecond, time.Second, 5*time.Millisecond, 2)

	start := time.Now()
	rl.Wait()
	rl.RecordSend()
	rl.Wait()
	rl.RecordSend()

	if elapsed := time.Since(start); elapsed > 25*time.Millisecond {
		t.Fatalf("startup burst took %v, want <= 25ms", elapsed)
	}

	blockedAt := time.Now()
	rl.Wait()
	if waited := time.Since(blockedAt); waited < 45*time.Millisecond {
		t.Fatalf("third send waited %v, want >= 45ms", waited)
	}
}

func TestRateLimiterBacksOffAfterRetryAfter(t *testing.T) {
	t.Parallel()

	rl := newRateLimiter(100*time.Millisecond, 20*time.Millisecond, 300*time.Millisecond, 10*time.Millisecond, 2)
	before := rl.interval

	rl.RecordRetryAfter(0)

	if rl.interval <= before {
		t.Fatalf("interval = %v, want > %v", rl.interval, before)
	}
	if rl.tokens != 0 {
		t.Fatalf("tokens = %v, want 0", rl.tokens)
	}
}
