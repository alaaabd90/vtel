package tunnel

import (
	"context"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/alaaabd90/vtel/vtellog"
)

const (
	dnsCacheTTL        = 300 * time.Second
	dnsCacheNegTTL     = 5 * time.Second
	dnsCacheMaxEntries = 2048
	dnsResolveTimeout  = 5 * time.Second
)

// dnsCache is a small size-bounded cache in front of net.DefaultResolver,
// ported from gdrive's exitDNSCache (internal/gdrive/dnscache.go): without
// it, Server.handleConnect ran a fresh DNS resolution on every single
// CONNECT, even for the same target hit repeatedly on the same server-side
// process. Positive hits are cached for dnsCacheTTL; negative (failed)
// lookups for the much shorter dnsCacheNegTTL, so a transient resolver
// hiccup doesn't get stuck as a false negative for 5 minutes.
type dnsCache struct {
	mu      sync.Mutex
	entries map[string]dnsCacheEntry
}

type dnsCacheEntry struct {
	addrs   []string
	expires time.Time
	neg     bool
}

func newDNSCache() *dnsCache {
	return &dnsCache{entries: make(map[string]dnsCacheEntry, 128)}
}

func (c *dnsCache) resolve(ctx context.Context, host string) ([]string, bool) {
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.entries[host]
	c.mu.Unlock()
	if ok && now.Before(entry.expires) {
		if entry.neg {
			vtellog.Debugf("[dns] cache hit (negative) host=%s", host)
			return nil, false
		}
		vtellog.Debugf("[dns] cache hit host=%s addrs=%v", host, entry.addrs)
		return entry.addrs, true
	}

	lookupStart := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	vtellog.Debugf("[dns] cache miss, resolved host=%s addrs=%v err=%v in %v", host, addrs, err, time.Since(lookupStart))
	now = time.Now()
	c.mu.Lock()
	if len(c.entries) >= dnsCacheMaxEntries {
		// Simple reset rather than LRU eviction, matching gdrive's own
		// choice - a size-bounded cache is the safety valve, not a
		// precisely-tuned eviction policy.
		c.entries = make(map[string]dnsCacheEntry, 128)
	}
	if err != nil || len(addrs) == 0 {
		c.entries[host] = dnsCacheEntry{neg: true, expires: now.Add(dnsCacheNegTTL)}
	} else {
		c.entries[host] = dnsCacheEntry{addrs: addrs, expires: now.Add(dnsCacheTTL)}
	}
	c.mu.Unlock()

	if err != nil || len(addrs) == 0 {
		return nil, false
	}
	return addrs, true
}

// resolveTarget pre-resolves a hostname target through the cache and returns
// a target string with a random resolved IP substituted, so the actual
// net.DialTimeout call skips resolution on a cache hit. If host is already
// an IP literal, or resolution fails/misses, target is returned unchanged -
// dial falls back to doing its own resolution.
//
// Deliberately not ported: gdrive's address-family preference
// (pickAddrForFamily, prefer_ipv4/ipv6_only/etc). That exists for gdrive's
// mobile-tethering Happy-Eyeballs context; vtel's socks5.Server.RejectIPv6
// already made the opposite call in Stage 7 (default false - vtel's targets
// are ordinary internet hosts, not a tethering scenario), so there's no
// family-preference knob here to feed.
func (c *dnsCache) resolveTarget(ctx context.Context, target string) string {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return target
	}
	if net.ParseIP(host) != nil {
		return target
	}

	resolveCtx, cancel := context.WithTimeout(ctx, dnsResolveTimeout)
	addrs, ok := c.resolve(resolveCtx, host)
	cancel()
	if !ok || len(addrs) == 0 {
		return target
	}
	return net.JoinHostPort(addrs[rand.Intn(len(addrs))], port)
}
