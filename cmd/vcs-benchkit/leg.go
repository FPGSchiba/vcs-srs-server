package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	fs.StringVar(&o.CalSteps, "cal-steps", "64,128,256,512,1024,2048,4096", "calibration bot counts, ascending")
	fs.DurationVar(&o.CalHold, "cal-hold", 3*time.Second, "calibration seconds per step")
	fs.BoolVar(&o.Sysctl, "sysctl", true, "raise net.core.rmem_max/wmem_max with sudo (never the defaults)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q (all inputs are flags)", fs.Arg(0))
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
	if err := leg.Run(ctx, o, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "leg written to %s\n", o.OutDir)
	return nil
}
