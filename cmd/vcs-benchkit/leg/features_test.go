package leg

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func hasArg(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

func TestBotArgsByFeatures(t *testing.T) {
	v2 := Features{Version: 2, Set: map[string]bool{}}
	got := BotRunArgs(v2)
	if !hasArg(got, "--bounds", `{"gc_pause_p99":"5ms"}`) || len(got) != 2 {
		t.Errorf("v2 args = %q, want only --bounds {gc_pause_p99:5ms}", got)
	}
	for _, f := range []string{"--ping-every", "--join-rate-max", "--join-rate-scale", "--max-in-flight", "--ramp-talker"} {
		if hasFlag(got, f) {
			t.Errorf("v2 must not get %s (a v2 bot rejects unknown flags)", f)
		}
	}

	v3 := ParseFeatures([]byte(`{"type":"features","schema":"loadbot-features/1","features":["burst","control_lost","gc_warn","join_rate","ping","talker_pause"]}`), nil)
	got = BotRunArgs(v3)
	for flag, val := range map[string]string{
		"--ping-every": "5s", "--join-rate-max": "12", "--join-rate-scale": "5000", "--max-in-flight": "8", "--ramp-talker": "silent",
		"--bounds": `{"gc_pause_p99":"5ms","gc_pause_warn_p99":"2ms"}`,
	} {
		if !hasArg(got, flag, val) {
			t.Errorf("v3 args %q lack %s %s", got, flag, val)
		}
	}

	// Each flag follows its own feature, not the bot's version.
	only := Features{Version: 3, Set: map[string]bool{"ping": true}}
	got = BotRunArgs(only)
	if !hasArg(got, "--ping-every", "5s") || hasFlag(got, "--max-in-flight") || hasFlag(got, "--ramp-talker") || hasFlag(got, "--join-rate-max") {
		t.Errorf("ping-only args = %q", got)
	}
	if !hasArg(got, "--bounds", `{"gc_pause_p99":"5ms"}`) {
		t.Errorf("without gc_warn the bounds carry only the hard limit: %q", got)
	}
}

func TestBotBoundsJSON(t *testing.T) {
	if got := BotBoundsJSON(Features{Version: 2, Set: map[string]bool{}}); got != `{"gc_pause_p99":"5ms"}` {
		t.Errorf("v2 bounds = %s", got)
	}
	if got := BotBoundsJSON(Features{Version: 3, Set: map[string]bool{"gc_warn": true}}); got != `{"gc_pause_p99":"5ms","gc_pause_warn_p99":"2ms"}` {
		t.Errorf("v3 bounds = %s", got)
	}
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestGeneratorEnv(t *testing.T) {
	for _, kind := range []string{"run", "calibrate"} {
		env := GeneratorEnv(kind)
		for _, kv := range []string{"GOGC=off", "GOMEMLIMIT=1GiB", "GRPC_GO_LOG_SEVERITY_LEVEL=info", "GRPC_GO_LOG_VERBOSITY_LEVEL=2"} {
			if !envHas(env, kv) {
				t.Errorf("%s env %q lacks %s", kind, env, kv)
			}
		}
	}
	// The reflector speaks no gRPC: it gets the GC pair and no grpc variables.
	env := GeneratorEnv("reflect")
	if !envHas(env, "GOGC=off") || !envHas(env, "GOMEMLIMIT=1GiB") {
		t.Errorf("reflect env %q lacks the GC pair", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "GRPC_GO_LOG") {
			t.Errorf("reflect env carries %s", e)
		}
	}
	if len(GeneratorEnv("nonsense")) != 0 {
		t.Error("an unknown kind must add nothing")
	}
}

// GOMAXPROCS comes from PinCommand; the generator variables ride on top of it.
func TestProcessEnvCarriesGOMAXPROCSAndGeneratorEnv(t *testing.T) {
	for _, kind := range []string{"run", "calibrate", "reflect"} {
		_, _, pinEnv, err := PinCommand("linux", "vcs-loadbot", []string{kind}, "2-3")
		if err != nil {
			t.Fatal(err)
		}
		env := processEnv([]string{"PATH=/bin", "GOGC=100"}, pinEnv, kind)
		if !envHas(env, "GOMAXPROCS=2") || !envHas(env, "GOGC=off") || !envHas(env, "PATH=/bin") {
			t.Errorf("%s env = %q", kind, env)
		}
		// exec.Cmd keeps the LAST of duplicate keys, so GOGC=off must come after GOGC=100.
		last := ""
		for _, e := range env {
			if strings.HasPrefix(e, "GOGC=") {
				last = e
			}
		}
		if last != "GOGC=off" {
			t.Errorf("%s: last GOGC is %q, want GOGC=off", kind, last)
		}
	}
}

func TestServerEnvIsTheGRPCLogPair(t *testing.T) {
	got := ServerEnv()
	if len(got) != 2 || !envHas(got, "GRPC_GO_LOG_SEVERITY_LEVEL=info") || !envHas(got, "GRPC_GO_LOG_VERBOSITY_LEVEL=2") {
		t.Errorf("ServerEnv = %q", got)
	}
}

func fakeBotScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary")
	}
	p := filepath.Join(t.TempDir(), "fakebot")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProbeFeaturesTimeout(t *testing.T) {
	bin := fakeBotScript(t, "exec sleep 30")
	t0 := time.Now()
	f := ProbeFeatures(bin, 100*time.Millisecond)
	if f.Version != 2 || len(f.Set) != 0 {
		t.Errorf("a probe that hangs is a v2 bot: %+v", f)
	}
	if el := time.Since(t0); el > 5*time.Second {
		t.Errorf("took %v: the timeout did not kill the probe", el)
	}
}

func TestProbeFeaturesParsesV3AndTreatsFailureAsV2(t *testing.T) {
	line := `{"type":"features","schema":"loadbot-features/1","features":["burst","ping"]}`
	f := ProbeFeatures(fakeBotScript(t, "echo '"+line+"'"), 5*time.Second)
	if f.Version != 3 || !f.Has("burst") || !f.Has("ping") {
		t.Errorf("v3 probe = %+v", f)
	}
	f = ProbeFeatures(fakeBotScript(t, "echo 'unknown subcommand features' >&2; exit 2"), 5*time.Second)
	if f.Version != 2 {
		t.Errorf("exit 2 = %+v, want v2", f)
	}
	f = ProbeFeatures(filepath.Join(t.TempDir(), "absent"), 5*time.Second)
	if f.Version != 2 {
		t.Errorf("a missing binary = %+v, want v2", f)
	}
}

func TestBotParamsFlowIntoArgsAndEnv(t *testing.T) {
	p := DefaultBotParams()
	p.PingEvery, p.JoinRateMax, p.JoinRateScale, p.MaxInFlight = 0, 20, 3000, 4
	p.GCPauseP99, p.GCPauseWarnP99, p.GRPCLog = 7*time.Millisecond, 3*time.Millisecond, "off"
	v3 := Features{Version: 3, Set: map[string]bool{"ping": true, "join_rate": true, "talker_pause": true, "gc_warn": true}}
	got := BotRunArgsWith(v3, p)
	for flag, val := range map[string]string{
		"--ping-every": "0s", "--join-rate-max": "20", "--join-rate-scale": "3000", "--max-in-flight": "4", "--ramp-talker": "silent",
		"--bounds": `{"gc_pause_p99":"7ms","gc_pause_warn_p99":"3ms"}`,
	} {
		if !hasArg(got, flag, val) {
			t.Errorf("args %q lack %s %s", got, flag, val)
		}
	}
	for _, kind := range []string{"run", "calibrate", "reflect"} {
		for _, e := range GeneratorEnvWith(kind, "off") {
			if strings.HasPrefix(e, "GRPC_GO_LOG") {
				t.Errorf("%s: grpc-log off must add no GRPC_GO_LOG variable, got %q", kind, e)
			}
		}
		if !envHas(GeneratorEnvWith(kind, "off"), "GOGC=off") {
			t.Errorf("%s: GOGC=off is not tied to --grpc-log", kind)
		}
	}
	if len(ServerEnvWith("off")) != 0 {
		t.Errorf("ServerEnvWith(off) = %q", ServerEnvWith("off"))
	}
	if !envHas(ServerEnvWith("warning"), "GRPC_GO_LOG_SEVERITY_LEVEL=warning") {
		t.Errorf("ServerEnvWith(warning) = %q", ServerEnvWith("warning"))
	}
	// The defaults are what the round-1 wrappers pass.
	if g, w := BotRunArgs(v3), BotRunArgsWith(v3, DefaultBotParams()); strings.Join(g, " ") != strings.Join(w, " ") {
		t.Errorf("BotRunArgs %q != defaults %q", g, w)
	}
}

func TestGeneratorGCOffIsR29(t *testing.T) {
	for _, kind := range []string{"run", "calibrate", "reflect"} {
		if got, want := GeneratorEnvFor(kind, "info", "off"), GeneratorEnvWith(kind, "info"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: off = %q, want %q", kind, got, want)
		}
	}
	if got := GeneratorEnvFor("reflect", "info", "off"); !reflect.DeepEqual(got, []string{"GOGC=off", "GOMEMLIMIT=1GiB"}) {
		t.Errorf("reflect off = %q", got)
	}
}

// W6 kit fix 3 (D1): an empty mode is the Go defaults, no longer GOGC=off.
func TestGeneratorEnvEmptyModeIsDefault(t *testing.T) {
	for _, kind := range []string{"run", "calibrate", "reflect"} {
		for _, e := range GeneratorEnvFor(kind, "info", "") {
			if strings.HasPrefix(e, "GOGC=") || strings.HasPrefix(e, "GOMEMLIMIT=") {
				t.Errorf("%s: empty mode carries %s", kind, e)
			}
		}
	}
	if DefaultBotParams().GeneratorGC != GCDefault {
		t.Errorf("DefaultBotParams().GeneratorGC = %q", DefaultBotParams().GeneratorGC)
	}
	env := processEnvFor([]string{"GOGC=50"}, nil, "run", "info", "")
	for _, e := range env {
		if strings.HasPrefix(e, "GOGC=") {
			t.Errorf("empty mode kept the inherited %s", e)
		}
	}
}

func TestGeneratorGCDefaultMode(t *testing.T) {
	for _, kind := range []string{"run", "calibrate"} {
		env := GeneratorEnvFor(kind, "info", "default")
		for _, e := range env {
			if strings.HasPrefix(e, "GOGC=") || strings.HasPrefix(e, "GOMEMLIMIT=") {
				t.Errorf("%s default env carries %s", kind, e)
			}
		}
		if !envHas(env, "GRPC_GO_LOG_SEVERITY_LEVEL=info") {
			t.Errorf("%s default env %q lost the grpc log env", kind, env)
		}
	}
	if env := GeneratorEnvFor("reflect", "info", "default"); len(env) != 0 {
		t.Errorf("reflect default env = %q, want empty", env)
	}
}

func TestProcessEnvGeneratorGCDefaultStripsInherited(t *testing.T) {
	base := []string{"PATH=/bin", "GOGC=50", "GOMEMLIMIT=2GiB"}
	pin := []string{"GOMAXPROCS=4"}
	env := processEnvFor(base, pin, "run", "info", "default")
	for _, e := range env {
		if strings.HasPrefix(e, "GOGC=") || strings.HasPrefix(e, "GOMEMLIMIT=") {
			t.Errorf("default env still has %s: %q", e, env)
		}
	}
	if !envHas(env, "PATH=/bin") || !envHas(env, "GOMAXPROCS=4") {
		t.Errorf("default env lost PATH or GOMAXPROCS: %q", env)
	}
	if len(base) != 3 || base[1] != "GOGC=50" {
		t.Errorf("base was modified: %q", base)
	}
	env = processEnvFor(base, pin, "run", "info", "off")
	last := ""
	for _, e := range env {
		if strings.HasPrefix(e, "GOGC=") {
			last = e
		}
	}
	if last != "GOGC=off" {
		t.Errorf("off: last GOGC = %q", last)
	}
}

func TestParseGeneratorGC(t *testing.T) {
	for _, ok := range []string{"off", "default"} {
		if err := ParseGeneratorGC(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	err := ParseGeneratorGC("on")
	if err == nil || !strings.Contains(err.Error(), "off") || !strings.Contains(err.Error(), "default") {
		t.Errorf("err = %v, want one naming off and default", err)
	}
}
