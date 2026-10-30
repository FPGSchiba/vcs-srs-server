package leg

import (
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// AnchoredScale is the bot's join-rate scale lifted so the rate at top bots is
// at least floor: max(scale, floor*top). floor <= 0 returns scale unchanged.
func AnchoredScale(scale, floor float64, top int) float64 {
	if floor <= 0 {
		return scale
	}
	return max(scale, floor*float64(top))
}

// CapInput is everything HostCap needs; MemTop 0 means memory is unknown.
type CapInput struct {
	Model         sweep.JoinModel
	Floor         float64
	HasRule       bool
	Budget        time.Duration
	Steps         int
	PortRange     [2]int
	UlimitN       int
	Explicit      int
	GenCPUs       int
	BotsPerGenCPU int
	MemTop        int
	CalBots       int
}

// Caps is HostCap's answer.
type Caps struct {
	NMax  int
	Top   int
	Start int
	// By is the key of the binding limit: time, memory, generator-cpu, ports
	// or explicit.
	By string
	// Scale is the anchored join scale (0 when the bot rule is off).
	Scale float64
	// Limits holds every limit that applied, by key.
	Limits map[string]int
}

// HostCap is N_max: the minimum of the time budget, the port and descriptor
// budget, memory, generator CPU and an explicit cap, together with Top (the
// largest bot count any step may request), the sweep's start and the anchored
// join scale.
func HostCap(in CapInput) Caps {
	portU := max(0, min((in.PortRange[1]-in.PortRange[0]+1)/2, (in.UlimitN-256)/4))
	limits := map[string]int{"ports": portU / 2}
	if in.MemTop > 0 {
		limits["memory"] = in.MemTop
	}
	if in.GenCPUs > 0 && in.BotsPerGenCPU > 0 {
		limits["generator-cpu"] = in.GenCPUs * in.BotsPerGenCPU
	}
	if in.Explicit > 0 {
		limits["explicit"] = in.Explicit
	}
	hostMax := limits["ports"]
	for _, k := range []string{"memory", "generator-cpu", "explicit"} {
		if v, ok := limits[k]; ok {
			hostMax = min(hostMax, v)
		}
	}

	top0 := min(2*hostMax, portU)
	if in.MemTop > 0 {
		top0 = min(top0, in.MemTop)
	}
	c := Caps{Limits: limits}
	tm := in.Model
	if in.HasRule {
		c.Scale = AnchoredScale(in.Model.Scale, in.Floor, top0)
		tm = sweep.JoinModel{Max: in.Model.Max, Scale: c.Scale}
	}
	limits["time"] = sweep.NMax(tm, in.Budget, in.Steps)

	c.NMax = limits["time"]
	c.By = "time"
	for _, k := range []string{"memory", "generator-cpu", "ports", "explicit"} {
		if v, ok := limits[k]; ok && v < c.NMax {
			c.NMax, c.By = v, k
		}
	}
	c.NMax = max(c.NMax, 0)

	c.Top = min(2*c.NMax, portU)
	if in.MemTop > 0 {
		c.Top = min(c.Top, in.MemTop)
	}
	c.Start = sweep.HostStart(in.CalBots, c.NMax)
	return c
}
