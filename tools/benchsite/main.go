// Command benchsite hosts the results-site tooling. Subcommands: scan, render.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/FPGSchiba/vcs-srs-server/tools/benchsite/sanitize"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

const usage = "usage: benchsite scan --dir D [--extra LITERAL]... | benchsite render -in RUNS -out SITE"

func run(args []string, errw *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(errw, usage)
		return 2
	}
	switch args[0] {
	case "scan":
		return runScan(args[1:], errw)
	case "render":
		return runRender(args[1:], errw)
	}
	fmt.Fprintln(errw, usage)
	return 2
}

func runRender(args []string, errw *os.File) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(errw)
	in := fs.String("in", "", "directory of run directories")
	out := fs.String("out", "", "output site directory")
	if err := fs.Parse(args); err != nil || *in == "" || *out == "" {
		return 2
	}
	if err := Render(*in, *out); err != nil {
		fmt.Fprintln(errw, "render:", err)
		return 1
	}
	return 0
}

func runScan(args []string, errw *os.File) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(errw)
	dir := fs.String("dir", "", "directory to scan")
	var extra multi
	fs.Var(&extra, "extra", "literal that must not appear (repeatable)")
	if err := fs.Parse(args); err != nil || *dir == "" {
		return 2
	}
	fnd := sanitize.Scan(*dir, extra)
	for _, f := range fnd {
		fmt.Fprintln(errw, f)
	}
	if len(fnd) > 0 {
		return 1
	}
	return 0
}
