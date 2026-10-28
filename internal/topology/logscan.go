package topology

import (
	"regexp"
	"strings"
)

// Allow lists regexps for ERROR lines a test expects. A race report is
// never allowable.
type Allow []*regexp.Regexp

func (a Allow) matches(line string) bool {
	for _, re := range a {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// ErrorLines returns every line with level=ERROR that no allow regexp
// matches, plus every "WARNING: DATA RACE" line (never allowable).
func ErrorLines(log string, allow Allow) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		switch {
		case strings.Contains(line, "WARNING: DATA RACE"):
			out = append(out, line)
		case strings.Contains(line, "level=ERROR") && !allow.matches(line):
			out = append(out, line)
		}
	}
	return out
}

var bindErrStrings = []string{
	"address already in use",
	"Only one usage of each socket address",
	"forbidden by its access permissions",
}

// IsBindError reports whether some level=ERROR line is a port bind failure
// (the strings the client fixture uses to retry on a lost port race).
func IsBindError(log string) bool {
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "level=ERROR") {
			continue
		}
		for _, s := range bindErrStrings {
			if strings.Contains(line, s) {
				return true
			}
		}
	}
	return false
}

var streamRe = regexp.MustCompile(`msg="Stream established with voice node" serverId=(?:"([^"]*)"|(\S+))`)

// StreamEstablished returns the node ids that control logged a
// "Stream established with voice node" line for.
func StreamEstablished(log string) map[string]bool {
	out := map[string]bool{}
	for _, m := range streamRe.FindAllStringSubmatch(log, -1) {
		id := m[1]
		if id == "" {
			id = m[2]
		}
		out[id] = true
	}
	return out
}
