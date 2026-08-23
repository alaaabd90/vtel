package pool

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func newTestLinks(n int) []*Link {
	links := make([]*Link, n)
	for i := 0; i < n; i++ {
		links[i] = &Link{ID: i}
	}
	return links
}

func TestPickLeastConnExcluding_PicksLowestLoad(t *testing.T) {
	links := newTestLinks(3)
	links[0].AcquireStream()
	links[0].AcquireStream()
	links[1].AcquireStream()
	p := NewPool(links)

	got := p.PickLeastConnExcluding(nil)
	if got == nil || got.ID != 2 {
		t.Fatalf("expected link 2 (0 active), got %v", got)
	}
}

func TestPickLeastConnExcluding_RespectsExclude(t *testing.T) {
	links := newTestLinks(2)
	p := NewPool(links)

	got := p.PickLeastConnExcluding(map[int]bool{0: true})
	if got == nil || got.ID != 1 {
		t.Fatalf("expected link 1, got %v", got)
	}
}

func TestPickLeastConnExcluding_AllExcludedReturnsNil(t *testing.T) {
	links := newTestLinks(2)
	p := NewPool(links)

	got := p.PickLeastConnExcluding(map[int]bool{0: true, 1: true})
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestUnhealthyLinkExcludedThenDegradesGracefully(t *testing.T) {
	links := newTestLinks(2)
	p := NewPool(links)

	for i := 0; i < UnhealthyThreshold; i++ {
		links[0].RecordFailure()
	}

	got := p.PickLeastConnExcluding(nil)
	if got == nil || got.ID != 1 {
		t.Fatalf("expected healthy link 1, got %v", got)
	}

	if !p.AnyHealthy() {
		t.Fatalf("expected at least one healthy link")
	}

	// Now make both unhealthy: pool must degrade gracefully instead of
	// returning nil.
	for i := 0; i < UnhealthyThreshold; i++ {
		links[1].RecordFailure()
	}
	if p.AnyHealthy() {
		t.Fatalf("expected no healthy links")
	}
	got = p.PickLeastConnExcluding(nil)
	if got == nil {
		t.Fatalf("expected graceful degradation to return a link, got nil")
	}
}

func TestRunWarmupCallsImmediatelyAndStopsOnCancel(t *testing.T) {
	l := &Link{ID: 0}
	var calls atomic.Int64
	l.WarmFn = func() error {
		calls.Add(1)
		return nil
	}
	p := NewPool([]*Link{l})

	ctx, cancel := context.WithCancel(context.Background())
	p.RunWarmup(ctx)

	deadline := time.Now().Add(1 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("RunWarmup did not call WarmFn immediately")
	}

	cancel()
	after := calls.Load()
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != after {
		t.Fatalf("WarmFn kept being called after ctx cancellation: %d -> %d", after, calls.Load())
	}
}

func TestRunWarmupSkipsLinksWithoutWarmFn(t *testing.T) {
	l := &Link{ID: 0} // no WarmFn set
	p := NewPool([]*Link{l})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.RunWarmup(ctx) // must not panic on a nil WarmFn

	time.Sleep(50 * time.Millisecond)
}

func TestRunWarmupFailureMarksLinkUnhealthy(t *testing.T) {
	l := &Link{ID: 0}
	l.WarmFn = func() error { return errors.New("unreachable") }
	for i := 0; i < UnhealthyThreshold-1; i++ {
		l.RecordFailure()
	}
	p := NewPool([]*Link{l})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.RunWarmup(ctx)

	deadline := time.Now().Add(1 * time.Second)
	for p.AnyHealthy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.AnyHealthy() {
		t.Fatal("expected the link to become unhealthy after a failing WarmFn crossed the threshold")
	}
}

func TestRecordSuccessClearsFailures(t *testing.T) {
	l := &Link{ID: 0}
	for i := 0; i < UnhealthyThreshold; i++ {
		l.RecordFailure()
	}
	l.RecordSuccess()
	p := NewPool([]*Link{l})
	if !p.AnyHealthy() {
		t.Fatalf("expected link healthy again after RecordSuccess")
	}
}
