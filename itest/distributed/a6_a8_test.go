//go:build integration

package distributed

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
	"github.com/FPGSchiba/vcs-srs-server/voice"
)

// A6: a GraphQL kickClient ends the kicked bot's update stream and its voice
// session: the node stops relaying to it and stops answering its keepalives.
// Not probed before the first run (design section 3.6); the expectations are
// the design's.
func TestKickEndsStreamAndVoice(t *testing.T) {
	const mhz = float32(251.0)
	f := freqKHz(mhz)

	blue1, blue2 := newBot(t, "Blue"), newBot(t, "Blue")
	for _, b := range []*bot{blue1, blue2} {
		b.tune(mhz)
		if !b.hello(b.coalitionAddr, 2*time.Second) {
			t.Fatalf("%s bot HELLO to %s not ACKed", b.coalition, b.coalitionAddr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out struct {
		KickClient struct {
			Success bool    `json:"success"`
			Message *string `json:"message"`
		} `json:"kickClient"`
	}
	if err := top.GraphQL(ctx, `mutation($id: ID!, $r: String!){kickClient(clientId:$id, reason:$r){success message}}`,
		map[string]any{"id": blue1.guid, "r": "itest"}, &out); err != nil {
		t.Fatalf("kickClient: %v", err)
	}
	if !out.KickClient.Success {
		t.Fatalf("kickClient success = false, message %v", out.KickClient.Message)
	}

	// Blue1's update stream ends within 2 s: Recv fails, which also closes
	// the updates channel.
	select {
	case err := <-blue1.streamErr:
		t.Logf("Blue1 stream ended: %v", err)
	case <-time.After(2 * time.Second):
		t.Errorf("Blue1 update stream still open 2s after kick")
	}

	// Blue2 sends 10 frames; Blue1 receives none.
	blue2.send(blue2.coalitionAddr, f, seqRange(1, 10)...)
	if got := blue1.recv(blue1.coalitionAddr, f, 700*time.Millisecond); len(got) != 0 {
		t.Errorf("kicked Blue1 received %d frames, want 0: %v", len(got), got)
	}

	// Blue1's keepalive gets no ACK within 1.5 s.
	c := blue1.sock(blue1.coalitionAddr)
	ka := voice.NewVCSKeepalivePacket(blue1.guidU).SerializePacket()
	if _, err := c.Write(ka); err != nil {
		t.Fatalf("keepalive send: %v", err)
	}
	buf := make([]byte, 2048)
	deadline := time.Now().Add(1500 * time.Millisecond)
	_ = c.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if err != nil {
			continue // timeout or stale ICMP refusal: keep waiting out the window
		}
		if p, perr := voice.ParsePacket(buf[:n]); perr == nil && p.Type == voice.PacketTypeKeepalive {
			t.Errorf("kicked Blue1's keepalive was ACKed")
			break
		}
	}
}

// A7: a re-tune re-routes within 1 s.
func TestRetuneReroutesWithinOneSecond(t *testing.T) {
	const m1, m2 = float32(251.0), float32(252.0)
	f1, f2 := freqKHz(m1), freqKHz(m2)

	blue1, blue2 := newBot(t, "Blue"), newBot(t, "Blue")
	blue1.tune(m1)
	blue2.tune(m1, m2)
	for _, b := range []*bot{blue1, blue2} {
		if !b.hello(b.coalitionAddr, 2*time.Second) {
			t.Fatalf("%s bot HELLO to %s not ACKed", b.coalition, b.coalitionAddr)
		}
	}

	// Blue2 sends 50 frames/s on both frequencies, interleaved, in the
	// background for the whole test.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for seq := uint32(1); ; seq++ {
			select {
			case <-stop:
				return
			case <-tick.C:
				c := blue2.sock(blue2.coalitionAddr)
				for _, fk := range []uint32{f1, f2} {
					pkt := voice.NewVCSVoicePacket(blue2.guidU, seq, fk, framePayload(seq))
					_, _ = c.Write(pkt.SerializePacket())
				}
			}
		}
	}()
	defer func() { close(stop); <-done }()

	if got := blue1.recv(blue1.coalitionAddr, f1, 500*time.Millisecond); len(got) == 0 {
		t.Fatalf("Blue1 received nothing on 251.0 before the re-tune")
	}
	blue1.tune(m2)
	if got := blue1.recv(blue1.coalitionAddr, f2, time.Second); len(got) == 0 {
		t.Errorf("Blue1 received nothing on 252.0 within 1s of re-tuning")
	}
	// The sniffing window above ended 1 s after the re-tune: 251.0 is silent now.
	if got := blue1.recv(blue1.coalitionAddr, f1, 500*time.Millisecond); len(got) != 0 {
		t.Errorf("Blue1 still received %d frames on 251.0 after the re-tune", len(got))
	}
}

// A8: a node whose VoiceControl serverName does not match the control
// certificate never registers; its log shows the x509 mismatch, retried
// (voiceontrol/client.go:220-282).
func TestNodeWithWrongServerNameNeverRegisters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stop, logPath, err := top.StartExtraNode(ctx, "nodeX", "wrong.example")
	if err != nil {
		t.Fatalf("StartExtraNode: %v", err)
	}
	t.Cleanup(stop)

	known := map[string]bool{
		top.Addr(topology.NodeA):  true,
		top.Addr(topology.NodeB):  true,
		top.Addr(topology.Global): true,
	}
	// Its own address is not exposed, so assert the registry never grows
	// beyond the three known nodes for 5 s.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := top.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		for _, n := range st.Nodes {
			if !known[n.Address] {
				t.Fatalf("unexpected node registered: %+v", n)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}

	re := regexp.MustCompile(`x509: certificate is valid for .* not wrong\.example`)
	var log []byte
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		log, _ = os.ReadFile(logPath)
		if re.Match(log) || time.Now().After(end) {
			break
		}
	}
	if !re.Match(log) {
		t.Errorf("nodeX log has no line matching %q", re)
	}

	stop()
	// A1's predicate still holds.
	st, err := top.Status(ctx)
	if err != nil {
		t.Fatalf("Status after kill: %v", err)
	}
	want := map[topology.Role]string{
		topology.NodeA:  top.Addr(topology.NodeA),
		topology.NodeB:  top.Addr(topology.NodeB),
		topology.Global: top.Addr(topology.Global),
	}
	if err := topology.DistributedReady(st, want, []string{"Blue", "Red"}); err != nil {
		t.Errorf("DistributedReady after A8: %v", err)
	}
}
