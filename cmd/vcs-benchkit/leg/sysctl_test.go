package leg

import (
	"errors"
	"strings"
	"testing"
)

type fakeSys struct {
	files map[string]string
	ran   []string
	fail  bool
}

func (f *fakeSys) read(p string) ([]byte, error) {
	if v, ok := f.files[p]; ok {
		return []byte(v + "\n"), nil
	}
	return nil, errors.New("no such file")
}

func (f *fakeSys) run(name string, args ...string) ([]byte, error) {
	f.ran = append(f.ran, name+" "+strings.Join(args, " "))
	if f.fail {
		return []byte("sudo: a password is required"), errors.New("exit status 1")
	}
	return nil, nil
}

func TestRaiseSocketBuffers(t *testing.T) {
	f := &fakeSys{files: map[string]string{
		"/proc/sys/net/core/rmem_max": "1048576", "/proc/sys/net/core/wmem_max": "4194304",
		"/proc/sys/net/core/rmem_default": "1048576",
	}}
	rec := RaiseSocketBuffers(f.read, f.run, 8<<20)
	if rec["rmem_max_before"] != "1048576" || rec["rmem_max_after"] != "8388608" {
		t.Errorf("rec = %v", rec)
	}
	// wmem_max (4 MiB) is raised too; rmem_default is never touched (design
	// 6.3: it would change the server's effective buffers).
	for _, c := range f.ran {
		if strings.Contains(c, "default") {
			t.Fatalf("touched a default buffer: %q", c)
		}
	}
	if len(f.ran) != 2 {
		t.Errorf("commands = %q", f.ran)
	}
}

func TestRaiseSocketBuffersLeavesLargeValuesAlone(t *testing.T) {
	f := &fakeSys{files: map[string]string{
		"/proc/sys/net/core/rmem_max": "16777216", "/proc/sys/net/core/wmem_max": "16777216",
	}}
	rec := RaiseSocketBuffers(f.read, f.run, 8<<20)
	if len(f.ran) != 0 {
		t.Errorf("lowered or rewrote a larger value: %q", f.ran)
	}
	if rec["rmem_max_after"] != "16777216" {
		t.Errorf("rec = %v", rec)
	}
}

func TestRaiseSocketBuffersFailureIsRecordedNotFatal(t *testing.T) {
	f := &fakeSys{files: map[string]string{
		"/proc/sys/net/core/rmem_max": "1048576", "/proc/sys/net/core/wmem_max": "212992",
	}, fail: true}
	rec := RaiseSocketBuffers(f.read, f.run, 8<<20)
	if !strings.Contains(rec["rmem_max_error"], "password") {
		t.Errorf("rec = %v", rec)
	}
	if rec["rmem_max_after"] != "1048576" {
		t.Errorf("a failed raise must report the unchanged value: %v", rec)
	}
}

func TestRaiseSocketBuffersUnreadable(t *testing.T) {
	f := &fakeSys{files: map[string]string{}}
	rec := RaiseSocketBuffers(f.read, f.run, 8<<20)
	if rec["rmem_max_error"] == "" || len(f.ran) != 0 {
		t.Errorf("rec = %v ran = %q", rec, f.ran)
	}
}
