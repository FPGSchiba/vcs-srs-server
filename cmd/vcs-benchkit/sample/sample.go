// Package sample holds pure parsers for the Linux counters the bench kit
// samples: /proc/<pid>/stat, /proc/stat and `ss -uamp`. They take bytes and
// return values; reading files and running ss is the caller's job.
package sample

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// CPUTimes is one cpu line of /proc/stat, in clock ticks (proc_stat(5)).
// Guest columns are already counted inside User and Nice, so they are not kept.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// ParsePIDStat returns utime and stime (fields 14 and 15) from the contents of
// /proc/<pid>/stat. The comm field may contain spaces and parentheses, so the
// fields are split after the last ')' (proc_pid_stat(5)).
func ParsePIDStat(b []byte) (utime, stime uint64, err error) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("pid stat: no closing paren after comm")
	}
	// rest[0] is field 3 (state), so utime (14) is rest[11] and stime (15) rest[12].
	rest := strings.Fields(string(b[i+1:]))
	if len(rest) < 13 {
		return 0, 0, fmt.Errorf("pid stat: %d fields after comm, need at least 13", len(rest))
	}
	if utime, err = strconv.ParseUint(rest[11], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("pid stat utime: %w", err)
	}
	if stime, err = strconv.ParseUint(rest[12], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("pid stat stime: %w", err)
	}
	return utime, stime, nil
}

// ParseProcStat parses the "cpu" and "cpuN" lines of /proc/stat, keyed by the
// first token ("cpu" is the aggregate). A line with 7 numbers (a kernel without
// the steal column) parses with Steal = 0. Fewer than 7 is an error.
func ParseProcStat(b []byte) (map[string]CPUTimes, error) {
	out := map[string]CPUTimes{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !isCPUName(f[0]) {
			continue
		}
		nums := f[1:]
		if len(nums) < 7 {
			return nil, fmt.Errorf("proc stat: %s has %d columns, need at least 7", f[0], len(nums))
		}
		var v [8]uint64
		for i := 0; i < len(v) && i < len(nums); i++ {
			n, err := strconv.ParseUint(nums[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("proc stat: %s column %d: %w", f[0], i+1, err)
			}
			v[i] = n
		}
		out[f[0]] = CPUTimes{User: v[0], Nice: v[1], System: v[2], Idle: v[3], IOWait: v[4], IRQ: v[5], SoftIRQ: v[6], Steal: v[7]}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("proc stat: no cpu lines")
	}
	return out, nil
}

func isCPUName(s string) bool {
	if s == "cpu" {
		return true
	}
	if !strings.HasPrefix(s, "cpu") || len(s) == 3 {
		return false
	}
	_, err := strconv.ParseUint(s[3:], 10, 32)
	return err == nil
}

// ParseSSDrops reads `ss -uamp` output and returns, per local "addr:port", the
// d<n> value of the skmem line (the socket's drop counter, ss(8)). Sockets
// sharing a local address are summed. A socket whose skmem has no d<n> is
// listed with 0. The header line, a leading Netid column, and skmem either on
// its own continuation line or on the socket line itself are tolerated.
//
// An EMPTY map means the output was unparseable, not that there were zero
// drops: a caller must treat a missing key for a socket it expects (for example
// a bot socket, D14) as an error or as an invalid step, never as 0.
//
// The d<n> value is an unsigned 32-bit kernel counter; diff two samples with
// DropDelta, not plain subtraction.
func ParseSSDrops(out []byte) map[string]uint64 {
	res := map[string]uint64{}
	var local string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if i := strings.Index(line, "skmem:("); i >= 0 && local != "" {
				res[local] += skmemDrops(line[i+len("skmem:("):])
				local = ""
			}
			continue
		}
		local = socketLocal(strings.Fields(line))
		if local != "" {
			if _, ok := res[local]; !ok {
				res[local] = 0
			}
			// Some ss versions print skmem on the socket line itself.
			if i := strings.Index(line, "skmem:("); i >= 0 {
				res[local] += skmemDrops(line[i+len("skmem:("):])
				local = ""
			}
		}
	}
	return res
}

// socketLocal finds the local address in a socket line: it follows the state
// and the two numeric queue columns, with or without a leading Netid column.
func socketLocal(f []string) string {
	for i := 0; i+3 < len(f) && i < 2; i++ {
		if isUint(f[i+1]) && isUint(f[i+2]) {
			return f[i+3]
		}
	}
	return ""
}

func isUint(s string) bool {
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

func skmemDrops(s string) uint64 {
	if i := strings.IndexByte(s, ')'); i >= 0 {
		s = s[:i]
	}
	for _, tok := range strings.Split(s, ",") {
		if len(tok) > 1 && tok[0] == 'd' {
			if n, err := strconv.ParseUint(tok[1:], 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// DropDelta is the number of drops between two ParseSSDrops samples of one
// socket. d<n> is an unsigned 32-bit counter in the kernel, so a cur below prev
// is a wrap, not a decrease.
func DropDelta(prev, cur uint64) uint64 {
	return uint64(uint32(cur) - uint32(prev))
}
