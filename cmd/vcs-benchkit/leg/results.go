package leg

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
}

// Calibration is the generator's receive-side calibration of one leg.
type Calibration struct {
	Steps               []CalStep
	ReceivePPS          float64
	LowerBound          bool
	ReflectorSendErrors uint64
	Senders             int
}

// ParseCalibrate reads `vcs-loadbot calibrate` stdout: one calibration line per
// step and a final receive_capacity line, which must be present.
func ParseCalibrate(out []byte) (steps []CalStep, pps float64, lowerBound bool, err error) {
	haveCap := false
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
			return nil, 0, false, fmt.Errorf("calibrate output: %w", err)
		}
		switch head.Type {
		case "calibration":
			var s CalStep
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, 0, false, fmt.Errorf("calibrate output: %w", err)
			}
			steps = append(steps, s)
		case "receive_capacity":
			pps, lowerBound, haveCap = head.PPS, head.LowerBound, true
		}
	}
	if !haveCap {
		return steps, 0, false, fmt.Errorf("calibrate output has no receive_capacity line")
	}
	return steps, pps, lowerBound, nil
}

// ParseReflectorStats returns the reflector's send-error count from its final
// reflector_stats line.
func ParseReflectorStats(out []byte) (uint64, error) {
	for _, raw := range bytes.Split(out, []byte("\n")) {
		var s struct {
			Type       string `json:"type"`
			SendErrors uint64 `json:"send_errors"`
		}
		if json.Unmarshal(raw, &s) == nil && s.Type == "reflector_stats" {
			return s.SendErrors, nil
		}
	}
	return 0, fmt.Errorf("reflector output has no reflector_stats line")
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
	switch k {
	case sweep.KneeFound:
		return "knee"
	case sweep.GeneratorBound:
		return "generator-bound"
	case sweep.TimeBound:
		return "time-bound"
	case sweep.BelowRange:
		return "below-range"
	}
	return fmt.Sprintf("kind-%d", int(k))
}

// WriteStepsCSV writes one row per measured step. Every row carries the label,
// the payload label, the join-timing qualification and the topology.
func WriteStepsCSV(w io.Writer, o *Outcome, topology string) error {
	cw := csv.NewWriter(w)
	header := []string{
		"seq", "kind", "n", "outcome", "reasons", "held", "settle_s",
		"loss_ratio", "late_loss_ratio", "one_way_p99_ms", "expected", "unique", "lost", "dups", "reordered", "burst_max",
		"rex_ramp", "rex_settle", "rex_hold", "join_p99_us", "hello_tries_max",
		"server_cpu", "server_drops", "softirq_ticks_server_set", "steal_ticks", "sample_s",
		"shard_queue_full", "update_stream_behind", "node_feed_behind", "node_deregistered",
		"gen_cpu", "gen_socket_drops", "gen_rx_lag_p99_us", "gen_wheel_lag_p99_us", "gen_sched_p99_us", "gen_gc_p99_us", "gen_violations",
		"topology", "label", "payload", "join_label", "not_measured",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, r := range o.Rows {
		rx := ""
		if r.Step.Health.RxLagP99Us != nil {
			rx = strconv.FormatInt(*r.Step.Health.RxLagP99Us, 10)
		}
		// late_loss_ratio and one_way_p99_ms are never measured by the C1 bot:
		// the cells stay empty so no reader mistakes them for a measurement.
		rec := []string{
			strconv.Itoa(r.Seq), r.Kind, strconv.Itoa(r.N), r.Outcome.String(), strings.Join(r.Reasons, "; "),
			strconv.FormatBool(r.Held), cell(r.SettleSeconds),
			cell(r.Data.LossRatio), "", "", u(r.Step.Expected), u(r.Step.Unique), u(r.Step.Lost), u(r.Step.Dups), u(r.Step.Reordered), strconv.Itoa(r.Step.BurstMax),
			u(r.Step.ResourceExhausted.Ramp), u(r.Step.ResourceExhausted.Settle), u(r.Step.ResourceExhausted.Hold),
			strconv.FormatInt(r.Step.JoinP99Us, 10), strconv.Itoa(r.Step.HelloTriesMax),
			cell(r.Data.ServerCPU), u(r.Window.ServerDrops), u(r.Window.SoftIRQTicks), u(r.Window.StealTicks), cell(r.Window.Seconds),
			u(r.Logs.ShardQueueFull), u(r.Logs.UpdateStreamBehind), u(r.Logs.NodeFeedBehind), u(r.Logs.NodeDeregistered),
			cell(r.Data.GenCPU), u(r.Step.Health.SocketDrops), rx, strconv.FormatInt(r.Step.Health.WheelLagP99Us, 10),
			strconv.FormatInt(r.Step.Health.SchedP99Us, 10), strconv.FormatInt(r.Step.Health.GCPauseP99Us, 10), strings.Join(r.Step.Violations, "; "),
			topology, Label, PayloadLabel, JoinLabel, NotMeasured,
		}
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

// KneeMeta is the per-arm context of knees.csv.
type KneeMeta struct {
	Arm, Scenario, Topology string
	Predicted, NMax         int
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
	rec := []string{
		m.Arm, m.Scenario, o.Result.String(), kindName(o.Result.Kind), strconv.Itoa(o.Result.N),
		cn, co, on, oo,
		strconv.Itoa(m.Predicted), strconv.Itoa(m.NMax), m.Topology, Label, PayloadLabel, JoinLabel, NotMeasured,
		strings.Join(o.Notes, "; "),
	}
	if err := cw.Write(rec); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// WriteCalibrationCSV writes the calibration steps and the receive-capacity row.
func WriteCalibrationCSV(w io.Writer, c *Calibration, topology string) error {
	cw := csv.NewWriter(w)
	header := []string{
		"kind", "n", "egress_pps", "valid", "violations", "lower_bound",
		"reflector_send_errors", "reflector_senders", "topology", "label", "payload",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, s := range c.Steps {
		if err := cw.Write([]string{
			"step", strconv.Itoa(s.N), cell(s.EgressPPS), strconv.FormatBool(s.Valid), strings.Join(s.Violations, "; "), "",
			"", "", topology, Label, PayloadLabel,
		}); err != nil {
			return err
		}
	}
	if err := cw.Write([]string{
		"receive_capacity", "", cell(c.ReceivePPS), "", "", strconv.FormatBool(c.LowerBound),
		u(c.ReflectorSendErrors), strconv.Itoa(c.Senders), topology, Label, PayloadLabel,
	}); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}
