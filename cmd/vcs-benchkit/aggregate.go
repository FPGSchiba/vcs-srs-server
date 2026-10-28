package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// legKnee is one leg's knees.csv row.
type legKnee struct {
	dir, arm, scenario, result, kind, topology, predicted string
	n                                                     int
}

// readKnees reads the single data row of a leg's knees.csv by column name.
func readKnees(path, dir string) (legKnee, error) {
	f, err := os.Open(path)
	if err != nil {
		return legKnee{}, err
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return legKnee{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(recs) != 2 {
		return legKnee{}, fmt.Errorf("%s: want a header and one row, got %d records", path, len(recs))
	}
	col := map[string]string{}
	for i, h := range recs[0] {
		if i < len(recs[1]) {
			col[h] = recs[1][i]
		}
	}
	n, err := strconv.Atoi(col["n"])
	if err != nil {
		return legKnee{}, fmt.Errorf("%s: n %q is not a number", path, col["n"])
	}
	return legKnee{
		dir: dir, arm: col["arm"], scenario: col["scenario"], result: col["result"],
		kind: col["result_kind"], topology: col["topology"], predicted: col["predicted_knee"], n: n,
	}, nil
}

// medianInt of ascending values; an even count averages the middle pair, rounding down.
func medianInt(sorted []int) int {
	m := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[m]
	}
	return (sorted[m-1] + sorted[m]) / 2
}

// copyTree copies the regular files under src into dst, keeping relative paths.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o644)
	})
}

// runAggregate merges the legs under --in (one <repeat>-<scenario>/knees.csv
// each) into aggregate.csv, and copies every leg under <out>/legs.
// A censored leg (generator-bound, time-bound, below range) contributes its
// bound as n and sets censored=true, so the statistics are lower bounds then.
// A leg whose knee failed its confirm step keeps result_kind=knee; it sets
// unconfirmed=true instead, so the flag is not buried in the results text.
func runAggregate(args []string, stderr io.Writer) error {
	fl := flag.NewFlagSet("aggregate", flag.ContinueOnError)
	fl.SetOutput(stderr)
	in := fl.String("in", "", "directory holding the downloaded leg results (required)")
	out := fl.String("out", "", "output directory (required)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" || fl.NArg() > 0 {
		return fmt.Errorf("aggregate: --in and --out are required, and nothing else")
	}
	var legs []legKnee
	err := filepath.WalkDir(*in, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "knees.csv" {
			return err
		}
		rel, err := filepath.Rel(*in, filepath.Dir(p))
		if err != nil {
			return err
		}
		lk, err := readKnees(p, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		legs = append(legs, lk)
		return nil
	})
	if err != nil {
		return err
	}
	if len(legs) == 0 {
		return fmt.Errorf("aggregate: no knees.csv under %s", *in)
	}
	groups := map[string][]legKnee{}
	var keys []string
	for _, l := range legs {
		k := l.arm + "\x00" + l.scenario
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], l)
	}
	sort.Strings(keys)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(*out, "aggregate.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	if err := cw.Write([]string{"arm", "scenario", "repeats", "median_n", "min_n", "max_n", "censored", "unconfirmed", "results", "predicted_knee", "topology", "label"}); err != nil {
		return err
	}
	for _, k := range keys {
		g := groups[k]
		sort.Slice(g, func(i, j int) bool { return g[i].dir < g[j].dir })
		var ns []int
		var results []string
		topos := map[string]bool{}
		censored, unconfirmed := false, false
		for _, l := range g {
			ns = append(ns, l.n)
			results = append(results, l.result)
			topos[l.topology] = true
			if l.kind != "knee" {
				censored = true
			}
			if strings.Contains(l.result, "unconfirmed") {
				unconfirmed = true
			}
		}
		sort.Ints(ns)
		var tl []string
		for t := range topos {
			tl = append(tl, t)
		}
		sort.Strings(tl)
		topo := tl[0]
		if len(tl) > 1 {
			topo = "mixed: " + strings.Join(tl, " | ")
		}
		if err := cw.Write([]string{
			g[0].arm, g[0].scenario, strconv.Itoa(len(g)),
			strconv.Itoa(medianInt(ns)), strconv.Itoa(ns[0]), strconv.Itoa(ns[len(ns)-1]),
			strconv.FormatBool(censored), strconv.FormatBool(unconfirmed), strings.Join(results, "; "), g[0].predicted, topo, label,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	return copyTree(*in, filepath.Join(*out, "legs"))
}
