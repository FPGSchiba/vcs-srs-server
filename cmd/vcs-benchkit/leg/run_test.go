package leg

import (
	"encoding/json"
	"strconv"
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
		"bad arm":      func(o *Options) { o.Arms = "S,X" },
		"dup arm":      func(o *Options) { o.Arms = "S,S" },
		"seq one cpu":  func(o *Options) { o.Arms = "S,Seq"; o.ServerSet = "0" },
		"scenario":     func(o *Options) { o.Scenario = "nope" },
		"server bin":   func(o *Options) { o.ServerBin = "" },
		"loadbot bin":  func(o *Options) { o.LoadbotBin = "" },
		"out":          func(o *Options) { o.OutDir = "" },
		"no cpu sets":  func(o *Options) { o.ServerSet, o.GenSet = "", "" },
		"overlap":      func(o *Options) { o.GenSet = "1,2" },
		"floor":        func(o *Options) { o.JoinRateFloor = -1 },
		"mem per bot":  func(o *Options) { o.MemPerBotMiB = -1 },
		"margin high":  func(o *Options) { o.MemMargin = 1 },
		"margin low":   func(o *Options) { o.MemMargin = -0.1 },
		"reserve":      func(o *Options) { o.MemReserveMiB = -1 },
		"bots per cpu": func(o *Options) { o.BotsPerGenCPU = -1 },
	}
	for name, mut := range cases {
		o := ok
		mut(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// W6 kit fix 3 (D8): the prediction is context only, no longer required.
	np := ok
	np.PredictedKnee = 0
	if err := np.Validate(); err != nil {
		t.Errorf("no predicted knee must now be valid: %v", err)
	}
	// Each host flag is named in its rejection.
	for flag, mut := range map[string]func(o *Options){
		"--join-rate-floor":  func(o *Options) { o.JoinRateFloor = -1 },
		"--mem-per-bot-mib":  func(o *Options) { o.MemPerBotMiB = -1 },
		"--mem-margin":       func(o *Options) { o.MemMargin = 1 },
		"--mem-reserve-mib":  func(o *Options) { o.MemReserveMiB = -1 },
		"--bots-per-gen-cpu": func(o *Options) { o.BotsPerGenCPU = -1 },
	} {
		bad := ok
		mut(&bad)
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), flag) {
			t.Errorf("%s: err = %v", flag, err)
		}
	}
	// S5: arm lists and both scenarios are accepted; a Seq arm on one cpu names itself.
	for _, good := range []func(o *Options){
		func(o *Options) { o.Arms = "D2,S,Seq" },
		func(o *Options) { o.Scenario = "org-op" },
	} {
		g := ok
		good(&g)
		if err := g.Validate(); err != nil {
			t.Errorf("must be valid: %v", err)
		}
	}
	o := ok
	o.Arms, o.ServerSet = "Seq", "0"
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "needs 2 server cpus") {
		t.Errorf("err = %v", err)
	}
}

func capN(budget time.Duration, ulimit, explicit int) int {
	return HostCap(CapInput{
		Model: sweep.JoinModel{Const: 27}, Budget: budget, Steps: sweepSteps,
		PortRange: [2]int{32768, 60999}, UlimitN: ulimit, Explicit: explicit,
	}).NMax
}

func TestHostCapPortsAndExplicit(t *testing.T) {
	// A big time budget is capped by the port/descriptor budget: the overload
	// step runs at 2x N_max, so 2 x N_max must still pass the pre-flight.
	n := capN(100*time.Minute, 65536, 0)
	if n <= 0 || n > 7058 {
		t.Fatalf("cap = %d, want within (0, 7058]", n)
	}
	if err := sweep.Preflight([2]int{32768, 60999}, 65536, 2*n); err != nil {
		t.Errorf("2 x cap does not pass the pre-flight: %v", err)
	}
	// ulimit -n 1024 caps lower than the ports do.
	small := capN(100*time.Minute, 1024, 0)
	if small >= n || small <= 0 {
		t.Errorf("ulimit-bound cap = %d, port-bound %d", small, n)
	}
	if err := sweep.Preflight([2]int{32768, 60999}, 1024, 2*small); err != nil {
		t.Errorf("2 x small cap does not pass the pre-flight: %v", err)
	}
	// An explicit cap wins when it is lower.
	if got := capN(100*time.Minute, 65536, 32); got != 32 {
		t.Errorf("explicit cap = %d", got)
	}
	// A budget below the fixed costs leaves no step at all.
	if got := capN(time.Minute, 65536, 0); got != 0 {
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

func TestManifestRound2(t *testing.T) {
	o := Options{RampChunk: 250, Bursts: true, BurstAdd: 150, BurstInFlight: 16, Bot: DefaultBotParams()}
	v3 := Features{Version: 3, Set: map[string]bool{"ping": true, "join_rate": true, "talker_pause": true, "gc_warn": true, "burst": true}}
	m := &Manifest{}
	m.recordRound2(o, v3)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bot_features", "bot_flags", "generator_env", "server_env", "ramp", "burst", "bounds_generator", "decisions_round2"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("manifest lacks %q: %s", k, b)
		}
	}
	ramp := got["ramp"].(map[string]any)
	if ramp["chunk"] != float64(250) || ramp["rate_max"] != float64(12) || ramp["rate_scale"] != float64(5000) ||
		ramp["max_in_flight"] != float64(8) || ramp["talker"] != "silent" {
		t.Errorf("ramp = %v", ramp)
	}
	burst := got["burst"].(map[string]any)
	if burst["add"] != float64(150) || burst["in_flight"] != float64(16) || burst["enabled"] != true {
		t.Errorf("burst = %v", burst)
	}
	bg := got["bounds_generator"].(map[string]any)
	if bg["gc_pause_p99_us"] != float64(5000) || bg["gc_pause_warn_p99_us"] != float64(2000) {
		t.Errorf("bounds_generator = %v", bg)
	}
	dec := got["decisions_round2"].(map[string]any)
	for i := 1; i <= 12; i++ {
		id := "R2-" + strconv.Itoa(i)
		v, ok := dec[id].(string)
		if !ok || !strings.Contains(v, "->") {
			t.Errorf("decisions_round2[%s] = %v, want an old -> new string", id, dec[id])
		}
	}
	// A v2 bot gets only --bounds.
	m2 := &Manifest{}
	m2.recordRound2(o, Features{Version: 2, Set: map[string]bool{}})
	if len(m2.BotFlags) != 2 {
		t.Errorf("v2 bot_flags = %q", m2.BotFlags)
	}
	// A v2 bot never saw the rate rule, so the record must not claim it applied;
	// it cannot run bursts either.
	r2 := m2.Ramp
	if r2.RateMax != 0 || r2.RateScale != 0 || r2.MaxInFlight != 0 {
		t.Errorf("v2 ramp records an unapplied rate rule: %+v", r2)
	}
	if m2.Burst.Enabled {
		t.Errorf("v2 burst enabled although the bot has no burst feature: %+v", m2.Burst)
	}
}

func TestRound2SummaryNamesEffectiveValues(t *testing.T) {
	o := Options{RampChunk: 250, Bursts: true, BurstAdd: 150, BurstInFlight: 16, Bot: DefaultBotParams()}
	v3 := Features{Version: 3, Set: map[string]bool{"ping": true, "join_rate": true, "talker_pause": true, "gc_warn": true, "burst": true}}
	s := Round2Summary(o, v3)
	for _, want := range []string{"chunk 250", "rate min(12,5000/N)", "in-flight 8", "ping 5s", "bursts on", "gc 2ms/5ms", "grpc-log info"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q lacks %q", s, want)
		}
	}
	s2 := Round2Summary(o, Features{Version: 2, Set: map[string]bool{}})
	for _, want := range []string{"rate rule not applied", "ping off", "bursts off"} {
		if !strings.Contains(s2, want) {
			t.Errorf("v2 summary %q lacks %q", s2, want)
		}
	}
}

func TestValidateRejectsBadRound2Values(t *testing.T) {
	base := func() Options {
		o := Options{ServerBin: "s", LoadbotBin: "l", OutDir: "o", Scenario: "one-big-net", Arms: "S", ServerSet: "0,1", GenSet: "2,3", Topology: "t", PredictedKnee: 100, Bot: DefaultBotParams()}
		return o
	}
	cases := map[string]func(*Options){
		"--ping-every":      func(o *Options) { o.Bot.PingEvery = -time.Second },
		"--ramp-chunk":      func(o *Options) { o.RampChunk = -1 },
		"--burst-add":       func(o *Options) { o.BurstAdd = -1 },
		"--burst-in-flight": func(o *Options) { o.BurstInFlight = -1 },
		"gc warn":           func(o *Options) { o.Bot.GCPauseWarnP99 = 9 * time.Millisecond; o.Bot.GCPauseP99 = 5 * time.Millisecond },
	}
	for name, mut := range cases {
		o := base()
		mut(&o)
		if err := o.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	o := base()
	if err := o.Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestValidateRejectsUnknownGRPCLog(t *testing.T) {
	o := Options{ServerBin: "s", LoadbotBin: "l", OutDir: "o", Scenario: "one-big-net", Arms: "S", ServerSet: "0,1", GenSet: "2,3", Topology: "t", PredictedKnee: 100}
	o.Bot.GRPCLog = "loud"
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "--grpc-log") {
		t.Errorf("err = %v", err)
	}
	o.Bot.GRPCLog = "off"
	if err := o.Validate(); err != nil {
		t.Errorf("off must be valid: %v", err)
	}
}

func TestManifestLabel(t *testing.T) {
	const L = "local-docker, relative only. Not a capacity figure."
	if got := newManifest(Options{Label: L}, nil, nil).Label; got != L {
		t.Errorf("manifest label = %q, want %q", got, L)
	}
	if got := newManifest(Options{}, nil, nil).Label; got != Label {
		t.Errorf("default manifest label = %q, want leg.Label", got)
	}
}

func TestManifestGeneratorGC(t *testing.T) {
	feats := Features{Version: 3, Set: map[string]bool{}}
	for _, tc := range []struct {
		gc      string
		wantGC  string
		hasGOGC bool
	}{{"", "default", false}, {"off", "off", true}, {"default", "default", false}} {
		o := Options{Bot: BotParams{GeneratorGC: tc.gc}}
		m := newManifest(o, nil, nil)
		m.recordRound2(o, feats)
		if m.GeneratorGC != tc.wantGC {
			t.Errorf("%q: generator_gc = %q, want %q", tc.gc, m.GeneratorGC, tc.wantGC)
		}
		for kind, env := range m.GeneratorEnv {
			if got := envHas(env, "GOGC=off"); got != tc.hasGOGC {
				t.Errorf("%q: generator_env.%s GOGC=off present = %v", tc.gc, kind, got)
			}
		}
	}
	o := Options{Bot: BotParams{GeneratorGC: "default"}}
	if s := Round2Summary(o, Features{Version: 3, Set: map[string]bool{}}); !strings.Contains(s, "generator GC: default (Go defaults)") {
		t.Errorf("summary %q lacks the default-GC note", s)
	}
	// The summary always names the GC mode (W6 kit fix 3).
	if s := Round2Summary(Options{Bot: BotParams{GeneratorGC: "off"}}, Features{Version: 3, Set: map[string]bool{}}); !strings.Contains(s, "generator GC: off (GOGC=off GOMEMLIMIT=1GiB)") {
		t.Errorf("off summary %q lacks the off-GC note", s)
	}
	if s := Round2Summary(Options{}, Features{Version: 3, Set: map[string]bool{}}); !strings.Contains(s, "generator GC: default (Go defaults)") {
		t.Errorf("empty-mode summary %q lacks the default-GC note", s)
	}
}
