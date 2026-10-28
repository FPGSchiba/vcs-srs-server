package leg

import (
	"fmt"
	"strconv"
	"strings"
)

// CPUList expands a taskset(1) cpu list such as "0,1", "2-3" or "0-1,4".
func CPUList(s string) ([]int, error) {
	if s == "" {
		return nil, fmt.Errorf("empty cpu list")
	}
	var out []int
	for _, el := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(el, "-")
		l, err := strconv.Atoi(lo)
		if err != nil || l < 0 {
			return nil, fmt.Errorf("cpu list %q: bad element %q", s, el)
		}
		if !isRange {
			out = append(out, l)
			continue
		}
		h, err := strconv.Atoi(hi)
		if err != nil || h < l {
			return nil, fmt.Errorf("cpu list %q: bad range %q", s, el)
		}
		for c := l; c <= h; c++ {
			out = append(out, c)
		}
	}
	return out, nil
}

// PinCommand returns the command line that runs bin with args on the given
// cpu set: `taskset -c <cpus> bin args...` with GOMAXPROCS set to the set's
// size. An empty set is a pass-through. Only Linux has taskset, so a pin on
// any other goos is an error (a Windows run must not look pinned).
func PinCommand(goos, bin string, args []string, cpus string) (name string, argv, env []string, err error) {
	if cpus == "" {
		return bin, args, nil, nil
	}
	if goos != "linux" {
		return "", nil, nil, fmt.Errorf("cpu pinning needs taskset and is linux-only, not %s", goos)
	}
	list, err := CPUList(cpus)
	if err != nil {
		return "", nil, nil, err
	}
	argv = append([]string{"-c", cpus, bin}, args...)
	return "taskset", argv, []string{"GOMAXPROCS=" + strconv.Itoa(len(list))}, nil
}
