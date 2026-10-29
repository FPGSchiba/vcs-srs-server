package leg

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// What the kit asks of the load bot, by capability (design D6, I-8 and I-9).
// The defaults mirror the v3 bot's own, so passing them is explicit rather than
// different; a v2 bot rejects unknown flags, so each one is passed only when
// the bot's `features` line lists the capability behind it.
const (
	botGCPauseP99     = "5ms" // R2-8: hard bound (was 2ms)
	botGCPauseWarnP99 = "2ms" // R2-8: warning only
	botPingEvery      = "5s"  // the real client's connhealth period
	botJoinRateMax    = "12"
	botJoinRateScale  = "5000"
	botMaxInFlight    = "8"
	botRampTalker     = "silent"

	// ProbeTimeout bounds `vcs-loadbot features` (I-8).
	ProbeTimeout = 10 * time.Second
)

// ProbeFeatures runs `<bin> features` once and returns what the bot can do. A
// non-zero exit, a timeout, a missing binary or unparseable output is a v2 bot
// with no features (I-8): the probe never fails the leg.
func ProbeFeatures(bin string, timeout time.Duration) Features {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "features")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// A killed probe must not wait for a grandchild that still holds the pipe.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	return ParseFeatures(stdout.Bytes(), err)
}

// BotBoundsJSON is the --bounds value for `run` and `calibrate`: the 5 ms hard
// GC-pause bound for every bot, and the 2 ms warning bound when the bot has it.
func BotBoundsJSON(f Features) string {
	if f.Has("gc_warn") {
		return `{"gc_pause_p99":"` + botGCPauseP99 + `","gc_pause_warn_p99":"` + botGCPauseWarnP99 + `"}`
	}
	return `{"gc_pause_p99":"` + botGCPauseP99 + `"}`
}

// BotRunArgs are the flags the kit adds to `vcs-loadbot run` on top of the
// round-1 argv. A v2 bot gets only --bounds; a v3 bot gets each flag whose
// feature it lists.
func BotRunArgs(f Features) []string {
	var a []string
	if f.Has("ping") {
		a = append(a, "--ping-every", botPingEvery)
	}
	if f.Has("join_rate") {
		a = append(a, "--join-rate-max", botJoinRateMax, "--join-rate-scale", botJoinRateScale, "--max-in-flight", botMaxInFlight)
	}
	if f.Has("talker_pause") {
		a = append(a, "--ramp-talker", botRampTalker)
	}
	return append(a, "--bounds", BotBoundsJSON(f))
}

// grpcLogEnv makes grpc-go say why it closes a transport (for example a
// keepalive ack timeout), so a reset can be attributed to a side.
func grpcLogEnv() []string {
	return []string{"GRPC_GO_LOG_SEVERITY_LEVEL=info", "GRPC_GO_LOG_VERBOSITY_LEVEL=2"}
}

// GeneratorEnv is the environment the generator processes add (R2-9): no GC
// cycles during a hold on a runner with 16 GB, and grpc transport logging for
// the processes that speak gRPC. kind is "run", "calibrate" or "reflect"; the
// reflector speaks no gRPC and gets only the GC pair. An unknown kind adds
// nothing.
func GeneratorEnv(kind string) []string {
	gc := []string{"GOGC=off", "GOMEMLIMIT=1GiB"}
	switch kind {
	case "run", "calibrate":
		return append(gc, grpcLogEnv()...)
	case "reflect":
		return gc
	}
	return nil
}

// ServerEnv is what the kit puts in its own environment so the server inherits
// it (topology's pinnedCommand passes os.Environ()): the grpc transport log.
// Nothing here changes the server binary or its tuning.
func ServerEnv() []string { return grpcLogEnv() }

// processEnv is a generator process's environment: the base environment, then
// PinCommand's GOMAXPROCS, then the generator variables. exec.Cmd keeps the last
// of duplicate keys, so the kit's values win over an inherited GOGC.
func processEnv(base, pinEnv []string, kind string) []string {
	env := append([]string(nil), base...)
	env = append(env, pinEnv...)
	return append(env, GeneratorEnv(kind)...)
}
