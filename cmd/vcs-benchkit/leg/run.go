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
		Pins: pins,
		PredictedKnee: o.PredictedKnee, JoinRate: o.JoinRate, Budget: o.Budget.String(),
		Durations: map[string]string{
			"warmup": o.Warmup.String(), "hold": o.Hold.String(), "confirm": o.Confirm.String(),
			"overload": o.Overload.String(), "calibration_hold": o.CalHold.String(),
		},
		Bounds: sweep.DefaultBounds, EnvProbe: probeRaw,
	}
	fail := func(err error) error {
		m.Error = err.Error()
		_ = writeManifest(o.OutDir, m)
		return err
	}
	logf := func(f string, a ...any) { fmt.Fprintf(logw, f+"\n", a...) }

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

	// 2. Calibration: the generator's receive side on its own.
	legStart := time.Now()
	logf("calibration: reflector on %s, generator on %s", o.ServerSet, o.GenSet)
	cal, err := RunCalibration(ctx, CalConfig{
		LoadbotBin: o.LoadbotBin, ServerSet: o.ServerSet, GenSet: o.GenSet, Seed: o.Seed,
		Steps: o.CalSteps, Hold: o.CalHold, Senders: len(serverCPUs), StderrDir: o.OutDir,
	})
	if err != nil {
		return fail(fmt.Errorf("calibration: %w", err))
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
	env := &hostEnv{
		serverBin: o.ServerBin, loadbotBin: o.LoadbotBin, outDir: o.OutDir, scenario: o.Scenario, seed: o.Seed,
		serverPin: serverPin, serverGOMAXPROCS: 1, serverCPUs: serverCPUs, genSet: o.GenSet, genNCPU: len(genCPUs),
	}
	d := NewDriver(env, Config{
		Predicted: o.PredictedKnee, NMax: nMax, JoinRate: o.JoinRate, Budget: remaining,
		Warmup: o.Warmup, Hold: o.Hold, Confirm: o.Confirm, Overload: o.Overload, ProbeHold: time.Second,
		Bounds: sweep.DefaultBounds,
	})
	out, runErr := d.Run(ctx)
	m.Starts, m.Notes, m.ServerErrors = out.Starts, out.Notes, env.errorsSeen()
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
	logf("result: %s (calibration receive capacity %.0f pps, lower bound %v)", out.Result, cal.ReceivePPS, cal.LowerBound)
	return writeManifest(o.OutDir, m)
}
