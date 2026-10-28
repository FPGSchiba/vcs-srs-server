package main

import (
	"io"
	"testing"
	"time"
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
