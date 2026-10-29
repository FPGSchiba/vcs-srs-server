package leg

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Diagnostics (W6 fix 2, S-T7). Bench run 40859021110 lost two legs to a
// connection reset and left nothing to attribute it with: the server log lived in
// a scratch directory that was deleted, and the bot's stderr never reached the
// Actions log. This file keeps the server log in the leg artifact and prints,
// into the Actions log, what a person needs when a step loses control or a leg
// fails: the status, the control_lost events, the kernel TCP counters, the
// server log's WARN/ERROR summary and tail, and the bot's stderr tail.

const (
	// serverTailLines and botTailLines are how much of each log a block prints.
	serverTailLines = 200
	botTailLines    = 50
	// summaryTop is how many distinct WARN/ERROR messages the summary lists.
	summaryTop = 15
	// maxEvents is how many control_lost events a block lists.
	maxEvents = 8
	// maxDiagLine caps one printed log line, so a runaway line cannot flood the log.
	maxDiagLine = 600
	// heartbeatEvery is the period of the kit's heartbeat while a ramp, settle or
	// hold runs long.
	heartbeatEvery = 15 * time.Second
)

// serverLogGzName is the artifact file for the k-th server's log. It holds the
// standalone process log only: never the data directory, the config or the
// throwaway coalition passwords.
func serverLogGzName(k int) string { return fmt.Sprintf("server-%d.log.gz", k) }

// SaveServerLog writes text, gzipped, as <outDir>/server-<k>.log.gz and returns
// the path. It copies nothing else.
func SaveServerLog(outDir string, k int, text string) (string, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(text)); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	p := filepath.Join(outDir, serverLogGzName(k))
	return p, os.WriteFile(p, buf.Bytes(), 0o644)
}

// readGz returns the gunzipped text of a file written by SaveServerLog.
func readGz(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	return string(b), err
}

// Tail is the last n lines of text. A trailing newline does not make an empty
// last line, and "\r\n" is read as "\n".
func Tail(text string, n int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" || n <= 0 {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// SummaryEntry is one distinct WARN or ERROR message and how often it occurred.
type SummaryEntry struct {
	Level, Msg string
	Count      int
}

var (
	reSlogLevel = regexp.MustCompile(`^time=\S+\s+level=(\w+)`)
	reSlogMsg   = regexp.MustCompile(`(?:^|\s)msg=("(?:[^"\\]|\\.)*"|\S+)`)
	// grpc-go's own logger (GRPC_GO_LOG_SEVERITY_LEVEL) writes
	// "2026/10/29 11:13:03 WARNING: [transport] ..." to the same stream.
	reGrpcLine = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d (WARNING|ERROR): (.*)$`)
	reHexAddr  = regexp.MustCompile(`0x[0-9a-fA-F]+`)
)

// parseLogLevel returns the WARN or ERROR level and message of one log line, in
// slog text, slog JSON or grpc-go logger format; ok is false for any other line.
func parseLogLevel(line string) (level, msg string, ok bool) {
	line = strings.TrimRight(line, "\r")
	switch {
	case strings.HasPrefix(line, "{"):
		var j struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &j) != nil {
			return "", "", false
		}
		level, msg = j.Level, j.Msg
	case reGrpcLine.MatchString(line):
		m := reGrpcLine.FindStringSubmatch(line)
		level, msg = m[1], reHexAddr.ReplaceAllString(m[2], "0x*")
	default:
		lm := reSlogLevel.FindStringSubmatch(line)
		if lm == nil {
			return "", "", false
		}
		level = lm[1]
		if mm := reSlogMsg.FindStringSubmatch(line); mm != nil {
			msg = mm[1]
			if u, err := strconv.Unquote(msg); err == nil {
				msg = u
			}
		}
	}
	switch strings.ToUpper(level) {
	case "WARN", "WARNING":
		return "WARN", msg, true
	case "ERROR":
		return "ERROR", msg, true
	}
	return "", "", false
}

// LogSummary counts the WARN and ERROR lines of a server log by level and
// message: most frequent first, ties by level then message, at most top entries.
func LogSummary(log string, top int) []SummaryEntry {
	type key struct{ level, msg string }
	counts := map[key]int{}
	for _, line := range strings.Split(log, "\n") {
		if level, msg, ok := parseLogLevel(line); ok {
			counts[key{level, msg}]++
		}
	}
	out := make([]SummaryEntry, 0, len(counts))
	for k, c := range counts {
		out = append(out, SummaryEntry{Level: k.level, Msg: k.msg, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Count != b.Count:
			return a.Count > b.Count
		case a.Level != b.Level:
			return a.Level < b.Level
		}
		return a.Msg < b.Msg
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}

// DiagInput is everything one diagnostics block prints. Unavailable sources
// carry their reason in the *Err field and print as such.
type DiagInput struct {
	Tag, Title string
	// Status is the error or the reason the block is printed for.
	Status string
	Events []ControlLostEvent
	// Window is the sampler window the TCP counters come from (nil = none);
	// WindowName says which (ramp or hold).
	Window     *Window
	WindowName string
	// ServerLog and BotStderr are the log texts; the Err field replaces a text
	// that could not be read.
	ServerLog, ServerLogErr string
	BotStderr, BotStderrErr string
}

// diagLine writes one body line, indented. A log line that would read as a
// workflow command ("::error::...") is defanged: the logs are not trusted text.
func diagLine(w io.Writer, line string) {
	line = strings.TrimRight(line, "\r")
	if len(line) > maxDiagLine {
		line = line[:maxDiagLine] + "..."
	}
	if strings.HasPrefix(strings.TrimSpace(line), "::") {
		line = strings.Replace(line, "::", ": :", 1)
	}
	fmt.Fprintf(w, "  %s\n", line)
}

func writeSummary(w io.Writer, log, logErr string) {
	fmt.Fprintln(w, "server log: WARN/ERROR summary")
	if logErr != "" {
		diagLine(w, "not available: "+logErr)
		return
	}
	s := LogSummary(log, summaryTop)
	if len(s) == 0 {
		diagLine(w, "(no WARN or ERROR lines)")
	}
	for _, e := range s {
		diagLine(w, fmt.Sprintf("%dx %s %s", e.Count, e.Level, e.Msg))
	}
}

// tcpLine renders the kernel TCP counters of a window.
func tcpLine(w *Window) string {
	if !w.TCPOK {
		return "unread: " + w.TCPErr
	}
	s := fmt.Sprintf("tcp_abort_on_data=%d tcp_abort_on_close=%d tcp_abort_on_memory=%d tcp_abort_on_timeout=%d tcp_abort_on_linger=%d tcp_timeouts=%d estab_resets=%d out_rsts=%d softnet_squeezed=%d",
		w.TCPAbortOnData, w.TCPAbortOnClose, w.TCPAbortOnMemory, w.TCPAbortOnTimeout, w.TCPAbortOnLinger, w.TCPTimeouts, w.EstabResets, w.OutRsts, w.SoftnetSqueezed)
	if w.NetOK {
		s += fmt.Sprintf(" softnet_dropped=%d", w.SoftnetDropped)
	}
	if len(w.TCPMissing) > 0 {
		s += " (kernel lacks: " + strings.Join(w.TCPMissing, ",") + ")"
	}
	return s
}

// WriteDiagnostics writes one ::group:: block. The sections and their order are
// fixed: the status, the control_lost events, the TCP counters, the server log's
// WARN/ERROR summary and tail, and the load bot's stderr tail.
func WriteDiagnostics(w io.Writer, in DiagInput) {
	fmt.Fprintf(w, "::group::%s diagnostics: %s\n", in.Tag, in.Title)
	fmt.Fprintf(w, "status: %s\n", strings.ReplaceAll(in.Status, "\n", " | "))

	fmt.Fprintf(w, "control_lost events (first %d)\n", maxEvents)
	if len(in.Events) == 0 {
		diagLine(w, "(none)")
	}
	for _, ev := range in.Events[:min(maxEvents, len(in.Events))] {
		diagLine(w, fmt.Sprintf("bot=%d code=%s message=%q phase=%s age_s=%.1f since_last_ok_ping_s=%.1f",
			ev.Bot, ev.Code, ev.Message, ev.Phase, ev.AgeS, ev.SinceLastOKPingS))
	}

	name := in.WindowName
	if name == "" {
		name = "this window"
	}
	fmt.Fprintf(w, "tcp counters (%s)\n", name)
	if in.Window == nil {
		diagLine(w, "not sampled")
	} else {
		diagLine(w, tcpLine(in.Window))
	}

	writeSummary(w, in.ServerLog, in.ServerLogErr)

	fmt.Fprintf(w, "server log: last %d lines\n", serverTailLines)
	if in.ServerLogErr != "" {
		diagLine(w, "not available: "+in.ServerLogErr)
	}
	for _, l := range Tail(in.ServerLog, serverTailLines) {
		diagLine(w, l)
	}

	fmt.Fprintf(w, "load bot stderr: last %d lines\n", botTailLines)
	if in.BotStderrErr != "" {
		diagLine(w, "not available: "+in.BotStderrErr)
	}
	for _, l := range Tail(in.BotStderr, botTailLines) {
		diagLine(w, l)
	}
	fmt.Fprintln(w, "::endgroup::")
}

// diagSource is what the reporter reads the logs from: the live system while one
// runs, the saved copy once it was closed.
type diagSource interface {
	ServerLogText() (string, error)
	BotStderrText() (string, error)
}

// diagReporter prints the diagnostics for a leg. Its source is nil until the S
// arm's environment exists; a block printed before then says the logs are not
// available.
type diagReporter struct {
	w   io.Writer
	tag string
	src diagSource

	events []ControlLostEvent // control losses seen so far, newest last
	win    *Window            // the window of the last row that sampled one
	winTag string
}

func (r *diagReporter) logs() (in DiagInput) {
	in.Tag = r.tag
	if r.src == nil {
		in.ServerLogErr, in.BotStderrErr = "no server was started", "no load bot was started"
		return in
	}
	var err error
	if in.ServerLog, err = r.src.ServerLogText(); err != nil {
		in.ServerLogErr = err.Error()
	}
	if in.BotStderr, err = r.src.BotStderrText(); err != nil {
		in.BotStderrErr = err.Error()
	}
	return in
}

// rowWindow is the sampler window a row's TCP counters come from.
func rowWindow(row Row) (*Window, string) {
	if row.Held {
		w := row.Window
		return &w, "hold, this window"
	}
	w := row.RampWindow
	return &w, "ramp, this window"
}

// Row prints for a row that needs a person's attention: a control-lost step gets
// the whole block; a breach or an invalid step only the server log's summary.
func (r *diagReporter) Row(row Row) {
	if w, name := rowWindow(row); w.TCPOK || w.TCPErr != "" {
		r.win, r.winTag = w, name
	}
	switch row.Outcome.String() {
	case "control_lost":
		r.events = append(r.events, row.ControlLost...)
		in := r.logs()
		in.Title = fmt.Sprintf("control lost at %s N=%d", row.Kind, row.N)
		in.Status = strings.Join(row.Reasons, "; ")
		if in.Status == "" {
			in.Status = fmt.Sprintf("%d bots lost their control connection", row.ControlLostCount)
		}
		in.Events = row.ControlLost
		in.Window, in.WindowName = rowWindow(row)
		WriteDiagnostics(r.w, in)
	case "breach", "invalid":
		in := r.logs()
		fmt.Fprintf(r.w, "::group::%s diagnostics: %s at %s N=%d: server log WARN/ERROR summary\n", r.tag, row.Outcome, row.Kind, row.N)
		writeSummary(r.w, in.ServerLog, in.ServerLogErr)
		fmt.Fprintln(r.w, "::endgroup::")
	}
}

// Failure prints the full block for a leg that ended in an error.
func (r *diagReporter) Failure(err error) {
	in := r.logs()
	in.Title = "leg failed"
	in.Status = err.Error()
	in.Events = r.events[max(0, len(r.events)-maxEvents):]
	in.Window, in.WindowName = r.win, r.winTag
	WriteDiagnostics(r.w, in)
}

// Heartbeat (S-T7). A ramp, settle or hold can run for minutes, and until now
// the log said nothing in between. Every heartbeatEvery the kit prints one line
// with the processes' CPU over the last period, the bot count and the server's
// accepted HELLOs since the last line.

// heartbeatSample is one reading for a heartbeat line.
type heartbeatSample struct {
	// ServerCPU and GenCPU are the fractions over the last period; NaN = unread.
	ServerCPU, GenCPU float64
	// HelloAccepted is the server log's cumulative "Accepted voice HELLO" count.
	HelloAccepted uint64
	// Dead is the bot's control-lost count.
	Dead int
}

// heartbeat renders the lines of one phase.
type heartbeat struct {
	phase     string
	n         int
	start     time.Time
	prevHello uint64
	now       func() time.Time
	sample    func() heartbeatSample
	emit      func(line string)
}

// beat reads a sample and emits one line.
func (h *heartbeat) beat() {
	s := h.sample()
	delta := uint64(0)
	if s.HelloAccepted > h.prevHello {
		delta = s.HelloAccepted - h.prevHello
	}
	h.prevHello = s.HelloAccepted
	cpu := func(f float64) string {
		if math.IsNaN(f) {
			return dash
		}
		return fmt.Sprintf("%.2f", f)
	}
	h.emit(fmt.Sprintf("heartbeat %s N=%d: elapsed=%.0fs server_cpu=%s gen_cpu=%s alive=%d dead=%d hello_accepted+=%d",
		h.phase, h.n, h.now().Sub(h.start).Seconds(), cpu(s.ServerCPU), cpu(s.GenCPU), max(0, h.n-s.Dead), s.Dead, delta))
}

// runHeartbeat beats on every tick until stop is closed.
func runHeartbeat(stop <-chan struct{}, tick <-chan time.Time, h *heartbeat) {
	for {
		select {
		case <-stop:
			return
		case <-tick:
			h.beat()
		}
	}
}
