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
	// StderrDir receives reflect.log and calibrate.log; "" discards them.
	StderrDir string
}

func (c CalConfig) command(sub string, args []string, set string) (*exec.Cmd, error) {
	name, argv, env, err := PinCommand(runtime.GOOS, c.LoadbotBin, append([]string{sub}, args...), set)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, argv...)
	cmd.Env = append(os.Environ(), env...)
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

	cal, err := c.command("calibrate", []string{
		"--reflector", addr, "--allow", addr,
		"--seed", strconv.FormatInt(c.Seed, 10),
		"--steps", c.Steps, "--hold", strconv.FormatFloat(c.Hold.Seconds(), 'f', -1, 64),
	}, c.GenSet)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	cal.Stdout = &out
	if f, err := c.stderrFile("calibrate.log"); err != nil {
		return nil, err
	} else if f != nil {
		defer f.Close()
		cal.Stderr = f
	}
	runErr := cal.Run()
	steps, pps, lb, perr := ParseCalibrate(out.Bytes())
	if runErr != nil {
		return nil, fmt.Errorf("vcs-loadbot calibrate: %w (see calibrate.log)", runErr)
	}
	if perr != nil {
		return nil, perr
	}
	res := &Calibration{Steps: steps, ReceivePPS: pps, LowerBound: lb, Senders: senders}

	// Stop the reflector politely so it prints its stats line.
	_, _ = io.WriteString(rin, `{"quit":true}`+"\n")
	_ = rin.Close()
	rest, _ := io.ReadAll(rd)
	if n, err := ParseReflectorStats(rest); err == nil {
		res.ReflectorSendErrors = n
	}
	stopRefl()
	return res, nil
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
