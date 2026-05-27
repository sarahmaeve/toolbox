package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sarahmaeve/toolbox/pkg/pdfclean"
)

// runClean implements the `clean` subcommand: take the raw text output
// of `dump` and produce a markdown working copy with page markers,
// rejoined hyphens, and (optionally) image links sourced from an
// `images` manifest.
func runClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "path to an `images` manifest.tsv; when present, caption lines are paired with images")
	imgdir := fs.String("imgdir", "", "relative path used for image links in the output markdown (e.g. images/atp)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: toolbox-pdf clean [-manifest path -imgdir relpath] <input.txt> <output.md>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("expected <input.txt> <output.md>")
	}
	if (*manifest != "") != (*imgdir != "") {
		return fmt.Errorf("-manifest and -imgdir must be given together")
	}

	in, err := os.ReadFile(fs.Arg(0)) //nolint:gosec // G304: operator-supplied input path
	if err != nil {
		return err
	}

	out := pdfclean.Clean(string(in))

	if *manifest != "" {
		m, err := pdfclean.LoadManifestFile(*manifest)
		if err != nil {
			return err
		}
		out = pdfclean.LinkImages(out, m, *imgdir)
	}

	if err := os.WriteFile(fs.Arg(1), []byte(out), 0o644); err != nil { //nolint:gosec // G306: operator-readable output
		return err
	}
	return nil
}
