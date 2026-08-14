package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sarahmaeve/toolbox/internal/cliutil"
	"github.com/sarahmaeve/toolbox/pkg/pdf"
)

// runImages implements the `images` subcommand: extract embedded
// raster image XObjects from a PDF, write them as individual files
// (JPEG/PNG/TIFF depending on the source filter), and write a
// manifest.tsv recording each image's page, dimensions, colorspace,
// and source filter.
func runImages(args []string) error {
	fs := flag.NewFlagSet("images", flag.ContinueOnError)
	out := fs.String("out", "images", "directory to write extracted images and manifest into")
	page := fs.Int("page", 0, "extract only from this page (1-indexed)")
	pages := fs.String("pages", "", "extract only from this inclusive page range, e.g. 3-7")
	noStitch := fs.Bool("no-stitch", false, "disable adjacency-based panel stitching (emit one file per XObject)")
	stitchTol := fs.Float64("stitch-tol", 2.0, "panel adjacency tolerance in PDF points")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: toolbox-pdf images [-out DIR] [-page N | -pages N-M] [-no-stitch] [-stitch-tol PT] <file.pdf>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one PDF path")
	}
	path := fs.Arg(0)

	if *page != 0 && *pages != "" {
		return fmt.Errorf("-page and -pages are mutually exclusive")
	}

	from, to, err := cliutil.ParsePageRange(*page, *pages)
	if err != nil {
		return err
	}

	var images []pdf.Image
	if from == 0 && to == 0 {
		images, err = pdf.ExtractImages(path)
	} else {
		images, _, err = pdf.ExtractImagePages(path, from, to)
	}
	if err != nil {
		return err
	}

	if !*noStitch {
		images = pdf.StitchAdjacent(images, *stitchTol, func(g pdf.FigureGroup, err error) {
			fmt.Fprintf(os.Stderr, "stitch failed for page %d (%d panels): %v; emitting panels individually\n",
				g.Page, len(g.Parts), err)
		})
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if _, err := pdf.WriteImagesWithManifest(*out, base, images); err != nil {
		return err
	}

	fmt.Printf("wrote %d image(s) to %s\n", len(images), *out)
	return nil
}
