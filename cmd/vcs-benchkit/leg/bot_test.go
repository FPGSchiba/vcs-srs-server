package leg

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	// mode shapes what a failing set_n does: "" is the fatal "join failed"
	// error, "v2" writes the v2-joins.json fixture and carries on, "v1"
	// writes the v1 join-failure error line and exits, "nonjoin" writes a
	// fatal error that is not a join failure.
	mode string
	// progressOnSet writes the v2-progress.ndjson lines on every set_n.
	progressOnSet bool
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
				if f.progressOnSet {
					outW.Write(fixtureBytes("v2-progress.ndjson"))
				}
				if f.failSet > 0 && n >= f.failSet {
					switch f.mode {
					case "v2":
						outW.Write(fixtureBytes("v2-joins.json"))
					case "v1":
						outW.Write(fixtureBytes("v1-run-joinfail.ndjson"))
						return
					case "nonjoin":
						io.WriteString(outW, `{"type":"error","error":"set_n 3: decreasing N is not supported (have 4)"}`+"\n")
						return
					default:
						io.WriteString(outW, `{"type":"error","error":"join failed"}`+"\n")
						return
					}
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
	b := NewBot(w, r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Before any hold there is nothing to synchronise on (report would be a
	// fatal error): SetN must be fire-and-forget and send no report.
	if _, err := b.SetN(ctx, 2); err != nil {
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
	if _, err := b.SetN(ctx, 50); err != nil {
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
	b := NewBot(w, r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.SetN(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Hold(ctx, time.Second, "h"); err != nil {
		t.Fatal(err)
	}
	_, err := b.SetN(ctx, 10)
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
	b := NewBot(inW, outR, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Hold(ctx, time.Second, "x"); err == nil {
		t.Fatal("want a context error")
	}
}

// fixtureBytes reads a testdata/loadbot file for use off the test goroutine.
func fixtureBytes(name string) []byte {
	b, err := os.ReadFile(filepath.Join("testdata", "loadbot", name))
	if err != nil {
		panic(err)
	}
	return b
}

// primeBot returns a bot already past its first hold, so SetN synchronises.
func primeBot(t *testing.T, fb *fakeBot) (*Bot, context.Context, <-chan struct{}) {
	t.Helper()
	w, r, done := fb.serve()
	b := NewBot(w, r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if _, err := b.SetN(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Hold(ctx, time.Second, "h"); err != nil {
		t.Fatal(err)
	}
	return b, ctx, done
}

func TestBotSetNReturnsJoinsV2(t *testing.T) {
	b, ctx, _ := primeBot(t, &fakeBot{failSet: 10, mode: "v2"})
	j, err := b.SetN(ctx, 2076)
	if err != nil {
		t.Fatalf("err = %v, want a join report", err)
	}
	var want Joins
	if err := json.Unmarshal(fixture(t, "v2-joins.json"), &want); err != nil {
		t.Fatal(err)
	}
	want.Source = JoinsSourceV2
	if !reflect.DeepEqual(j, want) {
		t.Fatalf("joins = %+v\nwant    %+v", j, want)
	}
	// The bot is alive: the next hold works.
	if _, err := b.Hold(ctx, time.Second, "again"); err != nil {
		t.Fatalf("hold after a v2 join failure: %v", err)
	}
}

func TestBotSetNTurnsV1JoinFailureIntoJoins(t *testing.T) {
	b, ctx, done := primeBot(t, &fakeBot{failSet: 10, mode: "v1"})
	j, err := b.SetN(ctx, 2076)
	if err != nil {
		t.Fatalf("err = %v, want data", err)
	}
	if j.Source != JoinsSourceV1Error || !j.BotExited || j.JoinFailures != 1 ||
		j.FailuresByStage["hello"] != 1 || j.HelloTriesMax != 5 ||
		len(j.Failed) != 1 || j.Failed[0].Bot != 1669 || j.Failed[0].Stage != "hello" || j.Failed[0].Tries != 5 {
		t.Fatalf("joins = %+v", j)
	}
	if j.Requested != 2076 || j.Had != 2 || j.N != 2 {
		t.Fatalf("counts requested/had/n = %d/%d/%d, want 2076/2/2", j.Requested, j.Had, j.N)
	}
	<-done
	if _, err := b.Hold(ctx, time.Second, "later"); err == nil {
		t.Fatal("Hold after the v1 bot exited must fail")
	}
}

func TestBotSetNNonJoinErrorStaysFatal(t *testing.T) {
	b, ctx, _ := primeBot(t, &fakeBot{failSet: 10, mode: "nonjoin"})
	_, err := b.SetN(ctx, 10)
	if err == nil || !strings.Contains(err.Error(), "decreasing N") {
		t.Fatalf("err = %v, want the bot's fatal text", err)
	}
}

func TestBotRelaysProgress(t *testing.T) {
	fb := &fakeBot{progressOnSet: true}
	w, r, _ := fb.serve()
	var mu sync.Mutex
	var got []Progress
	b := NewBot(w, r, func(p Progress) {
		mu.Lock()
		got = append(got, p)
		first := len(got) == 1
		mu.Unlock()
		// A panicking callback must not kill the reader.
		if first {
			panic("callback bug")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.SetN(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Hold(ctx, time.Second, "h"); err != nil {
		t.Fatalf("hold after a panicking progress callback: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Phase != "ramp" || got[0].Requested != 2076 || got[1].Phase != "calibrate_ramp" || got[1].Target != 4096 {
		t.Fatalf("progress = %+v", got)
	}
}

func TestBotLastJoinsBeforeFirstHold(t *testing.T) {
	fb := &fakeBot{failSet: 1, mode: "v2"}
	w, r, _ := fb.serve()
	b := NewBot(w, r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, err := b.SetN(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if j.Source != JoinsSourcePending {
		t.Fatalf("SetN before the first hold: source = %q, want %q", j.Source, JoinsSourcePending)
	}
	if _, ok := b.LastJoins(); ok {
		t.Fatal("LastJoins before any joins line must report none")
	}
	if _, err := b.Hold(ctx, time.Second, "first"); err != nil {
		t.Fatal(err)
	}
	lj, ok := b.LastJoins()
	if !ok || lj.Source != JoinsSourceV2 || lj.Requested != 2076 || lj.JoinFailures != 2 {
		t.Fatalf("LastJoins = %+v, %v", lj, ok)
	}
}

func TestBotSetNWithoutJoinsLineIsNone(t *testing.T) {
	b, ctx, _ := primeBot(t, &fakeBot{})
	j, err := b.SetN(ctx, 50)
	if err != nil || j.Source != JoinsSourceNone {
		t.Fatalf("joins = %+v, err = %v", j, err)
	}
}
