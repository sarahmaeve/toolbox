// Command toolbox-pdf consolidates the wargames pdfdump / pdfimg /
// pdfclean trio into a single binary with subcommands.
//
// Subcommands:
//
//	toolbox-pdf dump   [-page N | -pages N-M] <file.pdf>
//	toolbox-pdf images [-out DIR] [-page N | -pages N-M] [-no-stitch] [-stitch-tol PT] <file.pdf>
//	toolbox-pdf clean  [-manifest path -imgdir relpath] <input.txt> <output.md>
//
// Targets digital (text-based) PDFs through PDF 1.7, including
// compressed cross-reference streams and object streams. Scanned PDFs
// whose images use JBIG2 are logged and skipped — use Poppler's
// `pdfimages` as a fallback for those.
package main

import (
	"fmt"
	"io"
	"os"
)

// version, commit, buildDate are stamped via -ldflags by the Makefile.
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
	case "dump":
		err = runDump(args)
	case "images":
		err = runImages(args)
	case "clean":
		err = runClean(args)
	case "version", "--version", "-v":
		fmt.Printf("toolbox-pdf %s (commit %s, built %s)\n", version, commit, buildDate)
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
	fmt.Fprint(w, `toolbox-pdf — extract text and images from digital PDFs

Usage:
  toolbox-pdf dump   [-page N | -pages N-M] <file.pdf>
  toolbox-pdf images [-out DIR] [-page N | -pages N-M] [-no-stitch] [-stitch-tol PT] <file.pdf>
  toolbox-pdf clean  [-manifest path -imgdir relpath] <input.txt> <output.md>
  toolbox-pdf version
  toolbox-pdf help

Run a subcommand with -h for flags.
`)
}
