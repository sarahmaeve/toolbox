package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sarahmaeve/toolbox/pkg/mcp"
	"github.com/sarahmaeve/toolbox/pkg/pdf"
	"github.com/sarahmaeve/toolbox/pkg/pdfclean"
)

// buildPDFTools returns the MCP tool slate that exposes pkg/pdf and
// pkg/pdfclean operations. None depend on the message store — they're
// pure-function tools over filesystem inputs/outputs.
func buildPDFTools() []mcp.Tool {
	return []mcp.Tool{
		&pdfExtractTextTool{},
		&pdfExtractPagesTool{},
		&pdfExtractImagesTool{},
		&pdfCleanTextTool{},
	}
}

// --- pdf_extract_text ------------------------------------------------------

type pdfExtractTextTool struct{}

func (pdfExtractTextTool) Name() string { return "pdf_extract_text" }
func (pdfExtractTextTool) Description() string {
	return "Extract text from a digital PDF (PDF 1.4–1.7, including compressed xrefs and object streams). Concatenates all pages with form-feed separators by default; pass page or pages to narrow."
}
func (pdfExtractTextTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":  {"type": "string"},
			"page":  {"type": "integer", "minimum": 1},
			"pages": {"type": "string"}
		},
		"required": ["path"],
		"additionalProperties": false
	}`)
}
func (pdfExtractTextTool) Handle(_ context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		Path  string `json:"path"`
		Page  int    `json:"page"`
		Pages string `json:"pages"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	if p.Page != 0 && p.Pages != "" {
		return mcp.Err(mcp.CodeSchemaViolation,
			"page and pages are mutually exclusive", nil)
	}

	from, to, err := parsePageRange(p.Page, p.Pages)
	if err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}

	pages, err := pdf.ExtractAllPages(p.Path)
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}

	if from == 0 && to == 0 {
		from, to = 1, len(pages)
	}
	if from < 1 || to > len(pages) || from > to {
		return mcp.Err(mcp.CodeSchemaViolation,
			fmt.Sprintf("page range %d-%d out of bounds (document has %d pages)", from, to, len(pages)),
			nil)
	}

	var sb strings.Builder
	for i := from; i <= to; i++ {
		if i > from {
			sb.WriteString("\f\n")
		}
		sb.WriteString(pages[i-1])
	}
	return mcp.OK(map[string]any{
		"text":       sb.String(),
		"page_count": len(pages),
		"pages_from": from,
		"pages_to":   to,
	})
}

// --- pdf_extract_pages -----------------------------------------------------

type pdfExtractPagesTool struct{}

func (pdfExtractPagesTool) Name() string { return "pdf_extract_pages" }
func (pdfExtractPagesTool) Description() string {
	return "Extract text from a PDF, one entry per page, preserving page order. Use when downstream processing wants to iterate pages individually rather than treat the document as one blob."
}
func (pdfExtractPagesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"}
		},
		"required": ["path"],
		"additionalProperties": false
	}`)
}
func (pdfExtractPagesTool) Handle(_ context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	pages, err := pdf.ExtractAllPages(p.Path)
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}
	type pageEntry struct {
		Page int    `json:"page"`
		Text string `json:"text"`
	}
	entries := make([]pageEntry, len(pages))
	for i, text := range pages {
		entries[i] = pageEntry{Page: i + 1, Text: text}
	}
	return mcp.OK(map[string]any{
		"page_count": len(pages),
		"pages":      entries,
	})
}

// --- pdf_extract_images ----------------------------------------------------

type pdfExtractImagesTool struct{}

func (pdfExtractImagesTool) Name() string { return "pdf_extract_images" }
func (pdfExtractImagesTool) Description() string {
	return "Extract embedded raster image XObjects from a PDF, write them to out_dir as individual files (JPEG/PNG/TIFF), and return a manifest of every image's page, dimensions, colorspace, filter, and bbox. Does NOT return image bytes — that would blow up the MCP frame for documents with many images."
}
func (pdfExtractImagesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":       {"type": "string"},
			"out_dir":    {"type": "string"},
			"page":       {"type": "integer", "minimum": 1},
			"pages":      {"type": "string"},
			"stitch":     {"type": "boolean"},
			"stitch_tol": {"type": "number", "minimum": 0}
		},
		"required": ["path", "out_dir"],
		"additionalProperties": false
	}`)
}
func (pdfExtractImagesTool) Handle(_ context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		Path      string  `json:"path"`
		OutDir    string  `json:"out_dir"`
		Page      int     `json:"page"`
		Pages     string  `json:"pages"`
		Stitch    *bool   `json:"stitch"`
		StitchTol float64 `json:"stitch_tol"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	if p.Page != 0 && p.Pages != "" {
		return mcp.Err(mcp.CodeSchemaViolation,
			"page and pages are mutually exclusive", nil)
	}
	from, to, err := parsePageRange(p.Page, p.Pages)
	if err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}

	images, err := pdf.ExtractImages(p.Path)
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}

	if from != 0 || to != 0 {
		filtered := images[:0]
		for _, img := range images {
			if img.Page >= from && img.Page <= to {
				filtered = append(filtered, img)
			}
		}
		images = filtered
	}

	stitch := true
	if p.Stitch != nil {
		stitch = *p.Stitch
	}
	stitchTol := 2.0
	if p.StitchTol > 0 {
		stitchTol = p.StitchTol
	}
	if stitch {
		images = stitchAdjacentImages(images, stitchTol)
	}

	if err := os.MkdirAll(p.OutDir, 0o755); err != nil { //nolint:gosec // G301: operator-supplied output dir
		return mcp.Err(mcp.CodeInternalError, fmt.Sprintf("mkdir %s: %v", p.OutDir, err), nil)
	}

	base := strings.TrimSuffix(filepath.Base(p.Path), filepath.Ext(p.Path))
	type entry struct {
		File       string  `json:"file"`
		Page       int     `json:"page"`
		Name       string  `json:"name"`
		Width      int     `json:"width"`
		Height     int     `json:"height"`
		BPC        int     `json:"bits_per_component"`
		ColorSpace string  `json:"colorspace"`
		Filter     string  `json:"filter"`
		BboxX      float64 `json:"bbox_x"`
		BboxY      float64 `json:"bbox_y"`
		BboxW      float64 `json:"bbox_w"`
		BboxH      float64 `json:"bbox_h"`
	}
	entries := make([]entry, 0, len(images))

	manifestPath := filepath.Join(p.OutDir, "manifest.tsv")
	mf, err := os.Create(manifestPath) //nolint:gosec // G304: operator-supplied output dir
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
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
		return mcp.Err(mcp.CodeInternalError, fmt.Sprintf("manifest header: %v", err), nil)
	}
	for _, img := range images {
		fname := fmt.Sprintf("%s-p%04d-%s.%s", base, img.Page, img.Name, img.Ext)
		fpath := filepath.Join(p.OutDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil { //nolint:gosec // G306: operator-readable output
			return mcp.Err(mcp.CodeInternalError, fmt.Sprintf("write %s: %v", fpath, err), nil)
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
			strconv.FormatFloat(img.BboxX, 'f', 2, 64),
			strconv.FormatFloat(img.BboxY, 'f', 2, 64),
			strconv.FormatFloat(img.BboxW, 'f', 2, 64),
			strconv.FormatFloat(img.BboxH, 'f', 2, 64),
		}
		if err := w.Write(row); err != nil {
			return mcp.Err(mcp.CodeInternalError, fmt.Sprintf("manifest row: %v", err), nil)
		}
		entries = append(entries, entry{
			File: fname, Page: img.Page, Name: img.Name,
			Width: img.Width, Height: img.Height, BPC: img.BitsPerComponent,
			ColorSpace: img.ColorSpace, Filter: img.Filter,
			BboxX: img.BboxX, BboxY: img.BboxY, BboxW: img.BboxW, BboxH: img.BboxH,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return mcp.Err(mcp.CodeInternalError, fmt.Sprintf("manifest flush: %v", err), nil)
	}

	return mcp.OK(map[string]any{
		"count":         len(entries),
		"out_dir":       p.OutDir,
		"manifest_path": manifestPath,
		"images":        entries,
	})
}

// stitchAdjacentImages mirrors toolbox-pdf images' stitching path.
func stitchAdjacentImages(images []pdf.Image, tolPt float64) []pdf.Image {
	groups := pdf.GroupAdjacent(images, tolPt)
	out := make([]pdf.Image, 0, len(groups))
	for _, g := range groups {
		if len(g.Parts) == 1 {
			out = append(out, g.Parts[0])
			continue
		}
		stitched, err := pdf.StitchGroup(g)
		if err != nil {
			// Best-effort: emit panels individually on stitch failure
			// so we never lose images to a stitching bug.
			out = append(out, g.Parts...)
			continue
		}
		out = append(out, stitched)
	}
	return out
}

// --- pdf_clean_text --------------------------------------------------------

type pdfCleanTextTool struct{}

func (pdfCleanTextTool) Name() string { return "pdf_clean_text" }
func (pdfCleanTextTool) Description() string {
	return "Clean raw PDF-extracted text into a markdown working copy: rejoin hyphen-split words, collapse blank-line runs, convert form-feeds to <!-- page N --> markers, fix bracketed citations and en-dash splits. Optionally insert image links from a manifest (manifest_path + imgdir)."
}
func (pdfCleanTextTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text":          {"type": "string"},
			"manifest_path": {"type": "string"},
			"imgdir":        {"type": "string"}
		},
		"required": ["text"],
		"additionalProperties": false
	}`)
}
func (pdfCleanTextTool) Handle(_ context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		Text         string `json:"text"`
		ManifestPath string `json:"manifest_path"`
		Imgdir       string `json:"imgdir"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	if (p.ManifestPath != "") != (p.Imgdir != "") {
		return mcp.Err(mcp.CodeSchemaViolation,
			"manifest_path and imgdir must be given together (or both omitted)", nil)
	}
	out := pdfclean.Clean(p.Text)
	if p.ManifestPath != "" {
		manifest, err := pdfclean.LoadManifestFile(p.ManifestPath)
		if err != nil {
			return mcp.Err(mcp.CodeInternalError, fmt.Sprintf("load manifest: %v", err), nil)
		}
		out = pdfclean.LinkImages(out, manifest, p.Imgdir)
	}
	return mcp.OK(map[string]any{"text": out})
}

// --- shared helpers --------------------------------------------------------

// parsePageRange validates and resolves the page/pages tool arguments
// into an inclusive (from, to) range. (0, 0) means "all pages".
func parsePageRange(page int, pages string) (int, int, error) {
	switch {
	case page != 0:
		if page < 1 {
			return 0, 0, fmt.Errorf("invalid page %d (must be >= 1)", page)
		}
		return page, page, nil
	case pages != "":
		parts := strings.SplitN(pages, "-", 2)
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("invalid pages %q (expected N-M)", pages)
		}
		from, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid pages start %q: %w", parts[0], err)
		}
		to, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid pages end %q: %w", parts[1], err)
		}
		if from < 1 || to < from {
			return 0, 0, fmt.Errorf("invalid pages range %d-%d", from, to)
		}
		return from, to, nil
	default:
		return 0, 0, nil
	}
}
