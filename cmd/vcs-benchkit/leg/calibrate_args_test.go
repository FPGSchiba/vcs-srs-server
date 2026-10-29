package leg

import (
	"testing"
	"time"
)

// The calibrate argv carries --bounds for any probed bot (v2 and v3 both accept
// it: the v2 bot's calibrate defines --bounds) and none for an unprobed one.
func TestCalibrateArgsBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Features
		want bool
	}{
		{"unprobed", Features{}, false},
		{"v2", Features{Version: 2}, true},
		{"v3", Features{Version: 3, Set: map[string]bool{"gc_warn": true}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := calibrateArgs(CalConfig{Seed: 7, Steps: "64", Hold: 3 * time.Second, Features: tc.f}, "127.0.0.1:1")
			b := flagVal(got, "--bounds")
			if tc.want && b != BotBoundsJSON(tc.f) {
				t.Errorf("--bounds = %q, want %q (args %q)", b, BotBoundsJSON(tc.f), got)
			}
			if !tc.want && b != "" {
				t.Errorf("unexpected --bounds %q", b)
			}
			if flagVal(got, "--reflector") != "127.0.0.1:1" || flagVal(got, "--steps") != "64" {
				t.Errorf("base args missing: %q", got)
			}
		})
	}
}
