package leg

import (
	"fmt"
	"regexp"
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

	// Source and BotExited are kit-only, never on the wire.
	Source    string `json:"-"`
	BotExited bool   `json:"-"`
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
