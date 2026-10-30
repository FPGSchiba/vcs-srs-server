// Command benchsite hosts the results-site tooling. Subcommand: scan.
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

func run(args []string, errw *os.File) int {
	if len(args) == 0 || args[0] != "scan" {
		fmt.Fprintln(errw, "usage: benchsite scan --dir D [--extra LITERAL]...")
		return 2
	}
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(errw)
	dir := fs.String("dir", "", "directory to scan")
	var extra multi
	fs.Var(&extra, "extra", "literal that must not appear (repeatable)")
	if err := fs.Parse(args[1:]); err != nil || *dir == "" {
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
