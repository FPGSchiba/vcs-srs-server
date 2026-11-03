package leg

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

func feats(names ...string) Features {
	f := Features{Version: 4, Set: map[string]bool{}}
	for _, n := range names {
		f.Set[n] = true
	}
	return f
}

func TestParseArms(t *testing.T) {
	got, err := ParseArms("D2,S")
	if err != nil || !reflect.DeepEqual(got, []string{"S", "D2"}) {
		t.Fatalf("ParseArms(D2,S) = %v, %v", got, err)
	}
	for _, bad := range []string{"S,S", "X", "", "S,,Seq"} {
		if _, err := ParseArms(bad); err == nil {
			t.Errorf("ParseArms(%q) must fail", bad)
		}
	}
}

func TestPlanArmTableMatchesDesign(t *testing.T) {
	gen := topology.Pin{CPUs: "2,3", GOMAXPROCS: 2}
	want := map[string]ArmPlan{
		"S": {Arm: "S", Gen: gen, Server: map[topology.Role]topology.Pin{
			topology.Standalone: {CPUs: "0", GOMAXPROCS: 1}}},
		"Seq": {Arm: "Seq", Gen: gen, Server: map[topology.Role]topology.Pin{
			topology.Standalone: {CPUs: "0,1", GOMAXPROCS: 2}}},
		"D2": {Arm: "D2", Gen: gen, Distributed: true, Server: map[topology.Role]topology.Pin{
			topology.Control: {CPUs: "0,1", GOMAXPROCS: 2},
			topology.NodeA:   {CPUs: "0", GOMAXPROCS: 1},
			topology.NodeB:   {CPUs: "1", GOMAXPROCS: 1},
			topology.Global:  {CPUs: "0,1", GOMAXPROCS: 2}}},
	}
	for arm, w := range want {
		got, err := PlanArm(arm, "0,1", "2,3")
		if err != nil {
			t.Fatalf("%s: %v", arm, err)
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s: got %+v want %+v", arm, got, w)
		}
	}
}

func TestPlanArmSetSizesAndOverlap(t *testing.T) {
	for _, arm := range []string{"Seq", "D2"} {
		_, err := PlanArm(arm, "0", "2,3")
		if err == nil || !strings.Contains(err.Error(), arm) || !strings.Contains(err.Error(), "needs 2 server cpus") {
			t.Errorf("%s with one cpu: err = %v", arm, err)
		}
	}
	if _, err := PlanArm("S", "0", "2,3"); err != nil {
		t.Errorf("S with one cpu: %v", err)
	}
	if _, err := PlanArm("S", "0,1,2", "4,5"); err == nil {
		t.Error("S with three server cpus must fail")
	}
	if _, err := PlanArm("S", "0,1", "1,2"); err == nil {
		t.Error("overlapping sets must fail")
	}
	if _, err := PlanArm("X", "0,1", "2,3"); err == nil {
		t.Error("unknown arm must fail")
	}
	// A range is expanded into the list taskset gets.
	p, err := PlanArm("Seq", "4-5", "6-7")
	if err != nil || p.Server[topology.Standalone].CPUs != "4,5" {
		t.Errorf("range: %+v, %v", p, err)
	}
}

func TestBotArgv(t *testing.T) {
	f := feats("ping", "join_rate", "talker_pause", "gc_warn")
	p := DefaultBotParams()
	addr := ArmAddrs{Control: "127.0.0.1:1", Voice: "127.0.0.1:2", Nodes: []string{"127.0.0.1:3", "127.0.0.1:4"}}

	wantS := append([]string{
		"run", "--scenario", "one-big-net", "--arm", "S",
		"--control", "127.0.0.1:1", "--voice", "127.0.0.1:2", "--allow", "127.0.0.1:1,127.0.0.1:2",
		"--passwords", "pw.json", "--unit", "LDBT", "--seed", "7",
	}, BotRunArgsWith(f, p)...)
	if got := BotArgv("S", "one-big-net", addr, "pw.json", 7, f, p); !reflect.DeepEqual(got, wantS) {
		t.Errorf("S argv:\n got %v\nwant %v", got, wantS)
	}
	seq := BotArgv("Seq", "org-op", addr, "pw.json", 7, f, p)
	if seq[4] != "Seq" || seq[2] != "org-op" {
		t.Errorf("Seq argv: %v", seq)
	}

	d2 := BotArgv("D2", "one-big-net", addr, "pw.json", 7, f, p)
	joined := strings.Join(d2, " ")
	if !strings.Contains(joined, "--arm D2") || !strings.Contains(joined, "--allow 127.0.0.1:1,127.0.0.1:3,127.0.0.1:4") {
		t.Errorf("D2 argv: %v", d2)
	}
	for _, a := range d2 {
		if a == "--voice" || a == "--payload" || a == "--parity" || a == "--hello-ladder" {
			t.Errorf("D2 argv must not carry %s: %v", a, d2)
		}
	}
	// The kit never passes --payload, --parity or --hello-ladder for any arm.
	for _, a := range wantS {
		if a == "--payload" || a == "--parity" || a == "--hello-ladder" {
			t.Errorf("S argv carries %s", a)
		}
	}
}

func TestArmGatesOnV3Features(t *testing.T) {
	v3 := feats("burst", "control_lost", "gc_warn", "hello_ladder", "join_rate", "ping", "talker_pause")
	if err := RequireFeatures([]string{"S", "D2"}, "one-big-net", v3); err == nil ||
		err.Error() != "bot lacks arm_d2; bump client_ref" {
		t.Errorf("D2 on v3: %v", err)
	}
	if err := RequireFeatures([]string{"S"}, "org-op", v3); err == nil ||
		err.Error() != "bot lacks org_op; bump client_ref" {
		t.Errorf("org-op on v3: %v", err)
	}
	if err := RequireFeatures([]string{"S", "Seq"}, "one-big-net", v3); err != nil {
		t.Errorf("S/Seq one-big-net on v3: %v", err)
	}
	both := RequireFeatures([]string{"D2"}, "org-op", v3)
	if both == nil || !strings.Contains(both.Error(), "arm_d2") || !strings.Contains(both.Error(), "org_op") {
		t.Errorf("both missing: %v", both)
	}
	v4 := feats("arm_d2", "org_op", "latency", "parity", "opus_corpus")
	if err := RequireFeatures([]string{"S", "Seq", "D2"}, "org-op", v4); err != nil {
		t.Errorf("v4: %v", err)
	}
}

func TestArmOrder(t *testing.T) {
	arms := []string{"S", "Seq", "D2"}
	a, b := ArmOrder(arms, 1), ArmOrder(arms, 1)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("not reproducible: %v vs %v", a, b)
	}
	s := append([]string(nil), a...)
	sort.Strings(s)
	if !reflect.DeepEqual(s, []string{"D2", "S", "Seq"}) {
		t.Errorf("not a permutation: %v", a)
	}
	if got := ArmOrder([]string{"S"}, 5); !reflect.DeepEqual(got, []string{"S"}) {
		t.Errorf("single arm: %v", got)
	}
	if len(arms) != 3 || arms[0] != "S" {
		t.Error("ArmOrder must not modify its input")
	}
}

func TestRxPPSPerBot(t *testing.T) {
	if got := RxPPSPerBot("one-big-net"); got != 50 {
		t.Errorf("one-big-net = %v", got)
	}
	if got := RxPPSPerBot("org-op"); got != 19.25 {
		t.Errorf("org-op = %v", got)
	}
	if !reflect.DeepEqual(Scenarios, []string{"one-big-net", "org-op"}) {
		t.Errorf("Scenarios = %v", Scenarios)
	}
}
