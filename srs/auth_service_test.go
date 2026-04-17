package srs

import (
	"net"
	"testing"
)

func TestSplitHostForBanCheck(t *testing.T) {
	cases := []struct {
		addr     string
		wantHost string
	}{
		{"192.168.1.1:54321", "192.168.1.1"},
		{"10.0.0.5:1234", "10.0.0.5"},
		{"[::1]:8080", "::1"},
	}
	for _, tc := range cases {
		host, _, err := net.SplitHostPort(tc.addr)
		if err != nil {
			t.Fatalf("SplitHostPort(%q) error: %v", tc.addr, err)
		}
		if host != tc.wantHost {
			t.Fatalf("addr %q: got host %q, want %q", tc.addr, host, tc.wantHost)
		}
	}
}
