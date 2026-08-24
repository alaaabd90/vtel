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

// TestRateLimiterRecoversProportionallyNotLinearly guards against a real
// bug found via live device testing: escalation on a 429 multiplies
// interval by 1.5x, but recovery used to subtract a single flat
// decreaseStep per successful send - so a handful of 429s in quick
// succession (observed in practice: two bots sharing one Telegram channel,
// each blind to the other's traffic against Telegram's per-chat rate
// limit) could escalate interval from ~1s to several seconds in moments,
// then take a hundred-plus successful sends at 50ms each to undo. Every
// send stayed multi-second long after the actual rate-limiting had
// stopped. Recovery must scale with how far escalated interval currently
// is, so a handful of successful sends undoes a handful of 429s in
// comparable time, not two orders of magnitude slower.
func TestRateLimiterRecoversProportionallyNotLinearly(t *testing.T) {
	t.Parallel()

	rl := newRateLimiter(1*time.Second, 400*time.Millisecond, 30*time.Second, 50*time.Millisecond, 3)
	for i := 0; i < 6; i++ {
		rl.RecordRetryAfter(0) // escalates interval 1.5x each call, ~7.6s after 6 hits
	}
	escalated := rl.interval
	if escalated < 5*time.Second {
		t.Fatalf("setup: interval = %v after 6 escalations, want >= 5s", escalated)
	}

	for i := 0; i < 10; i++ {
		rl.RecordSend()
	}

	// The old flat-50ms-per-send behavior would only reach ~7.1s after 10
	// sends (escalated - 10*50ms); proportional recovery should have cut
	// well into the excess above minInterval by then.
	if rl.interval > escalated/2 {
		t.Fatalf("interval = %v after 10 successful sends from %v, want well under half - recovery is too slow", rl.interval, escalated)
	}
}
