package pool

import "testing"

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
