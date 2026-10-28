package leg

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

const testTag = "[leg 2 S one-big-net]"

func TestLegTag(t *testing.T) {
	if got := LegTag(2, "S", "one-big-net"); got != testTag {
		t.Fatalf("LegTag = %q, want %q", got, testTag)
	}
}

func TestFormatCalLine(t *testing.T) {
	v2 := CalStep{N: 2048, Bots: 2048, EgressPPS: 0, ExpectedPPS: 102350, Valid: true, HelloTriesMax: 2}
	want := "[leg 2 S one-big-net] calibration N=2048: egress_pps=0 expected_pps=102350 delivered=0% valid=true hello_tries_max=2 violations=none"
	if got := FormatCalLine(testTag, v2); got != want {
		t.Errorf("v2:\n got  %s\n want %s", got, want)
	}
	// A v1 step has no expected_pps: the kit computes 50*(n-1) itself.
	v1 := CalStep{N: 64, EgressPPS: 3150, Valid: false, Violations: []string{"gen cpu", "rx lag"}}
	want = "[leg 2 S one-big-net] calibration N=64: egress_pps=3150 expected_pps=3150 delivered=100% valid=false hello_tries_max=0 violations=gen cpu,rx lag"
	if got := FormatCalLine(testTag, v1); got != want {
		t.Errorf("v1:\n got  %s\n want %s", got, want)
	}
}

func TestFormatCalStopped(t *testing.T) {
	c := &Calibration{
		Steps: make([]CalStep, 6), ReceivePPS: 102350, LowerBound: true,
		StoppedAt: 4096, StoppedBots: 3702, StopReason: "bot 3702: no HELLO_ACK after 5 tries",
	}
	want := "[leg 1 S one-big-net] calibration stopped at N=4096 (3702 bots up): bot 3702: no HELLO_ACK after 5 tries; receive capacity 102350 pps (lower bound true) from 6 completed steps"
	if got := FormatCalStopped("[leg 1 S one-big-net]", c); got != want {
		t.Errorf("v2:\n got  %s\n want %s", got, want)
	}
	c.StoppedBots = -1
	if got := FormatCalStopped("[leg 1 S one-big-net]", c); !strings.Contains(got, "N=4096 (? bots up)") {
		t.Errorf("v1 must show ? for the bots: %s", got)
	}
}

func TestFormatRampProgress(t *testing.T) {
	p := Progress{Phase: "ramp", Requested: 2076, Had: 1038, Done: 512, Joined: 510, JoinFailures: 2, InFlight: 16, HelloRetried: 9, ElapsedS: 5}
	want := "[leg 2 S one-big-net] ramp N=1038->2076: done=512 joined=510 join_failures=2 in_flight=16 hello_retried=9 elapsed=5s"
	if got := FormatRampProgress(testTag, p); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	cp := Progress{Phase: "calibrate_ramp", Target: 4096, Bots: 3000, HelloRetried: 12, ElapsedS: 5}
	want = "[leg 2 S one-big-net] calibration ramp N=4096: bots=3000 hello_retried=12 elapsed=5s"
	if got := FormatRampProgress(testTag, cp); got != want {
		t.Errorf("calibrate_ramp:\n got  %s\n want %s", got, want)
	}
}

func passRow() Row {
	return Row{
		Kind: "sweep", Seq: 1, N: 1038, Outcome: sweep.Pass, Held: true,
		Joins:  Joins{Schema: JoinsSchema, Requested: 1038, N: 1038, Joined: 1038, HelloTriesMax: 2},
		Data:   sweep.StepData{N: 1038, LossRatio: 0, ServerCPU: 0.39, GenCPU: 0.19},
		Window: Window{ServerCPU: 0.39, GenCPU: 0.19, NetOK: true, Seconds: 40},
		Logs:   LogCounts{HelloAccepted: 1036}, FailedAckedByServer: -1,
	}
}

func joinFailRow() Row {
	nan := math.NaN()
	return Row{
		Kind: "sweep", Seq: 2, N: 2076, Outcome: sweep.Breach,
		Reasons: []string{"join failure: 2 of 1038 new bots failed (hello 2); hello tries max 5"},
		Joins: Joins{Schema: JoinsSchema, Requested: 2076, Had: 1038, N: 2074, Joined: 1036, JoinFailures: 2,
			FailuresByStage: map[string]int{"hello": 2}, HelloTriesMax: 5},
		Data:       sweep.StepData{N: 2076, LossRatio: nan, ServerCPU: nan, GenCPU: nan, JoinFailures: 2},
		RampWindow: Window{ServerCPU: 0.97, GenCPU: 0.41, NetOK: true, SoftnetDropped: 120, Seconds: 30},
		RampLogs:   LogCounts{HelloAccepted: 1036, ShardQueueFull: 4}, FailedAckedByServer: 1,
	}
}

func TestFormatRowLine(t *testing.T) {
	want := `[leg 2 S one-big-net] sweep #1 N=1038: outcome=pass joined=1038 join_failures=0 hello_tries_max=2 loss=0.0000 server_cpu=0.39 gen_cpu=0.19 gen_health=ok ramp_server_cpu=- ramp_gen_cpu=- server_drops=0 softnet_dropped=0 hello_accepted=1036 hello_rejected=0 shard_queue_full=0 reasons=""`
	if got := FormatRowLine(testTag, passRow()); got != want {
		t.Errorf("pass:\n got  %s\n want %s", got, want)
	}
	want = `[leg 2 S one-big-net] sweep #2 N=2076: outcome=breach joined=2074 join_failures=2 hello_tries_max=5 loss=- server_cpu=- gen_cpu=- gen_health=- ramp_server_cpu=0.97 ramp_gen_cpu=0.41 server_drops=- softnet_dropped=120 hello_accepted=1036 hello_rejected=0 shard_queue_full=4 failed_acked_by_server=1/2 reasons="join failure: 2 of 1038 new bots failed (hello 2); hello tries max 5"`
	if got := FormatRowLine(testTag, joinFailRow()); got != want {
		t.Errorf("join failure:\n got  %s\n want %s", got, want)
	}
}

func resultsInput() ResultsInput {
	cal := &Calibration{
		Steps: []CalStep{
			{N: 1024, Bots: 1024, EgressPPS: 51150, ExpectedPPS: 51150, Valid: true, HelloTriesMax: 1},
			{N: 2048, Bots: 2048, EgressPPS: 0, ExpectedPPS: 102350, Valid: true, HelloTriesMax: 2},
		},
		ReceivePPS: 51150, LowerBound: true, StoppedAt: 4096, StoppedBots: 3702, StopReason: "bot 3702: no HELLO_ACK after 5 tries",
	}
	return ResultsInput{
		Seed: 2, Arm: "S", Scenario: "one-big-net", Result: "knee 1038", Cal: cal,
		Out:       &Outcome{Rows: []Row{passRow(), joinFailRow()}, Starts: 2},
		Predicted: 1400, NMax: 4000,
	}
}

func TestWriteResultsMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteResultsMarkdown(&buf, resultsInput()); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "results-leg2.md")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout with autocrlf turns the golden into CRLF.
	if strings.ReplaceAll(string(want), "\r\n", "\n") != buf.String() {
		t.Errorf("results.md differs from %s:\n--- got\n%s\n--- want\n%s", golden, buf.String(), want)
	}
}

func TestWriteResultsMarkdownDidNotFinish(t *testing.T) {
	var buf bytes.Buffer
	in := ResultsInput{Seed: 1, Arm: "S", Scenario: "one-big-net", Error: "calibration: no step completed: x | y"}
	if err := WriteResultsMarkdown(&buf, in); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, w := range []string{"### Leg 1 one-big-net (arm S)", "**Result:** did not finish: calibration: no step completed: x | y", "**Calibration:** not completed"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
	if strings.Contains(s, "| kind |") {
		t.Errorf("no rows, so no table:\n%s", s)
	}
}

func TestWriteResultsMarkdownEscapesPipes(t *testing.T) {
	r := passRow()
	r.Reasons = []string{"a | b"}
	var buf bytes.Buffer
	in := resultsInput()
	in.Out = &Outcome{Rows: []Row{r}}
	if err := WriteResultsMarkdown(&buf, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `a \| b`) {
		t.Errorf("reasons pipe not escaped:\n%s", buf.String())
	}
}

func TestDriverCallsOnRowForEveryRow(t *testing.T) {
	w := &fakeWorld{kneeN: 700}
	cfg := testConfig()
	var seen []Row
	var logs []string
	cfg.OnRow = func(r Row) { seen = append(seen, r) }
	cfg.Logf = func(f string, a ...any) { logs = append(logs, f) }
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(out.Rows) || len(seen) == 0 {
		t.Fatalf("OnRow saw %d rows, outcome has %d", len(seen), len(out.Rows))
	}
	for i := range seen {
		if seen[i].Seq != out.Rows[i].Seq {
			t.Errorf("row %d: OnRow Seq %d, outcome Seq %d", i, seen[i].Seq, out.Rows[i].Seq)
		}
	}
	started := 0
	for _, l := range logs {
		if strings.HasPrefix(l, "ramp N=") {
			started++
		}
	}
	if started == 0 {
		t.Errorf("no ramp lines logged: %v", logs)
	}
}

func TestRunWritesResultsMarkdownEvenWhenTheLegFails(t *testing.T) {
	dir := t.TempDir()
	var log bytes.Buffer
	err := Run(context.Background(), Options{
		ServerBin: "x", LoadbotBin: filepath.Join(dir, "no-such-loadbot"), OutDir: dir,
		Scenario: "one-big-net", Arms: "S", Seed: 7, ServerSet: "0", GenSet: "1", Topology: "t", PredictedKnee: 100,
	}, &log)
	if err == nil {
		t.Fatal("a missing load bot must fail the leg")
	}
	b, rerr := os.ReadFile(filepath.Join(dir, "results.md"))
	if rerr != nil {
		t.Fatalf("results.md not written: %v", rerr)
	}
	if !strings.Contains(string(b), "did not finish") || !strings.Contains(string(b), "### Leg 7 one-big-net (arm S)") {
		t.Errorf("results.md:\n%s", b)
	}
	if !strings.Contains(log.String(), "did not finish") {
		t.Errorf("results table not printed to the log:\n%s", log.String())
	}
}
