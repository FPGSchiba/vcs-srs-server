package voiceontrol

import (
	"strings"
	"testing"
)

func TestResolveAdvertisedAddr(t *testing.T) {
	ok := []struct {
		in   string
		lp   int
		host string
		port int
	}{
		{"127.0.0.1:5002", 5002, "127.0.0.1", 5002},
		{"vcs.example:6000", 5002, "vcs.example", 6000},
		{"203.0.113.5", 5010, "203.0.113.5", 5010},
		{" 203.0.113.5 ", 5010, "203.0.113.5", 5010},
		{"vcs.example", 5010, "vcs.example", 5010},
		{"[2001:db8::1]:5002", 0, "2001:db8::1", 5002},
		{"2001:db8::1", 5010, "2001:db8::1", 5010},
	}
	for _, c := range ok {
		host, port, err := resolveAdvertisedAddr(c.in, c.lp)
		if err != nil {
			t.Errorf("resolveAdvertisedAddr(%q, %d): unexpected error %v", c.in, c.lp, err)
			continue
		}
		if host != c.host || port != c.port {
			t.Errorf("resolveAdvertisedAddr(%q, %d) = (%q, %d), want (%q, %d)", c.in, c.lp, host, port, c.host, c.port)
		}
	}

	bad := []struct {
		in string
		lp int
	}{
		{"", 5002}, {"   ", 5002}, {"0.0.0.0:5002", 5002}, {"0.0.0.0", 5002},
		{"[::]:5002", 5002}, {"::", 5002}, {"host:0", 5002}, {"host:70000", 5002},
		{"host:abc", 5002}, {":5002", 5002}, {"host", 0}, {"host", 70000},
	}
	for _, c := range bad {
		host, port, err := resolveAdvertisedAddr(c.in, c.lp)
		if err == nil {
			t.Errorf("resolveAdvertisedAddr(%q, %d) = (%q, %d), want an error", c.in, c.lp, host, port)
			continue
		}
		if !strings.HasPrefix(err.Error(), "voiceControl.publicAddr") {
			t.Errorf("resolveAdvertisedAddr(%q, %d): error %q lacks the voiceControl.publicAddr prefix", c.in, c.lp, err)
		}
	}
}
