// Command toolbox-bridge is a reference binary that wires
// pkg/certs + pkg/messagestore + pkg/bridge into a runnable local
// service.
//
// Subcommands:
//
//	toolbox-bridge serve run        — run the HTTPS bridge in the foreground
//	toolbox-bridge serve start      — start the bridge as a background daemon
//	toolbox-bridge serve stop       — stop the running daemon
//	toolbox-bridge serve restart    — stop + start
//	toolbox-bridge serve status     — report whether the daemon is running
//	toolbox-bridge certs init       — install the managed CA (mkcert)
//	toolbox-bridge certs check      — preflight the TLS trust setup
//
// The serve commands load MessageTypes from --schemas-dir at startup:
// each file <name>.json registers a MessageType named <name>. Schemas
// must be strict-reject (additionalProperties:false).
package main

import (
	"fmt"
	"io"
	"os"
)

// EnvVar is the env var the bridge's certs setup writes. Aligns with
// Node's TLS stack so Claude Code's WebFetch picks up the CA anchor.
const EnvVar = "NODE_EXTRA_CA_CERTS"

// CertDir is the default location for the managed CA anchor.
const CertDir = "~/.toolbox/certs"

// version, commit, and buildDate are stamped by the Makefile via
// -ldflags. `go install` from a module path skips the stamp and
// leaves the dev defaults — that's fine for development; the
// Makefile path is the supported production install.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "certs":
		err = runCerts(args)
	case "init":
		err = runInit(args)
	case "doctor":
		err = runDoctor(args)
	case "version", "--version", "-v":
		fmt.Printf("toolbox-bridge %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	case "help", "-h", "--help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `toolbox-bridge — reference local message-bus binary

Setup (run once on a fresh machine):
  toolbox-bridge init             [--write-profile] [--seed-schemas]
  toolbox-bridge doctor                     diagnose the local setup

Day-to-day:
  toolbox-bridge serve run        [flags]   run in foreground
  toolbox-bridge serve start      [flags]   start as background daemon
  toolbox-bridge serve stop       [flags]   stop the daemon
  toolbox-bridge serve restart    [flags]   stop + start
  toolbox-bridge serve status     [flags]   report daemon state

Lower-level cert ops (init handles these for new users):
  toolbox-bridge certs init       [--write-profile]
  toolbox-bridge certs check

Run a subcommand with -h for flags.
`)
}
