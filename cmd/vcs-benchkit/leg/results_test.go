package leg

import (
	"bytes"
	"encoding/csv"
	"math"
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// The first 42 columns of steps.csv are a published contract: downstream
// readers index them by position, so W6 only ever appends.
var stepsCSVFirst42 = []string{
	"seq", "kind", "n", "outcome", "reasons", "held", "settle_s",
	"loss_ratio", "late_loss_ratio", "one_way_p99_ms", "expected", "unique", "lost", "dups", "reordered", "burst_max",
	"rex_ramp", "rex_settle", "rex_hold", "join_p99_us", "hello_tries_max",
	"server_cpu", "server_drops", "softirq_ticks_server_set", "steal_ticks", "sample_s",
	"shard_queue_full", "update_stream_behind", "node_feed_behind", "node_deregistered",
	"gen_cpu", "gen_socket_drops", "gen_rx_lag_p99_us", "gen_wheel_lag_p99_us", "gen_sched_p99_us", "gen_gc_p99_us", "gen_violations",
	"topology", "label", "payload", "join_label", "not_measured",
}

var stepsCSVAppended = []string{
	"requested", "joined", "join_failures", "join_failures_by_stage", "hello_tries_hist", "failed_acked_by_server",
	"ramp_s", "ramp_server_cpu", "ramp_gen_cpu", "ramp_server_drops", "ramp_softnet_dropped", "ramp_udp_rcvbuf_errors",
	"ramp_hello_accepted", "ramp_hello_rejected", "ramp_shard_queue_full",
	"softnet_dropped", "udp_rcvbuf_errors", "udp_sndbuf_errors",
}

func TestWriteStepsCSVAppendsJoinColumns(t *testing.T) {
	nan := math.NaN()
	joinFail := Row{
		Kind: "sweep", Seq: 1, N: 2076, Outcome: sweep.Breach, Reasons: []string{"join failure: x"}, FailedAckedByServer: 1,
		Data: sweep.StepData{LossRatio: nan, ServerCPU: nan, GenCPU: nan},
		Joins: Joins{
			Schema: JoinsSchema, Requested: 2076, Had: 1038, N: 2074, Joined: 1036, JoinFailures: 2,
			FailuresByStage: map[string]int{"control": 0, "radio": 0, "voice_dial": 0, "hello": 2},
			HelloTriesHist:  []int{0, 1006, 25, 4, 1, 0}, HelloTriesMax: 5, RampS: 38.4,
		},
		RampWindow: Window{ServerCPU: 0.8, GenCPU: 0.4, ServerDrops: 7, Seconds: 38, NetOK: true, SoftnetDropped: 9, UDPRcvbufErrors: 4},
		RampLogs:   LogCounts{HelloAccepted: 1038, HelloRejected: 3, ShardQueueFull: 6},
	}
	held := Row{
		Kind: "sweep", Seq: 2, N: 1038, Outcome: sweep.Pass, Held: true, FailedAckedByServer: -1,
		Data:   sweep.StepData{LossRatio: 0, ServerCPU: 0.4, GenCPU: 0.2},
		Window: Window{ServerCPU: 0.4, GenCPU: 0.2, Seconds: 30, NetOK: true, SoftnetDropped: 2, UDPRcvbufErrors: 1, UDPSndbufErrors: 3},
	}
	var buf bytes.Buffer
	if err := WriteStepsCSV(&buf, &Outcome{Rows: []Row{joinFail, held}}, "one-big-net"); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, stepsCSVFirst42...), stepsCSVAppended...)
	hdr := recs[0]
	// The first 60 columns are a contract; round 2 appended 27 more (checked below).
	if len(hdr) != len(want)+27 {
		t.Fatalf("header has %d columns, want %d: %v", len(hdr), len(want)+27, hdr)
	}
	for i := range want {
		if hdr[i] != want[i] {
			t.Errorf("column %d = %q, want %q", i, hdr[i], want[i])
		}
	}
	col := func(rec []string, name string) string {
		for i, h := range hdr {
			if h == name {
				return rec[i]
			}
		}
		t.Fatalf("no column %q", name)
		return ""
	}
	for name, w := range map[string]string{
		"requested": "2076", "joined": "1036", "join_failures": "2",
		"join_failures_by_stage": "control=0;radio=0;voice_dial=0;hello=2", "hello_tries_hist": "0;1006;25;4;1;0",
		"failed_acked_by_server": "1", "ramp_s": "38.4", "ramp_server_cpu": "0.8", "ramp_gen_cpu": "0.4",
		"ramp_server_drops": "7", "ramp_softnet_dropped": "9", "ramp_udp_rcvbuf_errors": "4",
		"ramp_hello_accepted": "1038", "ramp_hello_rejected": "3", "ramp_shard_queue_full": "6",
		"softnet_dropped": "", "udp_rcvbuf_errors": "", "udp_sndbuf_errors": "", // never held: empty, not 0
	} {
		if got := col(recs[1], name); got != w {
			t.Errorf("join-failure row %s = %q, want %q", name, got, w)
		}
	}
	// A hold that did not ramp: ramp cells empty, hold counters filled.
	for _, name := range []string{"requested", "joined", "join_failures", "join_failures_by_stage", "hello_tries_hist",
		"failed_acked_by_server", "ramp_s", "ramp_server_cpu", "ramp_softnet_dropped", "ramp_hello_accepted"} {
		if got := col(recs[2], name); got != "" {
			t.Errorf("held row %s = %q, want an empty cell", name, got)
		}
	}
	for name, w := range map[string]string{"softnet_dropped": "2", "udp_rcvbuf_errors": "1", "udp_sndbuf_errors": "3"} {
		if got := col(recs[2], name); got != w {
			t.Errorf("held row %s = %q, want %q", name, got, w)
		}
	}
	if !strings.HasPrefix(strings.Join(recs[1][:5], ","), "1,sweep,2076,breach") {
		t.Errorf("leading columns changed: %v", recs[1][:5])
	}
}

func TestWriteCalibrationCSVStoppedRow(t *testing.T) {
	cal := &Calibration{
		Steps:      []CalStep{{N: 64, EgressPPS: 3150, Valid: true}},
		ReceivePPS: 3150, LowerBound: true, Senders: 2,
		StoppedAt: 4096, StopReason: "bot 3702: no HELLO_ACK after 5 tries",
	}
	var b bytes.Buffer
	if err := WriteCalibrationCSV(&b, cal, "one-big-net"); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&b).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 || recs[2][0] != "stopped" || recs[3][0] != "receive_capacity" {
		t.Fatalf("rows = %v, want header, step, stopped, receive_capacity", recs)
	}
	if recs[2][1] != "4096" || recs[2][4] != "bot 3702: no HELLO_ACK after 5 tries" {
		t.Fatalf("stopped row = %v", recs[2])
	}
}

func TestKindNameControlLost(t *testing.T) {
	if got := kindName(sweep.ControlLostBound); got != "control-lost" {
		t.Fatalf("kindName(ControlLostBound) = %q, want control-lost", got)
	}
}

var stepsCSVRound2 = []string{
	"control_lost", "control_lost_phase", "control_lost_first", "ramp_mode", "ramp_talker", "ramp_joins_per_s", "ramp_join_p99_us",
	"ramp_hello_retried", "join_failed_chunk", "burst_talker", "burst_result", "gen_gc_cycles", "gen_warnings",
	"ping_sent", "ping_failed", "ping_rtt_p99_us",
	"tcp_abort_on_data", "tcp_abort_on_close", "tcp_abort_on_memory", "tcp_abort_on_timeout", "tcp_abort_on_linger", "tcp_timeouts",
	"tcp_estab_resets", "tcp_out_rsts", "softnet_squeezed", "ramp_tcp_abort_on_data", "ramp_tcp_estab_resets",
}

func TestWriteStepsCSVAppendsRound2Columns(t *testing.T) {
	r := v3Row()
	r.Step.Ping = StepPing{EveryS: 5, Sent: 100, Failed: 1, RTTP99Us: 850}
	r.Window.SoftnetSqueezed = 9
	r.RampWindow = Window{Seconds: 12, TCPOK: true, TCPAbortOnData: 4, EstabResets: 6}
	r.JoinFailedChunk = &ChunkRange{Had: 500, Requested: 750}
	b := burstRow()
	// A v2-bot row: none of the v3 cells were measured.
	v2 := passRow()
	v2.Joins = Joins{}
	var buf bytes.Buffer
	if err := WriteStepsCSV(&buf, &Outcome{Rows: []Row{r, b, v2}}, "one-big-net"); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]string{}, stepsCSVFirst42...), stepsCSVAppended...), stepsCSVRound2...)
	if len(want) != 42+18+27 {
		t.Fatalf("test table has %d columns", len(want))
	}
	hdr := recs[0]
	if len(hdr) != len(want) {
		t.Fatalf("header has %d columns, want %d", len(hdr), len(want))
	}
	for i := range want {
		if hdr[i] != want[i] {
			t.Errorf("column %d = %q, want %q", i, hdr[i], want[i])
		}
	}
	col := func(rec []string, name string) string {
		for i, h := range hdr {
			if h == name {
				return rec[i]
			}
		}
		t.Fatalf("no column %q", name)
		return ""
	}
	for name, w := range map[string]string{
		"control_lost": "2", "control_lost_phase": "hold", "control_lost_first": "bot 7 Unavailable: connection reset by peer",
		"ramp_mode": "ramp", "ramp_talker": "silent", "ramp_joins_per_s": "9.6", "ramp_join_p99_us": "7400000", "ramp_hello_retried": "30",
		"join_failed_chunk": "500->750", "burst_talker": "", "burst_result": "",
		"gen_gc_cycles": "12", "gen_warnings": "gc_pause_p99", "ping_sent": "100", "ping_failed": "1", "ping_rtt_p99_us": "850",
		"tcp_abort_on_data": "3", "tcp_abort_on_close": "0", "tcp_estab_resets": "5", "tcp_out_rsts": "0", "softnet_squeezed": "9",
		"ramp_tcp_abort_on_data": "4", "ramp_tcp_estab_resets": "6",
	} {
		if got := col(recs[1], name); got != w {
			t.Errorf("v3 row %s = %q, want %q", name, got, w)
		}
	}
	if col(recs[2], "burst_talker") != "live" || col(recs[2], "burst_result") != "join failures" || col(recs[2], "ramp_mode") != "burst" {
		t.Errorf("burst row: %v", recs[2][len(recs[2])-27:])
	}
	// A v2 row: every round-2 cell the bot or the kernel did not give is empty.
	for _, name := range []string{"control_lost_phase", "control_lost_first", "ramp_mode", "ramp_talker", "ramp_joins_per_s", "ramp_join_p99_us",
		"ramp_hello_retried", "join_failed_chunk", "burst_talker", "gen_warnings", "ping_sent", "ping_failed", "ping_rtt_p99_us",
		"tcp_abort_on_data", "tcp_estab_resets", "ramp_tcp_abort_on_data", "ramp_tcp_estab_resets"} {
		if got := col(recs[3], name); got != "" {
			t.Errorf("v2 row %s = %q, want an empty cell", name, got)
		}
	}
	if got := col(recs[3], "control_lost"); got != "0" {
		t.Errorf("control_lost = %q, want 0", got)
	}
}

func TestWriteCalibrationCSVLowerBoundReason(t *testing.T) {
	cal := &Calibration{
		Steps:      []CalStep{{N: 64, EgressPPS: 3150, Valid: true}},
		ReceivePPS: 3150, LowerBound: true, Senders: 2,
		LowerBoundReason: "delivery stop at N=2048",
	}
	var b bytes.Buffer
	if err := WriteCalibrationCSV(&b, cal, "one-big-net"); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&b).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	h, last := recs[0], recs[len(recs)-1]
	if h[len(h)-1] != "lower_bound_reason" || last[len(last)-1] != "delivery stop at N=2048" || last[0] != "receive_capacity" {
		t.Fatalf("header %v, row %v", h, last)
	}
	if len(recs[1]) != len(h) {
		t.Fatalf("step row has %d columns, header %d", len(recs[1]), len(h))
	}
}

func labelColumn(t *testing.T, csvText string) []string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(csvText)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := -1
	for i, h := range recs[0] {
		if h == "label" {
			col = i
		}
	}
	if col < 0 {
		t.Fatalf("no label column in %v", recs[0])
	}
	var out []string
	for _, r := range recs[1:] {
		out = append(out, r[col])
	}
	if len(out) == 0 {
		t.Fatal("no rows")
	}
	return out
}

func TestResultsCSVUseOptionLabel(t *testing.T) {
	const L = "local-docker, relative only. Not a capacity figure."
	cal := &Calibration{Steps: []CalStep{{N: 64, EgressPPS: 3150, Valid: true}}, ReceivePPS: 3150, StoppedAt: 4096, StopReason: "x"}
	out := &Outcome{Rows: []Row{{Seq: 1, Kind: "sweep", N: 10}}}
	var s, k, c bytes.Buffer
	if err := WriteStepsCSVLabeled(&s, out, "t", L); err != nil {
		t.Fatal(err)
	}
	if err := WriteKneesCSV(&k, out, KneeMeta{Arm: "S", Topology: "t", Label: L}); err != nil {
		t.Fatal(err)
	}
	if err := WriteCalibrationCSVLabeled(&c, cal, "t", L); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]*bytes.Buffer{"steps": &s, "knees": &k, "calibration": &c} {
		for _, v := range labelColumn(t, b.String()) {
			if v != L {
				t.Errorf("%s label = %q, want %q", name, v, L)
			}
		}
	}
}

func kneesRow(t *testing.T, res sweep.Result) map[string]string {
	t.Helper()
	var k bytes.Buffer
	if err := WriteKneesCSV(&k, &Outcome{Result: res}, KneeMeta{Arm: "S"}); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&k).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("knees.csv: %v %v", err, recs)
	}
	m := map[string]string{}
	for i, h := range recs[0] {
		m[h] = recs[1][i]
	}
	if recs[0][len(recs[0])-1] != "upper_n" {
		t.Errorf("last column = %q, want upper_n", recs[0][len(recs[0])-1])
	}
	return m
}

func TestKneesCSVUpperN(t *testing.T) {
	m := kneesRow(t, sweep.Result{Kind: sweep.Bracketed, N: 3013, Hi: 6027})
	if m["upper_n"] != "6027" || m["result_kind"] != "bracketed" || m["n"] != "3013" {
		t.Errorf("bracketed row = %v", m)
	}
	m = kneesRow(t, sweep.Result{Kind: sweep.KneeFound, N: 3013})
	if m["upper_n"] != "" || m["result_kind"] != "knee" {
		t.Errorf("knee row = %v", m)
	}
}

func TestLegLabelDefaultIsLegLabel(t *testing.T) {
	cal := &Calibration{Steps: []CalStep{{N: 64, Valid: true}}}
	out := &Outcome{Rows: []Row{{Seq: 1, Kind: "sweep", N: 10}}}
	var s, k, c, r bytes.Buffer
	if err := WriteStepsCSV(&s, out, "t"); err != nil {
		t.Fatal(err)
	}
	if err := WriteKneesCSV(&k, out, KneeMeta{Arm: "S"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteCalibrationCSV(&c, cal, "t"); err != nil {
		t.Fatal(err)
	}
	if err := WriteResultsMarkdown(&r, ResultsInput{Arm: "S"}); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]*bytes.Buffer{"steps": &s, "knees": &k, "calibration": &c} {
		for _, v := range labelColumn(t, b.String()) {
			if v != Label {
				t.Errorf("%s label = %q, want leg.Label", name, v)
			}
		}
	}
	if !strings.HasSuffix(r.String(), "_"+Label+"_\n") {
		t.Errorf("results.md foot = %q", r.String())
	}
	if (Options{}).LabelOrDefault() != Label {
		t.Error("empty Options.Label must resolve to leg.Label")
	}
}

func TestKindNameForLimit(t *testing.T) {
	if got := kindNameFor(sweep.Result{Kind: sweep.TimeBound, Limit: "generator-cpu"}); got != "generator-cpu-bound" {
		t.Errorf("got %q", got)
	}
	for _, l := range []string{"", "time"} {
		if got := kindNameFor(sweep.Result{Kind: sweep.TimeBound, Limit: l}); got != "time-bound" {
			t.Errorf("limit %q: got %q", l, got)
		}
	}
}

func TestKneesCSVPredictedEmptyWhenNotGiven(t *testing.T) {
	cell := func(pred int) string {
		var k bytes.Buffer
		if err := WriteKneesCSV(&k, &Outcome{Result: sweep.Result{}}, KneeMeta{Arm: "S", Predicted: pred}); err != nil {
			t.Fatal(err)
		}
		recs, err := csv.NewReader(&k).ReadAll()
		if err != nil || len(recs) != 2 {
			t.Fatalf("knees.csv: %v %v", err, recs)
		}
		for i, h := range recs[0] {
			if h == "predicted_knee" {
				return recs[1][i]
			}
		}
		t.Fatal("no predicted_knee column")
		return ""
	}
	if got := cell(0); got != "" {
		t.Errorf("no prediction: predicted_knee = %q, want empty", got)
	}
	if got := cell(24108); got != "24108" {
		t.Errorf("prediction 24108: predicted_knee = %q", got)
	}
}

func TestPredictedText(t *testing.T) {
	if predictedText(0) != "none" || predictedText(-1) != "none" || predictedText(7) != "7" {
		t.Errorf("predictedText: %q %q %q", predictedText(0), predictedText(-1), predictedText(7))
	}
}
