// Command anthill is the Anthill binary: the queen console, the daemon and
// the ant tools entry point (design D13). For now it only reports its
// version.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the Anthill version. Builds through the Makefile set it with
// -ldflags "-X main.version=..."; other builds report "dev".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line args, writes to stdout and stderr, and
// returns the process exit code: 0 on success, 1 if the output cannot be
// written, 2 for a usage error.
func run(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		usage(stderr, "")
	case args[0] != "version" && args[0] != "--version":
		usage(stderr, fmt.Sprintf("unknown command %q", args[0]))
	case len(args) > 1:
		usage(stderr, args[0]+" takes no arguments")
	default:
		if _, err := fmt.Fprintf(stdout, "anthill %s\n", version); err != nil {
			return 1
		}
		return 0
	}
	return 2
}

// usage writes problem, if any, and the usage line to stderr.
func usage(stderr io.Writer, problem string) {
	msg := "usage: anthill version\n"
	if problem != "" {
		msg = "anthill: " + problem + "\n" + msg
	}
	// If stderr itself cannot be written, there is nowhere left to report
	// it, so the error is deliberately discarded.
	_, _ = io.WriteString(stderr, msg)
}
