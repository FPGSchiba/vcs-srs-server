// Command vcs-benchkit holds the Wave 6 bench tooling: predict (the B-0
// model), leg (one arm of one scenario on one VM) and aggregate (merge legs).
package main

import (
	"fmt"
	"os"
)

// label is stamped on every bench artefact.
const label = "Shared GitHub runner (4 vCPU), loopback, relative only. Not a capacity figure."

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: vcs-benchkit <subcommand> [flags]\nsubcommands: predict, leg, aggregate")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "predict":
		err = runPredict(os.Args[2:], os.Stdout)
	case "aggregate":
		err = runAggregate(os.Args[2:], os.Stdout, os.Stderr)
	case "leg":
		err = runLeg(os.Args[2:], os.Stdout, os.Stderr)
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vcs-benchkit:", err)
		os.Exit(1)
	}
}
