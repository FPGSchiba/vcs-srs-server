package leg

import "testing"

func TestCountLog(t *testing.T) {
	log := `time=1 level=INFO msg="Voice server started"
time=2 level=WARN msg="Voice shard queue full" shard=0
time=3 level=WARN msg="Voice shard queue full" shard=1
time=4 level=WARN msg="Update stream fell behind" client=x
time=5 level=ERROR msg="Voice node deregistered" id=a
time=6 level=WARN msg="Voice node feed fell behind"
`
	got := CountLog(log)
	want := LogCounts{ShardQueueFull: 2, UpdateStreamBehind: 1, NodeFeedBehind: 1, NodeDeregistered: 1}
	if got != want {
		t.Fatalf("CountLog = %+v, want %+v", got, want)
	}
	if z := CountLog(""); z != (LogCounts{}) {
		t.Errorf("empty log = %+v", z)
	}
}

func TestLogCountsSub(t *testing.T) {
	a := LogCounts{ShardQueueFull: 5, NodeDeregistered: 1}
	b := LogCounts{ShardQueueFull: 2, NodeDeregistered: 3}
	got := a.Sub(b)
	// A log that shrank (rotated or truncated) must not underflow into a
	// huge count that would fake a breach.
	if got.ShardQueueFull != 3 || got.NodeDeregistered != 0 {
		t.Fatalf("Sub = %+v", got)
	}
}

func TestCountLogHelloOutcomes(t *testing.T) {
	log := `msg="Accepted voice HELLO" sender_id=a
msg="Accepted voice HELLO" sender_id=b
msg="Rejected voice HELLO" reason=x
`
	got := CountLog(log)
	if got.HelloAccepted != 2 || got.HelloRejected != 1 {
		t.Fatalf("CountLog = %+v", got)
	}
	if d := got.Sub(CountLog("msg=\"Accepted voice HELLO\" sender_id=a\n")); d.HelloAccepted != 1 || d.HelloRejected != 1 {
		t.Fatalf("Sub = %+v", d)
	}
}

func TestFailedAcked(t *testing.T) {
	before := "msg=\"Accepted voice HELLO\" sender_id=g1\n"
	after := before + "msg=\"Accepted voice HELLO\" sender_id=g2\nmsg=\"Rejected voice HELLO\" sender_id=g3\n"
	failed := []JoinFailure{{GUID: "g1"}, {GUID: "g2"}, {GUID: "g3"}}
	// g1 was accepted before the ramp, g3 was rejected: only g2 counts.
	if got := FailedAcked(before, after, failed); got != 1 {
		t.Errorf("FailedAcked = %d, want 1", got)
	}
	if got := FailedAcked("", "", []JoinFailure{{Bot: 3}}); got != -1 {
		t.Errorf("no GUIDs = %d, want -1", got)
	}
	if got := FailedAcked("", "", failed); got != 0 {
		t.Errorf("empty log = %d, want 0", got)
	}
}
