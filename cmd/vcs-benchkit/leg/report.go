package leg

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// The human-readable side of a leg (W6 fix, accepted proposal 2026-10-28): the
// progress lines printed to stdout while a leg runs and the per-leg results
// table. Nothing here decides anything; the CSVs stay the machine-readable
// record.

// LegTag is the prefix of every progress line of a leg.
func LegTag(seed int64, arm, scenario string) string {
	return fmt.Sprintf("[leg %d %s %s]", seed, arm, scenario)
}

// dash renders a figure that was not measured.
const dash = "-"

// frac renders a CPU fraction with two decimals, NaN as "-".
func frac(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dash
	}
	return fmt.Sprintf("%.2f", f)
}

func loss4(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dash
	}
	return fmt.Sprintf("%.4f", f)
}

func pct(f float64, decimals int) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dash
	}
	return fmt.Sprintf("%.*f%%", decimals, f*100)
}

// expectedPPS is a calibration step's expected receive rate: the bot's own
// figure (v2), else 50 packets/s per listener, n-1 of them (v1).
func expectedPPS(s CalStep) float64 {
	if s.ExpectedPPS > 0 {
		return s.ExpectedPPS
	}
	return 50 * float64(s.N-1)
}

func delivered(s CalStep) float64 {
	if e := expectedPPS(s); e > 0 {
		return s.EgressPPS / e
	}
	return math.NaN()
}

// FormatCalLine is the progress line of one completed calibration step.
func FormatCalLine(tag string, s CalStep) string {
	v := "none"
	if len(s.Violations) > 0 {
		v = strings.Join(s.Violations, ",")
	}
	return fmt.Sprintf("%s calibration N=%d: egress_pps=%.0f expected_pps=%.0f delivered=%s valid=%v hello_tries_max=%d violations=%s",
		tag, s.N, s.EgressPPS, expectedPPS(s), pct(delivered(s), 0), s.Valid, s.HelloTriesMax, v)
}

// FormatCalStopped is the line printed when calibration ended before its last
// step: where, why, and what receive capacity the completed steps still give.
func FormatCalStopped(tag string, c *Calibration) string {
	bots := "?"
	if c.StoppedBots >= 0 {
		bots = strconv.Itoa(c.StoppedBots)
	}
	return fmt.Sprintf("%s calibration stopped at N=%d (%s bots up): %s; receive capacity %.0f pps (lower bound %v) from %d completed steps",
		tag, c.StoppedAt, bots, c.StopReason, c.ReceivePPS, c.LowerBound, len(c.Steps))
}

// FormatRampProgress relays one informational `progress` line of the load bot.
func FormatRampProgress(tag string, p Progress) string {
	switch p.Phase {
	case "ramp":
		return fmt.Sprintf("%s ramp N=%d->%d: done=%d joined=%d join_failures=%d in_flight=%d hello_retried=%d elapsed=%.0fs",
			tag, p.Had, p.Requested, p.Done, p.Joined, p.JoinFailures, p.InFlight, p.HelloRetried, p.ElapsedS)
	case "calibrate_ramp":
		return fmt.Sprintf("%s calibration ramp N=%d: bots=%d hello_retried=%d elapsed=%.0fs",
			tag, p.Target, p.Bots, p.HelloRetried, p.ElapsedS)
	}
	return fmt.Sprintf("%s progress phase=%s elapsed=%.0fs", tag, p.Phase, p.ElapsedS)
}

// rowFacts are the figures a row line and a table row share. A figure the step
// never measured is "-": a join-failure step has no hold, so no loss and no
// hold CPU.
type rowFacts struct {
	joined, failures, tries                    string
	loss, serverCPU, genCPU, health            string
	rampServerCPU, rampGenCPU                  string
	drops, softnet                             string
	helloAccepted, helloRejected, queueFull    string
	failedAcked                                string
	lossPct, serverCPUPct, genCPUPct, reasonsS string
}

func factsOf(r Row) rowFacts {
	var f rowFacts
	j := r.Joins
	f.joined = strconv.Itoa(max(0, r.N-j.JoinFailures))
	f.failures = strconv.Itoa(j.JoinFailures)
	f.tries = strconv.Itoa(max(j.HelloTriesMax, r.Step.HelloTriesMax))
	f.loss, f.serverCPU, f.genCPU = loss4(r.Data.LossRatio), frac(r.Data.ServerCPU), frac(r.Data.GenCPU)
	f.lossPct, f.serverCPUPct, f.genCPUPct = pct(r.Data.LossRatio, 2), pct(r.Data.ServerCPU, 0), pct(r.Data.GenCPU, 0)
	f.health, f.drops = dash, dash
	win, logs := r.RampWindow, r.RampLogs
	if r.Held {
		f.health = "ok"
		if len(r.Step.Violations) > 0 {
			f.health = strings.Join(r.Step.Violations, ",")
		}
		f.drops = u(r.Window.ServerDrops)
		win, logs = r.Window, r.Logs
	}
	f.rampServerCPU, f.rampGenCPU = dash, dash
	if r.RampWindow.Seconds > 0 {
		f.rampServerCPU, f.rampGenCPU = frac(r.RampWindow.ServerCPU), frac(r.RampWindow.GenCPU)
	}
	f.softnet = dash
	if win.NetOK {
		f.softnet = u(win.SoftnetDropped)
	}
	f.helloAccepted, f.helloRejected, f.queueFull = u(logs.HelloAccepted), u(logs.HelloRejected), u(logs.ShardQueueFull)
	if j.JoinFailures > 0 && r.FailedAckedByServer >= 0 {
		f.failedAcked = fmt.Sprintf("%d/%d", r.FailedAckedByServer, j.JoinFailures)
	}
	f.reasonsS = strings.Join(r.Reasons, "; ")
	return f
}

// FormatRowLine is the progress line of one measured step.
func FormatRowLine(tag string, r Row) string {
	f := factsOf(r)
	s := fmt.Sprintf("%s %s #%d N=%d: outcome=%s joined=%s join_failures=%s hello_tries_max=%s loss=%s server_cpu=%s gen_cpu=%s gen_health=%s ramp_server_cpu=%s ramp_gen_cpu=%s server_drops=%s softnet_dropped=%s hello_accepted=%s hello_rejected=%s shard_queue_full=%s",
		tag, r.Kind, r.Seq, r.N, r.Outcome, f.joined, f.failures, f.tries, f.loss, f.serverCPU, f.genCPU, f.health,
		f.rampServerCPU, f.rampGenCPU, f.drops, f.softnet, f.helloAccepted, f.helloRejected, f.queueFull)
	if f.failedAcked != "" {
		s += " failed_acked_by_server=" + f.failedAcked
	}
	return s + fmt.Sprintf(" reasons=%q", f.reasonsS)
}

// ResultsInput is what one leg's results.md is built from. Cal and Out may be
// nil: a leg that died early still gets a results file saying so.
type ResultsInput struct {
	Seed            int64
	Arm, Scenario   string
	Result          string
	Error           string
	Cal             *Calibration
	Out             *Outcome
	Predicted, NMax int
}

// mdCell makes a string safe inside one table cell.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

func mdRow(w io.Writer, cells ...string) error {
	for i, c := range cells {
		cells[i] = mdCell(c)
	}
	_, err := fmt.Fprintf(w, "| %s |\n", strings.Join(cells, " | "))
	return err
}

// WriteResultsMarkdown writes one leg's results: a header, the calibration
// summary and one table of calibration, sweep, confirm and overload rows.
func WriteResultsMarkdown(w io.Writer, in ResultsInput) error {
	p := func(f string, a ...any) { _, _ = fmt.Fprintf(w, f, a...) }
	p("### Leg %d %s (arm %s)\n\n", in.Seed, in.Scenario, in.Arm)
	if in.Error != "" {
		p("**Result:** did not finish: %s  \n", in.Error)
	} else {
		p("**Result:** %s  \n", in.Result)
	}
	if c := in.Cal; c != nil {
		line := fmt.Sprintf("receive capacity %.0f pps (lower bound %v)", c.ReceivePPS, c.LowerBound)
		if c.StoppedAt > 0 {
			line += fmt.Sprintf("; stopped at N=%d: %s", c.StoppedAt, c.StopReason)
		}
		p("**Calibration:** %s  \n", line)
	} else {
		p("**Calibration:** not completed  \n")
	}
	starts := 0
	if in.Out != nil {
		starts = in.Out.Starts
	}
	p("**N_max:** %d · **predicted knee:** %d · **starts:** %d\n\n", in.NMax, in.Predicted, starts)

	hasCal := in.Cal != nil && len(in.Cal.Steps) > 0
	hasRows := in.Out != nil && len(in.Out.Rows) > 0
	if hasCal || hasRows {
		if err := mdRow(w, "kind", "#", "N", "joined", "outcome", "loss", "server CPU", "gen CPU", "gen health", "join failures", "hello tries max", "server drops", "reasons"); err != nil {
			return err
		}
		if err := mdRow(w, "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---"); err != nil {
			return err
		}
	}
	if hasCal {
		for _, s := range in.Cal.Steps {
			joined := s.Bots
			if joined == 0 {
				joined = s.N
			}
			health := "ok"
			if !s.Valid || len(s.Violations) > 0 {
				health = strings.Join(s.Violations, ",")
				if health == "" {
					health = "invalid"
				}
			}
			if err := mdRow(w, "calibration", dash, strconv.Itoa(s.N), strconv.Itoa(joined), "delivered "+pct(delivered(s), 0),
				dash, dash, dash, health, "0", strconv.Itoa(s.HelloTriesMax), dash, ""); err != nil {
				return err
			}
		}
	}
	if hasRows {
		for _, r := range in.Out.Rows {
			f := factsOf(r)
			if err := mdRow(w, r.Kind, strconv.Itoa(r.Seq), strconv.Itoa(r.N), f.joined, r.Outcome.String(),
				f.lossPct, f.serverCPUPct, f.genCPUPct, f.health, f.failures, f.tries, f.drops, f.reasonsS); err != nil {
				return err
			}
		}
	}
	if hasCal || hasRows {
		p("\n")
	}
	p("_%s_\n", Label)
	return nil
}
