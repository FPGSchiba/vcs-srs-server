package leg

import (
	"errors"
	"testing"
)

func fakeProc(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("no such file")
	}
}

func TestTakeTCPReadsExtAndSNMP(t *testing.T) {
	p := &procSampler{read: fakeProc(map[string]string{
		"/proc/net/netstat":      "TcpExt: TCPTimeouts TCPAbortOnData TCPAbortOnClose TCPAbortOnMemory TCPAbortOnTimeout TCPAbortOnLinger\nTcpExt: 1 2 3 4 5 6\n",
		"/proc/net/snmp":         "Tcp: AttemptFails EstabResets OutRsts\nTcp: 9 8 7\n",
		"/proc/net/softnet_stat": "00000001 00000002 00000003\n",
	})}
	var s snapshot
	p.takeTCP(&s)
	if !s.TCPOK || s.TCPErr != "" {
		t.Fatalf("TCPOK=%v err=%q", s.TCPOK, s.TCPErr)
	}
	if s.TCP.Timeouts != 1 || s.TCP.AbortOnData != 2 || s.TCP.AbortOnLinger != 6 || s.TCP.EstabResets != 8 || s.TCP.OutRsts != 7 {
		t.Errorf("TCP = %+v", s.TCP)
	}
	if len(s.SoftnetPerCPU) != 1 || s.SoftnetPerCPU[0].TimeSqueeze != 3 {
		t.Errorf("SoftnetPerCPU = %+v", s.SoftnetPerCPU)
	}
}

// A missing /proc/net/netstat is reported as unread, never as zero counters.
func TestTakeTCPUnreadIsNotZero(t *testing.T) {
	p := &procSampler{read: fakeProc(map[string]string{"/proc/net/snmp": "Tcp: AttemptFails EstabResets OutRsts\nTcp: 9 8 7\n"})}
	var s snapshot
	p.takeTCP(&s)
	if s.TCPOK || s.TCPErr == "" {
		t.Errorf("TCPOK=%v err=%q, want unread with a reason", s.TCPOK, s.TCPErr)
	}
}
