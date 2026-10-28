package topology

import (
	"fmt"
	"net"
)

// FreeTCPPort asks the OS for a free loopback TCP port and releases it.
// The port may be taken by another process before the caller binds it;
// callers retry on bind errors.
func FreeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("topology: free tcp port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// FreeUDPPort asks the OS for a free loopback UDP port (probed over UDP,
// not TCP, because the two port spaces are independent) and releases it.
func FreeUDPPort() (int, error) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return 0, fmt.Errorf("topology: free udp port: %w", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port, nil
}
