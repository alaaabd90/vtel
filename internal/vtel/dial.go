package vtel

import (
	"context"
	"net"
	"time"
)

// dial.go is the exit side's dial-out to the real destination. Deliberately
// minimal for v1: no upstream-proxy chaining (gdrive's socksdial.go) and no
// IPv4/IPv6 family-preference dance (gdrive's tunnel.go dialDirectExitTarget)
// - just a plain, timed net.Dial. Both are easy to add later behind this
// same signature if vtel ever needs them.

const exitDialTimeout = 30 * time.Second

// dialExitTarget dials target ("host:port") from the exit role. target's
// host may be a domain name - resolution happens here, on the exit side,
// same as it would for any other SOCKS5 proxy's real endpoint.
func dialExitTarget(ctx context.Context, target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, exitDialTimeout)
	defer cancel()
	var d net.Dialer
	return d.DialContext(ctx, "tcp", target)
}
