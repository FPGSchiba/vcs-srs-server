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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// sweepSteps is the step count the design budgets a leg for (design 6.4:
// "with ~7 steps").
const sweepSteps = 7

// socketBufferTarget is what the kit raises rmem_max and wmem_max to.
const socketBufferTarget = 8 << 20

// Options are the `vcs-benchkit leg` inputs.
type Options struct {
	ServerBin, LoadbotBin, OutDir string
	Scenario, Arms                string
	Seed                          int64
	// Q6 is the user's CPU-split decision, recorded verbatim in the manifest.
	Q6 string
	// ProbeFile is gate 0's env-probe.json; ServerSet, GenSet and Topology
	// override what it says.
	ProbeFile, ServerSet, GenSet, Topology string
	// PredictFile is predict.json from `vcs-benchkit predict`; PredictedKnee
	// overrides it.
	PredictFile   string
	PredictedKnee int
	// NMaxCap lowers the N_max the time and port budgets give (0 = no extra cap).
	NMaxCap              int
	ServerSHA, ClientSHA string

	Budget   time.Duration
	JoinRate float64

	Warmup, Hold, Confirm, Overload time.Duration
	CalSteps                        string
	CalHold                         time.Duration
	// Sysctl allows the kit to raise rmem_max/wmem_max (needs sudo).
	Sysctl bool

	// RampChunk splits a ramp into chunks of at most this many bots (0 = one set_n
	// per step). Bursts runs the join-burst phase after the sweep: BurstAdd bots at
	// BurstInFlight in flight. Bot holds the bot flags and the grpc log level (R2-1,
	// R2-4 to R2-9). A zero GC bound, ramp talker or grpc log level means the default.
	RampChunk               int
	Bursts                  bool
	BurstAdd, BurstInFlight int
	Bot                     BotParams
}

// Probe is the part of gate 0's env-probe.json the leg needs.
type Probe struct {
	ServerSet     string `json:"server_set"`
	GeneratorSet  string `json:"generator_set"`
	TopologyLabel string `json:"topology_label"`
}

func disjoint(a, b string) error {
	la, err := CPUList(a)
	if err != nil {
		return err
	}
	lb, err := CPUList(b)
	if err != nil {
		return err
	}
	in := map[int]bool{}
	for _, c := range la {
		in[c] = true
	}
	for _, c := range lb {
		if in[c] {
			return fmt.Errorf("server set %q and generator set %q share cpu %d", a, b, c)
		}
	}
	return nil
}

// ParseProbe reads env-probe.json and checks that the two CPU sets are valid,
// disjoint, and that the topology label is present.
func ParseProbe(b []byte) (Probe, error) {
	var p Probe
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("env probe: %w", err)
	}
	if p.TopologyLabel == "" {
		return p, errors.New("env probe: no topology_label")
	}
	if err := disjoint(p.ServerSet, p.GeneratorSet); err != nil {
		return p, fmt.Errorf("env probe: %w", err)
	}
	return p, nil
}

// PredictedKnee picks the B-0 prediction of a scenario and arm from predict.json.
func PredictedKnee(b []byte, scenario, arm string) (int, error) {
	var doc struct {
		Predictions []struct {
			Scenario string `json:"scenario"`
			Arm      string `json:"arm"`
			KneeN    int    `json:"knee_n"`
		} `json:"predictions"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return 0, fmt.Errorf("predict.json: %w", err)
	}
	for _, p := range doc.Predictions {
		if p.Scenario == scenario && p.Arm == arm {
			if p.KneeN <= 0 {
				return 0, fmt.Errorf("predict.json: %s/%s predicts knee %d", scenario, arm, p.KneeN)
			}
			return p.KneeN, nil
		}
	}
	return 0, fmt.Errorf("predict.json: no prediction for %s/%s", scenario, arm)
}

// Validate checks resolved options. S4 builds the S arm of one-big-net only.
func (o *Options) Validate() error {
	switch {
	case o.Arms != "S":
		return fmt.Errorf("--arms %q: S4 builds the S arm only; Seq and D2 arrive with S5", o.Arms)
	case o.Scenario != "one-big-net":
		return fmt.Errorf("--scenario %q: v1 runs one-big-net only", o.Scenario)
	case o.ServerBin == "" || o.LoadbotBin == "" || o.OutDir == "":
		return errors.New("--server-bin, --loadbot-bin and --out are required")
	case o.ServerSet == "" || o.GenSet == "":
		return errors.New("server and generator cpu sets are required (--probe, or --server-set and --gen-set)")
	case o.Topology == "":
		return errors.New("a topology label is required (--probe or --topology)")
	case o.PredictedKnee <= 0:
		return errors.New("a predicted knee is required (--predict or --predicted-knee)")
	case o.Bot.PingEvery < 0:
		return fmt.Errorf("--ping-every %v: must not be negative", o.Bot.PingEvery)
	case o.RampChunk < 0:
		return fmt.Errorf("--ramp-chunk %d: must not be negative", o.RampChunk)
	case o.BurstAdd < 0:
		return fmt.Errorf("--burst-add %d: must not be negative", o.BurstAdd)
	case o.BurstInFlight < 0:
		return fmt.Errorf("--burst-in-flight %d: must not be negative", o.BurstInFlight)
	case o.Bot.GCPauseWarnP99 > 0 && o.Bot.GCPauseP99 > 0 && o.Bot.GCPauseWarnP99 > o.Bot.GCPauseP99:
		return fmt.Errorf("gc warn bound %v exceeds the hard bound %v", o.Bot.GCPauseWarnP99, o.Bot.GCPauseP99)
	case o.Bot.GRPCLog != "" && o.Bot.GRPCLog != "off" && o.Bot.GRPCLog != "info" && o.Bot.GRPCLog != "warning" && o.Bot.GRPCLog != "error":
		return fmt.Errorf("--grpc-log %q: want info, warning, error or off", o.Bot.GRPCLog)
	}
	return disjoint(o.ServerSet, o.GenSet)
}

// SweepCap is N_max: the time budget's cap (design 6.4), further limited so the
// overload step at 2 x N_max passes the port and descriptor pre-flight, and by
// an explicit cap when one is given (> 0). 0 means no step fits.
func SweepCap(predicted int, joinRate float64, budget time.Duration, portRange [2]int, ulimitN, explicit int) int {
	n := sweep.NMax(predicted, joinRate, budget, sweepSteps)
	// The largest upper bound U that passes sweep.Preflight.
	u := min((portRange[1]-portRange[0]+1)/2, (ulimitN-256)/4)
	if u < 0 {
		u = 0
	}
	n = min(n, u/2)
	if explicit > 0 {
		n = min(n, explicit)
	}
	return n
}

func readPortRange(read func(string) ([]byte, error)) ([2]int, error) {
	b, err := read("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return [2]int{}, err
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return [2]int{}, fmt.Errorf("ip_local_port_range: %q", strings.TrimSpace(string(b)))
	}
	lo, err1 := strconv.Atoi(f[0])
	hi, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return [2]int{}, fmt.Errorf("ip_local_port_range: %q", strings.TrimSpace(string(b)))
	}
	return [2]int{lo, hi}, nil
}

func readUlimitN() (int, error) {
	out, err := runCmd("sh", "-c", "ulimit -n")
	if err != nil {
		return 0, fmt.Errorf("ulimit -n: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "unlimited" {
		return 1 << 30, nil
	}
	return strconv.Atoi(s)
}

// BuildPins is the CPU plan of a leg. The S arm is standalone on ONE vCPU, the
// first of the server set, with GOMAXPROCS 1 and therefore two shards (design
// 6.3). The calibration reflector gets the whole server set and the load
// generator its own set. The sets must be valid and disjoint.
func BuildPins(serverSet, genSet string) (map[string]PinRecord, error) {
	if err := disjoint(serverSet, genSet); err != nil {
		return nil, err
	}
	serverCPUs, _ := CPUList(serverSet)
	genCPUs, _ := CPUList(genSet)
	return map[string]PinRecord{
		"server":    {CPUs: strconv.Itoa(serverCPUs[0]), GOMAXPROCS: 1, Shards: topology.ShardCount(1)},
		"reflector": {CPUs: serverSet, GOMAXPROCS: len(serverCPUs)},
		"generator": {CPUs: genSet, GOMAXPROCS: len(genCPUs)},
	}, nil
}

// Manifest records everything needed to read a leg's numbers later.
type Manifest struct {
	Schema       string `json:"schema"`
	Label        string `json:"label"`
	Payload      string `json:"payload"`
	JoinLabel    string `json:"join_label"`
	NotMeasured  string `json:"not_measured"`
	Arm          string `json:"arm"`
	Scenario     string `json:"scenario"`
	Seed         int64  `json:"seed"`
	Q6           string `json:"q6"`
	Topology     string `json:"topology_label"`
	ServerSHA    string `json:"server_sha"`
	ClientSHA    string `json:"client_sha"`
	KitGoVersion string `json:"kit_go_version"`

	Pins map[string]PinRecord `json:"pins"`

	Sysctl    map[string]string `json:"sysctl"`
	PortRange [2]int            `json:"ip_local_port_range"`
	UlimitN   int               `json:"ulimit_n"`

	PredictedKnee int               `json:"predicted_knee"`
	NMax          int               `json:"n_max"`
	JoinRate      float64           `json:"join_rate_assumed"`
	Budget        string            `json:"budget"`
	Durations     map[string]string `json:"durations"`

	Bounds sweep.Bounds `json:"bounds"`

	// BotFeatures is what `vcs-loadbot features` listed ("v2" with none for a
	// bot without the subcommand); BotFlags the extra flags the kit then passed
	// to `run`; GeneratorEnv the variables added to each generator process, and
	// ServerEnv the ones put in the kit's own environment for the server to
	// inherit (grpc transport log only; the server binary is untouched).
	BotVersion   int                 `json:"bot_version,omitempty"`
	BotFeatures  []string            `json:"bot_features"`
	BotFlags     []string            `json:"bot_flags,omitempty"`
	GeneratorEnv map[string][]string `json:"generator_env,omitempty"`
	ServerEnv    []string            `json:"server_env,omitempty"`

	// Round 2 (W6 fix 2): the ramp and burst shapes, the generator bounds and every
	// design change with its old and new value.
	Ramp            *RampRecord       `json:"ramp,omitempty"`
	Burst           *BurstRecord      `json:"burst,omitempty"`
	BoundsGenerator *GenBoundsRecord  `json:"bounds_generator,omitempty"`
	DecisionsRound2 map[string]string `json:"decisions_round2,omitempty"`

	EnvProbe json.RawMessage `json:"env_probe,omitempty"`

	Result       string   `json:"result,omitempty"`
	Starts       int      `json:"starts,omitempty"`
	Notes        []string `json:"notes,omitempty"`
	ServerErrors []string `json:"server_error_lines,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// PinRecord is one process's CPU set and what follows from it.
type PinRecord struct {
	CPUs       string `json:"cpus"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	// Shards is recorded for the server only; the kit sets nothing.
	Shards int `json:"voice_shards,omitempty"`
}

func writeFile(dir, name string, fn func(io.Writer) error) error {
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644)
}

func writeManifest(dir string, m *Manifest) error {
	return writeFile(dir, "manifest.json", func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	})
}

// Run executes one leg: manifest, calibration, pre-flight, the S arm, files.
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
	if o.PredictedKnee <= 0 && o.PredictFile != "" {
		b, err := os.ReadFile(o.PredictFile)
		if err != nil {
			return err
		}
		if o.PredictedKnee, err = PredictedKnee(b, o.Scenario, o.Arms); err != nil {
			return err
		}
	}
	o.normalizeRound2()
	if err := o.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	serverCPUs, _ := CPUList(o.ServerSet)
	genCPUs, _ := CPUList(o.GenSet)
	pins, err := BuildPins(o.ServerSet, o.GenSet)
	if err != nil {
		return err
	}
	serverPin := pins["server"].CPUs

	m := &Manifest{
		Schema: "leg-manifest/1", Label: Label, Payload: PayloadLabel, JoinLabel: JoinLabel, NotMeasured: NotMeasured,
		Arm: o.Arms, Scenario: o.Scenario, Seed: o.Seed, Q6: o.Q6, Topology: o.Topology,
		ServerSHA: o.ServerSHA, ClientSHA: o.ClientSHA, KitGoVersion: runtime.Version(),
		Pins:          pins,
		PredictedKnee: o.PredictedKnee, JoinRate: o.JoinRate, Budget: o.Budget.String(),
		Durations: map[string]string{
			"warmup": o.Warmup.String(), "hold": o.Hold.String(), "confirm": o.Confirm.String(),
			"overload": o.Overload.String(), "calibration_hold": o.CalHold.String(),
		},
		Bounds: sweep.DefaultBounds, EnvProbe: probeRaw,
	}
	tag := LegTag(o.Seed, o.Arms, o.Scenario)
	// The reporter prints the diagnostics block into the Actions log; its source
	// is set once the S arm's environment exists.
	rep := &diagReporter{w: logw, tag: tag}
	fail := func(err error) error {
		m.Error = err.Error()
		_ = writeManifest(o.OutDir, m)
		rep.Failure(err)
		return err
	}
	logf := func(f string, a ...any) { fmt.Fprintf(logw, "%s "+f+"\n", append([]any{tag}, a...)...) }
	say := func(line string) { fmt.Fprintln(logw, line) }

	// Whatever happens next, the leg ends with its results table: in the log
	// for the person watching the run and in results.md for the step summary.
	var cal *Calibration
	var out *Outcome
	defer func() {
		in := ResultsInput{
			Seed: o.Seed, Arm: o.Arms, Scenario: o.Scenario, Result: m.Result, Error: m.Error,
			Cal: cal, Out: out, Predicted: o.PredictedKnee, NMax: m.NMax,
		}
		var buf bytes.Buffer
		if err := WriteResultsMarkdown(&buf, in); err != nil {
			logf("results.md: %v", err)
			return
		}
		if err := os.WriteFile(filepath.Join(o.OutDir, "results.md"), buf.Bytes(), 0o644); err != nil {
			logf("results.md: %v", err)
		}
		fmt.Fprintf(logw, "\n%s\n", buf.String())
	}()

	// 1. Manifest inputs: sysctls (raise rmem_max/wmem_max only), port range, ulimit.
	if o.Sysctl && runtime.GOOS == "linux" {
		m.Sysctl = RaiseSocketBuffers(os.ReadFile, runCmd, socketBufferTarget)
	} else {
		m.Sysctl = map[string]string{"skipped": "not requested or not linux"}
	}
	portRange, err := readPortRange(os.ReadFile)
	if err != nil {
		return fail(fmt.Errorf("read ip_local_port_range: %w", err))
	}
	ulimitN, err := readUlimitN()
	if err != nil {
		return fail(err)
	}
	m.PortRange, m.UlimitN = portRange, ulimitN

	// 1b. What this load bot can do (v2 has no `features`): it decides which v3
	// flags every later run passes. Probed once per leg, before calibration.
	feats := ProbeFeatures(o.LoadbotBin, ProbeTimeout)
	m.recordRound2(o, feats)
	logf("%s", Round2Summary(o, feats))
	if feats.Version == 2 {
		logf("load bot v2 (no features subcommand or probe failed)")
	} else {
		logf("load bot v%d, features %v", feats.Version, feats.Names())
	}

	// 2. Calibration: the generator's receive side on its own.
	legStart := time.Now()
	logf("calibration: reflector on %s, generator on %s", o.ServerSet, o.GenSet)
	cal, err = RunCalibration(ctx, CalConfig{
		LoadbotBin: o.LoadbotBin, ServerSet: o.ServerSet, GenSet: o.GenSet, Seed: o.Seed,
		Steps: o.CalSteps, Hold: o.CalHold, Senders: len(serverCPUs), StderrDir: o.OutDir, Features: feats, Bot: &o.Bot,
		OnLine: func(kind string, raw []byte) {
			switch kind {
			case "calibration":
				var s CalStep
				if json.Unmarshal(raw, &s) == nil {
					say(FormatCalLine(tag, s))
				}
			case "progress":
				var p Progress
				if json.Unmarshal(raw, &p) == nil {
					say(FormatRampProgress(tag, p))
				}
			}
		},
	})
	if err != nil {
		return fail(fmt.Errorf("calibration: %w", err))
	}
	var calNotes []string
	if cal.StoppedAt > 0 {
		note := fmt.Sprintf("calibration stopped at N=%d: %s", cal.StoppedAt, cal.StopReason)
		calNotes = append(calNotes, note)
		say(FormatCalStopped(tag, cal))
	}
	if err := writeFile(o.OutDir, "calibration.csv", func(w io.Writer) error { return WriteCalibrationCSV(w, cal, o.Topology) }); err != nil {
		return fail(err)
	}

	// 3. Pre-flight: the port and descriptor budget, and N_max.
	remaining := o.Budget - time.Since(legStart)
	if o.Budget <= 0 {
		remaining = 1000 * time.Hour
	}
	nMax := SweepCap(o.PredictedKnee, o.JoinRate, remaining, portRange, ulimitN, o.NMaxCap)
	m.NMax = nMax
	if nMax < 1 {
		return fail(errors.New("pre-flight: no sweep step fits the time, port and descriptor budgets"))
	}
	if err := sweep.Preflight(portRange, ulimitN, 2*nMax); err != nil {
		return fail(err)
	}
	logf("pre-flight ok: predicted knee %d, N_max %d", o.PredictedKnee, nMax)

	// 4-8. The S arm.
	scratch, err := os.MkdirTemp("", "vcs-benchkit-leg-")
	if err != nil {
		return fail(fmt.Errorf("scratch dir: %w", err))
	}
	defer os.RemoveAll(scratch)
	// The server inherits the kit's environment (topology passes os.Environ()), so
	// its grpc transport log says why it closes a connection. Set only now: the
	// reflector, which speaks no gRPC, has already run.
	for _, kv := range ServerEnvWith(o.Bot.GRPCLog) {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return fail(fmt.Errorf("set %s: %w", k, err))
		}
	}
	env := &hostEnv{
		serverBin: o.ServerBin, loadbotBin: o.LoadbotBin, outDir: o.OutDir, scratchDir: scratch, scenario: o.Scenario, seed: o.Seed,
		serverPin: serverPin, serverGOMAXPROCS: 1, serverCPUs: serverCPUs, genSet: o.GenSet, genNCPU: len(genCPUs), features: feats, bot: &o.Bot,
		onProgress:    func(p Progress) { say(FormatRampProgress(tag, p)) },
		onControlLost: func(ev ControlLostEvent) { say(FormatControlLostLine(tag, ev)) },
		logf:          logf,
	}
	rep.src = env
	// The time guard prices a ramp with the bot's rate rule only when the bot has it.
	var rateMax, rateScale float64
	if feats.Has("join_rate") {
		rateMax, rateScale = o.Bot.JoinRateMax, o.Bot.JoinRateScale
	}
	d := NewDriver(env, Config{
		Predicted: o.PredictedKnee, NMax: nMax, JoinRate: o.JoinRate, Budget: remaining,
		Warmup: o.Warmup, Hold: o.Hold, Confirm: o.Confirm, Overload: o.Overload, ProbeHold: time.Second,
		Bounds:    sweep.DefaultBounds,
		RampChunk: o.RampChunk, RateMax: rateMax, RateScale: rateScale,
		BurstEnabled: o.Bursts, BurstAdd: o.BurstAdd, BurstInFlight: o.BurstInFlight,
		OnRow: func(r Row) {
			if r.Kind == "burst" {
				say(FormatBurstLine(tag, r))
			} else {
				say(FormatRowLine(tag, r))
			}
			rep.Row(r)
		},
		Logf: logf,
	})
	var runErr error
	out, runErr = d.Run(ctx)
	m.Starts, m.Notes, m.ServerErrors = out.Starts, append(calNotes, out.Notes...), env.errorsSeen()
	if runErr == nil {
		m.Result = out.Result.String()
		if out.Unconfirmed {
			m.Result += " (unconfirmed: the confirm step did not pass)"
		}
	}

	// 9. Files, written even after a failure so the partial run is inspectable.
	if err := writeFile(o.OutDir, "steps.csv", func(w io.Writer) error { return WriteStepsCSV(w, out, o.Topology) }); err != nil {
		return fail(err)
	}
	if runErr != nil {
		return fail(runErr)
	}
	if err := writeFile(o.OutDir, "knees.csv", func(w io.Writer) error {
		return WriteKneesCSV(w, out, KneeMeta{Arm: o.Arms, Scenario: o.Scenario, Topology: o.Topology, Predicted: o.PredictedKnee, NMax: nMax})
	}); err != nil {
		return fail(err)
	}
	logf("result: %s (calibration %s)", out.Result, FormatCapacity(cal))
	return writeManifest(o.OutDir, m)
}

// syncWriter serialises writes so progress lines from the bot's reader
// goroutine never interleave with the leg's own.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// RampRecord is the ramp shape a leg ran with (R2-3 to R2-5).
type RampRecord struct {
	Chunk       int     `json:"chunk"`
	RateMax     float64 `json:"rate_max"`
	RateScale   float64 `json:"rate_scale"`
	MaxInFlight int     `json:"max_in_flight"`
	Talker      string  `json:"talker"`
}

// BurstRecord is the join-burst phase's shape (R2-6).
type BurstRecord struct {
	Add      int  `json:"add"`
	InFlight int  `json:"in_flight"`
	Enabled  bool `json:"enabled"`
}

// GenBoundsRecord is the generator's GC-pause bounds (R2-8).
type GenBoundsRecord struct {
	GCPauseP99Us     int64 `json:"gc_pause_p99_us"`
	GCPauseWarnP99Us int64 `json:"gc_pause_warn_p99_us"`
}

// decisionsRound2 are the design changes of W6 fix round 2 with old -> new
// values, recorded in every manifest so a result can be read against the round
// that produced it. R2-13 (reflector senders) and R2-14 (shard queue counter)
// are not changes and are not listed.
func decisionsRound2() map[string]string {
	return map[string]string{
		"R2-1":  "bot control plane: no Ping, no keepalive -> Ping every 5s, keepalive 75s/10s, death seen at once",
		"R2-2":  "dead bot: fatal hold error -> control_lost step outcome, rebuild, retry once",
		"R2-3":  "talker during a ramp: live -> silent, resumes on a fresh schedule",
		"R2-4":  "join rate: unbounded, 16 in flight -> min(12, 5000/N) joins/s, 8 in flight",
		"R2-5":  "ramp shape: one set_n per step -> chunks of at most 250 bots, stop at the first failing chunk",
		"R2-6":  "join-burst metric: none -> +150 bots at 16 in flight, talker silent and live, own table",
		"R2-7":  "HELLO ladder: 1.5/3.0/4.5/6.0/7.5s -> unchanged (no change, kept equal to the real client)",
		"R2-8":  "gc_pause_p99: 2ms hard -> 5ms hard, 2ms warning",
		"R2-9":  "generator GC: GOGC=100 -> GOGC=off GOMEMLIMIT=1GiB",
		"R2-10": "calibration talker: continuous -> per step from ramp end to drain, empty backlog each hold",
		"R2-11": "calibration steps: 64..1024,2048,4096 -> 64,128,256,512,1024,1536,1792,2048,4096",
		"R2-12": "calibration lower bound: bot lower_bound only -> OR reflector send errors and a delivery stop",
	}
}

// normalizeRound2 fills the defaults an Options literal leaves zero, so a leg
// never runs with a 0 ms GC bound or an empty grpc log level.
func (o *Options) normalizeRound2() {
	d := DefaultBotParams()
	if o.Bot.GCPauseP99 <= 0 {
		o.Bot.GCPauseP99 = d.GCPauseP99
	}
	if o.Bot.GCPauseWarnP99 <= 0 {
		o.Bot.GCPauseWarnP99 = d.GCPauseWarnP99
	}
	if o.Bot.RampTalker == "" {
		o.Bot.RampTalker = d.RampTalker
	}
	if o.Bot.GRPCLog == "" {
		o.Bot.GRPCLog = d.GRPCLog
	}
}

// Round2Summary is one log line of the effective round-2 parameters, so the
// Actions log shows what a leg ran with without opening manifest.json.
func Round2Summary(o Options, feats Features) string {
	o.normalizeRound2()
	rate := "rate rule not applied (bot lacks join_rate)"
	if feats.Has("join_rate") {
		rate = fmt.Sprintf("rate min(%s,%s/N), in-flight %d", fmtFloat(o.Bot.JoinRateMax), fmtFloat(o.Bot.JoinRateScale), o.Bot.MaxInFlight)
	}
	ping := "ping off"
	if feats.Has("ping") && o.Bot.PingEvery > 0 {
		ping = "ping " + o.Bot.PingEvery.String()
	}
	bursts := "bursts off"
	if o.Bursts && feats.Has("burst") {
		bursts = fmt.Sprintf("bursts on (add %d, in-flight %d)", o.BurstAdd, o.BurstInFlight)
	}
	return fmt.Sprintf("round 2: chunk %d, %s, ramp talker %s, %s, %s, gc %v/%v, grpc-log %s",
		o.RampChunk, rate, o.Bot.RampTalker, ping, bursts, o.Bot.GCPauseWarnP99, o.Bot.GCPauseP99, o.Bot.GRPCLog)
}

// recordRound2 fills the manifest's bot, generator, ramp, burst and decision
// fields from the options and the probed bot features.
func (m *Manifest) recordRound2(o Options, feats Features) {
	o.normalizeRound2()
	m.BotVersion, m.BotFeatures, m.BotFlags = feats.Version, feats.Names(), BotRunArgsWith(feats, o.Bot)
	m.GeneratorEnv = map[string][]string{
		"run":       GeneratorEnvWith("run", o.Bot.GRPCLog),
		"calibrate": GeneratorEnvWith("calibrate", o.Bot.GRPCLog),
		"reflect":   GeneratorEnvWith("reflect", o.Bot.GRPCLog),
	}
	m.ServerEnv = ServerEnvWith(o.Bot.GRPCLog)
	// The rate rule and the bursts are applied only when the bot has the feature;
	// record what ran, not what the flags asked for.
	ramp := &RampRecord{Chunk: o.RampChunk, Talker: o.Bot.RampTalker}
	if feats.Has("join_rate") {
		ramp.RateMax, ramp.RateScale, ramp.MaxInFlight = o.Bot.JoinRateMax, o.Bot.JoinRateScale, o.Bot.MaxInFlight
	}
	m.Ramp = ramp
	m.Burst = &BurstRecord{Add: o.BurstAdd, InFlight: o.BurstInFlight, Enabled: o.Bursts && feats.Has("burst")}
	m.BoundsGenerator = &GenBoundsRecord{GCPauseP99Us: o.Bot.GCPauseP99.Microseconds(), GCPauseWarnP99Us: o.Bot.GCPauseWarnP99.Microseconds()}
	m.DecisionsRound2 = decisionsRound2()
}
