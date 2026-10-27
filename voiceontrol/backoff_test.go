package voiceontrol

import (
	"testing"
	"time"
)

func TestNextBackoffCappedWithJitter(t *testing.T) {
	id := func(d time.Duration) time.Duration { return d }
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := nextBackoff(i, time.Second, 30*time.Second, id); got != w {
			t.Fatalf("attempt %d: %v, want %v", i, got, w)
		}
	}
	if got := nextBackoff(100, time.Second, 30*time.Second, id); got != 30*time.Second {
		t.Fatalf("overflow at attempt 100: %v", got)
	}
	for i := 0; i < 1000; i++ {
		d := defaultJitter(30 * time.Second)
		if d < 24*time.Second || d > 30*time.Second {
			t.Fatalf("jitter %v outside [24s,30s]", d)
		}
	}
}
