// Package schema is the published-results format "bench-results/1": typed
// structs with a fixed field list for every file a bench run publishes. There
// is no free-text field. Every string is an enum, a verbatim constant or
// matches a strict pattern, so a reason, an error, a host label, an endpoint or
// a log line cannot reach a published file by construction (design 7.2). The
// readers reject unknown JSON keys and any CSV header that is not exact.
package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Version is the schema_version of every published manifest.
const Version = "bench-results/1"

// Verbatim constants (plan Global Constraints).
const (
	Label          = "Shared GitHub runner (4 vCPU), loopback, relative only. Not a capacity figure."
	JoinLabel      = "closed-loop, <= 16 in flight; server-side cost only; client hash excluded"
	PayloadCorpus  = "opus-speech-corpus"
	PayloadSynth   = "synthetic-not-opus"
	ArmLabelD2     = "bot-only; real client: ROADMAP Phase 9"
	Topo2Cores     = "2 cores"
	Topo2HT        = "2 hyperthreads of 1 core"
	TopoMixed      = "mixed"
	TopoUnspecific = "unspecified"
)

// RunFiles are the files of one published run, in write order.
var RunFiles = []string{"manifest.json", "micro.csv", "calibration.csv", "steps.csv", "knees.csv", "summary.csv", "ratios.csv"}

var (
	reSHA      = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	reRunURL   = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/actions/runs/[0-9]+$`)
	reGoVer    = regexp.MustCompile(`^go[0-9]+(\.[0-9]+)*$`)
	reImage    = regexp.MustCompile(`^[A-Za-z0-9._-]{0,40}$`)
	reKernel   = regexp.MustCompile(`^[0-9A-Za-z._+-]{1,80}$`)
	reCPUModel = regexp.MustCompile(`^[A-Za-z0-9 ().@,_+-]{1,120}$`)
	reSibling  = regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`)
	reCPUs     = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)
	reIdent    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	reScenario = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	reBench    = regexp.MustCompile(`^Benchmark[A-Za-z0-9]{1,48}$`)
	reByRole   = regexp.MustCompile(`^$|^[A-Za-z][A-Za-z0-9_]*=[0-9.e+-]+(;[A-Za-z][A-Za-z0-9_]*=[0-9.e+-]+)*$`)
	reBound    = regexp.MustCompile(`^[a-z]{2,16}-bound$`)
	reIRQ      = regexp.MustCompile(`^(y|n|unknown)$`)
)

func in(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func ok(name string, good bool) error {
	if !good {
		return fmt.Errorf("schema: %s is not an allowed value", name)
	}
	return nil
}

func allOK(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func armOK(v string) bool          { return in(v, "S", "Seq", "D2") }
func outcomeOK(v string) bool      { return in(v, "pass", "breach", "invalid", "control_lost") }
func outcomeOrEmpty(v string) bool { return v == "" || outcomeOK(v) }
func kindOK(v string) bool {
	return in(v, "knee", "generator-bound", "time-bound", "below-range", "control-lost", "bracketed", "error") || reBound.MatchString(v)
}
func stepKindOK(v string) bool { return in(v, "sweep", "confirm", "overload", "burst") }
func payloadOK(v string) bool  { return in(v, PayloadCorpus, PayloadSynth) }
func armLabelOK(v string) bool { return in(v, "", ArmLabelD2) }
func topologyOK(v string) bool { return in(v, Topo2Cores, Topo2HT, TopoMixed, TopoUnspecific) }
func ratioOK(v string) bool    { return in(v, "Seq/S", "D2/S", "D2/Seq") }

// Corpus describes the Opus speech corpus (hash and frame-size statistics).
type Corpus struct {
	SHA256     string `json:"sha256"`
	Frames     int    `json:"frames"`
	SizeMin    int    `json:"size_min"`
	SizeMedian int    `json:"size_median"`
	SizeP95    int    `json:"size_p95"`
	SizeMax    int    `json:"size_max"`
}

// Env is the runner environment, from a named allowlist (no hostnames).
type Env struct {
	ImageOS           string   `json:"image_os"`
	ImageVersion      string   `json:"image_version"`
	Kernel            string   `json:"kernel"`
	CPUModel          string   `json:"cpu_model"`
	VCPUs             int      `json:"vcpus"`
	SMT               bool     `json:"smt"`
	Siblings          []string `json:"siblings"`
	StealTicks        int64    `json:"steal_ticks"`
	StealSeconds      int      `json:"steal_seconds"`
	IRQTimeAccounting string   `json:"irq_time_accounting"`
	TopologyLabel     string   `json:"topology_label"`
	RmemMax           int64    `json:"net_core_rmem_max"`
	WmemMax           int64    `json:"net_core_wmem_max"`
	PortRange         [2]int   `json:"net_ipv4_ip_local_port_range"`
}

// LegMeta is what one leg ran: its ordinal, scenario, seed and arm order.
type LegMeta struct {
	Leg      int      `json:"leg"`
	Scenario string   `json:"scenario"`
	Seed     int64    `json:"seed"`
	ArmOrder []string `json:"arm_order"`
}

// PinRow is one process role's CPU pin of one arm. Roles are names, never endpoints.
type PinRow struct {
	Arm        string `json:"arm"`
	Role       string `json:"role"`
	CPUs       string `json:"cpus"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	Shards     int    `json:"voice_shards"`
}

// Manifest is manifest.json.
type Manifest struct {
	SchemaVersion string    `json:"schema_version"`
	Label         string    `json:"label"`
	RunURL        string    `json:"run_url"`
	ServerSHA     string    `json:"server_sha"`
	ClientSHA     string    `json:"client_sha"`
	ServerDirty   bool      `json:"server_dirty"`
	ServerGo      string    `json:"server_go"`
	ClientGo      string    `json:"client_go"`
	Corpus        *Corpus   `json:"corpus"`
	Env           Env       `json:"env"`
	Legs          []LegMeta `json:"legs"`
	Pins          []PinRow  `json:"pins"`
	JoinLabel     string    `json:"join_label"`
}

// Validate reports the first field outside the schema's allowed values.
func (m Manifest) Validate() error {
	e := m.Env
	errs := []error{
		ok("schema_version", m.SchemaVersion == Version),
		ok("label", m.Label == Label),
		ok("join_label", m.JoinLabel == JoinLabel),
		ok("run_url", m.RunURL == "" || reRunURL.MatchString(m.RunURL)),
		ok("server_sha", reSHA.MatchString(m.ServerSHA)),
		ok("client_sha", reSHA.MatchString(m.ClientSHA)),
		ok("server_go", m.ServerGo == "" || reGoVer.MatchString(m.ServerGo)),
		ok("client_go", m.ClientGo == "" || reGoVer.MatchString(m.ClientGo)),
		ok("env.image_os", reImage.MatchString(e.ImageOS)),
		ok("env.image_version", reImage.MatchString(e.ImageVersion)),
		ok("env.kernel", reKernel.MatchString(e.Kernel)),
		ok("env.cpu_model", reCPUModel.MatchString(e.CPUModel)),
		ok("env.irq_time_accounting", reIRQ.MatchString(e.IRQTimeAccounting)),
		ok("env.topology_label", topologyOK(e.TopologyLabel)),
	}
	if c := m.Corpus; c != nil {
		errs = append(errs, ok("corpus.sha256", len(c.SHA256) == 64 && reSHA.MatchString(c.SHA256)))
	}
	for _, s := range e.Siblings {
		errs = append(errs, ok("env.siblings", reSibling.MatchString(s)))
	}
	for _, l := range m.Legs {
		errs = append(errs, ok("legs.scenario", reScenario.MatchString(l.Scenario)))
		for _, a := range l.ArmOrder {
			errs = append(errs, ok("legs.arm_order", armOK(a)))
		}
	}
	for _, p := range m.Pins {
		errs = append(errs, ok("pins.arm", armOK(p.Arm)), ok("pins.role", reIdent.MatchString(p.Role)), ok("pins.cpus", reCPUs.MatchString(p.CPUs)))
	}
	return allOK(errs...)
}

// DecodeManifest reads and validates a manifest, rejecting unknown keys.
func DecodeManifest(r io.Reader) (Manifest, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("manifest: trailing data")
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	return m, nil
}

// MicroRow is a row of micro.csv (the B-0 micro-benchmarks).
type MicroRow struct {
	Bench         string   `csv:"bench"`
	N             int      `csv:"n"`
	L             int      `csv:"l"`
	Logger        string   `csv:"logger"`
	Mode          string   `csv:"mode"`
	NsPerOp       float64  `csv:"ns_per_op"`
	NsPerSend     *float64 `csv:"ns_per_send"`
	NsPerScan     *float64 `csv:"ns_per_scan"`
	AllocsPerOp   float64  `csv:"allocs_per_op"`
	NsPerSendFull *float64 `csv:"ns_per_send_full"`
}

// Validate implements validator.
func (r MicroRow) Validate() error {
	return allOK(ok("micro.bench", reBench.MatchString(r.Bench)), ok("micro.logger", in(r.Logger, "text", "bus", "")),
		ok("micro.mode", in(r.Mode, "single", "parallel", "")))
}

// CalRow is a row of calibration.csv. Violation texts are not published.
type CalRow struct {
	Leg              int      `csv:"leg"`
	Kind             string   `csv:"kind"`
	N                int      `csv:"n"`
	EgressPPS        *float64 `csv:"egress_pps"`
	Valid            bool     `csv:"valid"`
	LowerBound       bool     `csv:"lower_bound"`
	ReflectorErrors  uint64   `csv:"reflector_send_errors"`
	ReflectorSenders int      `csv:"reflector_senders"`
}

// Validate implements validator.
func (r CalRow) Validate() error {
	return ok("calibration.kind", in(r.Kind, "step", "stopped", "receive_capacity"))
}

// StepRow is a row of steps.csv: one measured step of one leg and arm. The
// verdict is the outcome enum; the reasons behind it are not published.
type StepRow struct {
	Leg             int      `csv:"leg"`
	Arm             string   `csv:"arm"`
	Scenario        string   `csv:"scenario"`
	Seq             int      `csv:"seq"`
	Kind            string   `csv:"kind"`
	N               int      `csv:"n"`
	Outcome         string   `csv:"outcome"`
	Held            bool     `csv:"held"`
	LossRatio       *float64 `csv:"loss_ratio"`
	LateLossRatio   *float64 `csv:"late_loss_ratio"`
	OneWayP50Ms     *float64 `csv:"one_way_p50_ms"`
	OneWayP99Ms     *float64 `csv:"one_way_p99_ms"`
	OneWayMaxMs     *float64 `csv:"one_way_max_ms"`
	JitterP99Ms     *float64 `csv:"jitter_p99_ms"`
	JitterMeanMs    *float64 `csv:"jitter_mean_ms"`
	GapP99Ms        *float64 `csv:"gap_p99_ms"`
	Expected        uint64   `csv:"expected"`
	Unique          uint64   `csv:"unique"`
	Lost            uint64   `csv:"lost"`
	Dups            uint64   `csv:"dups"`
	Reordered       uint64   `csv:"reordered"`
	FanoutPPS       *float64 `csv:"fanout_pps"`
	Presses         int      `csv:"presses"`
	DualPresses     int      `csv:"dual_presses"`
	VoiceEndpoints  int      `csv:"voice_endpoints"`
	JoinP99Us       int64    `csv:"join_p99_us"`
	RTTP99Us        *int64   `csv:"rtt_p99_us"`
	RadioUpdateP99  *int64   `csv:"radio_update_p99_us"`
	RadioPropP99    *int64   `csv:"radio_prop_p99_us"`
	SecretToAckP99  *int64   `csv:"secret_to_ack_p99_us"`
	ServerCPU       *float64 `csv:"server_cpu"`
	ServerCPUByRole string   `csv:"server_cpu_by_role"`
	ServerDrops     uint64   `csv:"server_drops"`
	SoftIRQTicks    uint64   `csv:"softirq_ticks_server_set"`
	StealTicks      uint64   `csv:"steal_ticks"`
	GenCPU          *float64 `csv:"gen_cpu"`
	GenSocketDrops  uint64   `csv:"gen_socket_drops"`
	NodeDeregs      uint64   `csv:"node_deregistered"`
	ControlLost     int      `csv:"control_lost"`
	ArmLabel        string   `csv:"arm_label"`
	Payload         string   `csv:"payload"`
	CorpusSHA256    string   `csv:"corpus_sha256"`
}

// Validate implements validator.
func (r StepRow) Validate() error {
	return allOK(
		ok("steps.arm", armOK(r.Arm)), ok("steps.scenario", reScenario.MatchString(r.Scenario)),
		ok("steps.kind", stepKindOK(r.Kind)), ok("steps.outcome", outcomeOK(r.Outcome)),
		ok("steps.arm_label", armLabelOK(r.ArmLabel)), ok("steps.payload", payloadOK(r.Payload)),
		ok("steps.corpus_sha256", r.CorpusSHA256 == "" || (len(r.CorpusSHA256) == 64 && reSHA.MatchString(r.CorpusSHA256))),
		ok("steps.server_cpu_by_role", reByRole.MatchString(r.ServerCPUByRole)),
	)
}

// KneeRow is a row of knees.csv. The result text and the notes are not published.
type KneeRow struct {
	Leg             int    `csv:"leg"`
	Arm             string `csv:"arm"`
	Scenario        string `csv:"scenario"`
	ResultKind      string `csv:"result_kind"`
	N               int    `csv:"n"`
	UpperN          *int   `csv:"upper_n"`
	ConfirmN        *int   `csv:"confirm_n"`
	ConfirmOutcome  string `csv:"confirm_outcome"`
	OverloadN       *int   `csv:"overload_n"`
	OverloadOutcome string `csv:"overload_outcome"`
	PredictedKnee   *int   `csv:"predicted_knee"`
	NMax            int    `csv:"n_max"`
	Topology        string `csv:"topology"`
	ArmLabel        string `csv:"arm_label"`
	Payload         string `csv:"payload"`
}

// Validate implements validator.
func (r KneeRow) Validate() error {
	return allOK(
		ok("knees.arm", armOK(r.Arm)), ok("knees.scenario", reScenario.MatchString(r.Scenario)),
		ok("knees.result_kind", kindOK(r.ResultKind)),
		ok("knees.confirm_outcome", outcomeOrEmpty(r.ConfirmOutcome)), ok("knees.overload_outcome", outcomeOrEmpty(r.OverloadOutcome)),
		ok("knees.topology", topologyOK(r.Topology)), ok("knees.arm_label", armLabelOK(r.ArmLabel)),
		ok("knees.payload", payloadOK(r.Payload)),
	)
}

// SummaryRow is a row of summary.csv: one arm x scenario across the repeats.
type SummaryRow struct {
	Arm           string `csv:"arm"`
	Scenario      string `csv:"scenario"`
	Repeats       int    `csv:"repeats"`
	MedianN       int    `csv:"median_n"`
	MinN          int    `csv:"min_n"`
	MaxN          int    `csv:"max_n"`
	Censored      bool   `csv:"censored"`
	Unconfirmed   bool   `csv:"unconfirmed"`
	PredictedKnee *int   `csv:"predicted_knee"`
	Topology      string `csv:"topology"`
	ArmLabel      string `csv:"arm_label"`
}

// Validate implements validator.
func (r SummaryRow) Validate() error {
	return allOK(ok("summary.arm", armOK(r.Arm)), ok("summary.scenario", reScenario.MatchString(r.Scenario)),
		ok("summary.topology", topologyOK(r.Topology)), ok("summary.arm_label", armLabelOK(r.ArmLabel)))
}

// RatioRow is a row of ratios.csv: a within-leg knee ratio across the legs.
type RatioRow struct {
	Scenario     string   `csv:"scenario"`
	Ratio        string   `csv:"ratio"`
	Legs         int      `csv:"legs"`
	Median       *float64 `csv:"median"`
	Min          *float64 `csv:"min"`
	Max          *float64 `csv:"max"`
	CensoredLegs int      `csv:"censored_legs"`
	Topology     string   `csv:"topology"`
}

// Validate implements validator.
func (r RatioRow) Validate() error {
	return allOK(ok("ratios.scenario", reScenario.MatchString(r.Scenario)), ok("ratios.ratio", ratioOK(r.Ratio)),
		ok("ratios.topology", topologyOK(r.Topology)))
}

// Run is everything one published run directory holds.
type Run struct {
	Manifest    Manifest
	Micro       []MicroRow
	Calibration []CalRow
	Steps       []StepRow
	Knees       []KneeRow
	Summary     []SummaryRow
	Ratios      []RatioRow
}

// WriteRun validates run and writes its seven files into dir. Nothing is
// written when any row is outside the schema.
func WriteRun(dir string, run Run) error {
	if err := run.Manifest.Validate(); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	files := map[string][]byte{}
	mb, err := json.MarshalIndent(run.Manifest, "", "  ")
	if err != nil {
		return err
	}
	files["manifest.json"] = append(mb, '\n')
	put := func(name string, b []byte, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files[name] = b
		return nil
	}
	b1, e1 := WriteCSV(run.Micro)
	b2, e2 := WriteCSV(run.Calibration)
	b3, e3 := WriteCSV(run.Steps)
	b4, e4 := WriteCSV(run.Knees)
	b5, e5 := WriteCSV(run.Summary)
	b6, e6 := WriteCSV(run.Ratios)
	if err := allOK(put("micro.csv", b1, e1), put("calibration.csv", b2, e2), put("steps.csv", b3, e3),
		put("knees.csv", b4, e4), put("summary.csv", b5, e5), put("ratios.csv", b6, e6)); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range RunFiles {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ReadRun reads and validates a run directory written by WriteRun. A file that
// fails the schema fails the read with its path.
func ReadRun(dir string) (Run, error) {
	var run Run
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return run, err
	}
	run.Manifest, err = DecodeManifest(f)
	f.Close()
	if err != nil {
		return run, fmt.Errorf("%s: %w", filepath.Join(dir, "manifest.json"), err)
	}
	read := func(name string, fn func(io.Reader) error) error {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		defer f.Close()
		if err := fn(f); err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		return nil
	}
	return run, allOK(
		read("micro.csv", func(r io.Reader) (err error) { run.Micro, err = ReadCSV[MicroRow](r, Header[MicroRow]()); return }),
		read("calibration.csv", func(r io.Reader) (err error) { run.Calibration, err = ReadCSV[CalRow](r, Header[CalRow]()); return }),
		read("steps.csv", func(r io.Reader) (err error) { run.Steps, err = ReadCSV[StepRow](r, Header[StepRow]()); return }),
		read("knees.csv", func(r io.Reader) (err error) { run.Knees, err = ReadCSV[KneeRow](r, Header[KneeRow]()); return }),
		read("summary.csv", func(r io.Reader) (err error) { run.Summary, err = ReadCSV[SummaryRow](r, Header[SummaryRow]()); return }),
		read("ratios.csv", func(r io.Reader) (err error) { run.Ratios, err = ReadCSV[RatioRow](r, Header[RatioRow]()); return }),
	)
}
