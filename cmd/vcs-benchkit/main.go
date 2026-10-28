// Command vcs-benchkit holds the Wave 6 bench tooling. Only the predict
// subcommand exists so far; later PRs add more.
package main

import (
	"fmt"
	"os"
)

// label is stamped on every bench artefact.
const label = "Shared GitHub runner (4 vCPU), loopback, relative only. Not a capacity figure."

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: vcs-benchkit <subcommand> [flags]\nsubcommands: predict")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "predict":
		err = runPredict(os.Args[2:], os.Stdout)
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vcs-benchkit:", err)
		os.Exit(1)
	}
}
