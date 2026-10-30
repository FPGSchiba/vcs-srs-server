package leg

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// rxPPSPerBot is what one bot receives in one-big-net: every bot hears the one
// talker at 50 frames/s. The calibration's receive capacity divided by it is the
// number of bots the generator can listen for.
const rxPPSPerBot = 50

// minMemBots is the fewest bots a host must fit for a leg to be worth running.
const minMemBots = 64

// memoryPreflight is the memory pre-flight. It returns the memory top (0 =
// unknown, -1 = below the reserve; also returned with a refusal), the
// calibration steps trimmed to it, and a note for the log and the manifest. A
// host that fits fewer than minMemBots bots is refused.
func memoryPreflight(o Options, mf MemFacts) (memTop int, calSteps, note string, err error) {
	perBot := int64(o.MemPerBotMiB * (1 << 20))
	reserve := int64(o.MemReserveMiB) << 20
	memTop = MemTop(mf, perBot, reserve, o.MemMargin)
	switch {
	case memTop == -1 || (memTop > 0 && memTop < minMemBots):
		fits := max(memTop, 0)
		return memTop, o.CalSteps, "", fmt.Errorf("pre-flight: memory: %s fits %d bots at %s MiB/bot (margin %s, reserve %d MiB); need at least %d",
			mf.Describe(), fits, fmtFloat(o.MemPerBotMiB), fmtFloat(o.MemMargin), o.MemReserveMiB, minMemBots)
	case memTop == 0:
		return 0, o.CalSteps, "memory pre-flight skipped: " + mf.Describe(), nil
	}
	var keep, dropped []string
	for _, f := range strings.Split(o.CalSteps, ",") {
		f = strings.TrimSpace(f)
		n, perr := strconv.Atoi(f)
		if perr != nil {
			// Not ours to judge: calibration reports a malformed step list.
			return memTop, o.CalSteps, "", nil
		}
		if n > memTop {
			dropped = append(dropped, f)
		} else {
			keep = append(keep, f)
		}
	}
	if len(dropped) == 0 {
		return memTop, o.CalSteps, "", nil
	}
	if len(keep) == 0 {
		return memTop, o.CalSteps, "", fmt.Errorf("pre-flight: memory: every calibration step (%s) is above the memory top %d", o.CalSteps, memTop)
	}
	return memTop, strings.Join(keep, ","),
		fmt.Sprintf("calibration steps above the memory top %d dropped: %s", memTop, strings.Join(dropped, ",")), nil
}

// planHost sizes the leg from the host: N_max, Top, the sweep start and, with
// the bot's rate rule, the anchored join scale, which it writes into o.Bot so
// the bot argv carries it. The prediction is not an input.
func planHost(o *Options, feats Features, memTop, calBots int, budget time.Duration, portRange [2]int, ulimitN, genCPUs int) Caps {
	in := CapInput{
		Model: sweep.JoinModel{Const: o.JoinRate}, Floor: o.JoinRateFloor, Budget: budget, Steps: sweepSteps,
		PortRange: portRange, UlimitN: ulimitN, Explicit: o.NMaxCap,
		GenCPUs: genCPUs, BotsPerGenCPU: o.BotsPerGenCPU, MemTop: memTop, CalBots: calBots,
	}
	if feats.Has("join_rate") {
		in.HasRule = true
		in.Model = sweep.JoinModel{Max: o.Bot.JoinRateMax, Scale: o.Bot.JoinRateScale}
	}
	caps := HostCap(in)
	if in.HasRule {
		o.Bot.JoinRateScale = caps.Scale
	}
	return caps
}

// driverConfig is the Driver's configuration from the options and the host
// caps. The caller adds the callbacks.
func driverConfig(o Options, caps Caps, remaining time.Duration, feats Features) Config {
	var rateMax, rateScale float64
	if feats.Has("join_rate") {
		rateMax, rateScale = o.Bot.JoinRateMax, o.Bot.JoinRateScale
	}
	return Config{
		Predicted: o.PredictedKnee, NMax: caps.NMax, Start: caps.Start, Top: caps.Top, NMaxBy: caps.By,
		JoinRate: o.JoinRate, Budget: remaining,
		Warmup: o.Warmup, Hold: o.Hold, Confirm: o.Confirm, Overload: o.Overload, ProbeHold: time.Second,
		Bounds:    sweep.DefaultBounds,
		RampChunk: o.RampChunk, RateMax: rateMax, RateScale: rateScale,
		BurstEnabled: o.Bursts, BurstAdd: o.BurstAdd, BurstInFlight: o.BurstInFlight,
	}
}

// describeCaps is the readable log form of a Caps: every limit that applied,
// then the binding one.
func describeCaps(c Caps) string {
	keys := make([]string, 0, len(c.Limits))
	for k := range c.Limits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, c.Limits[k]))
	}
	return fmt.Sprintf("N_max limits: %s; binding: %s", strings.Join(parts, " "), c.By)
}

// HostCapsRecord is the manifest's host_caps.
type HostCapsRecord struct {
	NMax    int            `json:"n_max"`
	By      string         `json:"by"`
	Top     int            `json:"top"`
	Start   int            `json:"start"`
	CalBots int            `json:"cal_bots"`
	Limits  map[string]int `json:"limits"`
}

// MemoryRecord is the manifest's memory block.
type MemoryRecord struct {
	Source       string  `json:"source"`
	MemAvailable int64   `json:"mem_available"`
	CgroupLimit  int64   `json:"cgroup_limit"`
	CgroupUsage  int64   `json:"cgroup_usage"`
	Avail        int64   `json:"avail"`
	PerBotMiB    float64 `json:"per_bot_mib"`
	Margin       float64 `json:"margin"`
	ReserveMiB   int     `json:"reserve_mib"`
	MemTop       int     `json:"mem_top"`
	// Refused is true when the pre-flight refused the host; MemTop is then the
	// real figure (-1 = below the reserve), not "unknown".
	Refused bool `json:"refused,omitempty"`
}

func (m *Manifest) recordMemory(o Options, mf MemFacts, memTop int, refused bool) {
	m.Memory = &MemoryRecord{
		Source: mf.Source, MemAvailable: mf.MemAvailable, CgroupLimit: mf.CgroupLimit, CgroupUsage: mf.CgroupUsage, Avail: mf.Avail,
		PerBotMiB: o.MemPerBotMiB, Margin: o.MemMargin, ReserveMiB: o.MemReserveMiB, MemTop: memTop, Refused: refused,
	}
}

func (m *Manifest) recordCaps(c Caps, calBots int) {
	limits := make(map[string]int, len(c.Limits))
	for k, v := range c.Limits {
		limits[k] = v
	}
	m.NMax = c.NMax
	m.HostCaps = &HostCapsRecord{NMax: c.NMax, By: c.By, Top: c.Top, Start: c.Start, CalBots: calBots, Limits: limits}
}

// recordOutcome sets the manifest's result fields from the driver's outcome. A
// run error records no result; a stopped-early leg records its best bracket and
// the error that stopped it.
func (m *Manifest) recordOutcome(out *Outcome, runErr error) {
	if runErr != nil {
		return
	}
	m.Result = out.Result.String()
	// What the bot actually sent and measured replaces the pre-run constants.
	m.Payload, m.NotMeasured = outcomePayload(out), outcomeNotMeasured(out)
	if out.Unconfirmed {
		m.Result += " (unconfirmed: the confirm step did not pass)"
	}
	m.StoppedEarly = out.StoppedEarly
}

// legResultsInput is what results.md is written from.
func legResultsInput(o Options, m *Manifest, cal *Calibration, out *Outcome) ResultsInput {
	return ResultsInput{
		Seed: o.Seed, Arm: o.Arms, Scenario: o.Scenario, Result: m.Result, Error: m.Error, StoppedEarly: m.StoppedEarly,
		Cal: cal, Out: out, Predicted: o.PredictedKnee, NMax: m.NMax, Label: o.Label,
	}
}

// writeOutcomeFiles writes steps.csv always, so a partial run is inspectable,
// and knees.csv only when the run did not fail.
func writeOutcomeFiles(o Options, out *Outcome, nMax int, runErr error) error {
	if err := writeFile(o.OutDir, "steps.csv", func(w io.Writer) error { return WriteStepsCSVLabeled(w, out, o.Topology, o.Label) }); err != nil {
		return err
	}
	if runErr != nil {
		return nil
	}
	return writeFile(o.OutDir, "knees.csv", func(w io.Writer) error {
		return WriteKneesCSV(w, out, KneeMeta{Arm: o.Arms, Scenario: o.Scenario, Topology: o.Topology, Predicted: o.PredictedKnee, NMax: nMax, Label: o.Label})
	})
}

// decisionsRound3 are the W6 kit fix 3 changes with old -> new values, recorded
// in every manifest so a result can be read against the kit that produced it.
func decisionsRound3() map[string]string {
	return map[string]string{
		"R3-1":  "generator GC (--generator-gc): off -> default (GOGC=off GOMEMLIMIT=1GiB -> Go defaults); off cost ~8x generator CPU at N=2410",
		"R3-2":  "bench-host --gogc default: off -> default",
		"R3-3":  "bot join rule: min(12, 5000/N) -> min(12, max(5000, floor*Top)/N), --join-rate-floor 4 joins/s",
		"R3-4":  "time cap and guard: ramps priced at a constant --join-rate -> priced with the bot's rule, overload ramp included",
		"R3-5":  "N_max: min(time, ports/descriptors, --n-max) -> min(time, ports/descriptors, memory, generator CPU, --n-max), binding limit recorded",
		"R3-6":  "sweep start: predicted knee / 4 -> HostStart(calibration bots, N_max)",
		"R3-7":  "warm-up N: max(2, predicted/10) -> max(2, start/2)",
		"R3-8":  "predicted knee: required -> optional, context only",
		"R3-9":  "bench-host --micro skip: needs a prediction -> needs none",
		"R3-10": "overload N: min(2k, 2*N_max) -> min(2k, 2*N_max, Top)",
		"R3-11": "guard tail reserve: confirm + overload + 2*R(0,lo) -> confirm + overload + R(0,lo) + R(lo, overload N), confirm and overload each guarded",
		"R3-12": "guard stop after a breach: knee lo -> lo <= knee < hi (bracketed)",
		"R3-13": "run error after >= 1 sweep step: no result, exit 1 -> best bracket, stopped_early, knees.csv, exit 0",
		"R3-14": "time-bound at N_max: always time-bound -> kind named by the binding limit",
		"R3-15": "new tunables: none -> --mem-per-bot-mib 2, --mem-margin 0.2, --mem-reserve-mib 512, --bots-per-gen-cpu 1500",
	}
}
