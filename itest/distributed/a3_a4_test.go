//go:build integration

package distributed

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// owners returns the coalition -> owning node role map of the shared topology.
func owners(t *testing.T) map[string]topology.Role {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := top.CoalitionMap(ctx)
	if err != nil {
		t.Fatalf("CoalitionMap: %v", err)
	}
	return m
}

// other returns the non-global node role that is not r.
func other(r topology.Role) topology.Role {
	if r == topology.NodeA {
		return topology.NodeB
	}
	return topology.NodeA
}

// A3: a HELLO is only accepted by the node that owns the bot's coalition. A
// cross-node HELLO gets no ACK, and the node logs a WARN (not an ERROR), so
// the line needs no allowlist entry (voice/server.go:379-384).
func TestHelloAcksOnlyOnOwnNode(t *testing.T) {
	owner := owners(t)
	own := owner["Blue"]
	foreign := other(own)

	b := newBot(t, "Blue")
	if got, want := b.coalitionAddr, top.Addr(own); got != want {
		t.Fatalf("coalitionAddr = %q, want %q", got, want)
	}
	if !b.hello(top.Addr(own), 2*time.Second) {
		t.Fatalf("HELLO to own node %s (%s) not ACKed within 2s", own, top.Addr(own))
	}
	// Snapshot the cumulative log so only lines caused by this HELLO count.
	before, err := top.LogText(foreign)
	if err != nil {
		t.Fatalf("LogText(%s): %v", foreign, err)
	}
	baseWarns := countWarnUnknownClient(before)
	if b.hello(top.Addr(foreign), 2*time.Second) {
		t.Fatalf("HELLO to foreign node %s (%s) was ACKed", foreign, top.Addr(foreign))
	}

	// The reject log is written asynchronously; poll briefly.
	var log string
	deadline := time.Now().Add(2 * time.Second)
	for {
		var err error
		if log, err = top.LogText(foreign); err != nil {
			t.Fatalf("LogText(%s): %v", foreign, err)
		}
		if countWarnUnknownClient(log) > baseWarns || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if countWarnUnknownClient(log) <= baseWarns {
		t.Errorf("foreign node %s log gained no new WARN line matching %q", foreign, "unknown client")
	}
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "unknown client") && strings.Contains(l, "level=ERROR") {
			t.Errorf("unknown-client reject logged at ERROR, want WARN: %s", l)
		}
	}
}

func countWarnUnknownClient(log string) int {
	n := 0
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, "unknown client") {
			n++
		}
	}
	return n
}

func seqRange(from, to uint32) []uint32 {
	var s []uint32
	for i := from; i <= to; i++ {
		s = append(s, i)
	}
	return s
}

// A4: voice on a coalition frequency stays within the coalition. 251.0 is not
// in any global or test list.
func TestCoalitionVoiceIsolated(t *testing.T) {
	const mhz = float32(251.0)
	f := freqKHz(mhz)
	owner := owners(t)

	blue1, blue2, red1 := newBot(t, "Blue"), newBot(t, "Blue"), newBot(t, "Red")
	for _, b := range []*bot{blue1, blue2, red1} {
		b.tune(mhz)
		if !b.hello(b.coalitionAddr, 2*time.Second) {
			t.Fatalf("%s bot HELLO to %s not ACKed", b.coalition, b.coalitionAddr)
		}
	}
	if owner["Blue"] == owner["Red"] {
		t.Fatalf("Blue and Red share node %s", owner["Blue"])
	}

	blue1.send(blue1.coalitionAddr, f, seqRange(1, 25)...)

	frames := blue2.recvFrames(blue2.coalitionAddr, f, 1500*time.Millisecond)
	if len(frames) != 25 {
		t.Fatalf("Blue2 received %d frames, want 25", len(frames))
	}
	// Compare keyed by seq: UDP may reorder, so arrival order is not asserted.
	got := map[uint32][]byte{}
	for _, p := range frames {
		got[binary.BigEndian.Uint32(p)] = p
	}
	for seq := uint32(1); seq <= 25; seq++ {
		p, ok := got[seq]
		if !ok {
			t.Errorf("Blue2 missing frame seq %d", seq)
		} else if !bytes.Equal(p, framePayload(seq)) {
			t.Errorf("Blue2 frame seq %d payload differs from what Blue1 sent", seq)
		}
	}
	if got := red1.recv(red1.coalitionAddr, f, 500*time.Millisecond); len(got) != 0 {
		t.Errorf("Red1 received %d frames, want 0: %v", len(got), got)
	}
}

// A4b: a global frequency does not cross coalitions today.
//
// This asserts OBSERVED behaviour (finding F2 / P-D7), not the intended one.
// Two layers cause it: the client discards the global address, so real
// clients never use the global node (client internal/app/voice.go:699); and
// each node checks IsFrequencyGlobal / IsFrequencyTest against its OWN YAML
// (voice/server.go:484, :674), which ships globalFrequencies: [] and is not
// fed by control.
//
// Intended (Q1 = B): control owns the global/test lists and pushes them to
// every node; clients send/receive global voice via the global node using
// globalAddr; when that lands, flip Red1 to 25/25.
func TestGlobalVoiceDoesNotCrossCoalitions_D7(t *testing.T) {
	const mhz = float32(249.0)
	f := freqKHz(mhz)

	blue1, blue2, red1 := newBot(t, "Blue"), newBot(t, "Blue"), newBot(t, "Red")
	for _, b := range []*bot{blue1, blue2, red1} {
		b.tune(mhz)
		if !b.hello(b.coalitionAddr, 2*time.Second) {
			t.Fatalf("%s bot HELLO to %s not ACKed", b.coalition, b.coalitionAddr)
		}
	}

	blue1.send(blue1.coalitionAddr, f, seqRange(1, 25)...)

	if got := blue2.recv(blue2.coalitionAddr, f, 1500*time.Millisecond); len(got) != 25 {
		t.Errorf("Blue2 received %d frames, want 25", len(got))
	}
	if got := red1.recv(red1.coalitionAddr, f, 500*time.Millisecond); len(got) != 0 {
		t.Errorf("Red1 received %d frames, want 0 (F2: observed behaviour): %v", len(got), got)
	}
}
