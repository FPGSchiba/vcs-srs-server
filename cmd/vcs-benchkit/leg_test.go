package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/leg"
)

func TestParseLegFlagsDefaults(t *testing.T) {
	o, err := parseLegFlags([]string{"--server-bin", "s", "--loadbot-bin", "l", "--out", "o", "--q6", "Server 2 / bots 2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.Scenario != "one-big-net" || o.Arms != "S" || o.JoinRate != 27 || o.Hold != 40*time.Second ||
		o.Confirm != 120*time.Second || o.Overload != 60*time.Second || o.Warmup != 30*time.Second || !o.Sysctl {
		t.Errorf("defaults = %+v", o)
	}
	if o.Q6 != "Server 2 / bots 2" {
		t.Errorf("Q6 = %q", o.Q6)
	}
}

func TestParseLegFlagsRejectsPositional(t *testing.T) {
	if _, err := parseLegFlags([]string{"stray"}, io.Discard); err == nil {
		t.Error("a positional argument must be an error")
	}
}

func TestLegRefusesNonSArm(t *testing.T) {
	err := runLeg([]string{"--server-bin", "s", "--loadbot-bin", "l", "--out", t.TempDir(), "--arms", "Seq",
		"--server-set", "0", "--gen-set", "1", "--topology", "t", "--predicted-knee", "100"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("--arms Seq must be refused in S4")
	}
}

func TestDefaultCalSteps(t *testing.T) {
	o, err := parseLegFlags([]string{"--server-bin", "s", "--loadbot-bin", "l", "--out", "o", "--q6", "Server 2 / bots 2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if want := "64,128,256,512,1024,1536,1792,2048,4096"; o.CalSteps != want {
		t.Errorf("CalSteps = %q, want %q", o.CalSteps, want)
	}
}

func TestParseLegFlagsRound2(t *testing.T) {
	base := []string{"--server-bin", "s", "--loadbot-bin", "l", "--out", "o"}
	// Defaults are the round-2 values (R2-1, R2-4, R2-5, R2-6, R2-8).
	o, err := parseLegFlags(base, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.RampChunk != 250 || o.Bot.JoinRateMax != 12 || o.Bot.JoinRateScale != 5000 || o.Bot.MaxInFlight != 8 ||
		o.Bot.PingEvery != 5*time.Second || o.BurstAdd != 150 || o.BurstInFlight != 16 || !o.Bursts ||
		o.Bot.GCPauseP99 != 5*time.Millisecond || o.Bot.GCPauseWarnP99 != 2*time.Millisecond || o.Bot.GRPCLog != "info" {
		t.Errorf("defaults = %+v", o)
	}
	// Every flag overrides.
	o, err = parseLegFlags(append(append([]string{}, base...), "--ramp-chunk", "100", "--join-rate-max", "20", "--join-rate-scale", "3000",
		"--max-in-flight", "4", "--ping-every", "0s", "--burst-add", "60", "--burst-in-flight", "8", "--bursts=false",
		"--gc-pause-p99", "7ms", "--gc-pause-warn-p99", "3ms", "--grpc-log", "off"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.RampChunk != 100 || o.Bot.JoinRateMax != 20 || o.Bot.JoinRateScale != 3000 || o.Bot.MaxInFlight != 4 ||
		o.Bot.PingEvery != 0 || o.BurstAdd != 60 || o.BurstInFlight != 8 || o.Bursts ||
		o.Bot.GCPauseP99 != 7*time.Millisecond || o.Bot.GCPauseWarnP99 != 3*time.Millisecond || o.Bot.GRPCLog != "off" {
		t.Errorf("overrides = %+v", o)
	}
}

func TestParseLegFlagsLabel(t *testing.T) {
	base := []string{"--server-bin", "s", "--loadbot-bin", "l", "--out", "o"}
	o, err := parseLegFlags(base, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.Label != leg.Label {
		t.Errorf("default Label = %q, want leg.Label", o.Label)
	}
	const want = "x, relative only. Not a capacity figure."
	o, err = parseLegFlags(append(append([]string{}, base...), "--label", want), io.Discard)
	if err != nil || o.Label != want {
		t.Fatalf("Label = %q err = %v", o.Label, err)
	}
	_, err = parseLegFlags(append(append([]string{}, base...), "--label", "a capacity figure"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), `--label must say "relative only"`) {
		t.Errorf("err = %v, want the relative-only refusal", err)
	}
}
