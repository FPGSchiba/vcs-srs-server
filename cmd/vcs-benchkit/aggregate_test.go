package main

import (
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const kneesHeader = "arm,scenario,result,result_kind,n,confirm_n,confirm_outcome,overload_n,overload_outcome,predicted_knee,n_max,topology,label,payload,join_label,not_measured,notes\n"

// writeLeg writes <root>/<dir>/knees.csv with one row.
func writeLeg(t *testing.T, root, dir, arm, scen, result, kind, n, topo string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	row := strings.Join([]string{arm, scen, `"` + result + `"`, kind, n, "", "", "", "", "3000", "9000", `"` + topo + `"`, `"` + label + `"`, "synthetic-not-opus", "j", "nm", ""}, ",")
	if err := os.WriteFile(filepath.Join(d, "knees.csv"), []byte(kneesHeader+row+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAgg(t *testing.T, out string) []map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, "aggregate.csv"))
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	for _, r := range recs[1:] {
		m := map[string]string{}
		for i, h := range recs[0] {
			m[h] = r[i]
		}
		rows = append(rows, m)
	}
	return rows
}

func TestAggregateMedianMinMax(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	topo := "2 hyperthreads of 1 core"
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "knee 2400", "knee", "2400", topo)
	writeLeg(t, in, "2-one-big-net", "S", "one-big-net", "knee 3100", "knee", "3100", topo)
	writeLeg(t, in, "3-one-big-net", "S", "one-big-net", "knee 2800", "knee", "2800", topo)
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, os.Stderr); err != nil {
		t.Fatal(err)
	}
	rows := readAgg(t, out)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r["median_n"] != "2800" || r["min_n"] != "2400" || r["max_n"] != "3100" || r["repeats"] != "3" || r["censored"] != "false" {
		t.Fatalf("got %v", r)
	}
	if r["label"] != label || r["topology"] != topo || r["predicted_knee"] != "3000" {
		t.Fatalf("label/topology/prediction: %v", r)
	}
	if _, err := os.Stat(filepath.Join(out, "legs", "2-one-big-net", "knees.csv")); err != nil {
		t.Fatalf("leg not copied: %v", err)
	}
}

func TestAggregateCensoredIsALowerBound(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "knee 2400", "knee", "2400", "t")
	writeLeg(t, in, "2-one-big-net", "S", "one-big-net", "≥ 1800 (generator-bound)", "generator-bound", "1800", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, os.Stderr); err != nil {
		t.Fatal(err)
	}
	r := readAgg(t, out)[0]
	if r["censored"] != "true" || !strings.Contains(r["results"], "generator-bound") {
		t.Fatalf("censored leg must be flagged and listed: %v", r)
	}
	if r["min_n"] != "1800" || r["max_n"] != "2400" {
		t.Fatalf("got %v", r)
	}
}

func TestAggregateControlLostIsCensored(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "knee 2400", "knee", "2400", "t")
	writeLeg(t, in, "2-one-big-net", "S", "one-big-net", "≥ 972 (control lost at 1944)", "control-lost", "972", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, os.Stderr); err != nil {
		t.Fatal(err)
	}
	r := readAgg(t, out)[0]
	if r["censored"] != "true" || !strings.Contains(r["results"], "control lost at 1944") || r["min_n"] != "972" {
		t.Fatalf("a control-lost leg is a censored lower bound: %v", r)
	}
}

func TestAggregateEvenCountMedianRoundsDown(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee 1001", "knee", "1001", "t")
	writeLeg(t, in, "2-x", "S", "one-big-net", "knee 2000", "knee", "2000", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if got := readAgg(t, out)[0]["median_n"]; got != "1500" {
		t.Fatalf("median = %s, want 1500", got)
	}
}

func TestAggregateErrors(t *testing.T) {
	if err := runAggregate([]string{"--in", t.TempDir(), "--out", t.TempDir()}, io.Discard, os.Stderr); err == nil || !strings.Contains(err.Error(), "no knees.csv") {
		t.Fatalf("empty input: %v", err)
	}
	if err := runAggregate([]string{"--in", t.TempDir()}, io.Discard, os.Stderr); err == nil {
		t.Fatal("missing --out must error")
	}
	in := t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee abc", "knee", "abc", "t")
	if err := runAggregate([]string{"--in", in, "--out", t.TempDir()}, io.Discard, os.Stderr); err == nil {
		t.Fatal("non-numeric n must error")
	}
}

func TestAggregateMixedTopologyIsNamed(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee 10", "knee", "10", "2 cores")
	writeLeg(t, in, "2-x", "S", "one-big-net", "knee 12", "knee", "12", "2 hyperthreads of 1 core")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if got := readAgg(t, out)[0]["topology"]; !strings.HasPrefix(got, "mixed:") {
		t.Fatalf("topology = %q", got)
	}
}

func TestAggregateFlagsUnconfirmedKnee(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee 2400", "knee", "2400", "t")
	writeLeg(t, in, "2-x", "S", "one-big-net", "knee 2800 (unconfirmed: the confirm step did not pass)", "knee", "2800", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, os.Stderr); err != nil {
		t.Fatal(err)
	}
	r := readAgg(t, out)[0]
	if r["unconfirmed"] != "true" || r["censored"] != "false" {
		t.Fatalf("an unconfirmed knee must set unconfirmed, not censored: %v", r)
	}

	in2, out2 := t.TempDir(), t.TempDir()
	writeLeg(t, in2, "1-x", "S", "one-big-net", "knee 2400", "knee", "2400", "t")
	if err := runAggregate([]string{"--in", in2, "--out", out2}, io.Discard, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if got := readAgg(t, out2)[0]["unconfirmed"]; got != "false" {
		t.Fatalf("unconfirmed = %q, want false", got)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	os.Stdout = old
	_ = w.Close()
	return <-done
}

// Run 40752831443: all three legs aborted, so there was no knees.csv and the
// aggregate job printed nothing but "no knees.csv". It must still show a table.
func TestAggregateZeroLegsStillPrintsTable(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	for _, d := range []string{"1-one-big-net", "2-one-big-net", "3-one-big-net"} {
		if err := os.MkdirAll(filepath.Join(in, d), 0o755); err != nil {
			t.Fatal(err)
		}
		m := `{"schema":"leg-manifest/1","arm":"S","scenario":"one-big-net","error":"set_n 2076: loadbot: bot 1669: hello: no HELLO_ACK after 5 tries"}`
		if err := os.WriteFile(filepath.Join(in, d, "manifest.json"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	stdout := captureStdout(t, func() { err = runAggregate([]string{"--in", in, "--out", out}, os.Stdout, io.Discard) })
	if err == nil {
		t.Fatal("zero legs must still be an error")
	}
	md, rerr := os.ReadFile(filepath.Join(out, "aggregate.md"))
	if rerr != nil {
		t.Fatalf("aggregate.md not written (err = %v): %v", err, rerr)
	}
	for _, d := range []string{"1-one-big-net", "2-one-big-net", "3-one-big-net"} {
		if !strings.Contains(string(md), d) {
			t.Errorf("aggregate.md has no row for %s:\n%s", d, md)
		}
		if !strings.Contains(stdout, d) {
			t.Errorf("stdout has no row for %s:\n%s", d, stdout)
		}
	}
}

// writeFailedLeg writes a leg that aborted: a manifest with an error, no knees.csv.
func writeFailedLeg(t *testing.T, root, dir, errText string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	m := `{"schema":"leg-manifest/1","arm":"S","scenario":"one-big-net","error":"` + errText + `"}`
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addLegDetail adds calibration.csv and steps.csv to a finished leg.
func addLegDetail(t *testing.T, root, dir string) {
	t.Helper()
	cal := "kind,n,egress_pps,valid,violations,lower_bound\nstep,2048,102350,true,,\nreceive_capacity,,102350,,,true\n"
	steps := "seq,kind,n,outcome\n1,sweep,1038,pass\n2,sweep,2076,breach\n3,sweep,1557,pass\n"
	for name, body := range map[string]string{"calibration.csv": cal, "steps.csv": steps} {
		if err := os.WriteFile(filepath.Join(root, dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAggregateMarkdownGolden(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeFailedLeg(t, in, "1-one-big-net", "calibration: a|b")
	writeLeg(t, in, "2-one-big-net", "S", "one-big-net", "knee 1557", "knee", "1557", "t")
	addLegDetail(t, in, "2-one-big-net")
	writeLeg(t, in, "3-one-big-net", "S", "one-big-net", "knee 1700", "knee", "1700", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "aggregate.md"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "aggregate.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(got), "\r\n", "\n") != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("aggregate.md differs from testdata/aggregate.md:\n%s", got)
	}
}

func TestAggregatePrintsTableToStdout(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee 10", "knee", "10", "t")
	var buf bytes.Buffer
	if err := runAggregate([]string{"--in", in, "--out", out}, &buf, io.Discard); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(filepath.Join(out, "aggregate.md"))
	if buf.String() != string(md) || !strings.Contains(buf.String(), "1 of 1 legs finished") {
		t.Fatalf("stdout:\n%s", buf.String())
	}
}

func TestReadLegInfoShowsCalibrationStopReason(t *testing.T) {
	in := t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee 10", "knee", "10", "t")
	cal := "kind,n,egress_pps,valid,violations,lower_bound\nstep,2048,102350,true,,\n" +
		"stopped,4096,,,bot 3702: no HELLO_ACK after 5 tries,\nreceive_capacity,,102350,,,true\n"
	if err := os.WriteFile(filepath.Join(in, "1-x", "calibration.csv"), []byte(cal), 0o644); err != nil {
		t.Fatal(err)
	}
	li, err := readLegInfo(in, "1-x", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(li.calib, "102350") || !strings.Contains(li.calib, "stopped at 4096: bot 3702: no HELLO_ACK after 5 tries") {
		t.Fatalf("calib cell = %q", li.calib)
	}
}

// addRound2Steps writes a steps.csv with the round-2 columns: one step that lost
// control, and a silent and a live burst.
func addRound2Steps(t *testing.T, root, dir string) {
	t.Helper()
	steps := "seq,kind,n,outcome,join_failures,control_lost,burst_talker\n" +
		"1,sweep,972,pass,0,0,\n" +
		"2,sweep,1336,control_lost,0,2,\n" +
		"3,burst,818,pass,0,0,silent\n" +
		"4,burst,818,pass,30,0,live\n"
	if err := os.WriteFile(filepath.Join(root, dir, "steps.csv"), []byte(steps), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateRound2(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "≥ 972 (control lost at 1336)", "control-lost", "972", "t")
	addRound2Steps(t, in, "1-one-big-net")
	writeLeg(t, in, "2-one-big-net", "S", "one-big-net", "knee 1557", "knee", "1557", "t")
	addLegDetail(t, in, "2-one-big-net")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(out, "aggregate.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(md)
	if !strings.Contains(s, "| control lost (steps) | bursts (silent/live failures) |") {
		t.Errorf("leg table lacks the round-2 columns:\n%s", s)
	}
	if !strings.Contains(s, "| 972:pass 1336:control_lost 818:pass 818:pass | 1 | 0/30 |") {
		t.Errorf("leg 1 round-2 cells:\n%s", s)
	}
	// A leg whose steps.csv predates round 2 shows a dash, never a zero.
	if !strings.Contains(s, "| 1038:pass 2076:breach 1557:pass | - | - |") {
		t.Errorf("leg 2 must show dashes:\n%s", s)
	}
	// A control-lost result is a censored lower bound, as before.
	if r := readAgg(t, out)[0]; r["censored"] != "true" {
		t.Errorf("aggregate.csv: %v", r)
	}
}

func TestAggregateLabelFlag(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	topo := "2 hyperthreads of 1 core"
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "knee 2400", "knee", "2400", topo)
	writeLeg(t, in, "2-one-big-net", "S", "one-big-net", "knee 3100", "knee", "3100", topo)
	const want = "local-docker, relative only. Not a capacity figure."
	if err := runAggregate([]string{"--in", in, "--out", out, "--label", want}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	rows := readAgg(t, out)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range rows {
		if r["label"] != want {
			t.Errorf("csv label = %q, want %q", r["label"], want)
		}
	}
	md, err := os.ReadFile(filepath.Join(out, "aggregate.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(md), "\n_"+want+"_\n") {
		t.Errorf("aggregate.md does not end with the label: %q", md)
	}
}

func TestAggregateLabelDefault(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "knee 2400", "knee", "2400", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, r := range readAgg(t, out) {
		if r["label"] != label {
			t.Errorf("csv label = %q, want the default constant", r["label"])
		}
	}
	md, _ := os.ReadFile(filepath.Join(out, "aggregate.md"))
	if !strings.HasSuffix(string(md), "\n_"+label+"_\n") {
		t.Errorf("aggregate.md default label missing: %q", md)
	}
}

func TestAggregateLabelNeedsRelativeOnly(t *testing.T) {
	in := t.TempDir()
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "knee 2400", "knee", "2400", "t")
	err := runAggregate([]string{"--in", in, "--out", t.TempDir(), "--label", "capacity"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `--label must say "relative only"`) {
		t.Errorf("err = %v", err)
	}
}
