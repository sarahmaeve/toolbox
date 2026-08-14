// Package pdfocr connects selective PDF page-image extraction to an OCR
// engine. The PDF parser and OCR implementations deliberately remain
// independent packages.
package pdfocr

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
	"github.com/sarahmaeve/toolbox/pkg/pdf"
	_ "golang.org/x/image/tiff"
)

// Page is one OCR result and its 1-indexed PDF page number.
type Page struct {
	Number int
	OCR    ocr.Result
}

// Options controls recognition and optional engine-neutral refinement.
type Options struct {
	Recognition               ocr.Options
	RestoreGlossaryDiacritics bool
	RefineSuperscripts        bool
	RefineFootnotes           bool
	DetectItalics             bool
}

// ExtractPages recognizes only the requested inclusive PDF page range. For
// scan-backed pages containing several image XObjects, it chooses the image
// with the largest painted area (falling back to pixel area).
func ExtractPages(ctx context.Context, pdfPath string, from, to int, engine ocr.Engine, options Options) ([]Page, int, error) {
	if engine == nil {
		return nil, 0, fmt.Errorf("PDF OCR: nil engine")
	}
	images, pageCount, err := pdf.ExtractImagePages(pdfPath, from, to)
	if err != nil {
		return nil, pageCount, err
	}

	byPage := make(map[int][]pdf.Image, to-from+1)
	for _, candidate := range images {
		byPage[candidate.Page] = append(byPage[candidate.Page], candidate)
	}

	pages := make([]Page, 0, to-from+1)
	for pageNumber := from; pageNumber <= to; pageNumber++ {
		candidate, ok := dominantImage(byPage[pageNumber])
		if !ok {
			return nil, pageCount, fmt.Errorf("PDF OCR: page %d has no supported image", pageNumber)
		}
		pageImage, _, err := image.Decode(bytes.NewReader(candidate.Data))
		if err != nil {
			return nil, pageCount, fmt.Errorf("PDF OCR: decode page %d image %q (%s): %w", pageNumber, candidate.Name, candidate.Ext, err)
		}
		recognitionOptions := options.Recognition
		if options.RefineSuperscripts || options.RefineFootnotes || options.DetectItalics {
			recognitionOptions.CollectSymbolBounds = true
		}
		result, err := engine.Recognize(ctx, pageImage, recognitionOptions)
		if err != nil {
			return nil, pageCount, fmt.Errorf("PDF OCR: recognize page %d with %s: %w", pageNumber, engine.Name(), err)
		}
		if options.RefineFootnotes {
			if err := refineFootnoteRegion(ctx, pageImage, engine, recognitionOptions, &result); err != nil {
				return nil, pageCount, fmt.Errorf("PDF OCR: refine page %d footnotes with %s: %w", pageNumber, engine.Name(), err)
			}
		}
		if options.RefineSuperscripts {
			if err := refineSuperscripts(ctx, pageImage, engine, recognitionOptions, &result); err != nil {
				return nil, pageCount, fmt.Errorf("PDF OCR: refine page %d superscripts with %s: %w", pageNumber, engine.Name(), err)
			}
		}
		if options.RestoreGlossaryDiacritics {
			ocr.RestoreGlossaryDiacritics(&result, recognitionOptions.CustomWords)
		}
		if options.DetectItalics {
			detectItalicSpans(pageImage, &result)
		}
		pages = append(pages, Page{Number: pageNumber, OCR: result})
	}
	return pages, pageCount, nil
}

// ExtractMarkdownPages recognizes the selected pages and renders their OCR
// geometry as Markdown. Italic detection is enabled because it operates on the
// page raster and must run before the decoded image is released.
func ExtractMarkdownPages(ctx context.Context, pdfPath string, from, to int, engine ocr.Engine, options Options) ([]string, int, error) {
	options.DetectItalics = true
	pages, pageCount, err := ExtractPages(ctx, pdfPath, from, to, engine, options)
	if err != nil {
		return nil, pageCount, err
	}
	return RenderMarkdownPages(pages), pageCount, nil
}

func dominantImage(images []pdf.Image) (pdf.Image, bool) {
	if len(images) == 0 {
		return pdf.Image{}, false
	}
	best := images[0]
	bestScore := imageScore(best)
	for _, candidate := range images[1:] {
		if score := imageScore(candidate); score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best, true
}

func imageScore(candidate pdf.Image) float64 {
	if paintedArea := candidate.BboxW * candidate.BboxH; paintedArea > 0 {
		return paintedArea
	}
	return float64(candidate.Width) * float64(candidate.Height)
}
