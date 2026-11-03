// Package sanitize is scan 1 of the fail-closed publish gate: it rejects any
// file under a directory that carries an address, host or path that must not
// reach the public results site. Scan 2 (.github/scripts/bench-scan.sh) is an
// independent shell+jq implementation checked against the same fixtures.
package sanitize

import (
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is one rejected line. It never carries the matched text, so a scan
// log cannot leak the value it rejected.
type Finding struct {
	File string
	Line int
	Rule string
}

func (f Finding) String() string { return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Rule) }

var (
	reIPv4 = regexp.MustCompile(`(?:^|[^0-9.])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?:$|[^0-9.])`)
	reTok  = regexp.MustCompile(`[0-9A-Fa-f:.%]+`)
)

var literals = []struct{ rule, text string }{
	{"cloudapp-host", ".internal.cloudapp.net"},
	{"runner-host", "fv-az"},
	{"runner-path", "/home/runner"},
}

// Scan checks every regular file under dir. extra holds literal strings
// (hostname, RUNNER_* and GITHUB_TOKEN values); empty entries are ignored.
func Scan(dir string, extra []string) []Finding {
	var out []Finding
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			out = append(out, Finding{File: p, Rule: "unreadable"})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			out = append(out, Finding{File: rel, Rule: "unreadable"})
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, rule := range scanLine(line, extra) {
				out = append(out, Finding{File: rel, Line: i + 1, Rule: rule})
			}
		}
		return nil
	})
	return out
}

func scanLine(line string, extra []string) []string {
	var rules []string
	if reIPv4.MatchString(line) {
		rules = append(rules, "ipv4-literal")
	}
	for _, tok := range reTok.FindAllString(line, -1) {
		if strings.Count(tok, ":") < 2 {
			continue
		}
		if _, err := netip.ParseAddr(tok); err == nil {
			rules = append(rules, "ipv6-literal")
			break
		}
	}
	for _, l := range literals {
		if strings.Contains(line, l.text) {
			rules = append(rules, l.rule)
		}
	}
	for _, e := range extra {
		if e != "" && strings.Contains(line, e) {
			rules = append(rules, "extra-literal")
		}
	}
	return rules
}
