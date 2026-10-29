package leg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CalConfig drives the calibration of one leg (design 6.3): the reflector on
// the server CPU set, the calibrate driver on the generator CPU set.
type CalConfig struct {
	LoadbotBin string
	ServerSet  string // reflector CPU set; "" runs unpinned
	GenSet     string // calibrate CPU set; "" runs unpinned
	Seed       int64
	Steps      string // ascending bot counts, "64,128,..."
	Hold       time.Duration
	// Senders is the reflector's send goroutines on its one socket: one per
	// CPU of the server set, and at least 2.
	Senders int
	// Features is the load bot's probe result. Version 0 (not probed) passes no
	// --bounds, so the bot's own defaults apply.
	Features Features
	// Bot holds the tunable bot flags and the grpc log level; nil = the defaults.
	Bot *BotParams
	// StderrDir receives reflect.log and calibrate.log; "" discards them.
	StderrDir string
	// OnLine, when set, receives each stdout line of calibrate as it is read: its
	// "type" and the raw line without the newline.
	OnLine func(kind string, raw []byte)
}

func (c CalConfig) command(sub string, args []string, set string) (*exec.Cmd, error) {
	name, argv, pinEnv, err := PinCommand(runtime.GOOS, c.LoadbotBin, append([]string{sub}, args...), set)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, argv...)
	cmd.Env = processEnvWith(os.Environ(), pinEnv, sub, botParamsOr(c.Bot).GRPCLog)
	return cmd, nil
}

func (c CalConfig) stderrFile(name string) (*os.File, error) {
	if c.StderrDir == "" {
		return nil, nil
	}
	return os.Create(c.StderrDir + string(os.PathSeparator) + name)
}

// RunCalibration starts the reflector, runs calibrate against it and reads the
// reflector's send-error count. The reflector is always stopped.
func RunCalibration(ctx context.Context, c CalConfig) (*Calibration, error) {
	senders := max(2, c.Senders)

	refl, err := c.command("reflect", []string{"--listen", "127.0.0.1:0", "--senders", strconv.Itoa(senders)}, c.ServerSet)
	if err != nil {
		return nil, err
	}
	rin, err := refl.StdinPipe()
	if err != nil {
		return nil, err
	}
	rout, err := refl.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if f, err := c.stderrFile("reflect.log"); err != nil {
		return nil, err
	} else if f != nil {
		defer f.Close()
		refl.Stderr = f
	}
	if err := refl.Start(); err != nil {
		return nil, fmt.Errorf("start reflector: %w", err)
	}
	reaped := false
	stopRefl := func() {
		if reaped {
			return
		}
		reaped = true
		_ = refl.Process.Kill()
		_ = refl.Wait()
	}
	defer stopRefl()

	rd := bufio.NewReader(rout)
	addr, err := readReady(ctx, rd, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("reflector: %w", err)
	}

	calArgs := calibrateArgs(c, addr)
	cal, err := c.command("calibrate", calArgs, c.GenSet)
	if err != nil {
		return nil, err
	}
	// Stdout is read line by line so progress is visible while calibration runs.
	calOut, err := cal.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// The stderr tail is kept even without a log file: it is the stop reason of
	// a v1 bot, which says why it died only there.
	tail := &tailWriter{max: 4096}
	cal.Stderr = tail
	if f, err := c.stderrFile("calibrate.log"); err != nil {
		return nil, err
	} else if f != nil {
		defer f.Close()
		cal.Stderr = io.MultiWriter(f, tail)
	}
	if err := cal.Start(); err != nil {
		return nil, fmt.Errorf("start calibrate: %w", err)
	}
	killed := make(chan struct{})
	defer close(killed)
	go func() {
		select {
		case <-ctx.Done():
			_ = cal.Process.Kill()
		case <-killed:
		}
	}()
	var out bytes.Buffer
	sc := bufio.NewScanner(calOut)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		out.Write(line)
		out.WriteByte('\n')
		if c.OnLine != nil {
			c.OnLine(lineType(line), append([]byte(nil), line...))
		}
	}
	// A read error (a line over the limit) stops the scanner, not calibrate:
	// keep draining so it can exit instead of blocking on a full pipe.
	scanErr := sc.Err()
	_, _ = io.Copy(io.Discard, calOut)
	runErr := cal.Wait()

	// The reflector is always stopped politely, also after a calibrate
	// failure, so its drop counters are read either way.
	_, _ = io.WriteString(rin, `{"quit":true}`+"\n")
	_ = rin.Close()
	rest, _ := io.ReadAll(rd)
	stopRefl()
	rstats, rerr := ParseReflectorStatsFull(rest)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scanErr != nil {
		return nil, fmt.Errorf("read calibrate output: %w", scanErr)
	}
	steps, pps, lb, haveCap, stop, perr := parseCalibrateLines(out.Bytes())
	if perr != nil {
		return nil, perr
	}
	reason := ""
	if stop != nil {
		reason = stop.Reason
	}
	if reason == "" {
		reason = stderrReason(tail.String())
	}
	if len(steps) == 0 {
		if reason == "" && runErr != nil {
			reason = runErr.Error()
		}
		if reason == "" {
			reason = "calibrate printed no calibration step"
		}
		return nil, fmt.Errorf("no step completed: %s", reason)
	}
	if !haveCap {
		// The bot died before its receive_capacity line (a v1 bot): the largest
		// valid completed step, as a lower bound.
		pps, lb = 0, true
		for _, s := range steps {
			if s.Valid && s.EgressPPS > pps {
				pps = s.EgressPPS
			}
		}
	}
	res := &Calibration{Steps: steps, ReceivePPS: pps, LowerBound: lb, Senders: senders, StoppedBots: -1}
	if rerr == nil {
		res.ReflectorSendErrors = rstats.SendErrors
		res.ReflectorQueueFull, res.ReflectorWriteErrors = rstats.QueueFullDrops, rstats.WriteErrors
	}
	// The reflector dropping destinations, or delivery giving out, means the
	// figure is what the rig could push, not what the server can receive: OR
	// both into the bot's own flag and say why.
	var why []string
	if res.ReflectorSendErrors > 0 {
		why = append(why, fmt.Sprintf("reflector dropped %d destinations", res.ReflectorSendErrors))
	}
	if stop != nil && stop.Stage == "delivery" {
		why = append(why, fmt.Sprintf("delivery stop at N=%d", stop.N))
	}
	if len(why) > 0 {
		res.LowerBound = true
		res.LowerBoundReason = strings.Join(why, "; ")
	}
	switch {
	case stop != nil:
		res.StoppedAt, res.StoppedBots, res.StopReason = stop.N, stop.Bots, reason
	case runErr != nil:
		res.StoppedAt = nextStepAbove(c.Steps, steps[len(steps)-1].N)
		if reason == "" {
			reason = runErr.Error()
		}
		// A v1 bot never says which step failed or why it exited: both are
		// the kit's inference, so the text must not read as measured.
		res.StopReason = "inferred: " + reason
	}
	return res, nil
}

// lineType returns the "type" of one NDJSON line, "" when it has none.
func lineType(line []byte) string {
	var h struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(line, &h)
	return h.Type
}

// stderrReason is the last non-empty line of a v1 bot's stderr with its
// "vcs-loadbot: calibrate: " prefix removed.
func stderrReason(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		l = strings.TrimPrefix(l, "vcs-loadbot: calibrate: ")
		return strings.TrimPrefix(l, "vcs-loadbot: ")
	}
	return ""
}

// nextStepAbove is the first value of the ascending --steps list above n, 0
// when there is none.
func nextStepAbove(steps string, n int) int {
	for _, f := range strings.Split(steps, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && v > n {
			return v
		}
	}
	return 0
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// readReady waits for the reflector's first line, {"type":"ready","addr":...}.
func readReady(ctx context.Context, rd *bufio.Reader, timeout time.Duration) (string, error) {
	type result struct {
		addr string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			ch <- result{err: fmt.Errorf("no ready line: %w", err)}
			return
		}
		var r struct {
			Type string `json:"type"`
			Addr string `json:"addr"`
		}
		if err := json.Unmarshal(line, &r); err != nil || r.Type != "ready" || r.Addr == "" {
			ch <- result{err: fmt.Errorf("unexpected first line %q", bytes.TrimSpace(line))}
			return
		}
		ch <- result{addr: r.Addr}
	}()
	select {
	case r := <-ch:
		return r.addr, r.err
	case <-time.After(timeout):
		return "", fmt.Errorf("not ready after %s", timeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// calibrateArgs is the argv of `vcs-loadbot calibrate`. Any probed bot (v2 or
// v3) gets --bounds, as both define it on calibrate; an unprobed one (Version 0)
// gets none, so its own defaults apply.
func calibrateArgs(c CalConfig, addr string) []string {
	a := []string{
		"--reflector", addr, "--allow", addr,
		"--seed", strconv.FormatInt(c.Seed, 10),
		"--steps", c.Steps, "--hold", strconv.FormatFloat(c.Hold.Seconds(), 'f', -1, 64),
	}
	if c.Features.Version != 0 {
		a = append(a, "--bounds", BotBoundsJSONWith(c.Features, botParamsOr(c.Bot)))
	}
	return a
}
