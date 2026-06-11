package pdf

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// This file holds the shared image-output pipeline used by both the
// toolbox-pdf CLI and the toolbox-mcp pdf_extract_images tool: panel
// stitching, page filtering, canonical filenames, and the manifest.tsv
// writer. Extracted from two drifting copies in the binaries so both
// emit byte-identical layouts (pkg/pdfclean parses the manifest back).

// ImageFileName returns the canonical on-disk name for an extracted
// image: <base>-pNNNN-<Name>.<Ext>. base is typically the source PDF's
// filename stem.
func ImageFileName(base string, img Image) string {
	return fmt.Sprintf("%s-p%04d-%s.%s", base, img.Page, img.Name, img.Ext)
}

// FilterPages returns only the images whose Page lies in [from, to]
// (1-indexed, inclusive). Filters in place; the input slice's backing
// array is reused.
func FilterPages(images []Image, from, to int) []Image {
	out := images[:0]
	for _, img := range images {
		if img.Page >= from && img.Page <= to {
			out = append(out, img)
		}
	}
	return out
}

// StitchAdjacent groups adjacent panels into figures and stitches each
// multi-panel group into one PNG. Single-panel groups pass through.
// On stitch failure (rare: decode error on a panel) the group's panels
// are emitted individually so a stitching bug never loses images;
// onErr, when non-nil, is invoked with the failed group so callers can
// log the fallback.
func StitchAdjacent(images []Image, tolPt float64, onErr func(g FigureGroup, err error)) []Image {
	groups := GroupAdjacent(images, tolPt)
	out := make([]Image, 0, len(groups))
	for _, g := range groups {
		if len(g.Parts) == 1 {
			out = append(out, g.Parts[0])
			continue
		}
		stitched, err := StitchGroup(g)
		if err != nil {
			if onErr != nil {
				onErr(g, err)
			}
			out = append(out, g.Parts...)
			continue
		}
		out = append(out, stitched)
	}
	return out
}

// manifestColumns is the manifest.tsv header row. pkg/pdfclean's
// ParseManifest reads this format back; file, page, and name are its
// required columns.
var manifestColumns = []string{
	"file", "page", "name",
	"width", "height", "bpc", "colorspace", "filter",
	"bbox_x", "bbox_y", "bbox_w", "bbox_h",
}

// WriteImagesWithManifest writes every image into outDir under its
// ImageFileName, plus a manifest.tsv describing them, creating outDir
// if needed. Returns the manifest path. Bbox coordinates are rendered
// with two decimals — below the precision of any layout decision
// downstream, keeps the TSV readable.
func WriteImagesWithManifest(outDir, base string, images []Image) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil { //nolint:gosec // G301: operator-supplied dir holds operator-readable images
		return "", fmt.Errorf("create %s: %w", outDir, err)
	}

	manifestPath := filepath.Join(outDir, "manifest.tsv")
	mf, err := os.Create(manifestPath) //nolint:gosec // G304: operator-supplied output dir
	if err != nil {
		return "", err
	}
	defer mf.Close() //nolint:errcheck

	w := csv.NewWriter(mf)
	w.Comma = '\t'
	if err := w.Write(manifestColumns); err != nil {
		return "", fmt.Errorf("manifest header: %w", err)
	}

	pt := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	for _, img := range images {
		fname := ImageFileName(base, img)
		fpath := filepath.Join(outDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil { //nolint:gosec // G306: operator-readable output
			return "", fmt.Errorf("writing %s: %w", fpath, err)
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
			pt(img.BboxX), pt(img.BboxY), pt(img.BboxW), pt(img.BboxH),
		}
		if err := w.Write(row); err != nil {
			return "", fmt.Errorf("manifest row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", fmt.Errorf("manifest flush: %w", err)
	}
	return manifestPath, nil
}
