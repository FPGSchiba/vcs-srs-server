package leg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// The processes a leg starts, as variables so a test can stand fakes in: the
// load bot's feature probe, the one calibration, each arm's environment and the
// driver that runs an arm's protocol.
var (
	probeFeatures  = ProbeFeatures
	runCalibration = RunCalibration
	newArmEnv      = NewArmEnv
	runDriver      = func(ctx context.Context, env Env, cfg Config) (*Outcome, error) {
		return NewDriver(env, cfg).Run(ctx)
	}
)

// armPins is the CPU plan of one arm as the manifest records it: the arm's own
// server pins (one "server" for S and Seq, one entry per role for D2), the
// reflector on the whole server set and the generator on its own.
func armPins(plan ArmPlan, serverSet, genSet string) map[string]PinRecord {
	serverCPUs, _ := CPUList(serverSet)
	genCPUs, _ := CPUList(genSet)
	pins := map[string]PinRecord{
		"reflector": {CPUs: serverSet, GOMAXPROCS: len(serverCPUs)},
		"generator": {CPUs: genSet, GOMAXPROCS: len(genCPUs)},
	}
	if !plan.Distributed {
		p := plan.Server[topology.Standalone]
		pins["server"] = PinRecord{CPUs: p.CPUs, GOMAXPROCS: p.GOMAXPROCS, Shards: topology.ShardCount(p.GOMAXPROCS)}
		return pins
	}
	for role, p := range plan.Server {
		r := PinRecord{CPUs: p.CPUs, GOMAXPROCS: p.GOMAXPROCS}
		if role == topology.NodeA || role == topology.NodeB {
			r.Shards = topology.ShardCount(p.GOMAXPROCS)
		}
		pins[string(role)] = r
	}
	return pins
}

// armPredictions is each arm's B-0 prediction, context only: --predicted-knee
// applies to every arm, else predict.json is read per arm, else none.
func armPredictions(o Options, arms []string) (map[string]int, error) {
	out := make(map[string]int, len(arms))
	var raw []byte
	if o.PredictedKnee <= 0 && o.PredictFile != "" {
		b, err := os.ReadFile(o.PredictFile)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	for _, a := range arms {
		switch {
		case o.PredictedKnee > 0:
			out[a] = o.PredictedKnee
		case raw != nil:
			n, err := PredictedKnee(raw, o.Scenario, a)
			if err != nil {
				return nil, err
			}
			out[a] = n
		}
	}
	return out, nil
}

// firstHeldStep is the bot's step line of the first held row, else nil.
func firstHeldStep(o *Outcome) *Step {
	if o == nil {
		return nil
	}
	for i := range o.Rows {
		if o.Rows[i].Held {
			return &o.Rows[i].Step
		}
	}
	return nil
}

// armRun is one arm of a leg: its options and manifest, what it produced and
// the results.md section written from them.
type armRun struct {
	arm string
	o   Options
	m   *Manifest
	out *Outcome
	res ArmResult
	err error
	md  []byte
}

// legRun is what the arms of one leg share: the host's facts, the one
// calibration and the log.
type legRun struct {
	o         Options
	logw      io.Writer
	arms      []string // canonical order
	order     []string // run order
	predicted map[string]int
	base      *Manifest
	feats     Features
	memTop    int
	calNotes  []string
	portRange [2]int
	ulimitN   int
	serverCPU []int
	genCPUs   []int
	cal       *Calibration
	scratch   string
	runs      map[string]*armRun
}

// newArmRun starts an arm: its output directory and a manifest copied from the
// leg's base with the arm's own name, prediction and pins.
func (lc *legRun) newArmRun(arm string) *armRun {
	o := lc.o
	o.Arms, o.OutDir, o.PredictedKnee = arm, filepath.Join(lc.o.OutDir, arm), lc.predicted[arm]
	m := *lc.base
	m.Arm, m.PredictedKnee, m.ArmOrder = arm, o.PredictedKnee, lc.order
	if plan, err := PlanArm(arm, o.ServerSet, o.GenSet); err == nil {
		m.Pins = armPins(plan, o.ServerSet, o.GenSet)
	}
	m.Notes = append([]string(nil), lc.calNotes...)
	ar := &armRun{arm: arm, o: o, m: &m}
	_ = os.MkdirAll(o.OutDir, 0o755)
	lc.runs[arm] = ar
	return ar
}

// fail records the arm as not finished: its manifest carries the error, the
// leg index only the kind.
func (ar *armRun) fail(err error) {
	ar.err = err
	ar.res = ArmResult{Result: kindError, Kind: kindError}
	ar.m.Error = err.Error()
	_ = writeManifest(ar.o.OutDir, ar.m)
}

// emitResults builds the arm's results.md section, writes it into the arm
// directory and prints it to the log, for the person watching the run.
func (lc *legRun) emitResults(ar *armRun, logf func(string, ...any)) {
	var buf bytes.Buffer
	if err := WriteResultsMarkdown(&buf, legResultsInput(ar.o, ar.m, lc.cal, ar.out)); err != nil {
		logf("results.md: %v", err)
		return
	}
	ar.md = buf.Bytes()
	if err := os.WriteFile(filepath.Join(ar.o.OutDir, "results.md"), ar.md, 0o644); err != nil {
		logf("results.md: %v", err)
	}
	fmt.Fprintf(lc.logw, "\n%s\n", ar.md)
}

// failAll ends the leg before any arm ran: every arm is recorded as not
// finished, so the artifact still says what was asked for and why it stopped.
func (lc *legRun) failAll(err error, rep *diagReporter) error {
	rep.Failure(err)
	for _, a := range lc.order {
		ar := lc.newArmRun(a)
		ar.fail(err)
		lc.emitResults(ar, func(f string, a ...any) { fmt.Fprintf(lc.logw, f+"\n", a...) })
	}
	return err
}

// index is leg.json from the arms that ran.
func (lc *legRun) index() LegIndex {
	arms := make(map[string]ArmResult, len(lc.arms))
	for _, a := range lc.arms {
		if ar := lc.runs[a]; ar != nil {
			arms[a] = ar.res
		} else {
			arms[a] = ArmResult{Result: kindError, Kind: kindError}
		}
	}
	ratios, notes := Ratios(arms)
	return LegIndex{
		Schema: LegIndexSchema, Scenario: lc.o.Scenario, Seed: lc.o.Seed, ArmOrder: lc.order,
		Arms: arms, Ratios: ratios, RatioNotes: notes, Label: lc.o.LabelOrDefault(), TopologyLabel: lc.o.Topology,
	}
}

// finish writes leg.json and the combined results.md: one section per arm in
// canonical order, then the ratios. It prints the ratios, since each arm
// printed its own section.
func (lc *legRun) finish(logf func(string, ...any)) {
	idx := lc.index()
	if err := writeFile(lc.o.OutDir, "leg.json", func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(idx)
	}); err != nil {
		logf("leg.json: %v", err)
	}
	var buf bytes.Buffer
	for _, a := range lc.arms {
		if ar := lc.runs[a]; ar != nil && len(ar.md) > 0 {
			buf.Write(ar.md)
			buf.WriteString("\n")
		}
	}
	var ratios bytes.Buffer
	if err := writeRatiosMarkdown(&ratios, idx); err != nil {
		logf("results.md: %v", err)
		return
	}
	buf.Write(ratios.Bytes())
	if err := os.WriteFile(filepath.Join(lc.o.OutDir, "results.md"), buf.Bytes(), 0o644); err != nil {
		logf("results.md: %v", err)
	}
	fmt.Fprintf(lc.logw, "\n%s\n", ratios.String())
}

// Run executes one leg: manifest inputs, the load bot's features, memory
// pre-flight and one calibration, then every arm in the seed's order, each on a
// fresh topology with its own pre-flight, budget, files and manifest, then
// leg.json and the combined results.md. An arm that fails is recorded and the
// remaining arms still run; the leg then returns an error naming the failures.
func Run(ctx context.Context, o Options, logw io.Writer) error {
	// Progress arrives from the bot's reader goroutine as well as this one.
	logw = &syncWriter{w: logw}
	// Resolve inputs: explicit flags win over the probe and prediction files.
	var probeRaw []byte
	if o.ProbeFile != "" {
		b, err := os.ReadFile(o.ProbeFile)
		if err != nil {
			return err
		}
		p, err := ParseProbe(b)
		if err != nil {
			return err
		}
		probeRaw = b
		if o.ServerSet == "" {
			o.ServerSet = p.ServerSet
		}
		if o.GenSet == "" {
			o.GenSet = p.GeneratorSet
		}
		if o.Topology == "" {
			o.Topology = p.TopologyLabel
		}
	}
	o.normalizeRound2()
	if err := o.Validate(); err != nil {
		return err
	}
	arms, _ := ParseArms(o.Arms)
	predicted, err := armPredictions(o, arms)
	if err != nil {
		return err
	}
	if o.readFile == nil {
		o.readFile = os.ReadFile
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	serverCPUs, _ := CPUList(o.ServerSet)
	genCPUs, _ := CPUList(o.GenSet)

	lc := &legRun{
		o: o, logw: logw, arms: arms, order: ArmOrder(arms, o.Seed), predicted: predicted,
		base: newManifest(o, nil, probeRaw), serverCPU: serverCPUs, genCPUs: genCPUs, runs: map[string]*armRun{},
	}
	tag := LegTag(o.Seed, strings.Join(arms, "+"), o.Scenario)
	rep := &diagReporter{w: logw, tag: tag}
	logf := func(f string, a ...any) { fmt.Fprintf(logw, "%s "+f+"\n", append([]any{tag}, a...)...) }
	// Whatever happens next, the leg ends with leg.json and its results table.
	defer lc.finish(logf)

	// 1. Manifest inputs: sysctls (raise rmem_max/wmem_max only), port range, ulimit.
	if o.Sysctl && runtime.GOOS == "linux" {
		lc.base.Sysctl = RaiseSocketBuffers(o.readFile, runCmd, socketBufferTarget)
	} else {
		lc.base.Sysctl = map[string]string{"skipped": "not requested or not linux"}
	}
	lc.portRange, err = readPortRange(o.readFile)
	if err != nil {
		return lc.failAll(fmt.Errorf("read ip_local_port_range: %w", err), rep)
	}
	lc.ulimitN, err = readUlimitN()
	if err != nil {
		return lc.failAll(err, rep)
	}
	lc.base.PortRange, lc.base.UlimitN = lc.portRange, lc.ulimitN

	// 1b. What this load bot can do (v2 has no `features`): it decides which
	// flags every later run passes, and whether it can run the arms and the
	// scenario at all. Probed once per leg, before calibration or any server.
	lc.feats = probeFeatures(o.LoadbotBin, ProbeTimeout)
	lc.base.recordRound2(o, lc.feats)
	logf("%s", Round2Summary(o, lc.feats))
	if lc.feats.Version == 2 {
		logf("load bot v2 (no features subcommand or probe failed)")
	} else {
		logf("load bot v%d, features %v", lc.feats.Version, lc.feats.Names())
	}
	if err := RequireFeatures(arms, o.Scenario, lc.feats); err != nil {
		return lc.failAll(err, rep)
	}

	// 1c. Memory pre-flight, before any ramp: a host that cannot hold the bots is
	// refused, and calibration steps above what memory allows are dropped.
	mf := ReadMemFacts(o.readFile)
	memTop, calSteps, memNote, err := memoryPreflight(o, mf)
	lc.memTop = memTop
	lc.base.recordMemory(o, mf, memTop, err != nil)
	if err != nil {
		return lc.failAll(err, rep)
	}
	if memNote != "" {
		logf("%s", memNote)
		lc.calNotes = append(lc.calNotes, memNote)
	}
	o.CalSteps = calSteps
	lc.o.CalSteps = calSteps

	// 2. Calibration, once per leg: the generator's receive side on its own.
	logf("calibration: reflector on %s, generator on %s", o.ServerSet, o.GenSet)
	lc.cal, err = runCalibration(ctx, CalConfig{
		LoadbotBin: o.LoadbotBin, ServerSet: o.ServerSet, GenSet: o.GenSet, Seed: o.Seed,
		Steps: o.CalSteps, Hold: o.CalHold, Senders: len(serverCPUs), StderrDir: o.OutDir, Features: lc.feats, Bot: &o.Bot,
		OnLine: func(kind string, raw []byte) {
			switch kind {
			case "calibration":
				var s CalStep
				if json.Unmarshal(raw, &s) == nil {
					fmt.Fprintln(logw, FormatCalLine(tag, s))
				}
			case "progress":
				var p Progress
				if json.Unmarshal(raw, &p) == nil {
					fmt.Fprintln(logw, FormatRampProgress(tag, p))
				}
			}
		},
	})
	if err != nil {
		return lc.failAll(fmt.Errorf("calibration: %w", err), rep)
	}
	if lc.cal.StoppedAt > 0 {
		lc.calNotes = append(lc.calNotes, fmt.Sprintf("calibration stopped at N=%d: %s", lc.cal.StoppedAt, lc.cal.StopReason))
		fmt.Fprintln(logw, FormatCalStopped(tag, lc.cal))
	}
	if err := writeFile(o.OutDir, "calibration.csv", func(w io.Writer) error { return WriteCalibrationCSVLabeled(w, lc.cal, o.Topology, o.Label) }); err != nil {
		return lc.failAll(err, rep)
	}

	// 3. The arms. The scratch directory holds what must not reach the results
	// artifact: throwaway passwords and each server's data directory.
	scratch, err := os.MkdirTemp("", "vcs-benchkit-leg-")
	if err != nil {
		return lc.failAll(fmt.Errorf("scratch dir: %w", err), rep)
	}
	defer os.RemoveAll(scratch)
	lc.scratch = scratch
	// The server inherits the kit's environment (topology passes os.Environ()), so
	// its grpc transport log says why it closes a connection. Set only now: the
	// reflector, which speaks no gRPC, has already run.
	for _, kv := range ServerEnvWith(o.Bot.GRPCLog) {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return lc.failAll(fmt.Errorf("set %s: %w", k, err), rep)
		}
	}
	for _, arm := range lc.order {
		if cerr := ctx.Err(); cerr != nil {
			ar := lc.newArmRun(arm)
			ar.fail(fmt.Errorf("arm %s not run: %w", arm, cerr))
			lc.emitResults(ar, logf)
			continue
		}
		lc.runArm(ctx, arm)
	}
	var failed []string
	var first error
	for _, a := range lc.order {
		if ar := lc.runs[a]; ar != nil && ar.err != nil {
			failed = append(failed, a)
			if first == nil {
				first = ar.err
			}
		}
	}
	if first != nil {
		return fmt.Errorf("%d of %d arms did not finish (%s): %w", len(failed), len(arms), strings.Join(failed, ", "), first)
	}
	return nil
}

// runArm runs one arm on a fresh topology: its pre-flight (N_max, start and
// join scale from the host and this arm's budget), its sweep, its files and
// manifest. Failures are recorded on the arm, never returned.
func (lc *legRun) runArm(ctx context.Context, arm string) {
	ar := lc.newArmRun(arm)
	o, m := &ar.o, ar.m
	tag := LegTag(o.Seed, arm, o.Scenario)
	rep := &diagReporter{w: lc.logw, tag: tag}
	logf := func(f string, a ...any) { fmt.Fprintf(lc.logw, "%s "+f+"\n", append([]any{tag}, a...)...) }
	say := func(line string) { fmt.Fprintln(lc.logw, line) }
	// Each arm prints its own results table, in the log for the person watching
	// the run and in <arm>/results.md for the step summary.
	defer lc.emitResults(ar, logf)
	fail := func(err error) {
		ar.fail(err)
		rep.Failure(err)
	}
	logf("arm %s starts (order %s)", arm, strings.Join(lc.order, ", "))

	plan, err := PlanArm(arm, o.ServerSet, o.GenSet)
	if err != nil {
		fail(err)
		return
	}
	m.recordRound2(*o, lc.feats)

	// Pre-flight: the port and descriptor budget, and N_max. The budget is this
	// arm's own; the host decides N_max, the start and the join scale, and the
	// prediction is context only.
	remaining := o.Budget
	if o.Budget <= 0 {
		remaining = 1000 * time.Hour
	}
	calBots := int(lc.cal.ReceivePPS / RxPPSPerBot(o.Scenario))
	baseScale := o.Bot.JoinRateScale
	caps := planHost(o, lc.feats, lc.memTop, calBots, remaining, lc.portRange, lc.ulimitN, len(lc.genCPUs))
	m.recordCaps(caps, calBots)
	if lc.feats.Has("join_rate") {
		// The bot argv now carries the anchored scale.
		m.recordRound2(*o, lc.feats)
		m.Ramp.RateScaleBase = baseScale
	}
	if caps.NMax < 1 {
		fail(errors.New("pre-flight: no sweep step fits the time, port, descriptor, memory and generator budgets (" + describeCaps(caps) + ")"))
		return
	}
	if err := sweep.Preflight(lc.portRange, lc.ulimitN, caps.Top); err != nil {
		fail(err)
		return
	}
	rule := fmt.Sprintf("join rule constant %s/s (bot lacks join_rate)", fmtFloat(o.JoinRate))
	if lc.feats.Has("join_rate") {
		rule = fmt.Sprintf("join rule min(%s, %s/N) floor %s", fmtFloat(o.Bot.JoinRateMax), fmtFloat(o.Bot.JoinRateScale), fmtFloat(o.JoinRateFloor))
	}
	logf("pre-flight ok: N_max %d (%s), start %d, top %d, %s; predicted knee %s (context only)",
		caps.NMax, caps.By, caps.Start, caps.Top, rule, predictedText(o.PredictedKnee))
	logf("%s", describeCaps(caps))

	scratch := filepath.Join(lc.scratch, arm)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		fail(fmt.Errorf("scratch dir: %w", err))
		return
	}
	env := newArmEnv(plan, envBase{
		serverBin: o.ServerBin, loadbotBin: o.LoadbotBin, outDir: o.OutDir, scratchDir: scratch, scenario: o.Scenario, seed: o.Seed,
		serverCPUs: lc.serverCPU, features: lc.feats, bot: &o.Bot,
		onProgress:    func(p Progress) { say(FormatRampProgress(tag, p)) },
		onControlLost: func(ev ControlLostEvent) { say(FormatControlLostLine(tag, ev)) },
		logf:          logf,
	})
	if src, ok := env.(diagSource); ok {
		rep.src = src
	}
	cfg := driverConfig(*o, caps, remaining, lc.feats)
	cfg.OnRow = func(r Row) {
		if r.Kind == "burst" {
			say(FormatBurstLine(tag, r))
		} else {
			say(FormatRowLine(tag, r))
		}
		rep.Row(r)
	}
	cfg.Logf = logf
	out, runErr := runDriver(ctx, env, cfg)
	ar.out = out
	var envErrs []string
	if he, ok := env.(interface{ errorsSeen() []string }); ok {
		envErrs = he.errorsSeen()
	}
	m.Starts, m.Notes, m.ServerErrors = out.Starts, append(m.Notes, out.Notes...), envErrs
	m.recordOutcome(out, runErr)

	// Files, written even after a failure so the partial run is inspectable. An
	// arm that stopped early with a bracket is not a failure: it has a result.
	if err := writeOutcomeFiles(*o, out, caps.NMax, runErr); err != nil {
		fail(err)
		return
	}
	if runErr != nil {
		fail(fmt.Errorf("arm %s: %w", arm, runErr))
		return
	}
	logf("result: %s (calibration %s)", out.Result, FormatCapacity(lc.cal))
	ar.res = ArmResult{Result: out.Result.String(), Kind: kindNameFor(out.Result), N: out.Result.N}
	if err := writeManifest(o.OutDir, m); err != nil {
		logf("manifest: %v; recording arm %s as an error", err, arm)
		ar.err = err
		ar.res = ArmResult{Result: kindError, Kind: kindError}
	}
}
