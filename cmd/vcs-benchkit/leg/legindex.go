package leg

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// LegIndexSchema is the schema of leg.json.
const LegIndexSchema = "leg-index/1"

// ArmResult is one arm's entry in leg.json: the result text, its kind name
// (kindNameFor, or "error" for an arm that did not finish) and the knee or
// bound N. Nothing here is free text: a failure's message lives in the arm's
// manifest.json, never in the index.
type ArmResult struct {
	Result string `json:"result"`
	Kind   string `json:"kind"`
	N      int    `json:"n"`
}

// kindKnee and kindError are the two kind names Ratios reads.
const (
	kindKnee  = "knee"
	kindError = "error"
)

// LegIndex is leg.json (design I-6): what one leg ran, in which order, what
// each arm found, and the ratios between the arms of this one leg.
type LegIndex struct {
	Schema        string               `json:"schema"`
	Scenario      string               `json:"scenario"`
	Seed          int64                `json:"seed"`
	ArmOrder      []string             `json:"arm_order"`
	Arms          map[string]ArmResult `json:"arms"`
	Ratios        map[string]*float64  `json:"ratios"`
	RatioNotes    map[string]string    `json:"ratio_notes"`
	Label         string               `json:"label"`
	TopologyLabel string               `json:"topology_label"`
}

// RatioKeys are the three within-leg ratios, in report order: each is the
// first arm's knee over the second's.
var RatioKeys = []string{"Seq/S", "D2/S", "D2/Seq"}

// ratioSides splits "Seq/S" into its numerator and denominator arms.
func ratioSides(key string) (num, den string) {
	for i := range key {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// Ratios computes the within-leg ratios of the arms' knees. A ratio exists
// only when both arms found a knee (kind "knee"): a censored bound (generator-,
// memory- or time-bound, bracketed, control-lost), a failed arm or an arm that
// did not run gives null and a note naming which side is why. All three keys are
// always present in the first result; only a null ratio has a note.
func Ratios(arms map[string]ArmResult) (map[string]*float64, map[string]string) {
	ratios := make(map[string]*float64, len(RatioKeys))
	notes := map[string]string{}
	why := func(arm string) string {
		r, ok := arms[arm]
		switch {
		case !ok:
			return arm + " not run"
		case r.Kind == kindError:
			return arm + " error"
		case r.Kind != kindKnee:
			return arm + " censored"
		case r.N <= 0:
			return arm + " knee 0"
		}
		return ""
	}
	for _, key := range RatioKeys {
		num, den := ratioSides(key)
		var reasons []string
		if w := why(den); w != "" {
			reasons = append(reasons, w)
		}
		if w := why(num); w != "" {
			reasons = append(reasons, w)
		}
		if len(reasons) > 0 {
			ratios[key] = nil
			notes[key] = joinReasons(reasons)
			continue
		}
		v := float64(arms[num].N) / float64(arms[den].N)
		ratios[key] = &v
	}
	return ratios, notes
}

func joinReasons(r []string) string { return strings.Join(r, ", ") }

// HardStop is the wall-clock bound of a whole leg: every arm gets its own
// budget plus the 15 minutes the sweep reserves for its confirm, overload and
// teardown (the calibration, run once, comes out of that slack). At least one
// arm's worth.
func HardStop(budget time.Duration, arms int) time.Duration {
	return time.Duration(max(1, arms)) * (budget + 15*time.Minute)
}

// writeRatiosMarkdown appends the within-leg ratio table to the combined
// results.md.
func writeRatiosMarkdown(w io.Writer, idx LegIndex) error {
	p := func(f string, a ...any) { _, _ = fmt.Fprintf(w, f, a...) }
	p("### Within-leg ratios (%s, seed %d)\n\n", idx.Scenario, idx.Seed)
	if err := mdRow(w, "ratio", "value", "note"); err != nil {
		return err
	}
	if err := mdRow(w, "---", "---", "---"); err != nil {
		return err
	}
	for _, k := range RatioKeys {
		v, note := dash, idx.RatioNotes[k]
		if r := idx.Ratios[k]; r != nil {
			v = fmt.Sprintf("%.2f", *r)
		}
		if err := mdRow(w, k, v, note); err != nil {
			return err
		}
	}
	p("\n_Arm order: %s. Ratios are knee over knee within this one leg; a bound is never a ratio. %s_\n", strings.Join(idx.ArmOrder, ", "), LabelOr(idx.Label))
	return nil
}
