package leg

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBot speaks the vcs-loadbot NDJSON protocol: set_n and hold are silent,
// report prints the last hold's step (or a fatal error before any hold), and
// quit ends it. It records every command line it received.
type fakeBot struct {
	mu   sync.Mutex
	cmds []string
	// lossFor returns the loss ratio of a hold at n bots.
	lossFor func(n int) float64
	failSet int // set_n at or above this fails fatally (0 = never)
}

func (f *fakeBot) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

// serve runs the bot over a pair of pipes and returns the kit's ends.
func (f *fakeBot) serve() (toBot io.WriteCloser, fromBot io.Reader, done <-chan struct{}) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	d := make(chan struct{})
	go func() {
		defer close(d)
		defer outW.Close()
		defer inR.Close()
		sc := bufio.NewScanner(inR)
		n := 0
		var last map[string]any
		for sc.Scan() {
			line := sc.Text()
			f.mu.Lock()
			f.cmds = append(f.cmds, line)
			f.mu.Unlock()
			var c map[string]any
			_ = json.Unmarshal([]byte(line), &c)
			switch {
			case c["set_n"] != nil:
				n = int(c["set_n"].(float64))
				if f.failSet > 0 && n >= f.failSet {
					io.WriteString(outW, `{"type":"error","error":"join failed"}`+"\n")
					return
				}
			case c["hold"] != nil:
				loss := 0.0
				if f.lossFor != nil {
					loss = f.lossFor(n)
				}
				last = map[string]any{
					"type": "step", "n": n, "hold_s": c["hold"], "loss_ratio": loss,
					"expected": 1000, "unique": 990, "label": "x",
					"updates": map[string]any{"CLIENT_JOIN": 3},
				}
			case c["report"] != nil:
				if last == nil {
					io.WriteString(outW, `{"type":"error","error":"report: no hold has run yet"}`+"\n")
					return
				}
				rep := map[string]any{}
				for k, v := range last {
					rep[k] = v
				}
				rep["step"] = c["report"]
				b, _ := json.Marshal(rep)
				outW.Write(append(b, '\n'))
			case c["quit"] != nil:
				return
			}
		}
	}()
	return inW, outR, d
}

func TestBotHoldAndSync(t *testing.T) {
	fb := &fakeBot{lossFor: func(n int) float64 { return float64(n) / 1000 }}
	w, r, done := fb.serve()
	b := NewBot(w, r)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Before any hold there is nothing to synchronise on (report would be a
	// fatal error): SetN must be fire-and-forget and send no report.
	if err := b.SetN(ctx, 2); err != nil {
		t.Fatal(err)
	}
	st, err := b.Hold(ctx, 1500*time.Millisecond, "first")
	if err != nil {
		t.Fatal(err)
	}
	if st.Step != "first" || st.N != 2 || st.LossRatio != 0.002 {
		t.Fatalf("step = %+v", st)
	}
	// Afterwards SetN waits for the ramp: it sends a report and blocks on its label.
	if err := b.SetN(ctx, 50); err != nil {
		t.Fatal(err)
	}
	cmds := fb.seen()
	if len(cmds) != 5 || cmds[0] != `{"set_n":2}` || cmds[1] != `{"hold":1.5}` ||
		cmds[2] != `{"report":"first"}` || cmds[3] != `{"set_n":50}` || !strings.HasPrefix(cmds[4], `{"report":"sync-`) {
		t.Fatalf("commands = %q", cmds)
	}
	if err := b.Quit(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestBotErrorLineIsFatal(t *testing.T) {
	fb := &fakeBot{failSet: 10}
	w, r, done := fb.serve()
	b := NewBot(w, r)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.SetN(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Hold(ctx, time.Second, "h"); err != nil {
		t.Fatal(err)
	}
	err := b.SetN(ctx, 10)
	if err == nil || !strings.Contains(err.Error(), "join failed") {
		t.Fatalf("err = %v, want the bot's error text", err)
	}
	<-done
	// The bot is gone: every later call fails fast instead of hanging.
	if _, err := b.Hold(ctx, time.Second, "again"); err == nil {
		t.Fatal("Hold after the bot exited must fail")
	}
}

func TestBotContextCancel(t *testing.T) {
	// A bot that never answers: the call returns when ctx ends.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go io.Copy(io.Discard, inR)
	defer outW.Close()
	b := NewBot(inW, outR)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Hold(ctx, time.Second, "x"); err == nil {
		t.Fatal("want a context error")
	}
}
