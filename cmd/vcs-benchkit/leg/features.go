package leg

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"time"
)

// What the kit asks of the load bot, by capability (design D6, I-8 and I-9).
// The defaults mirror the v3 bot's own, so passing them is explicit rather than
// different; a v2 bot rejects unknown flags, so each one is passed only when
// the bot's `features` line lists the capability behind it.
const (
	botRampTalker = "silent"

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

// BotParams are the tunable bot flags and the generator logging level (R2-1,
// R2-4, R2-8). DefaultBotParams are the round-2 values; the leg flags override
// them one by one.
type BotParams struct {
	// PingEvery is the bot's control-plane Ping period (0 turns it off, which
	// reproduces the round-1 bot for an A/B run).
	PingEvery time.Duration
	// JoinRateMax and JoinRateScale are the join-rate rule min(max, scale/N);
	// MaxInFlight bounds concurrent joins.
	JoinRateMax, JoinRateScale float64
	MaxInFlight                int
	// RampTalker is "silent" (the talker is quiet during a ramp) or "live".
	RampTalker string
	// GCPauseP99 is the hard generator bound, GCPauseWarnP99 the warning.
	GCPauseP99, GCPauseWarnP99 time.Duration
	// GRPCLog is the grpc-go log severity ("info", "warning", "error") or "off".
	GRPCLog string
}

// DefaultBotParams are the round-2 values.
func DefaultBotParams() BotParams {
	return BotParams{
		PingEvery: 5 * time.Second, JoinRateMax: 12, JoinRateScale: 5000, MaxInFlight: 8, RampTalker: botRampTalker,
		GCPauseP99: 5 * time.Millisecond, GCPauseWarnP99: 2 * time.Millisecond, GRPCLog: "info",
	}
}

// botParamsOr returns the defaults for a nil pointer.
func botParamsOr(p *BotParams) BotParams {
	if p == nil {
		return DefaultBotParams()
	}
	return *p
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// BotBoundsJSON is the --bounds value for `run` and `calibrate` with the
// default parameters: the 5 ms hard GC-pause bound for every bot, and the 2 ms
// warning bound when the bot has it.
func BotBoundsJSON(f Features) string { return BotBoundsJSONWith(f, DefaultBotParams()) }

// BotBoundsJSONWith is BotBoundsJSON with explicit parameters.
func BotBoundsJSONWith(f Features, p BotParams) string {
	if f.Has("gc_warn") {
		return `{"gc_pause_p99":"` + p.GCPauseP99.String() + `","gc_pause_warn_p99":"` + p.GCPauseWarnP99.String() + `"}`
	}
	return `{"gc_pause_p99":"` + p.GCPauseP99.String() + `"}`
}

// BotRunArgs are the flags the kit adds to `vcs-loadbot run` on top of the
// round-1 argv, with the default parameters.
func BotRunArgs(f Features) []string { return BotRunArgsWith(f, DefaultBotParams()) }

// BotRunArgsWith is BotRunArgs with explicit parameters. A v2 bot gets only
// --bounds; a v3 bot gets each flag whose feature it lists.
func BotRunArgsWith(f Features, p BotParams) []string {
	var a []string
	if f.Has("ping") {
		a = append(a, "--ping-every", p.PingEvery.String())
	}
	if f.Has("join_rate") {
		a = append(a, "--join-rate-max", fmtFloat(p.JoinRateMax), "--join-rate-scale", fmtFloat(p.JoinRateScale),
			"--max-in-flight", strconv.Itoa(p.MaxInFlight))
	}
	if f.Has("talker_pause") {
		a = append(a, "--ramp-talker", p.RampTalker)
	}
	return append(a, "--bounds", BotBoundsJSONWith(f, p))
}

// grpcLogEnv makes grpc-go say why it closes a transport (for example a
// keepalive ack timeout), so a reset can be attributed to a side. "off" (or "")
// adds nothing.
func grpcLogEnv(level string) []string {
	if level == "" || level == "off" {
		return nil
	}
	return []string{"GRPC_GO_LOG_SEVERITY_LEVEL=" + level, "GRPC_GO_LOG_VERBOSITY_LEVEL=2"}
}

// GeneratorEnv is the generator environment with the default grpc log level.
func GeneratorEnv(kind string) []string { return GeneratorEnvWith(kind, "info") }

// GeneratorEnvWith is the environment the generator processes add (R2-9): no GC
// cycles during a hold on a runner with 16 GB, and grpc transport logging for
// the processes that speak gRPC. kind is "run", "calibrate" or "reflect"; the
// reflector speaks no gRPC and gets only the GC pair. An unknown kind adds
// nothing.
func GeneratorEnvWith(kind, grpcLog string) []string {
	gc := []string{"GOGC=off", "GOMEMLIMIT=1GiB"}
	switch kind {
	case "run", "calibrate":
		return append(gc, grpcLogEnv(grpcLog)...)
	case "reflect":
		return gc
	}
	return nil
}

// ServerEnv is what the kit puts in its own environment so the server inherits
// it (topology's pinnedCommand passes os.Environ()): the grpc transport log.
// Nothing here changes the server binary or its tuning.
func ServerEnv() []string { return ServerEnvWith("info") }

// ServerEnvWith is ServerEnv at an explicit level ("off" adds nothing).
func ServerEnvWith(grpcLog string) []string { return grpcLogEnv(grpcLog) }

// processEnv is a generator process's environment with the default grpc log level.
func processEnv(base, pinEnv []string, kind string) []string {
	return processEnvWith(base, pinEnv, kind, "info")
}

// processEnvWith is a generator process's environment: the base environment,
// then PinCommand's GOMAXPROCS, then the generator variables. exec.Cmd keeps the
// last of duplicate keys, so the kit's values win over an inherited GOGC.
func processEnvWith(base, pinEnv []string, kind, grpcLog string) []string {
	env := append([]string(nil), base...)
	env = append(env, pinEnv...)
	return append(env, GeneratorEnvWith(kind, grpcLog)...)
}
