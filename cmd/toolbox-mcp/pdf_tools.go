package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sarahmaeve/toolbox/internal/cliutil"
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

	from, to, err := cliutil.ParsePageRange(p.Page, p.Pages)
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
	from, to, err := cliutil.ParsePageRange(p.Page, p.Pages)
	if err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}

	images, err := pdf.ExtractImages(p.Path)
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}

	if from != 0 || to != 0 {
		images = pdf.FilterPages(images, from, to)
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
		images = pdf.StitchAdjacent(images, stitchTol, func(g pdf.FigureGroup, err error) {
			// slog goes to stderr or --log — never stdout, which is the
			// MCP protocol channel.
			slog.Warn("stitch failed; emitting panels individually",
				"page", g.Page, "panels", len(g.Parts), "error", err)
		})
	}

	base := strings.TrimSuffix(filepath.Base(p.Path), filepath.Ext(p.Path))
	manifestPath, err := pdf.WriteImagesWithManifest(p.OutDir, base, images)
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}

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
	for _, img := range images {
		entries = append(entries, entry{
			File: pdf.ImageFileName(base, img), Page: img.Page, Name: img.Name,
			Width: img.Width, Height: img.Height, BPC: img.BitsPerComponent,
			ColorSpace: img.ColorSpace, Filter: img.Filter,
			BboxX: img.BboxX, BboxY: img.BboxY, BboxW: img.BboxW, BboxH: img.BboxH,
		})
	}

	return mcp.OK(map[string]any{
		"count":         len(entries),
		"out_dir":       p.OutDir,
		"manifest_path": manifestPath,
		"images":        entries,
	})
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
