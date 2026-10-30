package leg

import (
	"fmt"
	"strconv"
	"strings"
)

// MemFacts is what the host can still give the bots, in bytes. 0 means
// unknown. Avail is the smaller of the kernel's MemAvailable and the cgroup
// headroom (limit - usage): inside a container the cgroup limit, not the
// host's RAM, is what the OOM killer enforces.
type MemFacts struct {
	MemAvailable, CgroupLimit, CgroupUsage, Avail int64
	// Source is the term that binds Avail: meminfo, cgroup2, cgroup1, or none.
	Source string
}

// cgroupNoLimit is the threshold above which a cgroup v1 limit is the
// kernel's "unlimited" sentinel (about 2^63 rounded down to a page).
const cgroupNoLimit = int64(1) << 60

// ReadMemFacts reads /proc/meminfo and the cgroup v2 (else v1) memory files
// through read. It never errors: anything unreadable is left unknown.
func ReadMemFacts(read func(string) ([]byte, error)) MemFacts {
	var f MemFacts
	if b, err := read("/proc/meminfo"); err == nil {
		f.MemAvailable = parseMemAvailable(string(b))
	}
	cgSource := ""
	if lim, ok := readCgroupInt(read, "/sys/fs/cgroup/memory.max"); ok && lim > 0 && lim < cgroupNoLimit {
		f.CgroupLimit, cgSource = lim, "cgroup2"
		f.CgroupUsage, _ = readCgroupInt(read, "/sys/fs/cgroup/memory.current")
	} else if lim, ok := readCgroupInt(read, "/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok && lim > 0 && lim < cgroupNoLimit {
		f.CgroupLimit, cgSource = lim, "cgroup1"
		f.CgroupUsage, _ = readCgroupInt(read, "/sys/fs/cgroup/memory/memory.usage_in_bytes")
	}

	f.Source = "none"
	if f.MemAvailable > 0 {
		f.Avail, f.Source = f.MemAvailable, "meminfo"
	}
	if f.CgroupLimit > 0 {
		head := max(f.CgroupLimit-f.CgroupUsage, 1) // full cgroup: ~0, not unknown
		if f.Avail == 0 || head < f.Avail {
			f.Avail, f.Source = head, cgSource
		}
	}
	return f
}

func parseMemAvailable(s string) int64 {
	for _, line := range strings.Split(s, "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		fs := strings.Fields(rest)
		if len(fs) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(fs[0], 10, 64)
		if err != nil || kb <= 0 {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// readCgroupInt parses a one-integer cgroup file. "max" and garbage are not ok.
func readCgroupInt(read func(string) ([]byte, error), path string) (int64, bool) {
	b, err := read(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// MemTop is the most bots the host's memory can hold:
// floor((Avail - reserve) * (1 - margin) / perBot). 0 means unknown (Avail or
// perBot unusable); -1 means Avail does not even cover the reserve.
func MemTop(f MemFacts, perBot, reserve int64, margin float64) int {
	if f.Avail <= 0 || perBot <= 0 {
		return 0
	}
	if f.Avail <= reserve {
		return -1
	}
	return int(float64(f.Avail-reserve) * (1 - margin) / float64(perBot))
}

// Describe is a one-line summary for the leg log and the manifest.
func (f MemFacts) Describe() string {
	ma := "MemAvailable unknown"
	if f.MemAvailable > 0 {
		ma = "MemAvailable " + gibStr(f.MemAvailable)
	}
	cg := "cgroup limit none"
	if f.CgroupLimit > 0 {
		cg = "cgroup limit " + gibStr(f.CgroupLimit) + ", used " + gibStr(f.CgroupUsage)
	}
	return fmt.Sprintf("%s, %s (%s)", ma, cg, f.Source)
}

func gibStr(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/float64(int64(1)<<30)) }
