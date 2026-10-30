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

// FormatCapacity renders the calibration receive capacity for the log. It uses
// the ASCII ">=" on purpose (log lines stay grep-able); results.md uses the
// unicode form.
func FormatCapacity(c *Calibration) string {
	if !c.LowerBound {
		return fmt.Sprintf("receive capacity %.0f pps", c.ReceivePPS)
	}
	s := fmt.Sprintf("receive capacity >= %.0f pps (lower bound", c.ReceivePPS)
	if c.LowerBoundReason != "" {
		s += ": " + c.LowerBoundReason
	}
	return s + ")"
}

// FormatCalStopped is the line printed when calibration ended before its last
// step: where, why, and what receive capacity the completed steps still give.
func FormatCalStopped(tag string, c *Calibration) string {
	bots := "?"
	if c.StoppedBots >= 0 {
		bots = strconv.Itoa(c.StoppedBots)
	}
	capacity := FormatCapacity(c)
	return fmt.Sprintf("%s calibration stopped at N=%d (%s bots up): %s; %s from %d completed steps",
		tag, c.StoppedAt, bots, c.StopReason, capacity, len(c.Steps))
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
	// healthWarn is health with the generator's warnings (a soft bound that was
	// crossed but is not a breach), for the results table.
	healthWarn string
	// Round 2 (W6 fix 2). A figure the bot or the kernel did not give is "-".
	ctlLost, ctlFirst, rampRate, rampTalker string
	gcCycles, warnings                      string
	tcpAbortOnData, estabResets             string
}

func factsOf(r Row) rowFacts {
	var f rowFacts
	j := r.Joins
	f.joined = strconv.Itoa(max(0, r.N-j.JoinFailures))
	f.failures = strconv.Itoa(j.JoinFailures)
	f.tries = strconv.Itoa(max(j.HelloTriesMax, r.Step.HelloTriesMax))
	f.loss, f.serverCPU, f.genCPU = loss4(r.Data.LossRatio), frac(r.Data.ServerCPU), frac(r.Data.GenCPU)
	f.lossPct, f.serverCPUPct, f.genCPUPct = pct(r.Data.LossRatio, 2), pct(r.Data.ServerCPU, 0), pct(r.Data.GenCPU, 0)
	f.health, f.drops, f.gcCycles = dash, dash, dash
	f.warnings = "none"
	win, logs := r.RampWindow, r.RampLogs
	if r.Held {
		f.health = "ok"
		if len(r.Step.Violations) > 0 {
			f.health = strings.Join(r.Step.Violations, ",")
		}
		f.drops = u(r.Window.ServerDrops)
		win, logs = r.Window, r.Logs
		if g := r.Step.Health.GCCycles; g != nil {
			f.gcCycles = u(*g)
		}
		if len(r.Step.Warnings) > 0 {
			f.warnings = strings.Join(r.Step.Warnings, ",")
		}
	}
	f.healthWarn = f.health
	if r.Held && len(r.Step.Violations) == 0 && len(r.Step.Warnings) > 0 {
		f.healthWarn = "ok (warn: " + strings.Join(r.Step.Warnings, ",") + ")"
	}
	f.ctlLost = strconv.Itoa(r.ControlLostCount)
	if len(r.ControlLost) > 0 {
		f.ctlFirst = describeLost(r.ControlLost[0])
	}
	f.rampRate, f.rampTalker = dash, dash
	if j.Mode != "" {
		f.rampRate, f.rampTalker = fmt.Sprintf("%.1f", j.JoinsPerS), j.Talker
		if f.rampTalker == "" {
			f.rampTalker = dash
		}
	}
	f.tcpAbortOnData, f.estabResets = dash, dash
	if win.TCPOK {
		f.tcpAbortOnData, f.estabResets = u(win.TCPAbortOnData), u(win.EstabResets)
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
	s := fmt.Sprintf("%s %s #%d N=%d: outcome=%s joined=%s join_failures=%s hello_tries_max=%s loss=%s server_cpu=%s gen_cpu=%s gen_health=%s ramp_server_cpu=%s ramp_gen_cpu=%s server_drops=%s softnet_dropped=%s hello_accepted=%s hello_rejected=%s shard_queue_full=%s control_lost=%s",
		tag, r.Kind, r.Seq, r.N, r.Outcome, f.joined, f.failures, f.tries, f.loss, f.serverCPU, f.genCPU, f.health,
		f.rampServerCPU, f.rampGenCPU, f.drops, f.softnet, f.helloAccepted, f.helloRejected, f.queueFull, f.ctlLost)
	if f.ctlFirst != "" {
		s += fmt.Sprintf(" control_lost_first=%q", f.ctlFirst)
	}
	s += fmt.Sprintf(" ramp_joins_per_s=%s ramp_talker=%s gc_cycles=%s warnings=%s tcp_abort_on_data=%s estab_resets=%s",
		f.rampRate, f.rampTalker, f.gcCycles, f.warnings, f.tcpAbortOnData, f.estabResets)
	if f.failedAcked != "" {
		s += " failed_acked_by_server=" + f.failedAcked
	}
	return s + fmt.Sprintf(" reasons=%q", f.reasonsS)
}

// FormatBurstLine is the progress line of one join-burst row: the bots added at
// once with the talker silent or live, and how many of them failed to join.
func FormatBurstLine(tag string, r Row) string {
	j := r.Joins
	from, to := j.Had, j.Requested
	if to == 0 { // a burst that lost control reports no joins line
		from, to = r.N, r.N
	}
	rate, p99, joined, fails, retried, triesMax := dash, dash, dash, dash, dash, dash
	if j.Mode != "" || j.Requested != 0 { // otherwise the bot gave no joins line: no join data exists
		joined, fails, triesMax = strconv.Itoa(j.Joined), strconv.Itoa(j.JoinFailures), strconv.Itoa(j.HelloTriesMax)
		retried = fmt.Sprintf("%d/%d", j.HelloRetried, to-from)
	}
	if j.Mode != "" {
		rate, p99 = fmt.Sprintf("%.1f", j.JoinsPerS), fmt.Sprintf("%.2fs", float64(j.JoinP99Us)/1e6)
	}
	return fmt.Sprintf("%s burst #%d talker=%s N=%d->%d: joined=%s join_failures=%s joins_per_s=%s join_p99=%s hello_retried=%s hello_tries_max=%s result=%s",
		tag, r.Seq, r.BurstTalker, from, to, joined, fails, rate, p99, retried, triesMax, r.BurstResult)
}

// ResultsInput is what one leg's results.md is built from. Cal and Out may be
// nil: a leg that died early still gets a results file saying so.
type ResultsInput struct {
	Seed          int64
	Arm, Scenario string
	Result        string
	Error         string
	// StoppedEarly is the reason a leg ended before its sweep finished (a run
	// error or the budget); the result then comes from the sweep state.
	StoppedEarly    string
	Cal             *Calibration
	Out             *Outcome
	Predicted, NMax int
	// Label is the footer label; empty means Label.
	Label string
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
		if in.StoppedEarly != "" {
			p("**Result:** %s; stopped early: %s  \n", in.Result, in.StoppedEarly)
		} else {
			p("**Result:** %s  \n", in.Result)
		}
	}
	if c := in.Cal; c != nil {
		line := fmt.Sprintf("receive capacity %.0f pps", c.ReceivePPS)
		if c.LowerBound {
			line = fmt.Sprintf("receive capacity ≥ %.0f pps (lower bound", c.ReceivePPS)
			if c.LowerBoundReason != "" {
				line += ": " + c.LowerBoundReason
			}
			line += ")"
		}
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
	if in.Predicted > 0 {
		p("**N_max:** %d · **predicted knee:** %d · **starts:** %d\n\n", in.NMax, in.Predicted, starts)
	} else {
		p("**N_max:** %d · **predicted knee:** not given · **starts:** %d\n\n", in.NMax, starts)
	}

	hasCal := in.Cal != nil && len(in.Cal.Steps) > 0
	// Join bursts measure join cost, not steady capacity: their own table.
	var steps, bursts []Row
	if in.Out != nil {
		for _, r := range in.Out.Rows {
			if r.Kind == "burst" {
				bursts = append(bursts, r)
			} else {
				steps = append(steps, r)
			}
		}
	}
	hasRows := len(steps) > 0
	if hasCal || hasRows {
		if err := mdRow(w, "kind", "#", "N", "joined", "outcome", "loss", "server CPU", "gen CPU", "gen health", "join failures", "hello tries max", "server drops", "control lost", "ramp joins/s", "reasons"); err != nil {
			return err
		}
		if err := mdRow(w, "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---"); err != nil {
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
				dash, dash, dash, health, "0", strconv.Itoa(s.HelloTriesMax), dash, dash, dash, ""); err != nil {
				return err
			}
		}
	}
	if hasRows {
		for _, r := range steps {
			f := factsOf(r)
			if err := mdRow(w, r.Kind, strconv.Itoa(r.Seq), strconv.Itoa(r.N), f.joined, r.Outcome.String(),
				f.lossPct, f.serverCPUPct, f.genCPUPct, f.healthWarn, f.failures, f.tries, f.drops, f.ctlLost, f.rampRate, f.reasonsS); err != nil {
				return err
			}
		}
	}
	if hasCal || hasRows {
		p("\n")
	}
	if len(bursts) > 0 {
		p("#### Join bursts\n\n")
		if err := mdRow(w, "talker", "from N", "added", "joined", "failures", "joins/s", "join p99", "hello retried", "result"); err != nil {
			return err
		}
		if err := mdRow(w, "---", "---", "---", "---", "---", "---", "---", "---", "---"); err != nil {
			return err
		}
		for _, r := range bursts {
			j := r.Joins
			from, added := j.Had, j.Requested-j.Had
			if j.Requested == 0 {
				from, added = r.N, 0
			}
			rate, p99 := dash, dash
			if j.Mode != "" {
				rate, p99 = fmt.Sprintf("%.1f", j.JoinsPerS), fmt.Sprintf("%.2fs", float64(j.JoinP99Us)/1e6)
			}
			if err := mdRow(w, r.BurstTalker, strconv.Itoa(from), strconv.Itoa(added), strconv.Itoa(j.Joined), strconv.Itoa(j.JoinFailures),
				rate, p99, strconv.Itoa(j.HelloRetried), r.BurstResult); err != nil {
				return err
			}
		}
		p("\n")
	}
	p("_%s_\n", LabelOr(in.Label))
	return nil
}

// FormatControlLostLine is the log line printed the moment the load bot reports
// a lost control connection, so the Actions log shows each death live and in
// order, not only in the step's row. A v2 hold death has no age, ping data,
// address or running total: those print as "-".
func FormatControlLostLine(tag string, ev ControlLostEvent) string {
	age, ping, addr, total := dash, dash, dash, dash
	if !ev.BotExited {
		age = strconv.FormatFloat(ev.AgeS, 'f', 1, 64)
	}
	if ev.SinceLastOKPingS >= 0 && !ev.BotExited {
		ping = strconv.FormatFloat(ev.SinceLastOKPingS, 'f', 1, 64)
	}
	if ev.LocalAddr != "" {
		addr = ev.LocalAddr
	}
	if ev.DeadTotal > 0 {
		total = strconv.Itoa(ev.DeadTotal)
	}
	return fmt.Sprintf("%s control_lost bot=%d cause=%s code=%s phase=%s age_s=%s since_last_ok_ping_s=%s local_addr=%s dead_total=%s message=%q",
		tag, ev.Bot, ev.Cause, ev.Code, ev.Phase, age, ping, addr, total, ev.Message)
}
