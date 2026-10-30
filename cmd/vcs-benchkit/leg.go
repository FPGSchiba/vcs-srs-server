package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/leg"
)

// parseLegFlags turns `vcs-benchkit leg` arguments into leg.Options.
func parseLegFlags(args []string, errw io.Writer) (leg.Options, error) {
	fs := flag.NewFlagSet("leg", flag.ContinueOnError)
	fs.SetOutput(errw)
	var o leg.Options
	fs.StringVar(&o.ServerBin, "server-bin", "", "headless server binary (required)")
	fs.StringVar(&o.LoadbotBin, "loadbot-bin", "", "vcs-loadbot binary (required)")
	fs.StringVar(&o.Scenario, "scenario", "one-big-net", "scenario: one-big-net or org-op (org-op needs a bot with the org_op feature)")
	fs.StringVar(&o.Arms, "arms", "S", "arms to run: comma list of S, Seq, D2, in a seeded order on one calibration (Seq and D2 need a 2-cpu server set; D2 needs a bot with arm_d2)")
	fs.Int64Var(&o.Seed, "seed", 1, "seed")
	fs.StringVar(&o.OutDir, "out", "", "output directory (required)")
	fs.StringVar(&o.Q6, "q6", "", "the user's Q6 CPU-split decision, recorded verbatim in the manifest")
	fs.StringVar(&o.ProbeFile, "probe", "", "gate-0 env-probe.json (cpu sets and topology label)")
	fs.StringVar(&o.ServerSet, "server-set", "", "server cpu set (overrides --probe)")
	fs.StringVar(&o.GenSet, "gen-set", "", "generator cpu set (overrides --probe)")
	fs.StringVar(&o.Label, "label", leg.Label, "label written to the CSVs, results.md and manifest; must say \"relative only\"")
	fs.StringVar(&o.Topology, "topology", "", "topology label (overrides --probe)")
	fs.StringVar(&o.PredictFile, "predict", "", "predict.json from `vcs-benchkit predict`, context only")
	fs.IntVar(&o.PredictedKnee, "predicted-knee", 0, "B-0 predicted knee, context only (overrides --predict); no decision reads it")
	fs.IntVar(&o.NMaxCap, "n-max", 0, "extra cap on N_max (0 = the time and port budgets only)")
	fs.StringVar(&o.ServerSHA, "server-sha", "", "server commit, recorded in the manifest")
	fs.StringVar(&o.ClientSHA, "client-sha", "", "client commit, recorded in the manifest")
	fs.DurationVar(&o.Budget, "budget", 100*time.Minute, "time budget per arm (0 = unlimited); the leg is hard-stopped at arms x (budget + 15m)")
	fs.Float64Var(&o.JoinRate, "join-rate", 27, "assumed joins per second (design 6.4: ~27/s on one vCPU)")
	fs.DurationVar(&o.Warmup, "warmup", 30*time.Second, "warm-up hold (discarded)")
	fs.DurationVar(&o.Hold, "hold", 40*time.Second, "measured hold per step")
	fs.DurationVar(&o.Confirm, "confirm", 120*time.Second, "confirm hold at the knee")
	fs.DurationVar(&o.Overload, "overload", 60*time.Second, "overload hold at 2x the knee")
	fs.StringVar(&o.CalSteps, "cal-steps", "64,128,256,512,1024,1536,1792,2048,4096", "calibration bot counts, ascending")
	fs.DurationVar(&o.CalHold, "cal-hold", 3*time.Second, "calibration seconds per step")
	fs.BoolVar(&o.Sysctl, "sysctl", true, "raise net.core.rmem_max/wmem_max with sudo (never the defaults)")
	d := leg.DefaultBotParams()
	fs.IntVar(&o.RampChunk, "ramp-chunk", 250, "split a ramp into chunks of at most this many bots, stopping at the first failing chunk (0 = one set_n per step)")
	fs.Float64Var(&o.Bot.JoinRateMax, "join-rate-max", d.JoinRateMax, "bot join rate cap, joins per second: min(max, scale/N)")
	fs.Float64Var(&o.Bot.JoinRateScale, "join-rate-scale", d.JoinRateScale, "bot join rate scale: min(max, scale/N)")
	fs.IntVar(&o.Bot.MaxInFlight, "max-in-flight", d.MaxInFlight, "bot joins in flight")
	fs.DurationVar(&o.Bot.PingEvery, "ping-every", d.PingEvery, "bot control-plane Ping period (0 = off, the round-1 bot)")
	fs.IntVar(&o.BurstAdd, "burst-add", 150, "bots added by one join burst")
	fs.IntVar(&o.BurstInFlight, "burst-in-flight", 16, "joins in flight during a join burst")
	fs.BoolVar(&o.Bursts, "bursts", true, "run the join-burst phase after the sweep")
	fs.DurationVar(&o.Bot.GCPauseP99, "gc-pause-p99", d.GCPauseP99, "generator GC-pause p99 hard bound (invalidates a step)")
	fs.DurationVar(&o.Bot.GCPauseWarnP99, "gc-pause-warn-p99", d.GCPauseWarnP99, "generator GC-pause p99 warning bound")
	fs.StringVar(&o.Bot.GRPCLog, "grpc-log", d.GRPCLog, "grpc-go log severity for the server and the bot (info, warning, error; off disables)")
	fs.StringVar(&o.Bot.GeneratorGC, "generator-gc", d.GeneratorGC, "generator GC env: default (inherited GOGC/GOMEMLIMIT stripped, Go defaults) or off (GOGC=off GOMEMLIMIT=1GiB, R2-9)")
	fs.Float64Var(&o.JoinRateFloor, "join-rate-floor", 4, "minimum bot joins/s at any step (anchors --join-rate-scale to max(scale, floor*top); 0 = the R2-4 rule)")
	fs.Float64Var(&o.MemPerBotMiB, "mem-per-bot-mib", 2, "memory one bot costs, MiB (measured 1.8 MB, rounded up); must be > 0")
	fs.Float64Var(&o.MemMargin, "mem-margin", 0.2, "fraction of available memory left unused, in [0,1)")
	fs.IntVar(&o.MemReserveMiB, "mem-reserve-mib", 512, "memory held back for the server and the kit, MiB")
	fs.IntVar(&o.BotsPerGenCPU, "bots-per-gen-cpu", 1500, "bots one generator CPU can drive: caps N_max (0 = no generator-CPU cap)")
	o.Bot.RampTalker = d.RampTalker
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q (all inputs are flags)", fs.Arg(0))
	}
	if err := leg.ParseGeneratorGC(o.Bot.GeneratorGC); err != nil {
		return o, err
	}
	if o.MemPerBotMiB <= 0 {
		return o, fmt.Errorf("--mem-per-bot-mib %v: must be positive", o.MemPerBotMiB)
	}
	if !strings.Contains(o.Label, "relative only") {
		return o, errors.New(`--label must say "relative only"`)
	}
	return o, nil
}

func runLeg(args []string, stdout, stderr io.Writer) error {
	o, err := parseLegFlags(args, stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if o.Budget > 0 {
		// The budget bounds each arm's sweep; this is the hard stop for the
		// whole leg, with room for the confirm, overload and teardown each
		// arm reserves and for the one calibration.
		var cancel context.CancelFunc
		nArms := 1
		if arms, err := leg.ParseArms(o.Arms); err == nil {
			nArms = len(arms)
		}
		ctx, cancel = context.WithTimeout(ctx, leg.HardStop(o.Budget, nArms))
		defer cancel()
	}
	if err := leg.Run(ctx, o, stdout); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "leg written to %s\n", o.OutDir)
	return nil
}
