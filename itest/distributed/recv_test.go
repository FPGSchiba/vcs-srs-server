//go:build integration

package distributed

import (
	"net"
	"testing"
	"time"
)

// recvFrames on a closed socket must return, not busy-spin past its window.
func TestRecvFramesReturnsOnClosedSocket(t *testing.T) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	b := &bot{t: t, udp: map[string]*net.UDPConn{"x": c}}
	_ = c.Close()

	done := make(chan struct{})
	go func() {
		b.recvFrames("x", 1000, 5*time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recvFrames did not return on a closed socket")
	}
}
