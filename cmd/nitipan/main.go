// Command nitipan claims domains on one machine and shows who holds what.
package main

import (
	"os"

	"github.com/space-armada/nitipan/internal/cli"
	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/state"
)

func main() {
	deps := cli.Deps{
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Stdin:    os.Stdin,
		Getenv:   os.Getenv,
		Procs:    proc.System(),
		StateDir: state.ResolveDir,
	}

	os.Exit(cli.Execute(deps, os.Args[1:]))
}
