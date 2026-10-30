package leg

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// Labels stamped on every artefact (design 6.5, plan Global Constraints).
const (
	Label        = "Shared GitHub runner (4 vCPU), loopback, relative only. Not a capacity figure."
	PayloadLabel = "synthetic-not-opus"
	JoinLabel    = "closed-loop, <= 16 in flight; server-side cost only; client hash excluded"
	// NotMeasured names the breach criteria the C1 load bot cannot measure.
	NotMeasured = "late_loss;one_way_p99"
)

// CalStep is one calibration step of `vcs-loadbot calibrate`.
type CalStep struct {
	N          int      `json:"n"`
	EgressPPS  float64  `json:"egress_pps"`
	Valid      bool     `json:"valid"`
	Violations []string `json:"violations"`
	// v2 bot only; zero when the bot did not report them.
	Bots          int     `json:"bots"`
	ExpectedPPS   float64 `json:"expected_pps"`
	HelloTriesMax int     `json:"hello_tries_max"`
	HelloRetried  int     `json:"hello_retried"`
}

// Calibration is the generator's receive-side calibration of one leg.
type Calibration struct {
	Steps      []CalStep
	ReceivePPS float64
	LowerBound bool
	// LowerBoundReason says why ReceivePPS is only a lower bound (empty when it
	// is not one). S-T9 fills it; results.md shows it next to the figure.
	LowerBoundReason    string
	ReflectorSendErrors uint64
	Senders             int

	// StoppedAt is the step N that could not complete, 0 when calibration ran
	// every step. StoppedBots is the bots up when it stopped, -1 when the bot
	// did not say (a v1 bot). StopReason is the bot's own words, prefixed "inferred: " for a v1 bot, whose stop step and reason the kit guesses.
	StoppedAt   int
	StoppedBots int
	StopReason  string
	// Reflector counters of a v2 reflector; nil for a v1 one.
	ReflectorQueueFull   *uint64
	ReflectorWriteErrors *uint64
}

// parseCalibrateLines reads `vcs-loadbot calibrate` stdout: calibration steps,
// the optional receive_capacity line and the optional calibration_stopped line.
func parseCalibrateLines(out []byte) (steps []CalStep, pps float64, lowerBound, haveCap bool, stop *CalStopped, err error) {
	for _, raw := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var head struct {
			Type       string  `json:"type"`
			PPS        float64 `json:"pps"`
			LowerBound bool    `json:"lower_bound"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, 0, false, false, nil, fmt.Errorf("calibrate output: %w", err)
		}
		switch head.Type {
		case "calibration":
			var s CalStep
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, 0, false, false, nil, fmt.Errorf("calibrate output: %w", err)
			}
			steps = append(steps, s)
		case "calibration_stopped":
			var st CalStopped
			if err := json.Unmarshal(raw, &st); err != nil {
				return nil, 0, false, false, nil, fmt.Errorf("calibrate output: %w", err)
			}
			stop = &st
		case "receive_capacity":
			pps, lowerBound, haveCap = head.PPS, head.LowerBound, true
		}
	}
	return steps, pps, lowerBound, haveCap, stop, nil
}

// ParseCalibrate reads `vcs-loadbot calibrate` stdout. A missing
// receive_capacity line is tolerated when at least one step completed (a bot
// that died mid-calibration): the capacity is then the largest valid step and a
// lower bound. No completed step at all is an error.
func ParseCalibrate(out []byte) (steps []CalStep, pps float64, lowerBound bool, err error) {
	steps, pps, lowerBound, haveCap, _, err := parseCalibrateLines(out)
	if err != nil {
		return nil, 0, false, err
	}
	if len(steps) == 0 {
		return nil, 0, false, fmt.Errorf("calibrate output has no completed step")
	}
	if !haveCap {
		pps, lowerBound = 0, true
		for _, s := range steps {
			if s.Valid && s.EgressPPS > pps {
				pps = s.EgressPPS
			}
		}
	}
	return steps, pps, lowerBound, nil
}

// ParseReflectorStatsFull reads both the v1 and the v2 reflector_stats line.
func ParseReflectorStatsFull(out []byte) (ReflectorStats, error) {
	for _, raw := range bytes.Split(out, []byte("\n")) {
		var s ReflectorStats
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &head) == nil && head.Type == "reflector_stats" && json.Unmarshal(raw, &s) == nil {
			return s, nil
		}
	}
	return ReflectorStats{}, fmt.Errorf("reflector output has no reflector_stats line")
}

// ParseReflectorStats returns the reflector's send-error count from its final
// reflector_stats line.
func ParseReflectorStats(out []byte) (uint64, error) {
	s, err := ParseReflectorStatsFull(out)
	return s.SendErrors, err
}

// cell renders a float; NaN (unmeasured) is an empty cell, never a number.
func cell(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	return strconv.FormatFloat(f, 'g', 6, 64)
}

func u(n uint64) string { return strconv.FormatUint(n, 10) }

// kindName is the lowercase name of a sweep result kind.
func kindName(k sweep.ResultKind) string {
	return kindNameFor(sweep.Result{Kind: k})
}

// kindNameFor is kindName for a whole result: a TimeBound result names the
// limit that bound N_max when it was not the time budget.
func kindNameFor(r sweep.Result) string {
	k := r.Kind
	switch k {
	case sweep.KneeFound:
		return "knee"
	case sweep.GeneratorBound:
		return "generator-bound"
	case sweep.TimeBound:
		if r.Limit != "" && r.Limit != "time" {
			return r.Limit + "-bound"
		}
		return "time-bound"
	case sweep.BelowRange:
		return "below-range"
	case sweep.ControlLostBound:
		return "control-lost"
	case sweep.Bracketed:
		return "bracketed"
	}
	return fmt.Sprintf("kind-%d", int(k))
}

// WriteStepsCSV writes one row per measured step. Every row carries the label,
// the payload label, the join-timing qualification and the topology.
func WriteStepsCSV(w io.Writer, o *Outcome, topology string) error {
	return WriteStepsCSVLabeled(w, o, topology, "")
}

// LabelOr returns s, or Label when s is empty: the Actions-runner default.
func LabelOr(s string) string {
	if s == "" {
		return Label
	}
	return s
}

// WriteStepsCSVLabeled is WriteStepsCSV with the label column set to label
// (empty means Label).
func WriteStepsCSVLabeled(w io.Writer, o *Outcome, topology, label string) error {
	label = LabelOr(label)
	cw := csv.NewWriter(w)
	header := []string{
		"seq", "kind", "n", "outcome", "reasons", "held", "settle_s",
		"loss_ratio", "late_loss_ratio", "one_way_p99_ms", "expected", "unique", "lost", "dups", "reordered", "burst_max",
		"rex_ramp", "rex_settle", "rex_hold", "join_p99_us", "hello_tries_max",
		"server_cpu", "server_drops", "softirq_ticks_server_set", "steal_ticks", "sample_s",
		"shard_queue_full", "update_stream_behind", "node_feed_behind", "node_deregistered",
		"gen_cpu", "gen_socket_drops", "gen_rx_lag_p99_us", "gen_wheel_lag_p99_us", "gen_sched_p99_us", "gen_gc_p99_us", "gen_violations",
		"topology", "label", "payload", "join_label", "not_measured",
		// Appended by the W6 fix round. The columns above are a contract that
		// readers index by position: only ever append.
		"requested", "joined", "join_failures", "join_failures_by_stage", "hello_tries_hist", "failed_acked_by_server",
		"ramp_s", "ramp_server_cpu", "ramp_gen_cpu", "ramp_server_drops", "ramp_softnet_dropped", "ramp_udp_rcvbuf_errors",
		"ramp_hello_accepted", "ramp_hello_rejected", "ramp_shard_queue_full",
		"softnet_dropped", "udp_rcvbuf_errors", "udp_sndbuf_errors",
		// Appended by W6 fix 2 (27 columns, see round2Cells).
		"control_lost", "control_lost_phase", "control_lost_first", "ramp_mode", "ramp_talker", "ramp_joins_per_s", "ramp_join_p99_us",
		"ramp_hello_retried", "join_failed_chunk", "burst_talker", "burst_result", "gen_gc_cycles", "gen_warnings",
		"ping_sent", "ping_failed", "ping_rtt_p99_us",
		"tcp_abort_on_data", "tcp_abort_on_close", "tcp_abort_on_memory", "tcp_abort_on_timeout", "tcp_abort_on_linger", "tcp_timeouts",
		"tcp_estab_resets", "tcp_out_rsts", "softnet_squeezed", "ramp_tcp_abort_on_data", "ramp_tcp_estab_resets",
		// Appended by W6 S5 (18 columns, see v4Cells).
		"arm_label", "fanout_pps", "presses", "dual_presses", "voice_endpoints", "late_loss_500",
		"one_way_p50_ms", "one_way_max_ms", "one_way_actual_p99_ms", "jitter_p99_ms", "jitter_mean_ms", "gap_p99_ms",
		"rtt_p99_us", "radio_update_p99_us", "radio_prop_p99_us", "secret_to_ack_p99_us", "corpus_sha256", "server_cpu_by_role",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, r := range o.Rows {
		rx := ""
		if r.Step.Health.RxLagP99Us != nil {
			rx = strconv.FormatInt(*r.Step.Health.RxLagP99Us, 10)
		}
		// late_loss_ratio and one_way_p99_ms are filled only when the bot
		// measured them (a v4 bot on Linux): an empty cell is "not measured",
		// never zero.
		rec := []string{
			strconv.Itoa(r.Seq), r.Kind, strconv.Itoa(r.N), r.Outcome.String(), strings.Join(r.Reasons, "; "),
			strconv.FormatBool(r.Held), cell(r.SettleSeconds),
			cell(r.Data.LossRatio), lateLossCell(r), usToMs(r.held(r.Step.OneWayP99Us)), u(r.Step.Expected), u(r.Step.Unique), u(r.Step.Lost), u(r.Step.Dups), u(r.Step.Reordered), strconv.Itoa(r.Step.BurstMax),
			u(r.Step.ResourceExhausted.Ramp), u(r.Step.ResourceExhausted.Settle), u(r.Step.ResourceExhausted.Hold),
			strconv.FormatInt(r.Step.JoinP99Us, 10), strconv.Itoa(r.Step.HelloTriesMax),
			cell(r.Data.ServerCPU), u(r.Window.ServerDrops), u(r.Window.SoftIRQTicks), u(r.Window.StealTicks), cell(r.Window.Seconds),
			u(r.Logs.ShardQueueFull), u(r.Logs.UpdateStreamBehind), u(r.Logs.NodeFeedBehind), u(r.Logs.NodeDeregistered),
			cell(r.Data.GenCPU), u(r.Step.Health.SocketDrops), rx, strconv.FormatInt(r.Step.Health.WheelLagP99Us, 10),
			strconv.FormatInt(r.Step.Health.SchedP99Us, 10), strconv.FormatInt(r.Step.Health.GCPauseP99Us, 10), strings.Join(r.Step.Violations, "; "),
			topology, label, rowPayload(r), JoinLabel, notMeasured(r.Step),
		}
		rec = append(rec, joinCells(r)...)
		rec = append(rec, round2Cells(r)...)
		rec = append(rec, v4Cells(r)...)
		if len(rec) != len(header) {
			return fmt.Errorf("steps.csv: row has %d cells for %d columns", len(rec), len(header))
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// joinCells are the 18 steps.csv cells appended after not_measured: the ramp's
// join account, the ramp window and the hold's host network counters. A value
// that was not measured (no ramp, a v1 bot, an unread /proc file) is an empty
// cell, never a zero.
func joinCells(r Row) []string {
	cells := make([]string, 18)
	j := r.Joins
	if j.Schema != "" || j.Source == "v1-error" {
		cells[0], cells[1], cells[2] = strconv.Itoa(j.Requested), strconv.Itoa(j.Joined), strconv.Itoa(j.JoinFailures)
		parts := make([]string, 0, len(joinStages))
		for _, k := range joinStages {
			parts = append(parts, k+"="+strconv.Itoa(j.FailuresByStage[k]))
		}
		cells[3] = strings.Join(parts, ";")
		if len(j.HelloTriesHist) > 0 {
			h := make([]string, len(j.HelloTriesHist))
			for i, v := range j.HelloTriesHist {
				h[i] = strconv.Itoa(v)
			}
			cells[4] = strings.Join(h, ";")
		}
		if r.FailedAckedByServer >= 0 {
			cells[5] = strconv.Itoa(r.FailedAckedByServer)
		}
		if j.Schema != "" {
			cells[6] = cell(j.RampS)
		}
	}
	if rw := r.RampWindow; rw.Seconds > 0 {
		cells[7], cells[8], cells[9] = cell(rw.ServerCPU), cell(rw.GenCPU), u(rw.ServerDrops)
		if rw.NetOK {
			cells[10], cells[11] = u(rw.SoftnetDropped), u(rw.UDPRcvbufErrors)
		}
		cells[12], cells[13], cells[14] = u(r.RampLogs.HelloAccepted), u(r.RampLogs.HelloRejected), u(r.RampLogs.ShardQueueFull)
	}
	if r.Held && r.Window.NetOK {
		cells[15], cells[16], cells[17] = u(r.Window.SoftnetDropped), u(r.Window.UDPRcvbufErrors), u(r.Window.UDPSndbufErrors)
	}
	return cells
}

// round2Cells are the 27 steps.csv cells appended by W6 fix 2: control loss, the
// bot's ramp mode, the join burst, generator GC and warnings, the bot's Ping
// account and the kernel TCP counters. A value that was not measured (a v2 bot,
// no hold, an unread /proc file) is an empty cell, never a zero. The kit's own
// control_lost count is always written: it counts what the kit saw.
func round2Cells(r Row) []string {
	cells := make([]string, 27)
	cells[0], cells[1] = strconv.Itoa(r.ControlLostCount), r.ControlLostPhase
	if len(r.ControlLost) > 0 {
		cells[2] = describeLost(r.ControlLost[0])
	}
	if j := r.Joins; j.Mode != "" {
		cells[3], cells[4], cells[5] = j.Mode, j.Talker, cell(j.JoinsPerS)
		cells[6], cells[7] = strconv.FormatInt(j.JoinP99Us, 10), strconv.Itoa(j.HelloRetried)
	}
	if c := r.JoinFailedChunk; c != nil {
		cells[8] = fmt.Sprintf("%d->%d", c.Had, c.Requested)
	}
	cells[9], cells[10] = r.BurstTalker, r.BurstResult
	if r.Held {
		if g := r.Step.Health.GCCycles; g != nil {
			cells[11] = u(*g)
		}
		cells[12] = strings.Join(r.Step.Warnings, "; ")
	}
	if p := r.Step.Ping; p.EveryS > 0 {
		cells[13], cells[14], cells[15] = u(p.Sent), u(p.Failed), strconv.FormatInt(p.RTTP99Us, 10)
	}
	if w := r.Window; r.Held {
		if w.TCPOK {
			cells[16], cells[17], cells[18], cells[19], cells[20], cells[21] = u(w.TCPAbortOnData), u(w.TCPAbortOnClose),
				u(w.TCPAbortOnMemory), u(w.TCPAbortOnTimeout), u(w.TCPAbortOnLinger), u(w.TCPTimeouts)
			cells[22], cells[23] = u(w.EstabResets), u(w.OutRsts)
		}
		if w.NetOK {
			cells[24] = u(w.SoftnetSqueezed)
		}
	}
	if rw := r.RampWindow; rw.Seconds > 0 && rw.TCPOK {
		cells[25], cells[26] = u(rw.TCPAbortOnData), u(rw.EstabResets)
	}
	return cells
}

// v4Cells are the 18 steps.csv cells appended by W6 S5: the v4 step's load
// axes and latency distribution, the bot-side RPC timings, the corpus hash and
// the per-process server CPU. A value the bot did not measure (a v3 bot, a
// non-Linux bot, the D2 arm) or a row with no hold is an empty cell, never zero.
func v4Cells(r Row) []string {
	cells := make([]string, 18)
	if !r.Held || r.Step.Schema == "" {
		return cells
	}
	st := r.Step
	cells[0] = st.ArmLabel
	cells[1] = cell(st.FanoutPPS)
	cells[2], cells[3] = strconv.Itoa(st.Presses), strconv.Itoa(st.DualPresses)
	cells[4] = strconv.Itoa(st.VoiceEndpoints)
	if st.LateLoss500 != nil {
		cells[5] = u(*st.LateLoss500)
	}
	cells[6], cells[7] = usToMs(st.OneWayP50Us), usToMs(st.OneWayMaxUs)
	cells[8] = usToMs(st.OneWayActualP99Us)
	cells[9], cells[10], cells[11] = usToMs(st.JitterP99Us), usToMs(st.JitterMeanUs), usToMs(st.GapP99Us)
	cells[12] = strconv.FormatInt(st.RTTP99Us, 10)
	cells[13] = strconv.FormatInt(st.RadioUpdateP99Us, 10)
	if st.RadioPropP99Us != nil {
		cells[14] = strconv.FormatInt(*st.RadioPropP99Us, 10)
	}
	cells[15] = strconv.FormatInt(st.SecretToAckP99Us, 10)
	if st.Corpus != nil {
		cells[16] = st.Corpus.SHA256
	}
	cells[17] = cpuByRoleCell(r.Window.ServerCPUByRole)
	return cells
}

// held returns p only for a row whose hold ran, else nil.
func (r Row) held(p *int64) *int64 {
	if !r.Held {
		return nil
	}
	return p
}

func lateLossCell(r Row) string {
	if !r.Held || r.Step.LateLossRatio == nil {
		return ""
	}
	return cell(*r.Step.LateLossRatio)
}

// usToMs renders a microsecond latency as milliseconds; nil is an empty cell.
func usToMs(p *int64) string {
	if p == nil {
		return ""
	}
	return cell(float64(*p) / 1000)
}

// cpuByRoleCell renders "role=fraction;..." sorted by role; no processes is
// an empty cell.
func cpuByRoleCell(m map[string]float64) string {
	if len(m) == 0 {
		return ""
	}
	roles := make([]string, 0, len(m))
	for k := range m {
		roles = append(roles, k)
	}
	sort.Strings(roles)
	parts := make([]string, len(roles))
	for i, k := range roles {
		parts[i] = k + "=" + cell(m[k])
	}
	return strings.Join(parts, ";")
}

// rowPayload is the payload label the bot reported for r's step, or the
// synthetic constant before any v4 step exists.
func rowPayload(r Row) string {
	if r.Step.Payload != "" {
		return r.Step.Payload
	}
	return PayloadLabel
}

// outcomePayload is the payload of the first held step, else the constant.
func outcomePayload(o *Outcome) string {
	for _, r := range o.Rows {
		if r.Held && r.Step.Payload != "" {
			return r.Step.Payload
		}
	}
	return PayloadLabel
}

// outcomeNotMeasured is notMeasured of the first held step, else the constant.
func outcomeNotMeasured(o *Outcome) string {
	for _, r := range o.Rows {
		if r.Held {
			return notMeasured(r.Step)
		}
	}
	return NotMeasured
}

// KneeMeta is the per-arm context of knees.csv.
type KneeMeta struct {
	Arm, Scenario, Topology string
	Predicted, NMax         int
	// Label is the label column; empty means Label.
	Label string
}

func rowOf(o *Outcome, kind string) *Row {
	for i := range o.Rows {
		if o.Rows[i].Kind == kind {
			return &o.Rows[i]
		}
	}
	return nil
}

// WriteKneesCSV writes the arm's result: the knee (or its censored form), the
// confirm and overload outcomes, the B-0 prediction and the N_max cap.
func WriteKneesCSV(w io.Writer, o *Outcome, m KneeMeta) error {
	cw := csv.NewWriter(w)
	header := []string{
		"arm", "scenario", "result", "result_kind", "n",
		"confirm_n", "confirm_outcome", "overload_n", "overload_outcome",
		"predicted_knee", "n_max", "topology", "label", "payload", "join_label", "not_measured", "notes",
		// Appended by W6 kit fix 3: only ever append.
		"upper_n",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	cn, co, on, oo := "", "", "", ""
	if r := rowOf(o, "confirm"); r != nil {
		cn, co = strconv.Itoa(r.N), r.Outcome.String()
	}
	if r := rowOf(o, "overload"); r != nil {
		on, oo = strconv.Itoa(r.N), r.Outcome.String()
	}
	// upper_n is the smallest breached size of a bracketed result, else empty.
	upper := ""
	if o.Result.Kind == sweep.Bracketed {
		upper = strconv.Itoa(o.Result.Hi)
	}
	rec := []string{
		m.Arm, m.Scenario, o.Result.String(), kindNameFor(o.Result), strconv.Itoa(o.Result.N),
		cn, co, on, oo,
		predictedCell(m.Predicted), strconv.Itoa(m.NMax), m.Topology, LabelOr(m.Label), outcomePayload(o), JoinLabel, outcomeNotMeasured(o),
		strings.Join(o.Notes, "; "),
		upper,
	}
	if err := cw.Write(rec); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// predictedCell is the predicted_knee cell: empty when no prediction was given,
// so a reader cannot take 0 for a prediction of zero.
func predictedCell(p int) string {
	if p <= 0 {
		return ""
	}
	return strconv.Itoa(p)
}

// predictedText is the log form of a prediction: "none" when not given.
func predictedText(p int) string {
	if p <= 0 {
		return "none"
	}
	return strconv.Itoa(p)
}

// WriteCalibrationCSV writes the calibration steps and the receive-capacity row.
func WriteCalibrationCSV(w io.Writer, c *Calibration, topology string) error {
	return WriteCalibrationCSVLabeled(w, c, topology, "")
}

// WriteCalibrationCSVLabeled is WriteCalibrationCSV with the label column set to
// label (empty means Label).
func WriteCalibrationCSVLabeled(w io.Writer, c *Calibration, topology, label string) error {
	label = LabelOr(label)
	cw := csv.NewWriter(w)
	header := []string{
		"kind", "n", "egress_pps", "valid", "violations", "lower_bound",
		"reflector_send_errors", "reflector_senders", "topology", "label", "payload",
		"lower_bound_reason",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, s := range c.Steps {
		if err := cw.Write([]string{
			"step", strconv.Itoa(s.N), cell(s.EgressPPS), strconv.FormatBool(s.Valid), strings.Join(s.Violations, "; "), "",
			"", "", topology, label, PayloadLabel, "",
		}); err != nil {
			return err
		}
	}
	if c.StoppedAt > 0 {
		if err := cw.Write([]string{
			"stopped", strconv.Itoa(c.StoppedAt), "", "", c.StopReason, "",
			"", "", topology, label, PayloadLabel, "",
		}); err != nil {
			return err
		}
	}
	if err := cw.Write([]string{
		"receive_capacity", "", cell(c.ReceivePPS), "", "", strconv.FormatBool(c.LowerBound),
		u(c.ReflectorSendErrors), strconv.Itoa(c.Senders), topology, label, PayloadLabel, c.LowerBoundReason,
	}); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}
