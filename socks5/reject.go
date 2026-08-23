package socks5

// reject.go ports gdrive's immediate-reject checks
// (internal/gdrive/socksserver.go) verbatim in logic, adapted to vtel's
// already-parsed ConnectRequest (AddrType/Addr/Port) instead of gdrive's
// joined "host:port" string - vtel's SOCKS5 parser already hands out typed
// address bytes, so there's no need to re-parse a string here.
//
// Wired into Server.handleConn right after parsing req, before calling
// Handler: an immediate REP 0x04 avoids a wasted dial/tunnel round-trip for
// targets that can never be reached through this tunnel.

// isMappedDNSOverTLSProbe reports whether req is a DNS-over-TLS probe
// against the RFC 2544 benchmark range (198.18.0.0/15) on port 853 - seen
// when a client's OS probes "Private DNS" against a VPN DNS address that
// happens to land in this range.
func isMappedDNSOverTLSProbe(req *ConnectRequest) bool {
	if req.Port != 853 || req.AddrType != 0x01 {
		return false
	}
	return req.Addr[0] == 198 && (req.Addr[1] == 18 || req.Addr[1] == 19)
}

// isIPv6LiteralTarget reports whether req targets an IPv6 literal. Only
// checked when Server.RejectIPv6 is set.
func isIPv6LiteralTarget(req *ConnectRequest) bool {
	return req.AddrType == 0x04
}

// isMapDNSFakeIP reports whether req targets the 240.0.0.0/4 mapdns
// fake-IP range: a client-side fake-IP resolver's placeholder address with
// no real hostname mapping (cache miss/reconnect race) can never be dialed.
func isMapDNSFakeIP(req *ConnectRequest) bool {
	if req.AddrType != 0x01 {
		return false
	}
	return req.Addr[0]&0xF0 == 0xF0
}

// isBenchmarkIP reports whether req targets the RFC 2544 benchmark range
// 198.18.0.0/15 - not reachable on the public internet, and otherwise
// causes a long dial timeout on the server side for no reason.
func isBenchmarkIP(req *ConnectRequest) bool {
	if req.AddrType != 0x01 {
		return false
	}
	return req.Addr[0] == 198 && req.Addr[1]&0xFE == 18
}
