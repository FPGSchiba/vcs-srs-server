package main

import (
	"bytes"
	"encoding/csv"
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
	if err := runAggregate([]string{"--in", in, "--out", out}, os.Stderr); err != nil {
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
	if err := runAggregate([]string{"--in", in, "--out", out}, os.Stderr); err != nil {
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

func TestAggregateEvenCountMedianRoundsDown(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee 1001", "knee", "1001", "t")
	writeLeg(t, in, "2-x", "S", "one-big-net", "knee 2000", "knee", "2000", "t")
	if err := runAggregate([]string{"--in", in, "--out", out}, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if got := readAgg(t, out)[0]["median_n"]; got != "1500" {
		t.Fatalf("median = %s, want 1500", got)
	}
}

func TestAggregateErrors(t *testing.T) {
	if err := runAggregate([]string{"--in", t.TempDir(), "--out", t.TempDir()}, os.Stderr); err == nil || !strings.Contains(err.Error(), "no knees.csv") {
		t.Fatalf("empty input: %v", err)
	}
	if err := runAggregate([]string{"--in", t.TempDir()}, os.Stderr); err == nil {
		t.Fatal("missing --out must error")
	}
	in := t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee abc", "knee", "abc", "t")
	if err := runAggregate([]string{"--in", in, "--out", t.TempDir()}, os.Stderr); err == nil {
		t.Fatal("non-numeric n must error")
	}
}

func TestAggregateMixedTopologyIsNamed(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeLeg(t, in, "1-x", "S", "one-big-net", "knee 10", "knee", "10", "2 cores")
	writeLeg(t, in, "2-x", "S", "one-big-net", "knee 12", "knee", "12", "2 hyperthreads of 1 core")
	if err := runAggregate([]string{"--in", in, "--out", out}, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if got := readAgg(t, out)[0]["topology"]; !strings.HasPrefix(got, "mixed:") {
		t.Fatalf("topology = %q", got)
	}
}
