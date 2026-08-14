package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sarahmaeve/toolbox/internal/cliutil"
	"github.com/sarahmaeve/toolbox/pkg/ocr"
	visionocr "github.com/sarahmaeve/toolbox/pkg/ocr/vision"
	"github.com/sarahmaeve/toolbox/pkg/pdf"
	"github.com/sarahmaeve/toolbox/pkg/pdfocr"
)

// runDump implements the `dump` subcommand: extract text from a PDF and print
// it to stdout. Plain-text pages use form-feed separators; Markdown pages carry
// explicit page comments and are separated by a blank line.
func runDump(args []string) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	page := fs.Int("page", 0, "extract only this page (1-indexed)")
	pages := fs.String("pages", "", "extract this inclusive page range, e.g. 3-7")
	format := fs.String("format", "text", "output format: text or markdown")
	textSource := fs.String("text-source", "embedded", "text source: embedded or vision")
	ocrLanguages := fs.String("ocr-languages", "", "comma-separated Vision language codes; empty enables automatic detection")
	ocrLanguageCorrection := fs.Bool("ocr-language-correction", false, "enable Vision dictionary-based language correction")
	ocrGlossary := fs.String("ocr-glossary", "", "UTF-8 file containing one authoritative OCR term per line")
	ocrRefineSuperscripts := fs.Bool("ocr-refine-superscripts", true, "re-read likely citation markers in isolated image regions")
	ocrRefineFootnotes := fs.Bool("ocr-refine-footnotes", true, "re-read the detected small-text note region at higher effective resolution")
	outputPath := fs.String("out", "", "write output to this file after successful extraction (default: stdout)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: toolbox-pdf dump [-format text|markdown] [-text-source embedded|vision] [-page N | -pages N-M] <file.pdf>")
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
	outputFormat := strings.ToLower(strings.TrimSpace(*format))
	if outputFormat != "text" && outputFormat != "markdown" {
		return fmt.Errorf("invalid -format %q (expected text or markdown)", *format)
	}
	source := strings.ToLower(strings.TrimSpace(*textSource))
	if source != "embedded" && source != "vision" {
		return fmt.Errorf("invalid -text-source %q (expected embedded or vision)", *textSource)
	}
	if source == "vision" && from == 0 && to == 0 {
		return fmt.Errorf("-text-source vision requires -page or -pages to bound OCR work")
	}

	var extracted []string
	var pageCount int
	if source == "vision" {
		glossary, err := readOCRGlossary(*ocrGlossary)
		if err != nil {
			return err
		}
		options := pdfocr.Options{
			Recognition: ocr.Options{
				Languages:          parseCommaSeparated(*ocrLanguages),
				LanguageCorrection: *ocrLanguageCorrection,
				CustomWords:        glossary,
			},
			RestoreGlossaryDiacritics: len(glossary) > 0,
			RefineSuperscripts:        *ocrRefineSuperscripts,
			RefineFootnotes:           *ocrRefineFootnotes,
		}
		if outputFormat == "markdown" {
			extracted, pageCount, err = pdfocr.ExtractMarkdownPages(
				context.Background(), path, from, to, visionocr.New(), options,
			)
			if err != nil {
				return err
			}
		} else {
			results, count, extractionErr := pdfocr.ExtractPages(
				context.Background(), path, from, to, visionocr.New(), options,
			)
			if extractionErr != nil {
				return extractionErr
			}
			pageCount = count
			extracted = make([]string, 0, len(results))
			for _, result := range results {
				extracted = append(extracted, result.OCR.Text())
			}
		}
	} else if from == 0 && to == 0 {
		if outputFormat == "markdown" {
			extracted, err = pdf.ExtractAllPagesMarkdown(path)
		} else {
			extracted, err = pdf.ExtractAllPages(path)
		}
		if err != nil {
			return err
		}
		pageCount = len(extracted)
		from, to = 1, len(extracted)
	} else {
		if outputFormat == "markdown" {
			extracted, pageCount, err = pdf.ExtractMarkdownPages(path, from, to)
		} else {
			extracted, pageCount, err = pdf.ExtractPages(path, from, to)
		}
		if err != nil {
			return err
		}
	}
	if from < 1 || to > pageCount || from > to {
		return fmt.Errorf("page range %d-%d out of bounds (document has %d pages)",
			from, to, pageCount)
	}

	separator := "\n\f\n"
	if outputFormat == "markdown" {
		separator = "\n\n"
	}
	output := strings.Join(extracted, separator) + "\n"
	if path := strings.TrimSpace(*outputPath); path != "" {
		if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
			return fmt.Errorf("write extracted PDF output %q: %w", path, err)
		}
		return nil
	}
	fmt.Print(output)
	return nil
}

func readOCRGlossary(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read OCR glossary %q: %w", path, err)
	}
	var words []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line != "" && !strings.HasPrefix(line, "#") {
			words = append(words, line)
		}
	}
	return words, nil
}

func parseCommaSeparated(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
