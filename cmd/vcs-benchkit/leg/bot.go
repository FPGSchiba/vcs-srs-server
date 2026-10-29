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
	GCCycles      *uint64  `json:"gc_cycles"` // v3; nil = a v2 bot that does not report it
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

	// v3 additions (I-11); absent from a v2 step.
	ControlLost StepControlLost `json:"control_lost"`
	Ping        StepPing        `json:"ping"`
	Warnings    []string        `json:"warnings"`
}

type botLine struct {
	step Step
	err  error
	// joins is a decoded v2 `joins` line.
	joins *Joins
	// joinErr is a v1 error line whose text is a join failure (data, not a
	// fatal bot error).
	joinErr string
	// lost is a v2 hold death (an error line IsControlLostText matches): data,
	// returned by wait as a *ControlLostError.
	lost *ControlLostEvent
}

// ControlLostError is a dead bot as an error value, for the calls that cannot
// return it any other way (Hold and Burst). The event is data: callers
// type-assert it and record it instead of aborting the leg.
type ControlLostError struct{ Event ControlLostEvent }

func (e *ControlLostError) Error() string {
	ev := e.Event
	return fmt.Sprintf("loadbot: control lost: bot %d %s: %s", ev.Bot, ev.Code, ev.Message)
}

// ErrNotSupported is returned (wrapped) for a command the load bot's feature
// list does not include. Nothing is written to the bot in that case.
var ErrNotSupported = errors.New("loadbot: command not supported by this load bot")

// BotHooks are the optional callbacks and facts NewBot takes.
type BotHooks struct {
	// OnProgress receives every `progress` line synchronously on the reader
	// goroutine, so it must be quick; a panic in it is recovered. nil = no relay.
	OnProgress func(Progress)
	// OnControlLost receives every control-lost event (a v3 `control_lost` line
	// or a v2 hold death) synchronously on the reader goroutine, after it is
	// stored. A panic in it is recovered. nil = no relay.
	OnControlLost func(ControlLostEvent)
	// Features is what `vcs-loadbot features` reported; it gates Burst. The
	// zero value lists nothing.
	Features Features
}

// joinFailureError carries a join-failure text out of wait. It is an error to
// every caller except SetN, which turns it into Joins.
type joinFailureError struct{ text string }

func (e *joinFailureError) Error() string { return e.text }

// Bot drives a running `vcs-loadbot run` over its NDJSON stdin/stdout.
//
// The protocol has no acknowledgement for set_n, and a report before the
// first hold is a fatal error in the bot. So SetN waits for the ramp by
// sending a report after it (the bot executes commands in order, so the
// report line comes back only when the ramp is done), but only once a hold
// has run. Before that, SetN is fire-and-forget and the first Hold absorbs the
// ramp: give that Hold a context that allows for it.
//
// A join failure is data, not an abort: a v2 bot writes a `joins` line and
// carries on, and a v1 bot's join-failure error line (it then exits) is turned
// into the same Joins by SetN.
type Bot struct {
	mu   sync.Mutex
	w    io.Writer
	held bool
	seq  int
	n    int // bots alive as far as the kit knows (the next SetN's "had")

	lines chan botLine

	jmu       sync.Mutex
	lastJoins *Joins

	lmu  sync.Mutex
	lost []ControlLostEvent

	features Features
}

// NewBot starts reading r. The caller owns the process behind w and r.
// hooks.OnProgress and hooks.OnControlLost run synchronously on the reader
// goroutine, so they must be quick; a panic in either is recovered.
func NewBot(w io.Writer, r io.Reader, hooks BotHooks) *Bot {
	b := &Bot{w: w, lines: make(chan botLine, 16), features: hooks.Features}
	onProgress := hooks.OnProgress
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
				text := "loadbot: " + head.Error
				if IsJoinFailureText(text) {
					b.lines <- botLine{joinErr: text}
				} else if IsControlLostText(text) {
					ev := ControlLostFromV2Error(text, "hold")
					b.recordLost(ev, hooks.OnControlLost)
					b.lines <- botLine{lost: &ev}
				} else {
					b.lines <- botLine{err: errors.New(text)}
				}
			case "step":
				var s Step
				if err := json.Unmarshal(raw, &s); err != nil {
					b.lines <- botLine{err: fmt.Errorf("loadbot: bad step line: %w", err)}
					continue
				}
				b.lines <- botLine{step: s}
			case "joins":
				var j Joins
				if err := json.Unmarshal(raw, &j); err != nil {
					b.lines <- botLine{err: fmt.Errorf("loadbot: bad joins line: %w", err)}
					continue
				}
				j.Source = JoinsSourceV2
				b.setLastJoins(j)
				b.lines <- botLine{joins: &j}
			case StepControlLostType:
				var ev ControlLostEvent
				if err := json.Unmarshal(raw, &ev); err != nil {
					continue // informational on the wire: the step line carries the counts
				}
				b.recordLost(ev, hooks.OnControlLost)
			case "progress":
				if onProgress == nil {
					continue
				}
				var p Progress
				if err := json.Unmarshal(raw, &p); err != nil {
					continue // informational: an unreadable one is not worth a failure
				}
				relayProgress(onProgress, p)
			}
		}
	}()
	return b
}

// relayProgress calls fn and swallows a panic: a logging bug must not kill the
// reader, which would look like the bot exiting.
func relayProgress(fn func(Progress), p Progress) {
	defer func() { _ = recover() }()
	fn(p)
}

// recordLost stores ev, then relays it to fn (nil = none), recovering a panic.
func (b *Bot) recordLost(ev ControlLostEvent, fn func(ControlLostEvent)) {
	b.lmu.Lock()
	b.lost = append(b.lost, ev)
	b.lmu.Unlock()
	if fn == nil {
		return
	}
	defer func() { _ = recover() }()
	fn(ev)
}

// ControlLost returns every control-lost event read so far, in order. A v2
// hold death is among them, with BotExited set.
func (b *Bot) ControlLost() []ControlLostEvent {
	b.lmu.Lock()
	defer b.lmu.Unlock()
	return append([]ControlLostEvent(nil), b.lost...)
}

// DeadCount is the number of control-lost events read so far.
func (b *Bot) DeadCount() int {
	b.lmu.Lock()
	defer b.lmu.Unlock()
	return len(b.lost)
}

func (b *Bot) setLastJoins(j Joins) {
	b.jmu.Lock()
	b.lastJoins = &j
	b.jmu.Unlock()
}

// LastJoins returns the most recent `joins` line read from the bot. SetN
// before the first hold is fire-and-forget, so the line for that ramp arrives
// during the first Hold and is read here afterwards.
func (b *Bot) LastJoins() (Joins, bool) {
	b.jmu.Lock()
	defer b.jmu.Unlock()
	if b.lastJoins == nil {
		return Joins{}, false
	}
	return *b.lastJoins, true
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
		if ok && l.joinErr != "" {
			return &joinFailureError{text: l.joinErr}
		}
		if ok && l.lost != nil {
			return &ControlLostError{Event: *l.lost}
		}
		if !ok {
			return fmt.Errorf("loadbot: exited (write command: %w)", werr)
		}
	case <-time.After(250 * time.Millisecond):
	}
	return fmt.Errorf("loadbot: write command: %w", werr)
}

// wait returns the step line labelled label. Any error line from the bot, the
// bot closing its output, or ctx ending is an error. A join-failure error line
// is a *joinFailureError. The v2 joins line seen on the way is returned too.
func (b *Bot) wait(ctx context.Context, label string) (Step, *Joins, error) {
	var joins *Joins
	for {
		select {
		case <-ctx.Done():
			return Step{}, joins, ctx.Err()
		case l, ok := <-b.lines:
			if !ok {
				return Step{}, joins, errors.New("loadbot: exited before answering")
			}
			if l.err != nil {
				return Step{}, joins, l.err
			}
			if l.joinErr != "" {
				return Step{}, joins, &joinFailureError{text: l.joinErr}
			}
			if l.lost != nil {
				return Step{}, joins, &ControlLostError{Event: *l.lost}
			}
			if l.joins != nil {
				joins = l.joins
				continue
			}
			if l.step.Step == label {
				return l.step, joins, nil
			}
		}
	}
}

// SetN grows the bot count to n. See Bot for when it blocks.
//
// A join failure is returned as Joins with a nil error. After the sync report
// the result is the v2 `joins` line, or, for a v1 bot, the join failure
// recovered from its error line (BotExited is then set), or Source "none".
// Before the first hold there is nothing to wait for and the result is
// Source "pending". Any other bot error, a closed bot and a cancelled ctx stay
// errors.
func (b *Bot) SetN(ctx context.Context, n int) (Joins, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	had := b.n
	asJoins := func(err error) (Joins, bool) {
		var je *joinFailureError
		if errors.As(err, &je) {
			return JoinsFromV1Error(n, had, je.text), true
		}
		return Joins{}, false
	}
	if err := b.send(map[string]int{"set_n": n}); err != nil {
		if j, ok := asJoins(err); ok {
			return j, nil
		}
		return Joins{}, err
	}
	if !b.held {
		b.n = n
		return Joins{Source: JoinsSourcePending, Requested: n, Had: had}, nil
	}
	b.seq++
	label := fmt.Sprintf("sync-%d", b.seq)
	if err := b.send(map[string]string{"report": label}); err != nil {
		if j, ok := asJoins(err); ok {
			return j, nil
		}
		return Joins{}, err
	}
	_, joins, err := b.wait(ctx, label)
	if err != nil {
		if j, ok := asJoins(err); ok {
			return j, nil
		}
		return Joins{}, err
	}
	if joins != nil {
		b.n = joins.N
		return *joins, nil
	}
	b.n = n
	return Joins{Source: JoinsSourceNone, Requested: n, Had: had, N: n, Joined: n - had}, nil
}

// Hold runs one hold of d and returns its report. The report is requested
// right behind the hold, so it arrives when the hold has ended. The first hold
// absorbs the first ramp: a v1 join-failure error line seen here is an error,
// while a v2 joins line is only stored for LastJoins (the caller checks it).
func (b *Bot) Hold(ctx context.Context, d time.Duration, label string) (Step, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.send(map[string]float64{"hold": d.Seconds()}); err != nil {
		return Step{}, err
	}
	if err := b.send(map[string]string{"report": label}); err != nil {
		return Step{}, err
	}
	st, _, err := b.wait(ctx, label)
	if err == nil {
		b.held = true
	}
	return st, err
}

// Burst adds add bots with no rate limit at inFlight concurrency, with the
// talker silent or live during it (I-13), and returns the burst's `joins` line
// once the bot has finished. It needs the `burst` feature: without it the call
// returns ErrNotSupported and writes nothing. Join failures are data in the
// result; a dead bot is a *ControlLostError. The first hold must have run (the
// bot needs bots to add to, and the sync report needs a hold).
func (b *Bot) Burst(ctx context.Context, add, inFlight int, talker string) (Joins, error) {
	if !b.features.Has("burst") {
		return Joins{}, fmt.Errorf("%w: burst", ErrNotSupported)
	}
	if add < 1 || inFlight < 1 || inFlight > 16 || (talker != "silent" && talker != "live") {
		return Joins{}, fmt.Errorf("loadbot: burst: invalid arguments add=%d in_flight=%d talker=%q", add, inFlight, talker)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.held {
		return Joins{}, errors.New("loadbot: burst before the first hold")
	}
	had := b.n
	asJoins := func(err error) (Joins, bool) {
		var je *joinFailureError
		if errors.As(err, &je) {
			return JoinsFromV1Error(had+add, had, je.text), true
		}
		return Joins{}, false
	}
	cmd := map[string]any{"burst": map[string]any{"add": add, "in_flight": inFlight, "talker": talker}}
	if err := b.send(cmd); err != nil {
		if j, ok := asJoins(err); ok {
			return j, nil
		}
		return Joins{}, err
	}
	b.seq++
	label := fmt.Sprintf("sync-%d", b.seq)
	if err := b.send(map[string]string{"report": label}); err != nil {
		if j, ok := asJoins(err); ok {
			return j, nil
		}
		return Joins{}, err
	}
	_, joins, err := b.wait(ctx, label)
	if err != nil {
		if j, ok := asJoins(err); ok {
			return j, nil
		}
		return Joins{}, err
	}
	if joins == nil {
		return Joins{Source: JoinsSourceNone, Requested: had + add, Had: had, N: had + add, Joined: add}, nil
	}
	b.n = joins.N
	return *joins, nil
}

// Quit asks the bot to BYE its clients and exit.
func (b *Bot) Quit() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.send(map[string]bool{"quit": true})
}
