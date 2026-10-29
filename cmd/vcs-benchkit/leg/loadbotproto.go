package leg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The load bot's NDJSON protocol types (W6 fix, interface I-1, I-2, I-4, I-5).
// v1 is the bot at 62f5cf7 and v2 adds joins/progress/calibration_stopped and
// the split reflector counters. Every v2 addition is additive, so these types
// decode v1 output too, with absent keys left at their zero value (or nil for
// the pointer counters).

// JoinsSchema is the only value the v2 bot writes in Joins.Schema.
const JoinsSchema = "loadbot-joins/1"

// Joins sources, kit-only. "v2" is a decoded joins line, "v1-error" a join
// failure recovered from a v1 error line, "none" a v1 bot without failures,
// "pending" a SetN before the first hold (no joins line yet).
const (
	JoinsSourceV2      = "v2"
	JoinsSourceV1Error = "v1-error"
	JoinsSourceNone    = "none"
	JoinsSourcePending = "pending"
)

// JoinFailure is one entry of Joins.Failed.
type JoinFailure struct {
	Bot   int    `json:"bot"`
	GUID  string `json:"guid"`
	Stage string `json:"stage"`
	Tries int    `json:"tries"`
	Error string `json:"error"`
}

// RampHealth is the generator's scheduler/GC health over one ramp. Logged only.
type RampHealth struct {
	SchedP99us   int64 `json:"sched_p99_us"`
	GCPauseP99us int64 `json:"gc_pause_p99_us"`
}

// Joins is the `joins` line, written once per accepted set_n (I-1).
type Joins struct {
	Schema          string         `json:"schema"`
	Requested       int            `json:"requested"`
	Had             int            `json:"had"`
	N               int            `json:"n"`
	Joined          int            `json:"joined"`
	JoinFailures    int            `json:"join_failures"`
	FailuresByStage map[string]int `json:"failures_by_stage"`
	HelloTriesHist  []int          `json:"hello_tries_hist"`
	HelloTriesMax   int            `json:"hello_tries_max"`
	Failed          []JoinFailure  `json:"failed"`
	FailedTruncated bool           `json:"failed_truncated"`
	RampS           float64        `json:"ramp_s"`
	RampHealth      RampHealth     `json:"ramp_health"`
	Label           string         `json:"label"`

	// v3 additions (I-12). Absent on a v2 line, so they stay zero/nil there.
	Mode         string     `json:"mode"`
	Talker       string     `json:"talker"`
	JoinsPerS    float64    `json:"joins_per_s"`
	JoinP99Us    int64      `json:"join_p99_us"`
	HelloRetried int        `json:"hello_retried"`
	InFlightMax  int        `json:"in_flight_max"`
	RateLimit    *RateLimit `json:"rate_limit"`

	// Source and BotExited are kit-only, never on the wire.
	Source    string `json:"-"`
	BotExited bool   `json:"-"`
}

// RateLimit is the join rate a ramp ran under (I-12). A burst or an unbounded
// ramp has none (nil).
type RateLimit struct {
	MaxPerS float64 `json:"max_per_s"`
	Scale   float64 `json:"scale"`
}

// joinStages are the four keys failures_by_stage always carries.
var joinStages = []string{"control", "radio", "voice_dial", "hello"}

// helloHistLen is len(DefaultHelloLadder)+1, the length of hello_tries_hist.
const helloHistLen = 6

// maxFailedEntries is the cap on Joins.Failed (I-1).
const maxFailedEntries = 32

// Validate checks the I-1 invariants of a v2 joins line.
func (j Joins) Validate() error {
	if j.Schema != JoinsSchema {
		return fmt.Errorf("joins: schema %q, want %q", j.Schema, JoinsSchema)
	}
	if j.JoinFailures != j.Requested-j.Had-j.Joined {
		return fmt.Errorf("joins: join_failures %d != requested %d - had %d - joined %d",
			j.JoinFailures, j.Requested, j.Had, j.Joined)
	}
	if j.N != j.Had+j.Joined {
		return fmt.Errorf("joins: n %d != had %d + joined %d", j.N, j.Had, j.Joined)
	}
	sum := 0
	for _, k := range joinStages {
		v, ok := j.FailuresByStage[k]
		if !ok {
			return fmt.Errorf("joins: failures_by_stage lacks %q", k)
		}
		sum += v
	}
	if sum != j.JoinFailures {
		return fmt.Errorf("joins: failures_by_stage sums to %d, join_failures is %d", sum, j.JoinFailures)
	}
	if len(j.HelloTriesHist) != helloHistLen {
		return fmt.Errorf("joins: hello_tries_hist has %d entries, want %d", len(j.HelloTriesHist), helloHistLen)
	}
	if j.HelloTriesHist[0] != 0 {
		return fmt.Errorf("joins: hello_tries_hist[0] = %d, want 0", j.HelloTriesHist[0])
	}
	hs := 0
	for _, v := range j.HelloTriesHist {
		hs += v
	}
	if hs != j.Joined {
		return fmt.Errorf("joins: hello_tries_hist sums to %d, joined is %d", hs, j.Joined)
	}
	if len(j.Failed) > maxFailedEntries {
		return fmt.Errorf("joins: failed has %d entries, at most %d", len(j.Failed), maxFailedEntries)
	}
	if want := j.JoinFailures > maxFailedEntries; j.FailedTruncated != want {
		return fmt.Errorf("joins: failed_truncated is %v, want %v for join_failures %d", j.FailedTruncated, want, j.JoinFailures)
	}
	return nil
}

// Progress is the informational `progress` line (I-2). The kit relays it to
// its log and never decides on it.
type Progress struct {
	Phase        string  `json:"phase"`
	Requested    int     `json:"requested,omitempty"`
	Had          int     `json:"had,omitempty"`
	Done         int     `json:"done,omitempty"`
	Joined       int     `json:"joined,omitempty"`
	JoinFailures int     `json:"join_failures,omitempty"`
	InFlight     int     `json:"in_flight,omitempty"`
	Target       int     `json:"target,omitempty"`
	Bots         int     `json:"bots,omitempty"`
	HelloRetried int     `json:"hello_retried"`
	ElapsedS     float64 `json:"elapsed_s"`
	RatePerS     float64 `json:"rate_per_s"` // v3: the current limit, 0 = unbounded
	Talker       string  `json:"talker"`     // v3: "silent" or "live"
}

// CalStopped is the `calibration_stopped` line (I-4).
type CalStopped struct {
	N      int    `json:"n"`
	Bots   int    `json:"bots"`
	Stage  string `json:"stage"`
	Bot    int    `json:"bot"`
	Tries  int    `json:"tries"`
	Reason string `json:"reason"`
}

// ReflectorStats is the reflector's final line (I-5). The split counters are
// pointers so a v1 reflector's absent keys stay nil rather than reading as 0.
type ReflectorStats struct {
	SendErrors     uint64  `json:"send_errors"`
	QueueFullDrops *uint64 `json:"queue_full_drops"`
	WriteErrors    *uint64 `json:"write_errors"`
	Senders        int     `json:"senders"`
}

// joinFailureRE is the I-6 rule: after stripping a leading "loadbot: ", a join
// failure text starts with "bot N: " or "join LDBT-0042: ".
var joinFailureRE = regexp.MustCompile(`^(bot \d+: |join [A-Z0-9]{2,4}-\d{4}: )`)

var botIndexRE = regexp.MustCompile(`^bot (\d+):`)

func stripLoadbotPrefix(s string) string { return strings.TrimPrefix(s, "loadbot: ") }

// IsJoinFailureText reports whether a v1 error text is a join failure (data)
// rather than a fatal bot error.
func IsJoinFailureText(text string) bool {
	return joinFailureRE.MatchString(stripLoadbotPrefix(text))
}

// JoinsFromV1Error derives a Joins from a v1 bot's join-failure error text. A
// v1 bot exits after the error, so BotExited is set. The caller guarantees
// IsJoinFailureText(text); for other text the result is still well-formed with
// stage "control" and bot -1.
func JoinsFromV1Error(requested, had int, text string) Joins {
	t := stripLoadbotPrefix(text)
	bot := -1
	if m := botIndexRE.FindStringSubmatch(t); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			bot = v
		}
	}
	stage := "control"
	switch {
	case strings.Contains(t, ": hello: "):
		stage = "hello"
	case strings.Contains(t, ": dial voice: "):
		stage = "voice_dial"
	case strings.Contains(t, ": update radio: "):
		stage = "radio"
	}
	// I-1: Error is the cause, without the "bot N: <stage>:" prefix.
	cause := joinFailureRE.ReplaceAllString(t, "")
	for _, p := range []string{"hello: ", "dial voice: ", "update radio: "} {
		if stage != "control" && strings.HasPrefix(cause, p) {
			cause = strings.TrimPrefix(cause, p)
			break
		}
	}
	tries := 0
	if strings.Contains(t, "no HELLO_ACK after 5 tries") {
		tries = 5
	}
	by := map[string]int{}
	for _, k := range joinStages {
		by[k] = 0
	}
	by[stage] = 1
	return Joins{
		Schema:          JoinsSchema,
		Requested:       requested,
		Had:             had,
		N:               had,
		JoinFailures:    1,
		FailuresByStage: by,
		HelloTriesHist:  make([]int, helloHistLen),
		HelloTriesMax:   tries,
		Failed:          []JoinFailure{{Bot: bot, Stage: stage, Tries: tries, Error: cause}},
		Source:          JoinsSourceV1Error,
		BotExited:       true,
	}
}

// Features is the result of the `vcs-loadbot features` probe (I-8). Version is
// 3 when the probe parsed and 2 otherwise (a v2 bot has no such subcommand).
type Features struct {
	Version int
	Set     map[string]bool
}

// FeaturesSchema is the only value a v3 bot writes in the features line.
const FeaturesSchema = "loadbot-features/1"

// Has reports whether the bot lists the feature.
func (f Features) Has(name string) bool { return f.Set[name] }

// Names returns the features in ascending order.
func (f Features) Names() []string {
	out := make([]string, 0, len(f.Set))
	for k := range f.Set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParseFeatures interprets the probe's stdout and exit status. Any run error,
// unparseable output, or a line that is not a loadbot-features/1 line is a v2
// bot with no features (I-8).
func ParseFeatures(stdout []byte, runErr error) Features {
	v2 := Features{Version: 2, Set: map[string]bool{}}
	if runErr != nil {
		return v2
	}
	var line struct {
		Type     string   `json:"type"`
		Schema   string   `json:"schema"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &line); err != nil {
		return v2
	}
	if line.Type != "features" || line.Schema != FeaturesSchema {
		return v2
	}
	f := Features{Version: 3, Set: map[string]bool{}}
	for _, k := range line.Features {
		f.Set[k] = true
	}
	return f
}

// StepControlLostType is the `type` of an I-10 line.
const StepControlLostType = "control_lost"

// ControlLostEvent is one dead bot (I-10). BotExited is kit-only: set when the
// bot process itself exited (a v2 hold death), never from the wire.
type ControlLostEvent struct {
	Bot              int     `json:"bot"`
	Name             string  `json:"name"`
	GUID             string  `json:"guid"`
	Cause            string  `json:"cause"`
	Code             string  `json:"code"`
	Message          string  `json:"message"`
	Error            string  `json:"error"`
	Phase            string  `json:"phase"`
	AtUnixMs         int64   `json:"at_unix_ms"`
	AgeS             float64 `json:"age_s"`
	SinceLastOKPingS float64 `json:"since_last_ok_ping_s"`
	PingFailures     int     `json:"ping_failures"`
	LocalAddr        string  `json:"local_addr"`
	DeadTotal        int     `json:"dead_total"`

	BotExited bool `json:"-"`
}

// StepControlLost is the step line's control_lost block (I-11).
type StepControlLost struct {
	DeadTotal         int                `json:"dead_total"`
	DeadInHold        int                `json:"dead_in_hold"`
	DeadSinceLastStep int                `json:"dead_since_last_step"`
	Alive             int                `json:"alive"`
	First             []ControlLostEvent `json:"first"`
}

// StepPing is the step line's ping block (I-11).
type StepPing struct {
	EveryS   float64 `json:"every_s"`
	Sent     uint64  `json:"sent"`
	Failed   uint64  `json:"failed"`
	RTTP99Us int64   `json:"rtt_p99_us"`
}

// controlLostRE is the I-16 rule for a v2 hold death: after stripping a leading
// "loadbot: ", "hold: bot N died: <rest>".
var controlLostRE = regexp.MustCompile(`^hold: bot (\d+) died: (.*)$`)

var rpcErrorRE = regexp.MustCompile(`rpc error: code = (\w+) desc = (.*)`)

// IsControlLostText reports whether a v2 bot's error text is a hold death
// (data) rather than a fatal bot error.
func IsControlLostText(text string) bool {
	return controlLostRE.MatchString(stripLoadbotPrefix(text))
}

// ControlLostFromV2Error derives an event from a v2 hold-death text. The v2 bot
// exits after the error, so BotExited is set. The caller guarantees
// IsControlLostText(text); otherwise bot is -1 and the text is the message.
func ControlLostFromV2Error(text, phase string) ControlLostEvent {
	ev := ControlLostEvent{Bot: -1, Cause: "stream", Code: "Unknown", Phase: phase, SinceLastOKPingS: -1, BotExited: true}
	m := controlLostRE.FindStringSubmatch(stripLoadbotPrefix(text))
	if m == nil {
		ev.Message = text
		return ev
	}
	if v, err := strconv.Atoi(m[1]); err == nil {
		ev.Bot = v
	}
	rest := m[2]
	if r := rpcErrorRE.FindStringSubmatch(rest); r != nil {
		ev.Code, ev.Message = r[1], r[2]
	} else {
		ev.Message = rest
	}
	return ev
}
