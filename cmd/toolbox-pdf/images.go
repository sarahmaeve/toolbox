package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sarahmaeve/toolbox/pkg/pdf"
)

const (
	manifestName = "manifest.tsv"
	dirPerm      = 0o755
	filePerm     = 0o644
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

	from, to, err := parseRange(*page, *pages)
	if err != nil {
		return err
	}

	images, err := pdf.ExtractImages(path)
	if err != nil {
		return err
	}

	if from != 0 || to != 0 {
		images = filterRange(images, from, to)
	}

	if !*noStitch {
		images = stitchAdjacent(images, *stitchTol)
	}

	if err := os.MkdirAll(*out, dirPerm); err != nil {
		return err
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	manifestPath := filepath.Join(*out, manifestName)
	mf, err := os.Create(manifestPath) //nolint:gosec // G304: operator-supplied output dir
	if err != nil {
		return err
	}
	defer mf.Close() //nolint:errcheck

	w := csv.NewWriter(mf)
	w.Comma = '\t'
	header := []string{
		"file", "page", "name",
		"width", "height", "bpc", "colorspace", "filter",
		"bbox_x", "bbox_y", "bbox_w", "bbox_h",
	}
	if err := w.Write(header); err != nil {
		return fmt.Errorf("manifest header: %w", err)
	}

	for _, img := range images {
		fname := fmt.Sprintf("%s-p%04d-%s.%s", base, img.Page, img.Name, img.Ext)
		fpath := filepath.Join(*out, fname)
		if err := os.WriteFile(fpath, img.Data, filePerm); err != nil {
			return fmt.Errorf("writing %s: %w", fpath, err)
		}
		row := []string{
			fname,
			strconv.Itoa(img.Page),
			img.Name,
			strconv.Itoa(img.Width),
			strconv.Itoa(img.Height),
			strconv.Itoa(img.BitsPerComponent),
			img.ColorSpace,
			img.Filter,
			formatPt(img.BboxX),
			formatPt(img.BboxY),
			formatPt(img.BboxW),
			formatPt(img.BboxH),
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("manifest row: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("manifest flush: %w", err)
	}

	fmt.Printf("wrote %d image(s) to %s\n", len(images), *out)
	return nil
}

// filterRange returns only images whose Page is in [from, to] (1-indexed).
func filterRange(images []pdf.Image, from, to int) []pdf.Image {
	out := images[:0]
	for _, img := range images {
		if img.Page >= from && img.Page <= to {
			out = append(out, img)
		}
	}
	return out
}

// formatPt renders a PDF-point coordinate to the manifest with two
// decimals. Below the precision of any layout decision downstream;
// keeps the TSV readable.
func formatPt(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// stitchAdjacent groups adjacent panels into figures and stitches each
// multi-panel group into one PNG. Single-panel groups pass through.
// On stitch failure (rare: decode error on a panel) the original
// panels are kept individually so we always emit something.
func stitchAdjacent(images []pdf.Image, tolPt float64) []pdf.Image {
	groups := pdf.GroupAdjacent(images, tolPt)
	out := make([]pdf.Image, 0, len(groups))
	for _, g := range groups {
		if len(g.Parts) == 1 {
			out = append(out, g.Parts[0])
			continue
		}
		stitched, err := pdf.StitchGroup(g)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stitch failed for page %d (%d panels): %v; emitting panels individually\n",
				g.Page, len(g.Parts), err)
			out = append(out, g.Parts...)
			continue
		}
		out = append(out, stitched)
	}
	return out
}
