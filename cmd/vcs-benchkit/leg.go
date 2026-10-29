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
	fs.StringVar(&o.Scenario, "scenario", "one-big-net", "scenario (v1: one-big-net)")
	fs.StringVar(&o.Arms, "arms", "S", "arms to run (S4: S only)")
	fs.Int64Var(&o.Seed, "seed", 1, "seed")
	fs.StringVar(&o.OutDir, "out", "", "output directory (required)")
	fs.StringVar(&o.Q6, "q6", "", "the user's Q6 CPU-split decision, recorded verbatim in the manifest")
	fs.StringVar(&o.ProbeFile, "probe", "", "gate-0 env-probe.json (cpu sets and topology label)")
	fs.StringVar(&o.ServerSet, "server-set", "", "server cpu set (overrides --probe)")
	fs.StringVar(&o.GenSet, "gen-set", "", "generator cpu set (overrides --probe)")
	fs.StringVar(&o.Label, "label", leg.Label, "label written to the CSVs, results.md and manifest; must say \"relative only\"")
	fs.StringVar(&o.Topology, "topology", "", "topology label (overrides --probe)")
	fs.StringVar(&o.PredictFile, "predict", "", "predict.json from `vcs-benchkit predict`")
	fs.IntVar(&o.PredictedKnee, "predicted-knee", 0, "B-0 predicted knee (overrides --predict)")
	fs.IntVar(&o.NMaxCap, "n-max", 0, "extra cap on N_max (0 = the time and port budgets only)")
	fs.StringVar(&o.ServerSHA, "server-sha", "", "server commit, recorded in the manifest")
	fs.StringVar(&o.ClientSHA, "client-sha", "", "client commit, recorded in the manifest")
	fs.DurationVar(&o.Budget, "budget", 100*time.Minute, "leg time budget (0 = unlimited)")
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
	o.Bot.RampTalker = d.RampTalker
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q (all inputs are flags)", fs.Arg(0))
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
		// The budget bounds the sweep; this is the hard stop with room for
		// the confirm, overload and teardown it reserves.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Budget+15*time.Minute)
		defer cancel()
	}
	if err := leg.Run(ctx, o, stdout); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "leg written to %s\n", o.OutDir)
	return nil
}
