package protocol

import (
	"math/rand"
	"time"
)

// schedule.go holds Stage 8's "look normal" traffic-timing features. Honest
// framing (also in the README): these reduce *pattern* detectability -
// timing regularity that an observer watching packet/request timestamps
// could fingerprint - not *volume* visibility. An observer who already sees
// total bytes/day learns nothing new is hidden by jitter or quiet hours.
// This is deliberately a new vtel-specific addition, not a gdrive port -
// gdrive has no equivalent (its traffic doesn't need to resemble anything
// in particular; vtel's does, since it rides a general-purpose bot API).

// jitterFraction is the +/-15% randomness applied to idle/max-delay flush
// timers, breaking the exact periodicity a fixed debounce interval would
// otherwise produce.
const jitterFraction = 0.15

// jitter returns d scaled by a random factor in [1-jitterFraction,
// 1+jitterFraction). d<=0 is returned unchanged (nothing to jitter).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	factor := (1 - jitterFraction) + rand.Float64()*(2*jitterFraction)
	return time.Duration(float64(d) * factor)
}

// QuietHoursMultiplier scales the adaptive idle timeout during a configured
// quiet-hours window. Deliberately a slowdown, not a pause: a full on/off
// traffic pattern is itself a detectable signal, so quiet hours only widen
// the debounce, never stop sending entirely.
const QuietHoursMultiplier = 3.0

// QuietHoursConfig defines a daily window (in a given timezone) during
// which QuietHoursMultiplier applies.
type QuietHoursConfig struct {
	StartHour int    `json:"start_hour"` // 0-23, inclusive
	EndHour   int    `json:"end_hour"`   // 0-23, exclusive
	Timezone  string `json:"timezone"`   // IANA name, e.g. "America/New_York"; "" = UTC
}

// Active reports whether t, converted into q's Timezone, falls within
// [StartHour, EndHour) - handling a window that wraps past midnight
// (StartHour > EndHour, e.g. 23 -> 6). A nil q is always inactive, and an
// equal Start/EndHour is treated as a disabled (zero-width) window.
func (q *QuietHoursConfig) Active(t time.Time) bool {
	if q == nil || q.StartHour == q.EndHour {
		return false
	}
	loc := time.UTC
	if q.Timezone != "" {
		if l, err := time.LoadLocation(q.Timezone); err == nil {
			loc = l
		}
	}
	hour := t.In(loc).Hour()
	if q.StartHour < q.EndHour {
		return hour >= q.StartHour && hour < q.EndHour
	}
	return hour >= q.StartHour || hour < q.EndHour
}
