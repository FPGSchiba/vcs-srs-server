package leg

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// CanonicalArms are the benchmark arms in the order legs report them: S is one
// standalone process on one cpu, Seq the same process on two cpus, D2 a
// distributed topology (control, two nodes, global) on two cpus.
var CanonicalArms = []string{"S", "Seq", "D2"}

// Scenarios are the scenarios a leg can run.
var Scenarios = []string{"one-big-net", "org-op"}

// orgOpRxPPS is what one bot receives in org-op: 5% of the roster talks at 50
// frames/s, each to 7.7 listeners (the constants of predict.go). Constant
// arithmetic is exact, so this is exactly 19.25.
const orgOpRxPPS = 50 * 0.05 * 7.7

// RxPPSPerBot is the voice packets per second one bot receives in the
// scenario: the calibration's receive capacity divided by it is the number of
// bots the generator can listen for. An unknown scenario gets the one-big-net
// figure; Validate rejects it before any caller asks.
func RxPPSPerBot(scenario string) float64 {
	if scenario == "org-op" {
		return orgOpRxPPS
	}
	return rxPPSPerBot
}

// ParseArms reads a comma list of arms. It must be a non-empty subset of
// CanonicalArms with no duplicates and is returned in canonical order.
func ParseArms(csv string) ([]string, error) {
	seen := map[string]bool{}
	for _, a := range strings.Split(csv, ",") {
		a = strings.TrimSpace(a)
		if !validArm(a) {
			return nil, fmt.Errorf("--arms %q: %q is not one of %s", csv, a, strings.Join(CanonicalArms, ", "))
		}
		if seen[a] {
			return nil, fmt.Errorf("--arms %q: %q is listed twice", csv, a)
		}
		seen[a] = true
	}
	var out []string
	for _, a := range CanonicalArms {
		if seen[a] {
			out = append(out, a)
		}
	}
	return out, nil
}

func validArm(a string) bool {
	for _, c := range CanonicalArms {
		if a == c {
			return true
		}
	}
	return false
}

// ValidScenario reports whether s is one of Scenarios.
func ValidScenario(s string) bool {
	for _, c := range Scenarios {
		if s == c {
			return true
		}
	}
	return false
}

// ArmOrder is the order a leg runs the arms in: a seeded permutation of the
// arms taken in canonical order, so the same seed reproduces the order and no
// arm is always first (design I-6). The input is not modified.
func ArmOrder(arms []string, seed int64) []string {
	canon := make([]string, 0, len(arms))
	for _, c := range CanonicalArms {
		for _, a := range arms {
			if a == c {
				canon = append(canon, c)
			}
		}
	}
	out := make([]string, len(canon))
	for i, p := range rand.New(rand.NewSource(seed)).Perm(len(canon)) {
		out[i] = canon[p]
	}
	return out
}

// ArmPlan is the kit-owned process layout of one arm (design I-5).
type ArmPlan struct {
	Arm string
	// Server is the pin of each server process; S and Seq have only
	// Standalone, D2 has Control, NodeA, NodeB and Global.
	Server map[topology.Role]topology.Pin
	// Gen is the load generator's pin.
	Gen topology.Pin
	// Distributed is true for D2: the bot dials the nodes, never global.
	Distributed bool
}

func joinCPUs(c []int) string {
	s := make([]string, len(c))
	for i, v := range c {
		s[i] = strconv.Itoa(v)
	}
	return strings.Join(s, ",")
}

// PlanArm lays an arm out on the server set s and the generator set g. S takes
// one or two server cpus and uses the first. Seq and D2 need exactly two, and
// the sets must be valid and disjoint.
func PlanArm(arm, serverSet, genSet string) (ArmPlan, error) {
	if !validArm(arm) {
		return ArmPlan{}, fmt.Errorf("unknown arm %q", arm)
	}
	if err := disjoint(serverSet, genSet); err != nil {
		return ArmPlan{}, err
	}
	s, _ := CPUList(serverSet)
	g, _ := CPUList(genSet)
	if len(g) == 0 {
		return ArmPlan{}, fmt.Errorf("arm %s: empty generator cpu set", arm)
	}
	switch {
	case arm == "S" && (len(s) < 1 || len(s) > 2):
		return ArmPlan{}, fmt.Errorf("arm S needs 1 or 2 server cpus, got %d (%q)", len(s), serverSet)
	case arm != "S" && len(s) != 2:
		return ArmPlan{}, fmt.Errorf("arm %s needs 2 server cpus, got %d (%q)", arm, len(s), serverSet)
	}
	p := ArmPlan{Arm: arm, Gen: topology.Pin{CPUs: genSet, GOMAXPROCS: len(g)}}
	first := topology.Pin{CPUs: strconv.Itoa(s[0]), GOMAXPROCS: 1}
	switch arm {
	case "S":
		p.Server = map[topology.Role]topology.Pin{topology.Standalone: first}
	case "Seq":
		p.Server = map[topology.Role]topology.Pin{topology.Standalone: {CPUs: joinCPUs(s), GOMAXPROCS: 2}}
	case "D2":
		both := topology.Pin{CPUs: joinCPUs(s), GOMAXPROCS: 2}
		p.Distributed = true
		p.Server = map[topology.Role]topology.Pin{
			topology.Control: both,
			topology.NodeA:   first,
			topology.NodeB:   {CPUs: strconv.Itoa(s[1]), GOMAXPROCS: 1},
			topology.Global:  both,
		}
	}
	return p, nil
}

// ArmAddrs are the addresses the bot may dial. S and Seq use Control and Voice;
// D2 uses Control and the two Nodes (never global).
type ArmAddrs struct {
	Control, Voice string
	Nodes          []string
}

// BotArgv is the `vcs-loadbot run` argument list (without the binary) for an
// arm. The kit never passes --payload, --parity or --hello-ladder: the bot's
// defaults apply and the manifest records them from the step line.
func BotArgv(arm, scenario string, addr ArmAddrs, pw string, seed int64, f Features, p BotParams) []string {
	a := []string{"run", "--scenario", scenario, "--arm", arm, "--control", addr.Control}
	allow := []string{addr.Control}
	if arm == "D2" {
		allow = append(allow, addr.Nodes...)
	} else {
		a = append(a, "--voice", addr.Voice)
		allow = append(allow, addr.Voice)
	}
	a = append(a, "--allow", strings.Join(allow, ","), "--passwords", pw, "--unit", "LDBT",
		"--seed", strconv.FormatInt(seed, 10))
	return append(a, BotRunArgsWith(f, p)...)
}

// RequireFeatures fails when the bot lacks what the arms and scenario need:
// arm_d2 for D2, org_op for org-op. It fails before any process starts.
func RequireFeatures(arms []string, scenario string, f Features) error {
	var missing []string
	for _, a := range arms {
		if a == "D2" && !f.Has("arm_d2") {
			missing = append(missing, "arm_d2")
			break
		}
	}
	if scenario == "org-op" && !f.Has("org_op") {
		missing = append(missing, "org_op")
	}
	if len(missing) > 0 {
		return fmt.Errorf("bot lacks %s; bump client_ref", strings.Join(missing, ", "))
	}
	return nil
}
