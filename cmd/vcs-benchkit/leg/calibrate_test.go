package leg

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles as a fake vcs-loadbot: the calibration test re-executes
// this test binary with LEG_FAKE_LOADBOT=1 as its LoadbotBin.
func TestMain(m *testing.M) {
	if os.Getenv("LEG_FAKE_LOADBOT") == "1" {
		os.Exit(fakeLoadbot(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func flagVal(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func fakeLoadbot(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "reflect":
		if flagVal(args, "--listen") != "127.0.0.1:0" || flagVal(args, "--senders") != "2" {
			fmt.Fprintln(os.Stderr, "fake reflect: bad flags", args)
			return 2
		}
		fmt.Println(`{"type":"ready","addr":"127.0.0.1:45678"}`)
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"quit"`) {
				break
			}
		}
		if os.Getenv("BENCHKIT_FAKE_REFL") == "v2" {
			b, err := os.ReadFile("testdata/loadbot/v2-reflector-stats.json")
			if err != nil {
				fmt.Fprintln(os.Stderr, "fake reflect: fixture:", err)
				return 2
			}
			os.Stdout.Write(b)
			return 0
		}
		fmt.Println(`{"type":"reflector_stats","send_errors":4,"senders":2}`)
		return 0
	case "calibrate":
		if os.Getenv("BENCHKIT_FAKE_CAL") == "v1-die" {
			// v1 bot at 62f5cf7 (run 40752831443): completed steps on stdout,
			// the reason on stderr, exit 1.
			out, err := os.ReadFile("testdata/loadbot/v1-calibrate-stdout.ndjson")
			if err != nil {
				fmt.Fprintln(os.Stderr, "fake calibrate: fixture:", err)
				return 2
			}
			msg, err := os.ReadFile("testdata/loadbot/v1-calibrate-stderr.txt")
			if err != nil {
				fmt.Fprintln(os.Stderr, "fake calibrate: fixture:", err)
				return 2
			}
			os.Stdout.Write(out)
			os.Stderr.Write(msg)
			return 1
		}
		switch os.Getenv("BENCHKIT_FAKE_CAL") {
		case "v2-stop", "v2-stream":
			b, err := os.ReadFile("testdata/loadbot/v2-calibrate.ndjson")
			if err != nil {
				fmt.Fprintln(os.Stderr, "fake calibrate: fixture:", err)
				return 2
			}
			for _, l := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
				fmt.Printf("%s\n", l)
				if os.Getenv("BENCHKIT_FAKE_CAL") == "v2-stream" {
					time.Sleep(200 * time.Millisecond)
				}
			}
			return 0
		case "huge-line":
			// One line beyond the reader's 4 MiB limit, then more output: the kit
			// must keep draining stdout or this process blocks on a full pipe.
			fmt.Println(`{"type":"calibration","n":64,"egress_pps":3150,"valid":true,"violations":null}`)
			os.Stdout.Write(bytes.Repeat([]byte("x"), 5<<20))
			fmt.Println()
			for i := 0; i < 1<<16; i++ {
				fmt.Println(`{"type":"progress"}`)
			}
			return 0
		case "v1-none":
			fmt.Fprintln(os.Stderr, "vcs-loadbot: calibrate: bot 0: no HELLO_ACK after 5 tries")
			return 1
		case "v2-none":
			fmt.Println(`{"type":"calibration_stopped","n":64,"bots":0,"stage":"hello","bot":0,"tries":5,"reason":"bot 0: no HELLO_ACK after 5 tries"}`)
			return 1
		}
		if flagVal(args, "--reflector") != "127.0.0.1:45678" || flagVal(args, "--allow") != "127.0.0.1:45678" ||
			flagVal(args, "--steps") != "64,128" || flagVal(args, "--hold") != "3" || flagVal(args, "--seed") != "7" {
			fmt.Fprintln(os.Stderr, "fake calibrate: bad flags", args)
			return 2
		}
		fmt.Println(`{"type":"calibration","n":64,"egress_pps":3150,"valid":true,"violations":null}`)
		fmt.Println(`{"type":"calibration","n":128,"egress_pps":6350,"valid":false,"violations":["rx lag"]}`)
		fmt.Println(`{"type":"receive_capacity","pps":3150,"lower_bound":true}`)
		return 0
	}
	return 2
}

func TestRunCalibration(t *testing.T) {
	t.Setenv("LEG_FAKE_LOADBOT", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cal, err := RunCalibration(ctx, CalConfig{
		LoadbotBin: exe, Seed: 7, Steps: "64,128", Hold: 3 * time.Second, Senders: 1, StderrDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Steps) != 2 || cal.ReceivePPS != 3150 || !cal.LowerBound {
		t.Fatalf("cal = %+v", cal)
	}
	// One sender per server CPU, never fewer than two.
	if cal.Senders != 2 {
		t.Errorf("Senders = %d, want 2", cal.Senders)
	}
	if cal.ReflectorSendErrors != 4 {
		t.Errorf("ReflectorSendErrors = %d, want 4 (read from the reflector's last line)", cal.ReflectorSendErrors)
	}
}

func TestRunCalibrationBadFlagsFail(t *testing.T) {
	t.Setenv("LEG_FAKE_LOADBOT", "1")
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A different seed makes the fake calibrate exit 2: the error must name it.
	_, err := RunCalibration(ctx, CalConfig{LoadbotBin: exe, Seed: 8, Steps: "64,128", Hold: 3 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "calibrate") {
		t.Fatalf("err = %v", err)
	}
}

// Run 40752831443, leg 1: calibration died at the 4096 step after three steps
// had completed, and the kit threw all of them away.
func TestRunCalibrationKeepsCompletedStepsWhenCalibrateFails(t *testing.T) {
	t.Setenv("LEG_FAKE_LOADBOT", "1")
	t.Setenv("BENCHKIT_FAKE_CAL", "v1-die")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cal, err := RunCalibration(ctx, CalConfig{
		LoadbotBin: exe, Seed: 7, Steps: "64,1024,2048,4096", Hold: 3 * time.Second, Senders: 1, StderrDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("calibration aborted instead of ending at the last good step: %v", err)
	}
	if len(cal.Steps) != 3 || cal.ReceivePPS != 51150 || !cal.LowerBound {
		t.Fatalf("cal = %+v, want 3 steps, ReceivePPS 51150, LowerBound", cal)
	}
	if cal.StoppedAt != 4096 || cal.StopReason != "inferred: bot 3702: no HELLO_ACK after 5 tries" {
		t.Fatalf("v1 stop must be marked inferred; StoppedAt=%d StopReason=%q", cal.StoppedAt, cal.StopReason)
	}
}

func runFakeCal(t *testing.T, mode string, cfg CalConfig) (*Calibration, error) {
	t.Helper()
	t.Setenv("LEG_FAKE_LOADBOT", "1")
	t.Setenv("BENCHKIT_FAKE_CAL", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg.LoadbotBin, cfg.Seed, cfg.Hold, cfg.Senders = exe, 7, 3*time.Second, 1
	if cfg.Steps == "" {
		cfg.Steps = "64,1024,2048,4096"
	}
	if cfg.StderrDir == "" {
		cfg.StderrDir = t.TempDir()
	}
	return RunCalibration(ctx, cfg)
}

func TestRunCalibrationV2Stopped(t *testing.T) {
	cal, err := runFakeCal(t, "v2-stop", CalConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if cal.StoppedAt != 4096 || cal.StoppedBots != 3702 || cal.StopReason != "bot 3702: no HELLO_ACK after 5 tries" ||
		cal.ReceivePPS != 51150 || !cal.LowerBound || len(cal.Steps) != 3 {
		t.Fatalf("cal = %+v", cal)
	}
}

func TestRunCalibrationNoStepIsFatal(t *testing.T) {
	for _, mode := range []string{"v2-none", "v1-none"} {
		_, err := runFakeCal(t, mode, CalConfig{})
		const want = "no step completed: bot 0: no HELLO_ACK after 5 tries"
		if err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v, want exactly %q", mode, err, want)
		}
	}
}

func TestRunCalibrationStreamsProgress(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	var kinds []string
	_, err := runFakeCal(t, "v2-stream", CalConfig{OnLine: func(kind string, raw []byte) {
		mu.Lock()
		defer mu.Unlock()
		times = append(times, time.Now())
		kinds = append(kinds, kind)
		if len(raw) == 0 || raw[len(raw)-1] == '\n' {
			t.Errorf("raw line %q must be one line without the newline", raw)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(times) < 7 {
		t.Fatalf("OnLine got %d lines (%v), want all 7", len(times), kinds)
	}
	if d := times[len(times)-1].Sub(times[0]); d < 150*time.Millisecond {
		t.Fatalf("first-to-last line gap %s: lines were delivered at the end, not streamed", d)
	}
	if kinds[1] != "calibration" || kinds[0] != "progress" {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestRunCalibrationReadsSplitReflectorStats(t *testing.T) {
	v1, err := runFakeCal(t, "v2-stop", CalConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if v1.ReflectorSendErrors != 4 || v1.ReflectorQueueFull != nil || v1.ReflectorWriteErrors != nil {
		t.Fatalf("v1 stats: %+v", v1)
	}
	t.Setenv("BENCHKIT_FAKE_REFL", "v2")
	v2, err := runFakeCal(t, "v2-stop", CalConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if v2.ReflectorSendErrors != 3364676 || v2.ReflectorQueueFull == nil || *v2.ReflectorQueueFull != 3364000 ||
		v2.ReflectorWriteErrors == nil || *v2.ReflectorWriteErrors != 676 {
		t.Fatalf("v2 stats: %+v", v2)
	}
}

func TestParseCalibrateTolerant(t *testing.T) {
	out := []byte(`{"type":"calibration","n":64,"egress_pps":3150,"valid":true}
{"type":"calibration","n":128,"egress_pps":9000,"valid":false}
{"type":"calibration","n":256,"egress_pps":6000,"valid":true}
`)
	steps, pps, lb, err := ParseCalibrate(out)
	if err != nil || len(steps) != 3 || pps != 6000 || !lb {
		t.Fatalf("steps=%d pps=%v lb=%v err=%v", len(steps), pps, lb, err)
	}
	if _, _, _, err := ParseCalibrate([]byte(`{"type":"calibration_stopped","n":64}`)); err == nil {
		t.Fatal("no completed step must be an error")
	}
}

// A stdout line over the reader's limit is an error that names the cause, and
// the kit keeps draining the pipe so calibrate can exit instead of hanging on
// a full pipe until the leg's context ends.
func TestRunCalibrationOversizeLineFailsFast(t *testing.T) {
	start := time.Now()
	_, err := runFakeCal(t, "huge-line", CalConfig{})
	if err == nil || !strings.Contains(err.Error(), "calibrate output") {
		t.Fatalf("err = %v, want a calibrate output read error", err)
	}
	if el := time.Since(start); el > 15*time.Second {
		t.Fatalf("took %v: the kit stopped draining calibrate's stdout", el)
	}
}
