package topology

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Pin restricts one process to a set of CPUs and fixes its GOMAXPROCS.
type Pin struct {
	// CPUs is a taskset(1) cpu list such as "0,1" or "2-3". Empty means the
	// process is not pinned (and GOMAXPROCS is not set either).
	CPUs string
	// GOMAXPROCS is exported to a pinned process; it must be positive.
	GOMAXPROCS int
}

var cpuListRe = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)

// validCPUList reports whether s is a well-formed taskset cpu list. taskset
// rejects a descending range such as "3-2", which would otherwise surface only
// as a process that exits at spawn on the runner.
func validCPUList(s string) bool {
	if !cpuListRe.MatchString(s) {
		return false
	}
	for _, el := range strings.Split(s, ",") {
		lo, hi, ok := strings.Cut(el, "-")
		if !ok {
			continue
		}
		l, errL := strconv.Atoi(lo)
		h, errH := strconv.Atoi(hi)
		if errL != nil || errH != nil || l > h {
			return false
		}
	}
	return true
}

// pinnedCommand returns the executable, its arguments and the extra
// environment entries for running bin with argv under pin on goos. An empty
// pin is a pass-through. A pinned command is `taskset -c <cpus> bin argv...`
// with GOMAXPROCS=<n>, and only Linux has taskset.
func pinnedCommand(goos, bin string, argv []string, pin Pin) (name string, args, env []string, err error) {
	if pin.CPUs == "" {
		return bin, argv, nil, nil
	}
	if goos != "linux" {
		return "", nil, nil, fmt.Errorf("cpu pinning needs taskset and is linux-only, not %s", goos)
	}
	if !validCPUList(pin.CPUs) {
		return "", nil, nil, fmt.Errorf("invalid cpu list %q", pin.CPUs)
	}
	if pin.GOMAXPROCS <= 0 {
		return "", nil, nil, fmt.Errorf("a pin on cpus %s needs a positive GOMAXPROCS, got %d", pin.CPUs, pin.GOMAXPROCS)
	}
	args = make([]string, 0, len(argv)+3)
	args = append(args, "-c", pin.CPUs, bin)
	args = append(args, argv...)
	return "taskset", args, []string{"GOMAXPROCS=" + strconv.Itoa(pin.GOMAXPROCS)}, nil
}

// Mirrors voice/shard.go, whose constants are unexported.
const (
	minShards = 2
	maxShards = 8
)

// ShardCount is the number of voice shard workers a server with the given
// GOMAXPROCS starts: max(2, min(gomaxprocs, 8)), as voice/shard.go:35-37.
// It is recorded in the bench manifest only; nothing here sets it.
func ShardCount(gomaxprocs int) int {
	return max(minShards, min(gomaxprocs, maxShards))
}
