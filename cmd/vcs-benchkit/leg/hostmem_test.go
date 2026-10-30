package leg

import (
	"errors"
	"strings"
	"testing"
)

const gib = int64(1) << 30

func memReader(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("not found: " + p)
	}
}

const meminfo14 = "MemTotal:       16000000 kB\nMemFree:         100000 kB\nMemAvailable:   14680064 kB\nBuffers: 1 kB\n"

func TestMemFactsMeminfoOnly(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{"/proc/meminfo": meminfo14}))
	if f.Avail != 15032385536 || f.Source != "meminfo" || f.MemAvailable != 15032385536 {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsCgroup2Limit(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{
		"/proc/meminfo":                 meminfo14,
		"/sys/fs/cgroup/memory.max":     "8589934592\n",
		"/sys/fs/cgroup/memory.current": "1073741824\n",
	}))
	if f.Avail != 7*gib || f.Source != "cgroup2" || f.CgroupLimit != 8*gib || f.CgroupUsage != gib {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsCgroup2Max(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{
		"/proc/meminfo":                 meminfo14,
		"/sys/fs/cgroup/memory.max":     "max\n",
		"/sys/fs/cgroup/memory.current": "1073741824\n",
	}))
	if f.Avail != 15032385536 || f.Source != "meminfo" || f.CgroupLimit != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsCgroup1(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{
		"/proc/meminfo": meminfo14,
		"/sys/fs/cgroup/memory/memory.limit_in_bytes": "4294967296\n",
		"/sys/fs/cgroup/memory/memory.usage_in_bytes": "1073741824\n",
	}))
	if f.Avail != 3*gib || f.Source != "cgroup1" {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsCgroup1HugeIsNoLimit(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{
		"/proc/meminfo": meminfo14,
		"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
		"/sys/fs/cgroup/memory/memory.usage_in_bytes": "1073741824\n",
	}))
	if f.Avail != 15032385536 || f.Source != "meminfo" || f.CgroupLimit != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsCgroupLimitLooserThanHost(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{
		"/proc/meminfo":                 meminfo14,
		"/sys/fs/cgroup/memory.max":     "68719476736\n",
		"/sys/fs/cgroup/memory.current": "1073741824\n",
	}))
	if f.Avail != 15032385536 || f.Source != "meminfo" {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsCgroupOnly(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{
		"/sys/fs/cgroup/memory.max":     "8589934592\n",
		"/sys/fs/cgroup/memory.current": "1073741824\n",
	}))
	if f.Avail != 7*gib || f.Source != "cgroup2" {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsUsageOverLimitClampsToZeroKnown(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{
		"/proc/meminfo":                 meminfo14,
		"/sys/fs/cgroup/memory.max":     "1073741824\n",
		"/sys/fs/cgroup/memory.current": "2147483648\n",
	}))
	if f.Avail != 1 || f.Source != "cgroup2" {
		t.Fatalf("a full cgroup must read as ~0 available, not unknown: %+v", f)
	}
}

func TestMemFactsGarbage(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{"/proc/meminfo": "MemAvailable: lots kB\n"}))
	if f.Avail != 0 || f.Source != "none" {
		t.Fatalf("%+v", f)
	}
}

func TestMemFactsNone(t *testing.T) {
	f := ReadMemFacts(memReader(nil))
	if f.Avail != 0 || f.Source != "none" {
		t.Fatalf("%+v", f)
	}
}

func TestMemTop(t *testing.T) {
	const mib = int64(1) << 20
	if got := MemTop(MemFacts{Avail: 14 * gib}, 2*mib, 512*mib, 0.2); got != 5529 {
		t.Fatalf("got %d, want 5529", got)
	}
	if got := MemTop(MemFacts{}, 2*mib, 512*mib, 0.2); got != 0 {
		t.Fatalf("unknown: %d", got)
	}
	if got := MemTop(MemFacts{Avail: 256 * mib}, 2*mib, 512*mib, 0.2); got != -1 {
		t.Fatalf("tiny: %d", got)
	}
	if got := MemTop(MemFacts{Avail: 512 * mib}, 2*mib, 512*mib, 0.2); got != -1 {
		t.Fatalf("== reserve: %d", got)
	}
	if got := MemTop(MemFacts{Avail: 14 * gib}, 0, 512*mib, 0.2); got != 0 {
		t.Fatalf("perBot 0: %d", got)
	}
}

func TestMemFactsDescribe(t *testing.T) {
	f := ReadMemFacts(memReader(map[string]string{"/proc/meminfo": "MemAvailable: 14889779 kB\n"}))
	if d := f.Describe(); d != "MemAvailable 14.2 GiB, cgroup limit none (meminfo)" {
		t.Fatalf("%q", d)
	}
	g := ReadMemFacts(memReader(map[string]string{
		"/sys/fs/cgroup/memory.max":     "8589934592",
		"/sys/fs/cgroup/memory.current": "1073741824",
	}))
	if d := g.Describe(); !strings.Contains(d, "cgroup limit 8.0 GiB") || !strings.Contains(d, "(cgroup2)") || !strings.Contains(d, "MemAvailable unknown") {
		t.Fatalf("%q", d)
	}
	if d := (MemFacts{Source: "none"}).Describe(); !strings.Contains(d, "unknown") {
		t.Fatalf("%q", d)
	}
}
