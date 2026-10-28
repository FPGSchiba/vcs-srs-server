package leg

import (
	"reflect"
	"strings"
	"testing"
)

func TestCPUList(t *testing.T) {
	cases := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"0,1", []int{0, 1}, false},
		{"2-3", []int{2, 3}, false},
		{"0-1,4,6-7", []int{0, 1, 4, 6, 7}, false},
		{"3", []int{3}, false},
		{"", nil, true},
		{"3-2", nil, true},
		{"a", nil, true},
		{"0,,1", nil, true},
	}
	for _, c := range cases {
		got, err := CPUList(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("CPUList(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && !reflect.DeepEqual(got, c.want) {
			t.Errorf("CPUList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPinCommand(t *testing.T) {
	name, args, env, err := PinCommand("linux", "/b/vcs-loadbot", []string{"run", "--seed", "1"}, "2,3")
	if err != nil {
		t.Fatal(err)
	}
	if name != "taskset" || strings.Join(args, " ") != "-c 2,3 /b/vcs-loadbot run --seed 1" {
		t.Errorf("argv = %s %v", name, args)
	}
	if !reflect.DeepEqual(env, []string{"GOMAXPROCS=2"}) {
		t.Errorf("env = %v", env)
	}
	// An empty set is a pass-through, on any OS.
	name, args, env, err = PinCommand("windows", "b.exe", []string{"x"}, "")
	if err != nil || name != "b.exe" || len(args) != 1 || env != nil {
		t.Errorf("unpinned = %s %v %v %v", name, args, env, err)
	}
	if _, _, _, err = PinCommand("windows", "b.exe", nil, "0"); err == nil {
		t.Error("pinning on windows must be an error")
	}
	if _, _, _, err = PinCommand("linux", "b", nil, "3-2"); err == nil {
		t.Error("a descending range must be an error")
	}
}
