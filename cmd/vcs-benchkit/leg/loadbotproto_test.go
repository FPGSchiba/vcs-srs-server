package leg

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "loadbot", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseJoinsFixtures(t *testing.T) {
	var j Joins
	if err := json.Unmarshal(fixture(t, "v2-joins.json"), &j); err != nil {
		t.Fatal(err)
	}
	if err := j.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if j.Requested != 2076 || j.Had != 1038 || j.N != 2074 || j.Joined != 1036 || j.JoinFailures != 2 {
		t.Fatalf("counts: %+v", j)
	}
	if j.FailuresByStage["hello"] != 2 || j.HelloTriesMax != 5 || len(j.HelloTriesHist) != 6 {
		t.Fatalf("stages/hist: %+v", j)
	}
	if len(j.Failed) != 2 || j.Failed[1].Bot != 1669 || j.Failed[1].Stage != "hello" || j.Failed[1].Tries != 5 {
		t.Fatalf("failed: %+v", j.Failed)
	}
	if j.RampS != 38.4 || j.RampHealth.SchedP99us != 850 || j.RampHealth.GCPauseP99us != 120 {
		t.Fatalf("ramp: %+v", j)
	}

	var noop Joins
	if err := json.Unmarshal(fixture(t, "v2-joins-noop.json"), &noop); err != nil {
		t.Fatal(err)
	}
	if err := noop.Validate(); err != nil {
		t.Fatalf("noop Validate: %v", err)
	}
	if noop.JoinFailures != 0 || noop.Joined != 0 {
		t.Fatalf("noop: %+v", noop)
	}
}

func TestJoinsValidateRejectsBrokenInvariants(t *testing.T) {
	var j Joins
	if err := json.Unmarshal(fixture(t, "v2-joins.json"), &j); err != nil {
		t.Fatal(err)
	}
	bad := j
	bad.JoinFailures = 3
	if bad.Validate() == nil {
		t.Fatal("join_failures != requested-had-joined must fail")
	}
	bad = j
	bad.Schema = "x"
	if bad.Validate() == nil {
		t.Fatal("wrong schema must fail")
	}
	bad = j
	bad.HelloTriesHist = []int{0, 1}
	if bad.Validate() == nil {
		t.Fatal("short hist must fail")
	}
	bad = j
	bad.FailedTruncated = true
	if bad.Validate() == nil {
		t.Fatal("failed_truncated with join_failures <= 32 must fail")
	}
	bad = j
	bad.Failed = make([]JoinFailure, 33)
	if bad.Validate() == nil {
		t.Fatal("more than 32 failed entries must fail")
	}
	big := Joins{
		Schema: JoinsSchema, Requested: 100, Had: 0, N: 60, Joined: 60, JoinFailures: 40,
		FailuresByStage: map[string]int{"control": 0, "radio": 0, "voice_dial": 0, "hello": 40},
		HelloTriesHist:  []int{0, 60, 0, 0, 0, 0},
		Failed:          make([]JoinFailure, 32),
	}
	if big.Validate() == nil {
		t.Fatal("join_failures > 32 without failed_truncated must fail")
	}
	big.FailedTruncated = true
	if err := big.Validate(); err != nil {
		t.Fatalf("truncated 40 failures with 32 entries is valid: %v", err)
	}
}

func TestParseProgressFixtures(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, "v2-progress.ndjson"))))
	var ps []Progress
	for sc.Scan() {
		var p Progress
		if err := json.Unmarshal([]byte(sc.Text()), &p); err != nil {
			t.Fatal(err)
		}
		ps = append(ps, p)
	}
	if len(ps) != 2 {
		t.Fatalf("lines = %d", len(ps))
	}
	if ps[0].Phase != "ramp" || ps[0].Requested != 2076 || ps[0].Done != 512 || ps[0].InFlight != 16 || ps[0].HelloRetried != 9 || ps[0].ElapsedS != 5.0 {
		t.Fatalf("ramp: %+v", ps[0])
	}
	if ps[1].Phase != "calibrate_ramp" || ps[1].Target != 4096 || ps[1].Bots != 3000 {
		t.Fatalf("calibrate: %+v", ps[1])
	}
}

func TestParseCalStoppedFixture(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, "v2-calibrate.ndjson"))))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	found := false
	for sc.Scan() {
		var h struct{ Type string }
		_ = json.Unmarshal([]byte(sc.Text()), &h)
		if h.Type != "calibration_stopped" {
			continue
		}
		var c CalStopped
		if err := json.Unmarshal([]byte(sc.Text()), &c); err != nil {
			t.Fatal(err)
		}
		if c.N != 4096 || c.Bots != 3702 || c.Stage != "hello" || c.Bot != 3702 || c.Tries != 5 || c.Reason == "" {
			t.Fatalf("%+v", c)
		}
		found = true
	}
	if !found {
		t.Fatal("no calibration_stopped line")
	}
}

func TestParseReflectorStatsV2AndV1(t *testing.T) {
	var r ReflectorStats
	if err := json.Unmarshal(fixture(t, "v2-reflector-stats.json"), &r); err != nil {
		t.Fatal(err)
	}
	if r.SendErrors != 3364676 || r.Senders != 2 || r.QueueFullDrops == nil || *r.QueueFullDrops != 3364000 || r.WriteErrors == nil || *r.WriteErrors != 676 {
		t.Fatalf("%+v", r)
	}
	var v1 ReflectorStats
	if err := json.Unmarshal([]byte(`{"type":"reflector_stats","send_errors":7,"senders":2}`), &v1); err != nil {
		t.Fatal(err)
	}
	if v1.QueueFullDrops != nil || v1.WriteErrors != nil || v1.SendErrors != 7 {
		t.Fatalf("v1 split counters must stay nil: %+v", v1)
	}
}

func TestIsJoinFailureText(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, "join-failure-texts.txt"))))
	rows := 0
	for sc.Scan() {
		want, text, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			t.Fatalf("bad row %q", sc.Text())
		}
		rows++
		if got := IsJoinFailureText(text); got != (want == "true") {
			t.Errorf("IsJoinFailureText(%q) = %v, want %s", text, got, want)
		}
		if got := IsJoinFailureText("loadbot: " + text); got != (want == "true") {
			t.Errorf("with loadbot: prefix, %q = %v, want %s", text, got, want)
		}
	}
	if rows != 6 {
		t.Fatalf("rows = %d", rows)
	}
}

func TestJoinsFromV1Error(t *testing.T) {
	got := JoinsFromV1Error(2076, 1038, "loadbot: bot 1669: hello: no HELLO_ACK after 5 tries")
	if got.Source != "v1-error" || !got.BotExited || got.JoinFailures != 1 || got.FailuresByStage["hello"] != 1 || got.HelloTriesMax != 5 {
		t.Fatalf("%+v", got)
	}
	if len(got.Failed) != 1 || got.Failed[0].Bot != 1669 || got.Failed[0].Stage != "hello" || got.Failed[0].Tries != 5 {
		t.Fatalf("failed: %+v", got.Failed)
	}
	if got.Requested != 2076 || got.Had != 1038 {
		t.Fatalf("%+v", got)
	}

	c := JoinsFromV1Error(10, 5, "join LDBT-0042: rpc error: code = DeadlineExceeded desc = x")
	if c.Failed[0].Stage != "control" || c.Failed[0].Bot != -1 || c.FailuresByStage["control"] != 1 || c.Failed[0].Tries != 0 {
		t.Fatalf("%+v", c)
	}
	r := JoinsFromV1Error(10, 5, "bot 7: update radio: rpc error: code = Unavailable desc = x")
	if r.Failed[0].Stage != "radio" || r.Failed[0].Bot != 7 {
		t.Fatalf("%+v", r)
	}
	d := JoinsFromV1Error(10, 5, "bot 8: dial voice: boom")
	if d.Failed[0].Stage != "voice_dial" {
		t.Fatalf("%+v", d)
	}

	// I-1: Error is the cause without the "bot N: <stage>:" prefix, v1 too.
	for _, tc := range []struct{ got, want string }{
		{got.Failed[0].Error, "no HELLO_ACK after 5 tries"},
		{c.Failed[0].Error, "rpc error: code = DeadlineExceeded desc = x"},
		{r.Failed[0].Error, "rpc error: code = Unavailable desc = x"},
		{d.Failed[0].Error, "boom"},
		{JoinsFromV1Error(10, 5, "bot 9: rpc error: x").Failed[0].Error, "rpc error: x"},
	} {
		if tc.got != tc.want {
			t.Errorf("Error = %q, want %q", tc.got, tc.want)
		}
	}
}
