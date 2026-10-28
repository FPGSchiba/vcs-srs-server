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
