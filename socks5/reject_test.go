package socks5

import "testing"

func TestIsMappedDNSOverTLSProbe(t *testing.T) {
	cases := []struct {
		name string
		req  *ConnectRequest
		want bool
	}{
		{"match 198.18", &ConnectRequest{AddrType: 0x01, Addr: []byte{198, 18, 0, 1}, Port: 853}, true},
		{"match 198.19", &ConnectRequest{AddrType: 0x01, Addr: []byte{198, 19, 5, 5}, Port: 853}, true},
		{"wrong port", &ConnectRequest{AddrType: 0x01, Addr: []byte{198, 18, 0, 1}, Port: 443}, false},
		{"wrong range", &ConnectRequest{AddrType: 0x01, Addr: []byte{8, 8, 8, 8}, Port: 853}, false},
		{"not IPv4", &ConnectRequest{AddrType: 0x03, Addr: []byte("example.com"), Port: 853}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMappedDNSOverTLSProbe(c.req); got != c.want {
				t.Errorf("isMappedDNSOverTLSProbe(%+v) = %v, want %v", c.req, got, c.want)
			}
		})
	}
}

func TestIsIPv6LiteralTarget(t *testing.T) {
	if !isIPv6LiteralTarget(&ConnectRequest{AddrType: 0x04}) {
		t.Error("AddrType 0x04 should be an IPv6 literal target")
	}
	if isIPv6LiteralTarget(&ConnectRequest{AddrType: 0x01}) {
		t.Error("AddrType 0x01 should not be an IPv6 literal target")
	}
}

func TestIsMapDNSFakeIP(t *testing.T) {
	cases := []struct {
		name string
		req  *ConnectRequest
		want bool
	}{
		{"in range", &ConnectRequest{AddrType: 0x01, Addr: []byte{240, 0, 0, 1}}, true},
		{"upper bound", &ConnectRequest{AddrType: 0x01, Addr: []byte{255, 255, 255, 255}}, true},
		{"just below range", &ConnectRequest{AddrType: 0x01, Addr: []byte{239, 255, 255, 255}}, false},
		{"not IPv4", &ConnectRequest{AddrType: 0x04, Addr: make([]byte, 16)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMapDNSFakeIP(c.req); got != c.want {
				t.Errorf("isMapDNSFakeIP(%+v) = %v, want %v", c.req, got, c.want)
			}
		})
	}
}

func TestIsBenchmarkIP(t *testing.T) {
	cases := []struct {
		name string
		req  *ConnectRequest
		want bool
	}{
		{"198.18.x", &ConnectRequest{AddrType: 0x01, Addr: []byte{198, 18, 1, 1}}, true},
		{"198.19.x", &ConnectRequest{AddrType: 0x01, Addr: []byte{198, 19, 1, 1}}, true},
		{"198.20.x out of range", &ConnectRequest{AddrType: 0x01, Addr: []byte{198, 20, 1, 1}}, false},
		{"198.17.x out of range", &ConnectRequest{AddrType: 0x01, Addr: []byte{198, 17, 1, 1}}, false},
		{"not IPv4", &ConnectRequest{AddrType: 0x03, Addr: []byte("example.com")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isBenchmarkIP(c.req); got != c.want {
				t.Errorf("isBenchmarkIP(%+v) = %v, want %v", c.req, got, c.want)
			}
		})
	}
}
