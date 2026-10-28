// Package leg is the S4 leg driver: it runs one arm of one scenario on one VM
// (calibration, pre-flight, warm-up, sweep, confirm, overload) and writes the
// result files. The control flow is plain Go over small interfaces, so unit
// tests drive it with a fake load bot over NDJSON pipes; only host.go touches
// processes, /proc and ss.
package leg

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// PhaseCounts is a per-phase counter in the bot's report.
type PhaseCounts struct {
	Ramp   uint64 `json:"ramp"`
	Settle uint64 `json:"settle"`
	Hold   uint64 `json:"hold"`
}

// Health is the bot's D14 self-health block (microseconds; null = not available).
type Health struct {
	SocketDrops   uint64   `json:"socket_drops"`
	SendErrors    uint64   `json:"send_errors"`
	RxLagP99Us    *int64   `json:"rx_lag_p99_us"`
	WheelLagP99Us int64    `json:"wheel_lag_p99_us"`
	SchedP99Us    int64    `json:"sched_p99_us"`
	GCPauseP99Us  int64    `json:"gc_pause_p99_us"`
	GenCPU        *float64 `json:"gen_cpu"`
}

// Step is the part of the load bot's step report the kit uses. Unknown keys
// are ignored on purpose: the bot is built from a pinned client SHA and may
// grow fields (the kit then ignores them until it is taught to use them).
type Step struct {
	Type      string `json:"type"`
	Step      string `json:"step"`
	Label     string `json:"label"`
	Payload   string `json:"payload"`
	Arm       string `json:"arm"`
	Scenario  string `json:"scenario"`
	N         int    `json:"n"`
	Talkers   int    `json:"talkers"`
	Listeners int    `json:"listeners"`

	Expected  uint64  `json:"expected"`
	Unique    uint64  `json:"unique"`
	Lost      uint64  `json:"lost"`
	LossRatio float64 `json:"loss_ratio"`
	Dups      uint64  `json:"dups"`
	Reordered uint64  `json:"reordered"`
	BurstMax  int     `json:"burst_max"`

	Resyncs           PhaseCounts       `json:"resyncs"`
	ResourceExhausted PhaseCounts       `json:"resource_exhausted"`
	Updates           map[string]uint64 `json:"updates"`

	JoinP99Us     int64  `json:"join_p99_us"`
	JoinLabel     string `json:"join_label"`
	HelloTriesMax int    `json:"hello_tries_max"`

	Health     Health   `json:"health"`
	Violations []string `json:"violations"`
	Valid      bool     `json:"valid"`
}

type botLine struct {
	step Step
	err  error
}

// Bot drives a running `vcs-loadbot run` over its NDJSON stdin/stdout.
//
// The protocol has no acknowledgement for set_n, and a report before the
// first hold is a fatal error in the bot. So SetN waits for the ramp by
// sending a report after it (the bot executes commands in order, so the
// report line comes back only when the ramp is done), but only once a hold
// has run. Before that, SetN is fire-and-forget and the first Hold absorbs the
// ramp: give that Hold a context that allows for it.
type Bot struct {
	mu   sync.Mutex
	w    io.Writer
	held bool
	seq  int

	lines chan botLine
}

// NewBot starts reading r. The caller owns the process behind w and r.
func NewBot(w io.Writer, r io.Reader) *Bot {
	b := &Bot{w: w, lines: make(chan botLine, 16)}
	go func() {
		defer close(b.lines)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			raw := sc.Bytes()
			if len(raw) == 0 {
				continue
			}
			var head struct {
				Type  string `json:"type"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				b.lines <- botLine{err: fmt.Errorf("loadbot: unreadable output line %q: %w", truncate(string(raw), 120), err)}
				continue
			}
			switch head.Type {
			case "error":
				b.lines <- botLine{err: fmt.Errorf("loadbot: %s", head.Error)}
			case "step":
				var s Step
				if err := json.Unmarshal(raw, &s); err != nil {
					b.lines <- botLine{err: fmt.Errorf("loadbot: bad step line: %w", err)}
					continue
				}
				b.lines <- botLine{step: s}
			}
		}
	}()
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (b *Bot) send(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = b.w.Write(append(raw, '\n')); err != nil {
		return b.writeFailed(err)
	}
	return nil
}

// writeFailed explains a failed write. A bot that hit a fatal error writes its
// error line and exits, so the write often fails before the kit has read that
// line: prefer the bot's own words when they arrive promptly.
func (b *Bot) writeFailed(werr error) error {
	select {
	case l, ok := <-b.lines:
		if ok && l.err != nil {
			return l.err
		}
	case <-time.After(250 * time.Millisecond):
	}
	return fmt.Errorf("loadbot: write command: %w", werr)
}

// wait returns the step line labelled label. Any error line from the bot, the
// bot closing its output, or ctx ending is an error.
func (b *Bot) wait(ctx context.Context, label string) (Step, error) {
	for {
		select {
		case <-ctx.Done():
			return Step{}, ctx.Err()
		case l, ok := <-b.lines:
			if !ok {
				return Step{}, errors.New("loadbot: exited before answering")
			}
			if l.err != nil {
				return Step{}, l.err
			}
			if l.step.Step == label {
				return l.step, nil
			}
		}
	}
}

// SetN grows the bot count to n. See Bot for when it blocks.
func (b *Bot) SetN(ctx context.Context, n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.send(map[string]int{"set_n": n}); err != nil {
		return err
	}
	if !b.held {
		return nil
	}
	b.seq++
	label := fmt.Sprintf("sync-%d", b.seq)
	if err := b.send(map[string]string{"report": label}); err != nil {
		return err
	}
	_, err := b.wait(ctx, label)
	return err
}

// Hold runs one hold of d and returns its report. The report is requested
// right behind the hold, so it arrives when the hold has ended.
func (b *Bot) Hold(ctx context.Context, d time.Duration, label string) (Step, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.send(map[string]float64{"hold": d.Seconds()}); err != nil {
		return Step{}, err
	}
	if err := b.send(map[string]string{"report": label}); err != nil {
		return Step{}, err
	}
	st, err := b.wait(ctx, label)
	if err == nil {
		b.held = true
	}
	return st, err
}

// Quit asks the bot to BYE its clients and exit.
func (b *Bot) Quit() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.send(map[string]bool{"quit": true})
}
