package leg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestParseFeatures(t *testing.T) {
	f := ParseFeatures(fixture(t, "v3-features.json"), nil)
	if f.Version != 3 {
		t.Fatalf("Version = %d, want 3", f.Version)
	}
	for _, k := range []string{"burst", "control_lost", "gc_warn", "join_rate", "ping", "talker_pause"} {
		if !f.Has(k) {
			t.Errorf("Has(%q) = false", k)
		}
	}
	if f.Has("nope") {
		t.Error("Has(nope) = true")
	}

	// A v2 bot exits 2 with no stdout: v2, empty set.
	v2 := ParseFeatures(nil, errors.New("exit status 2"))
	if v2.Version != 2 || len(v2.Set) != 0 || v2.Has("burst") {
		t.Fatalf("v2 = %+v", v2)
	}
	// Unparseable output, or a wrong type/schema, is v2 as well.
	for _, b := range [][]byte{[]byte("garbage"), []byte(`{"type":"features","schema":"other/9","features":["burst"]}`), []byte(`{"type":"step"}`)} {
		if g := ParseFeatures(b, nil); g.Version != 2 || len(g.Set) != 0 {
			t.Errorf("ParseFeatures(%q) = %+v, want v2", b, g)
		}
	}
	// A non-zero exit wins over parseable stdout.
	if g := ParseFeatures(fixture(t, "v3-features.json"), errors.New("exit status 1")); g.Version != 2 {
		t.Errorf("exit error with stdout = %+v, want v2", g)
	}
}

func TestParseControlLostFixture(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(string(fixture(t, "v3-control-lost.json"))))
	dec.DisallowUnknownFields()
	var line struct {
		Type string `json:"type"`
		ControlLostEvent
	}
	if err := dec.Decode(&line); err != nil {
		t.Fatal(err)
	}
	e := line.ControlLostEvent
	if line.Type != StepControlLostType || e.Bot != 2 || e.Name != "LDBT-0002" || e.Cause != "stream" ||
		e.Code != "Unavailable" || e.Phase != "hold" || e.AgeS != 243.1 || e.SinceLastOKPingS != 3.2 ||
		e.LocalAddr != "127.0.0.1:41234" || e.DeadTotal != 1 || e.AtUnixMs != 1793272383000 || e.PingFailures != 0 ||
		e.Message != "error reading from server: connection reset by peer" || e.GUID == "" || e.Error == "" {
		t.Fatalf("event = %+v", e)
	}
	if e.BotExited {
		t.Error("BotExited must be kit-only, never from the wire")
	}
}

func TestParseV3StepAdditions(t *testing.T) {
	var s Step
	if err := json.Unmarshal(fixture(t, "v3-step.json"), &s); err != nil {
		t.Fatal(err)
	}
	if s.ControlLost.DeadInHold != 1 || s.ControlLost.DeadTotal != 1 || s.ControlLost.Alive != 1335 ||
		s.ControlLost.DeadSinceLastStep != 1 || len(s.ControlLost.First) != 1 || s.ControlLost.First[0].Bot != 2 {
		t.Fatalf("ControlLost = %+v", s.ControlLost)
	}
	if s.Ping.Sent != 10680 || s.Ping.EveryS != 5 || s.Ping.Failed != 0 || s.Ping.RTTP99Us != 850 {
		t.Fatalf("Ping = %+v", s.Ping)
	}
	if len(s.Warnings) != 1 || s.Warnings[0] != "gc_pause_p99" {
		t.Fatalf("Warnings = %v", s.Warnings)
	}
	if s.Health.GCCycles == nil || *s.Health.GCCycles != 3 {
		t.Fatalf("GCCycles = %v", s.Health.GCCycles)
	}
}

func TestParseV3JoinsAdditions(t *testing.T) {
	var r, b Joins
	if err := json.Unmarshal(fixture(t, "v3-joins-ramp.json"), &r); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixture(t, "v3-joins-burst.json"), &b); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("ramp Validate: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("burst Validate: %v", err)
	}
	if r.Mode != "ramp" || r.Talker != "silent" || r.JoinsPerS != 4 || r.JoinP99Us != 240000 || r.HelloRetried != 3 ||
		r.InFlightMax != 8 || r.RateLimit == nil || r.RateLimit.MaxPerS != 12 || r.RateLimit.Scale != 5000 {
		t.Fatalf("ramp = %+v", r)
	}
	if b.Mode != "burst" || b.Talker != "live" || b.JoinsPerS != 9.6 || b.JoinP99Us != 7400000 || b.HelloRetried != 30 ||
		b.InFlightMax != 16 || b.RateLimit != nil || b.JoinFailures != 30 {
		t.Fatalf("burst = %+v", b)
	}
}

func TestParseV3ProgressAdditions(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, "v3-progress.ndjson"))))
	saw := false
	for sc.Scan() {
		var p Progress
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Talker != "" || p.RatePerS != 0 {
			saw = true
		}
	}
	if !saw {
		t.Fatal("no progress line carried talker or rate_per_s")
	}
}

func TestIsControlLostText(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, "control-lost-texts.txt"))))
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		want, text, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("bad row %q", line)
		}
		n++
		if got := IsControlLostText(text); got != (want == "true") {
			t.Errorf("IsControlLostText(%q) = %v, want %s", text, got, want)
		}
	}
	if n != 6 {
		t.Fatalf("rows = %d, want 6", n)
	}
}

func TestControlLostFromV2Error(t *testing.T) {
	var line struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(fixture(t, "v2-hold-died.ndjson"), &line); err != nil {
		t.Fatal(err)
	}
	got := ControlLostFromV2Error(line.Error, "")
	want := ControlLostEvent{Bot: 2, Cause: "stream", Code: "Unavailable",
		Message: "error reading from server: connection reset by peer", SinceLastOKPingS: -1, BotExited: true}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if g := ControlLostFromV2Error("loadbot: hold: bot 55 died: update stream ended by the server", "settle"); g.Bot != 55 ||
		g.Code != "Unknown" || g.Message != "update stream ended by the server" || g.Phase != "settle" {
		t.Errorf("no-status text: %+v", g)
	}
	if g := ControlLostFromV2Error("hold: bot 7 died: resync: rpc error: code = Unauthenticated desc = session not found", "hold"); g.Bot != 7 ||
		g.Code != "Unauthenticated" || g.Message != "session not found" {
		t.Errorf("resync text: %+v", g)
	}
}

// --- v4 (W6 C2): fixtures shared byte-for-byte with the client lane ---

func TestV4FixturesDecode(t *testing.T) {
	var s Step
	if err := json.Unmarshal(fixture(t, "v4-step.json"), &s); err != nil {
		t.Fatal(err)
	}
	if s.Schema != "loadbot-step/4" || s.Payload != "opus-speech-corpus" || s.Scenario != "org-op" || s.ArmLabel != "" {
		t.Fatalf("identity = %+v", s)
	}
	if s.Corpus == nil || s.Corpus.Frames != 1566 || s.Corpus.SizeMedian != 121 || s.Corpus.SizeMax != 214 || s.Corpus.SHA256 == "" {
		t.Fatalf("Corpus = %+v", s.Corpus)
	}
	if s.FanoutPPS != 26300 || s.Presses != 1100 || s.DualPresses != 110 || s.VoiceEndpoints != 1 {
		t.Fatalf("load axes = %v %d %d %d", s.FanoutPPS, s.Presses, s.DualPresses, s.VoiceEndpoints)
	}
	if s.LateLoss60 == nil || *s.LateLoss60 != 35 || s.LateLoss500 == nil || *s.LateLoss500 != 0 ||
		s.LateLossRatio == nil || *s.LateLossRatio < 0.0000332 || *s.LateLossRatio > 0.0000333 {
		t.Fatalf("late loss = %v %v %v", s.LateLoss60, s.LateLoss500, s.LateLossRatio)
	}
	for name, p := range map[string]*int64{
		"p50": s.OneWayP50Us, "p99": s.OneWayP99Us, "max": s.OneWayMaxUs, "actual": s.OneWayActualP99Us,
		"jitter p99": s.JitterP99Us, "jitter mean": s.JitterMeanUs, "gap": s.GapP99Us, "radio prop": s.RadioPropP99Us,
	} {
		if p == nil {
			t.Errorf("%s is nil in the Linux fixture", name)
		}
	}
	if *s.OneWayP99Us != 900 || *s.GapP99Us != 20800 || *s.RadioPropP99Us != 3100 {
		t.Errorf("values = %d %d %d", *s.OneWayP99Us, *s.GapP99Us, *s.RadioPropP99Us)
	}
	if s.RTTP99Us != 300 || s.RadioUpdateP99Us != 2500 || s.SecretToAckP99Us != 1500 {
		t.Errorf("plain latencies = %d %d %d", s.RTTP99Us, s.RadioUpdateP99Us, s.SecretToAckP99Us)
	}
	if s.Parity == nil || !s.Parity.OK || s.Parity.ConnectMs != 95.2 {
		t.Errorf("Parity = %+v", s.Parity)
	}
	// The v3 half of the line still decodes.
	if s.Ping.Sent != 16000 || s.Health.GCCycles == nil || *s.Health.GCCycles != 3 || s.ControlLost.Alive != 2000 {
		t.Errorf("v3 fields lost: %+v", s)
	}

	var d Step
	if err := json.Unmarshal(fixture(t, "v4-step-d2.json"), &d); err != nil {
		t.Fatal(err)
	}
	if d.Arm != "D2" || d.ArmLabel != "bot-only; real client: ROADMAP Phase 9" || d.Parity != nil || d.VoiceEndpoints != 1 {
		t.Fatalf("D2 step = %+v", d)
	}
	for name, null := range map[string]bool{
		"late_loss_60": d.LateLoss60 == nil, "late_loss_500": d.LateLoss500 == nil, "late_loss_ratio": d.LateLossRatio == nil,
		"one_way_p50": d.OneWayP50Us == nil, "one_way_p99": d.OneWayP99Us == nil, "one_way_max": d.OneWayMaxUs == nil,
		"one_way_actual": d.OneWayActualP99Us == nil, "jitter_p99": d.JitterP99Us == nil, "jitter_mean": d.JitterMeanUs == nil,
		"gap": d.GapP99Us == nil, "radio_prop": d.RadioPropP99Us == nil,
	} {
		if !null {
			t.Errorf("%s must decode to nil in the D2 fixture", name)
		}
	}
	if got := notMeasured(d); got != "late_loss;one_way_p99" {
		t.Errorf("notMeasured(D2) = %q", got)
	}
	if got := notMeasured(s); got != "" {
		t.Errorf("notMeasured(v4 Linux) = %q", got)
	}

	f := ParseFeatures(fixture(t, "v4-features.json"), nil)
	for _, k := range []string{"arm_d2", "latency", "opus_corpus", "org_op", "parity", "hello_ladder"} {
		if !f.Set[k] {
			t.Errorf("v4 features lack %q: %v", k, f.Names())
		}
	}
	if f.Version != 3 {
		t.Errorf("Version = %d", f.Version)
	}
}

func TestV3StepStillDecodesWithoutV4Fields(t *testing.T) {
	var s Step
	if err := json.Unmarshal(fixture(t, "v3-step.json"), &s); err != nil {
		t.Fatal(err)
	}
	if s.Schema != "" || s.Corpus != nil || s.LateLossRatio != nil || s.OneWayP99Us != nil || s.Parity != nil {
		t.Fatalf("v3 step grew v4 values: %+v", s)
	}
	if got := notMeasured(s); got != "late_loss;one_way_p99" {
		t.Fatalf("notMeasured(v3) = %q", got)
	}
}

func TestNotMeasuredNamesEachMissingSide(t *testing.T) {
	one, ratio := int64(5), 0.0
	if got := notMeasured(Step{OneWayP99Us: &one}); got != "late_loss;one_way_p99" {
		t.Errorf("late loss missing: %q", got)
	}
	if got := notMeasured(Step{LateLossRatio: &ratio}); got != "late_loss;one_way_p99" {
		t.Errorf("one-way missing: %q", got)
	}
	if got := notMeasured(Step{LateLossRatio: &ratio, OneWayP99Us: &one}); got != "" {
		t.Errorf("both measured: %q", got)
	}
}

func TestParityLineCaptured(t *testing.T) {
	b := NewBot(io.Discard, bytes.NewReader(fixture(t, "v4-run-s.ndjson")), BotHooks{})
	st, err := b.Hold(context.Background(), time.Second, "step-3")
	if err != nil {
		t.Fatal(err)
	}
	if st.Schema != "loadbot-step/4" {
		t.Fatalf("step = %+v", st)
	}
	p, ok := b.Parity()
	if !ok || !p.OK || p.Arm != "S" || p.ConnectMs != 95.2 || p.Path != "session.Connect" || p.Schema != "loadbot-parity/1" {
		t.Fatalf("Parity = %+v, %v", p, ok)
	}
}

func TestD2RunHasNoParityLine(t *testing.T) {
	b := NewBot(io.Discard, bytes.NewReader(fixture(t, "v4-run-d2.ndjson")), BotHooks{})
	if _, err := b.Hold(context.Background(), time.Second, "step-3"); err != nil {
		t.Fatal(err)
	}
	if p, ok := b.Parity(); ok {
		t.Fatalf("D2 run reported parity %+v", p)
	}
}

func TestParityFailureIsAnArmFailureWithItsReason(t *testing.T) {
	in := `{"type":"parity","schema":"loadbot-parity/1","arm":"S","ok":false,"connect_ms":0,"error":"auth: bad password","path":"session.Connect"}` + "\n" +
		`{"type":"error","error":"parity failed"}` + "\n"
	b := NewBot(io.Discard, strings.NewReader(in), BotHooks{})
	_, err := b.Hold(context.Background(), time.Second, "step-1")
	if err == nil || !strings.Contains(err.Error(), "parity: auth: bad password") {
		t.Fatalf("err = %v, want one naming parity and the reason", err)
	}
	if p, ok := b.Parity(); !ok || p.OK {
		t.Fatalf("Parity = %+v, %v", p, ok)
	}
}
