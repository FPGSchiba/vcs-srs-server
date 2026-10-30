package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	legpkg "github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/leg"
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

func TestAggregateShowsBracketed(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	d := filepath.Join(in, "1-one-big-net")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	hdr := strings.TrimSuffix(kneesHeader, "\n") + ",upper_n\n"
	row := strings.Join([]string{"S", "one-big-net", `"3013 ≤ knee < 6027 (bracketed)"`, "bracketed", "3013", "", "", "", "", "24108", "7058", "t", `"` + label + `"`, "p", "j", "nm", "", "6027"}, ",")
	m := `{"schema":"leg-manifest/1","arm":"S","scenario":"one-big-net","stopped_early":"set_n 6026: context deadline exceeded"}`
	for name, body := range map[string]string{"knees.csv": hdr + row + "\n", "manifest.json": m} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout bytes.Buffer
	if err := runAggregate([]string{"--in", in, "--out", out}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(out, "aggregate.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"1 of 1 legs finished",
		"| 3013 ≤ knee < 6027 (bracketed) | 3013 | bracketed |",
		"stopped early: set_n 6026",
	} {
		if !strings.Contains(string(md), want) {
			t.Errorf("aggregate.md lacks %q:\n%s", want, md)
		}
	}
	if rows := readAgg(t, out); len(rows) != 1 || rows[0]["censored"] != "true" {
		t.Errorf("aggregate.csv rows = %v, want censored=true", rows)
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

// writeArmLeg writes the new layout: <root>/<leg>/<arm>/knees.csv.
func writeArmLeg(t *testing.T, root, leg, arm, scen, kind, n, topo string) {
	t.Helper()
	writeLeg(t, root, leg+"/"+arm, arm, scen, "knee "+n, kind, n, topo)
}

// writeLegJSON writes <root>/<leg>/leg.json from the arms' (kind, n) pairs.
func writeLegJSON(t *testing.T, root, leg, scen, topo string, arms map[string][2]string) {
	t.Helper()
	idx := legpkg.LegIndex{Schema: legpkg.LegIndexSchema, Scenario: scen, Seed: 1, Arms: map[string]legpkg.ArmResult{}, TopologyLabel: topo, Label: label}
	for a, kn := range arms {
		n, _ := strconv.Atoi(kn[1])
		idx.Arms[a] = legpkg.ArmResult{Result: "r", Kind: kn[0], N: n}
		idx.ArmOrder = append(idx.ArmOrder, a)
	}
	sort.Strings(idx.ArmOrder)
	idx.Ratios, idx.RatioNotes = legpkg.Ratios(idx.Arms)
	b, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, leg, "leg.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCSVRows(t *testing.T, path string) []map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
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

func TestAggregateRatiosAcrossLegs(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	topo := "2 cores"
	scen := "one-big-net"
	type leg struct {
		s, seq, d2 string
		d2kind     string
	}
	for i, l := range []leg{{"1000", "1500", "2000", "knee"}, {"1000", "1200", "1800", "knee"}, {"1000", "1800", "4000", "generator-bound"}} {
		dir := strconv.Itoa(i+1) + "-" + scen
		writeArmLeg(t, in, dir, "S", scen, "knee", l.s, topo)
		writeArmLeg(t, in, dir, "Seq", scen, "knee", l.seq, topo)
		writeArmLeg(t, in, dir, "D2", scen, l.d2kind, l.d2, topo)
		writeLegJSON(t, in, dir, scen, topo, map[string][2]string{"S": {"knee", l.s}, "Seq": {"knee", l.seq}, "D2": {l.d2kind, l.d2}})
	}
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]string{}
	for _, r := range readCSVRows(t, filepath.Join(out, "ratios.csv")) {
		by[r["ratio"]] = r
	}
	seq := by["Seq/S"]
	if seq["scenario"] != scen || seq["legs"] != "3" || seq["median"] != "1.50" || seq["min"] != "1.20" || seq["max"] != "1.80" || seq["censored_legs"] != "0" || seq["topology"] != topo || seq["label"] != label {
		t.Fatalf("Seq/S = %v", seq)
	}
	d2 := by["D2/S"]
	if d2["legs"] != "2" || d2["censored_legs"] != "1" || d2["median"] != "1.90" || d2["min"] != "1.80" || d2["max"] != "2.00" {
		t.Fatalf("D2/S = %v", d2)
	}
	md, _ := os.ReadFile(filepath.Join(out, "aggregate.md"))
	// three legs of three arms: the heading counts arm runs, not legs.
	if !strings.Contains(string(md), "### Aggregate: 9 of 9 arm runs finished (3 legs)") {
		t.Fatalf("aggregate.md heading does not count arm runs:\n%s", md)
	}
	if !strings.Contains(string(md), "Within-leg ratios") || !strings.Contains(string(md), "D2/S") {
		t.Fatalf("aggregate.md lacks ratios table:\n%s", md)
	}
	// the arm rows still aggregate from <leg>/<arm>/knees.csv.
	if rows := readAgg(t, out); len(rows) != 3 {
		t.Fatalf("aggregate rows = %d, want 3", len(rows))
	}
}

func TestAggregateOldLayoutHasNoRatios(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-one-big-net", "S", "one-big-net", "knee 2400", "knee", "2400", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if rows := readCSVRows(t, filepath.Join(out, "ratios.csv")); len(rows) != 0 {
		t.Fatalf("ratios = %v, want header only", rows)
	}
	if rows := readAgg(t, out); len(rows) != 1 || rows[0]["arm_label"] != "" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestAggregateArmLabelFromManifest(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeArmLeg(t, in, "1-org-op", "D2", "org-op", "knee", "900", "t")
	if err := os.WriteFile(filepath.Join(in, "1-org-op", "D2", "manifest.json"), []byte(`{"arm":"D2","scenario":"org-op","arm_label":"bot-only; real client: ROADMAP Phase 9"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runAggregate([]string{"--in", in, "--out", out}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if rows := readAgg(t, out); len(rows) != 1 || rows[0]["arm_label"] != "bot-only; real client: ROADMAP Phase 9" {
		t.Fatalf("rows = %v", rows)
	}
}
