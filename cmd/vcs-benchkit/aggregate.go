package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
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

// legInfo is everything the aggregate shows about one leg directory. A leg is
// any directory holding a knees.csv or a manifest.json: a leg that aborted has
// the manifest (with its error) and no knees.csv, and it must still be listed.
type legInfo struct {
	dir   string
	knee  *legKnee
	arm   string
	scen  string
	err   string
	calib string // "102350 (lower bound)" or "-"
	steps string // "1038:pass 2076:breach" or "-"
	// ctlLost is the number of steps with a dead bot, bursts the join failures of
	// the silent and the live join burst ("0/30"). Both are "-" when steps.csv
	// predates round 2 or never ran a burst: a dash, never a made-up zero.
	ctlLost, bursts string
}

// readColumns reads a CSV into one map per data row, keyed by header name.
func readColumns(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	var rows []map[string]string
	for _, r := range recs[1:] {
		m := map[string]string{}
		for i, h := range recs[0] {
			if i < len(r) {
				m[h] = r[i]
			}
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// readLegInfo gathers the optional per-leg files. An unreadable optional file
// shows as "-": the table is a log aid and must never fail a run.
func readLegInfo(root, dir string, kneeFile bool) (legInfo, error) {
	d := filepath.Join(root, filepath.FromSlash(dir))
	li := legInfo{dir: dir, calib: "-", steps: "-", ctlLost: "-", bursts: "-"}
	if kneeFile {
		lk, err := readKnees(filepath.Join(d, "knees.csv"), dir)
		if err != nil {
			return li, err
		}
		li.knee, li.arm, li.scen = &lk, lk.arm, lk.scenario
	}
	if b, err := os.ReadFile(filepath.Join(d, "manifest.json")); err == nil {
		var m struct {
			Arm      string `json:"arm"`
			Scenario string `json:"scenario"`
			Error    string `json:"error"`
		}
		if json.Unmarshal(b, &m) == nil {
			li.err = m.Error
			if li.arm == "" {
				li.arm, li.scen = m.Arm, m.Scenario
			}
		}
	}
	if rows, err := readColumns(filepath.Join(d, "calibration.csv")); err == nil {
		stop := ""
		for _, r := range rows {
			// calibration.csv has one stopped row and one receive_capacity row;
			// if a file ever held several, the last of each wins.
			if r["kind"] == "stopped" {
				stop = "stopped at " + r["n"]
				if r["violations"] != "" {
					stop += ": " + r["violations"]
				}
			}
			if r["kind"] == "receive_capacity" && r["egress_pps"] != "" {
				li.calib = r["egress_pps"]
				if r["lower_bound"] == "true" {
					li.calib += " (lower bound)"
				}
			}
		}
		if stop != "" {
			if li.calib == "-" {
				li.calib = stop
			} else {
				li.calib += " (" + stop + ")"
			}
		}
	}
	if rows, err := readColumns(filepath.Join(d, "steps.csv")); err == nil {
		var parts []string
		lost, haveLost := 0, false
		silent, live := "-", "-"
		for _, r := range rows {
			parts = append(parts, r["n"]+":"+r["outcome"])
			if v, ok := r["control_lost"]; ok {
				haveLost = true
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					lost++
				}
			}
			switch r["burst_talker"] {
			case "silent":
				silent = dash(r["join_failures"])
			case "live":
				live = dash(r["join_failures"])
			}
		}
		if len(parts) > 0 {
			li.steps = strings.Join(parts, " ")
		}
		if haveLost {
			li.ctlLost = strconv.Itoa(lost)
		}
		if silent != "-" || live != "-" {
			li.bursts = silent + "/" + live
		}
	}
	return li, nil
}

// mdCell makes s safe inside a Markdown table cell.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// aggRow is one arm x scenario line of aggregate.csv, kept for the markdown.
type aggRow struct {
	arm, scenario             string
	repeats, median, min, max int
	censored, unconfirmed     bool
}

// renderAggregateMarkdown builds aggregate.md: one row per leg directory, so a
// failed leg is visible, then the statistics table when any leg finished.
func renderAggregateMarkdown(legs []legInfo, rows []aggRow) string {
	var b bytes.Buffer
	finished := 0
	for _, l := range legs {
		if l.knee != nil {
			finished++
		}
	}
	fmt.Fprintf(&b, "### Aggregate: %d of %d legs finished\n\n", finished, len(legs))
	b.WriteString("| leg | result | N | kind | calibration pps | steps (N:outcome) | control lost (steps) | bursts (silent/live failures) | error |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, l := range legs {
		result, n, kind := "did not finish", "-", "-"
		if l.knee != nil {
			result, n, kind = l.knee.result, strconv.Itoa(l.knee.n), dash(l.knee.kind)
		}
		errText := l.err
		if l.knee == nil && errText == "" {
			errText = "no knees.csv"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			mdCell(l.dir), mdCell(result), n, mdCell(kind), mdCell(l.calib), mdCell(l.steps), mdCell(l.ctlLost), mdCell(l.bursts), mdCell(errText))
	}
	if len(rows) > 0 {
		b.WriteString("\n| arm | scenario | repeats | median N | min N | max N | censored | unconfirmed |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %t | %t |\n",
				mdCell(r.arm), mdCell(r.scenario), r.repeats, r.median, r.min, r.max, r.censored, r.unconfirmed)
		}
	}
	fmt.Fprintf(&b, "\n_%s_\n", label)
	return b.String()
}

// runAggregate merges the legs under --in (one <repeat>-<scenario> directory
// each) into aggregate.csv and aggregate.md, copies every leg under
// <out>/legs, and prints aggregate.md to stdout so the run log shows it.
// A leg without knees.csv did not finish: it is listed in aggregate.md with
// its manifest error, and when no leg finished the run still fails, after the
// table is written.
// A censored leg (generator-bound, time-bound, below range, control-lost) contributes its
// bound as n and sets censored=true, so the statistics are lower bounds then.
// A leg whose knee failed its confirm step keeps result_kind=knee; it sets
// unconfirmed=true instead, so the flag is not buried in the results text.
func runAggregate(args []string, stdout, stderr io.Writer) error {
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
	dirs := map[string]bool{} // leg dir -> has knees.csv
	err := filepath.WalkDir(*in, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (d.Name() != "knees.csv" && d.Name() != "manifest.json") {
			return err
		}
		rel, err := filepath.Rel(*in, filepath.Dir(p))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dirs[rel] = dirs[rel] || d.Name() == "knees.csv"
		return nil
	})
	if err != nil {
		return err
	}
	var names []string
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	var infos []legInfo
	var legs []legKnee
	for _, d := range names {
		li, err := readLegInfo(*in, d, dirs[d])
		if err != nil {
			return err
		}
		infos = append(infos, li)
		if li.knee != nil {
			legs = append(legs, *li.knee)
		}
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
	var rows []aggRow
	if len(legs) > 0 {
		if err := writeAggregateCSV(*out, groups, keys, &rows); err != nil {
			return err
		}
	}
	md := renderAggregateMarkdown(infos, rows)
	if err := os.WriteFile(filepath.Join(*out, "aggregate.md"), []byte(md), 0o644); err != nil {
		return err
	}
	if err := copyTree(*in, filepath.Join(*out, "legs")); err != nil {
		return err
	}
	fmt.Fprint(stdout, md)
	if len(legs) == 0 {
		return fmt.Errorf("aggregate: no knees.csv under %s", *in)
	}
	return nil
}

// writeAggregateCSV writes aggregate.csv and appends one aggRow per group.
func writeAggregateCSV(out string, groups map[string][]legKnee, keys []string, rows *[]aggRow) error {
	f, err := os.Create(filepath.Join(out, "aggregate.csv"))
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
		med := medianInt(ns)
		if err := cw.Write([]string{
			g[0].arm, g[0].scenario, strconv.Itoa(len(g)),
			strconv.Itoa(med), strconv.Itoa(ns[0]), strconv.Itoa(ns[len(ns)-1]),
			strconv.FormatBool(censored), strconv.FormatBool(unconfirmed), strings.Join(results, "; "), g[0].predicted, topo, label,
		}); err != nil {
			return err
		}
		*rows = append(*rows, aggRow{g[0].arm, g[0].scenario, len(g), med, ns[0], ns[len(ns)-1], censored, unconfirmed})
	}
	cw.Flush()
	return cw.Error()
}
