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
// Prefers IPv4 when a dual-stack lookup returns both: found via live
// testing that the server VPS has no real IPv6 route out (dial errors
// "network is unreachable" for the IPv6 address of ordinary dual-stack
// domains like web.facebook.com), so picking uniformly at random across
// both families made a random fraction of otherwise-fine connections fail
// outright for no reason a client could work around. This is not the same
// knob as gdrive's mobile-tethering Happy-Eyeballs preference (deliberately
// not ported - vtel's socks5.Server.RejectIPv6 already made the opposite
// call in Stage 7, since vtel's targets are ordinary internet hosts, not a
// tethering scenario): that's about which family a *client* prefers to
// attempt; this is the server picking the family it can actually route,
// which matters regardless of any client preference.
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
	return net.JoinHostPort(pickAddr(addrs), port)
}

// pickAddr picks a random address from a dual-stack result set, preferring
// IPv4 when any is present - see resolveTarget's doc comment.
func pickAddr(addrs []string) string {
	var v4 []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}
	if len(v4) > 0 {
		return v4[rand.Intn(len(v4))]
	}
	return addrs[rand.Intn(len(addrs))]
}
