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
	if len(hdr) != len(want) {
		t.Fatalf("header has %d columns, want %d: %v", len(hdr), len(want), hdr)
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
