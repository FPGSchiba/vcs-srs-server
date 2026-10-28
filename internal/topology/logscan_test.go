package topology

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestErrorLines(t *testing.T) {
	warn := `time=2026-10-28T10:00:00Z level=WARN msg="unknown client"`
	errLine := `time=2026-10-28T10:00:01Z level=ERROR msg="voice server error"`
	race := `WARNING: DATA RACE`
	allow := Allow{regexp.MustCompile(`voice server error`)}

	if got := ErrorLines(warn, nil); len(got) != 0 {
		t.Errorf("WARN line: got %v, want none", got)
	}
	if got := ErrorLines(errLine, nil); len(got) != 1 || got[0] != errLine {
		t.Errorf("ERROR line: got %v, want one", got)
	}
	if got := ErrorLines(errLine, allow); len(got) != 0 {
		t.Errorf("allowed ERROR line: got %v, want none", got)
	}
	if got := ErrorLines(race, Allow{regexp.MustCompile(`.*`)}); len(got) != 1 {
		t.Errorf("data race with allow-all: got %v, want one", got)
	}
	mixed := warn + "\n" + errLine + "\n" + race + "\n"
	if got := ErrorLines(mixed, nil); len(got) != 2 {
		t.Errorf("mixed log: got %v, want two", got)
	}
}

func TestIsBindError(t *testing.T) {
	for _, s := range []string{
		`level=ERROR msg="listen udp 127.0.0.1:5003: bind: address already in use"`,
		`level=ERROR msg="listen udp: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted."`,
		`level=ERROR msg="listen tcp: bind: An attempt was made to access a socket in a way forbidden by its access permissions."`,
	} {
		if !IsBindError(s) {
			t.Errorf("IsBindError(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		`level=ERROR msg="yaml: unmarshal errors"`,
		`level=WARN msg="address already in use"`,
		``,
	} {
		if IsBindError(s) {
			t.Errorf("IsBindError(%q) = true, want false", s)
		}
	}
}

func TestStreamEstablishedQuotedID(t *testing.T) {
	log := `msg="Stream established with voice node" serverId="id 3"
msg="Stream established with voice node" serverId="id4"
`
	got := StreamEstablished(log)
	if !got["id4"] {
		t.Errorf("quoted id4 not captured: %v", got)
	}
	for k := range got {
		if strings.Contains(k, `"`) {
			t.Errorf("captured id retains a quote: %q", k)
		}
	}
}

func TestStreamEstablished(t *testing.T) {
	log := `time=x level=INFO msg="Stream established with voice node" serverId=id1
time=y level=INFO msg="something else" serverId=zzz
time=z level=INFO msg="Stream established with voice node" serverId=id2
`
	want := map[string]bool{"id1": true, "id2": true}
	if got := StreamEstablished(log); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
