package protocol

import (
	"testing"
	"time"
)

func TestJitterStaysWithinBounds(t *testing.T) {
	t.Parallel()

	d := 100 * time.Millisecond
	min := time.Duration(float64(d) * (1 - jitterFraction))
	max := time.Duration(float64(d) * (1 + jitterFraction))

	for i := 0; i < 1000; i++ {
		got := jitter(d)
		if got < min || got > max {
			t.Fatalf("jitter(%v) = %v, want within [%v, %v]", d, got, min, max)
		}
	}
}

func TestJitterZeroOrNegativeUnchanged(t *testing.T) {
	t.Parallel()

	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %v, want 0", got)
	}
	if got := jitter(-5 * time.Millisecond); got != -5*time.Millisecond {
		t.Errorf("jitter(-5ms) = %v, want -5ms", got)
	}
}

func TestQuietHoursActiveNilConfig(t *testing.T) {
	t.Parallel()
	var q *QuietHoursConfig
	if q.Active(time.Now()) {
		t.Error("nil QuietHoursConfig should never be active")
	}
}

func TestQuietHoursActiveZeroWidthWindowDisabled(t *testing.T) {
	t.Parallel()
	q := &QuietHoursConfig{StartHour: 5, EndHour: 5}
	if q.Active(time.Date(2024, 1, 1, 5, 0, 0, 0, time.UTC)) {
		t.Error("equal start/end hour should be treated as disabled")
	}
}

func TestQuietHoursActiveNormalWindow(t *testing.T) {
	t.Parallel()
	q := &QuietHoursConfig{StartHour: 2, EndHour: 6, Timezone: "UTC"}

	cases := []struct {
		hour int
		want bool
	}{
		{1, false},
		{2, true},
		{4, true},
		{5, true},
		{6, false},
		{23, false},
	}
	for _, c := range cases {
		got := q.Active(time.Date(2024, 1, 1, c.hour, 0, 0, 0, time.UTC))
		if got != c.want {
			t.Errorf("Active at hour %d = %v, want %v", c.hour, got, c.want)
		}
	}
}

func TestQuietHoursActiveWrapsPastMidnight(t *testing.T) {
	t.Parallel()
	q := &QuietHoursConfig{StartHour: 23, EndHour: 6, Timezone: "UTC"}

	cases := []struct {
		hour int
		want bool
	}{
		{22, false},
		{23, true},
		{0, true},
		{5, true},
		{6, false},
		{12, false},
	}
	for _, c := range cases {
		got := q.Active(time.Date(2024, 1, 1, c.hour, 0, 0, 0, time.UTC))
		if got != c.want {
			t.Errorf("Active at hour %d = %v, want %v", c.hour, got, c.want)
		}
	}
}

func TestQuietHoursActiveInvalidTimezoneFallsBackToUTC(t *testing.T) {
	t.Parallel()
	q := &QuietHoursConfig{StartHour: 2, EndHour: 6, Timezone: "Not/A/Real/Zone"}
	// 3:00 UTC should still land inside [2,6) via the UTC fallback.
	if !q.Active(time.Date(2024, 1, 1, 3, 0, 0, 0, time.UTC)) {
		t.Error("expected fallback to UTC for an invalid timezone name")
	}
}
