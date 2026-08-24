package tunnel

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestResolveTargetLeavesIPLiteralUnchanged(t *testing.T) {
	c := newDNSCache()
	got := c.resolveTarget(context.Background(), "127.0.0.1:8080")
	if got != "127.0.0.1:8080" {
		t.Fatalf("resolveTarget(IP literal) = %q, want unchanged", got)
	}
}

func TestResolveTargetInvalidTargetUnchanged(t *testing.T) {
	c := newDNSCache()
	got := c.resolveTarget(context.Background(), "not-a-valid-target")
	if got != "not-a-valid-target" {
		t.Fatalf("resolveTarget(invalid) = %q, want unchanged", got)
	}
}

func TestDNSCacheCachesPositiveResult(t *testing.T) {
	c := newDNSCache()
	c.entries["example.invalid"] = dnsCacheEntry{
		addrs:   []string{"10.0.0.1", "10.0.0.2"},
		expires: time.Now().Add(dnsCacheTTL),
	}

	addrs, ok := c.resolve(context.Background(), "example.invalid")
	if !ok {
		t.Fatal("resolve() ok = false, want true for a fresh cache entry")
	}
	if len(addrs) != 2 {
		t.Fatalf("resolve() returned %d addrs, want 2", len(addrs))
	}
}

func TestDNSCacheNegativeEntryReturnsNotOK(t *testing.T) {
	c := newDNSCache()
	c.entries["nowhere.invalid"] = dnsCacheEntry{
		neg:     true,
		expires: time.Now().Add(dnsCacheNegTTL),
	}

	_, ok := c.resolve(context.Background(), "nowhere.invalid")
	if ok {
		t.Fatal("resolve() ok = true for a negative cache entry, want false")
	}
}

func TestDNSCacheExpiredEntryTriggersFreshLookup(t *testing.T) {
	c := newDNSCache()
	// Stale entry claiming a bogus address; since it's expired, resolve
	// must re-lookup rather than return this.
	c.entries["localhost"] = dnsCacheEntry{
		addrs:   []string{"203.0.113.99"}, // TEST-NET-3, not localhost's real address
		expires: time.Now().Add(-1 * time.Second),
	}

	addrs, ok := c.resolve(context.Background(), "localhost")
	if !ok {
		t.Fatal("resolve(localhost) ok = false, want true")
	}
	for _, a := range addrs {
		if a == "203.0.113.99" {
			t.Fatal("resolve() returned the stale expired address instead of re-resolving")
		}
	}
}

func TestResolveTargetSubstitutesResolvedIP(t *testing.T) {
	c := newDNSCache()
	c.entries["cached.invalid"] = dnsCacheEntry{
		addrs:   []string{"192.0.2.55"}, // TEST-NET-1
		expires: time.Now().Add(dnsCacheTTL),
	}

	got := c.resolveTarget(context.Background(), "cached.invalid:443")
	host, port, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("resolveTarget result %q not a valid host:port: %v", got, err)
	}
	if host != "192.0.2.55" {
		t.Fatalf("resolveTarget substituted host = %q, want 192.0.2.55", host)
	}
	if port != "443" {
		t.Fatalf("resolveTarget port = %q, want 443", port)
	}
}

// TestResolveTargetPrefersIPv4WhenBothAvailable guards against a real bug
// found via live device testing: the VPS running the server side has no
// real IPv6 route out, so picking uniformly at random across a dual-stack
// domain's IPv4 and IPv6 addresses made a random fraction of otherwise-fine
// connections dial-fail outright ("network is unreachable") for no reason a
// client could work around.
func TestResolveTargetPrefersIPv4WhenBothAvailable(t *testing.T) {
	c := newDNSCache()
	c.entries["dualstack.invalid"] = dnsCacheEntry{
		addrs:   []string{"2001:db8::1", "192.0.2.77"}, // IPv6 first, to catch an index-order bug too
		expires: time.Now().Add(dnsCacheTTL),
	}

	for i := 0; i < 20; i++ { // rand-backed pick, so try enough times to catch a wrong implementation
		got := c.resolveTarget(context.Background(), "dualstack.invalid:443")
		host, _, err := net.SplitHostPort(got)
		if err != nil {
			t.Fatalf("resolveTarget result %q not a valid host:port: %v", got, err)
		}
		if host != "192.0.2.77" {
			t.Fatalf("resolveTarget picked %q, want the IPv4 address 192.0.2.77 to be preferred", host)
		}
	}
}

func TestResolveTargetFallsBackToIPv6WhenNoIPv4(t *testing.T) {
	c := newDNSCache()
	c.entries["v6only.invalid"] = dnsCacheEntry{
		addrs:   []string{"2001:db8::1"},
		expires: time.Now().Add(dnsCacheTTL),
	}

	got := c.resolveTarget(context.Background(), "v6only.invalid:443")
	host, _, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("resolveTarget result %q not a valid host:port: %v", got, err)
	}
	if host != "2001:db8::1" {
		t.Fatalf("resolveTarget picked %q, want the only available IPv6 address", host)
	}
}

func TestDNSCacheEvictsWhenFull(t *testing.T) {
	c := newDNSCache()
	for i := 0; i < dnsCacheMaxEntries; i++ {
		c.entries[string(rune(i))] = dnsCacheEntry{expires: time.Now().Add(dnsCacheTTL)}
	}
	if len(c.entries) != dnsCacheMaxEntries {
		t.Fatalf("setup: got %d entries, want %d", len(c.entries), dnsCacheMaxEntries)
	}

	// An already-expired context makes LookupHost fail immediately, so this
	// exercises the miss/eviction path without a real network dependency.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	_, _ = c.resolve(ctx, "trigger-eviction.invalid")

	if len(c.entries) >= dnsCacheMaxEntries {
		t.Fatalf("cache did not reset when full: still has %d entries", len(c.entries))
	}
}
