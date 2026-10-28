package leg

import (
	"strings"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

func TestPredictedKnee(t *testing.T) {
	js := []byte(`{"label":"x","predictions":[
		{"scenario":"one-big-net","arm":"Seq","knee_n":9},
		{"scenario":"one-big-net","arm":"S","knee_n":3000,"validated":true},
		{"scenario":"org-op","arm":"S","knee_n":50}]}`)
	n, err := PredictedKnee(js, "one-big-net", "S")
	if err != nil || n != 3000 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := PredictedKnee(js, "one-big-net", "D2"); err == nil {
		t.Error("an absent scenario/arm must be an error")
	}
	if _, err := PredictedKnee([]byte(`{"predictions":[{"scenario":"one-big-net","arm":"S","knee_n":0}]}`), "one-big-net", "S"); err == nil {
		t.Error("a zero prediction must be an error")
	}
	if _, err := PredictedKnee([]byte(`nope`), "one-big-net", "S"); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestLoadProbe(t *testing.T) {
	p, err := ParseProbe([]byte(`{"schema":"env-probe/1","server_set":"0,1","generator_set":"2,3","topology_label":"2 hyperthreads of 1 core","smt":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.ServerSet != "0,1" || p.GeneratorSet != "2,3" || p.TopologyLabel != "2 hyperthreads of 1 core" {
		t.Fatalf("probe = %+v", p)
	}
	for _, bad := range []string{
		`{"server_set":"","generator_set":"2,3","topology_label":"x"}`,
		`{"server_set":"0,1","generator_set":"1,2","topology_label":"x"}`, // sets overlap
		`{"server_set":"0,1","generator_set":"2,3"}`,                      // no label
	} {
		if _, err := ParseProbe([]byte(bad)); err == nil {
			t.Errorf("ParseProbe(%s) must fail", bad)
		}
	}
}

func TestValidateOptions(t *testing.T) {
	ok := Options{ServerBin: "s", LoadbotBin: "l", OutDir: "o", Scenario: "one-big-net", Arms: "S", ServerSet: "0,1", GenSet: "2,3", Topology: "t", PredictedKnee: 100}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}
	cases := map[string]func(o *Options){
		"only S":        func(o *Options) { o.Arms = "S,Seq" },
		"seq later":     func(o *Options) { o.Arms = "Seq" },
		"scenario":      func(o *Options) { o.Scenario = "org-op" },
		"server bin":    func(o *Options) { o.ServerBin = "" },
		"loadbot bin":   func(o *Options) { o.LoadbotBin = "" },
		"out":           func(o *Options) { o.OutDir = "" },
		"no cpu sets":   func(o *Options) { o.ServerSet, o.GenSet = "", "" },
		"overlap":       func(o *Options) { o.GenSet = "1,2" },
		"no prediction": func(o *Options) { o.PredictedKnee = 0 },
	}
	for name, mut := range cases {
		o := ok
		mut(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// S4 builds the S arm only: the message must say where Seq and D2 arrive.
	o := ok
	o.Arms = "Seq"
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "S5") {
		t.Errorf("err = %v", err)
	}
}

func TestSweepCap(t *testing.T) {
	// A big time budget is capped by the port/descriptor budget: the overload
	// step runs at 2x N_max, so 2 x N_max must still pass the pre-flight.
	n := SweepCap(3000, 27, 100*time.Minute, [2]int{32768, 60999}, 65536, 0)
	if n <= 0 || n > 7058 {
		t.Fatalf("cap = %d, want within (0, 7058]", n)
	}
	if err := sweep.Preflight([2]int{32768, 60999}, 65536, 2*n); err != nil {
		t.Errorf("2 x cap does not pass the pre-flight: %v", err)
	}
	// ulimit -n 1024 caps lower than the ports do.
	small := SweepCap(3000, 27, 100*time.Minute, [2]int{32768, 60999}, 1024, 0)
	if small >= n || small <= 0 {
		t.Errorf("ulimit-bound cap = %d, port-bound %d", small, n)
	}
	if err := sweep.Preflight([2]int{32768, 60999}, 1024, 2*small); err != nil {
		t.Errorf("2 x small cap does not pass the pre-flight: %v", err)
	}
	// An explicit cap wins when it is lower.
	if got := SweepCap(3000, 27, 100*time.Minute, [2]int{32768, 60999}, 65536, 32); got != 32 {
		t.Errorf("explicit cap = %d", got)
	}
	// A budget below the fixed costs leaves no step at all.
	if got := SweepCap(3000, 27, time.Minute, [2]int{32768, 60999}, 65536, 0); got != 0 {
		t.Errorf("tiny budget cap = %d", got)
	}
}

// The S arm is one vCPU with GOMAXPROCS 1 and two shards, whatever the size of
// the server set (Q6: server 0,1 / generator 2,3). The reflector gets the whole
// server set.
func TestBuildPinsPutsTheServerOnTheFirstCPUOfTheSet(t *testing.T) {
	pins, err := BuildPins("0,1", "2,3")
	if err != nil {
		t.Fatal(err)
	}
	if got := pins["server"]; got.CPUs != "0" || got.GOMAXPROCS != 1 || got.Shards != 2 {
		t.Errorf("server pin = %+v, want cpus 0, gomaxprocs 1, 2 shards", got)
	}
	if got := pins["reflector"]; got.CPUs != "0,1" || got.GOMAXPROCS != 2 {
		t.Errorf("reflector pin = %+v, want the whole server set", got)
	}
	if got := pins["generator"]; got.CPUs != "2,3" || got.GOMAXPROCS != 2 {
		t.Errorf("generator pin = %+v", got)
	}
	// A set that does not start at 0 still pins to its own first CPU.
	pins, err = BuildPins("4-5", "6-7")
	if err != nil || pins["server"].CPUs != "4" {
		t.Fatalf("pins = %+v err = %v", pins, err)
	}
}

func TestBuildPinsRejectsOverlapAndBadSets(t *testing.T) {
	for _, c := range [][2]string{{"0,1", "1,2"}, {"", "2"}, {"0", ""}, {"x", "2"}} {
		if _, err := BuildPins(c[0], c[1]); err == nil {
			t.Errorf("BuildPins(%q, %q) must fail", c[0], c[1])
		}
	}
}
