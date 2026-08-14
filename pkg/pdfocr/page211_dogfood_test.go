//go:build darwin

package pdfocr

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"testing"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
	visionocr "github.com/sarahmaeve/toolbox/pkg/ocr/vision"
	"github.com/sarahmaeve/toolbox/pkg/pdf"
	_ "golang.org/x/image/tiff"
)

// TestVisionPage211 is an opt-in regression test for the scan-backed
// scholarly PDF that motivated OCR support. Set TOOLBOX_PDF_OCR_DOGFOOD to
// its local path. The fixture is intentionally not part of the repository.
func TestVisionPage211(t *testing.T) {
	path := os.Getenv("TOOLBOX_PDF_OCR_DOGFOOD")
	if path == "" {
		t.Skip("TOOLBOX_PDF_OCR_DOGFOOD is not set")
	}

	pages, _, err := ExtractPages(context.Background(), path, 211, 211, visionocr.New(), Options{
		Recognition: ocr.Options{
			Languages:          []string{"fr-FR", "en-US"},
			LanguageCorrection: true,
			CustomWords: []string{
				"Ḥadīṯ", "Bayāḍ", "Riyāḍ", "Maǧnūn", "ẓarīf", "maǧlis",
				"aǧīb", "Sayyida", "ḥūrī", "Ḥusayn", "Kitāb", "Ḥikāyāt", "aǧība",
			},
		},
		RestoreGlossaryDiacritics: true,
		RefineSuperscripts:        true,
	})
	if err != nil {
		t.Fatalf("OCR page 211: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("page count: got %d, want 1", len(pages))
	}
	text := pages[0].OCR.Text()
	for _, expected := range []string{
		"THE ḤADĪṮ BAYĀḌ WA RIYĀḌ",
		"elsewhere,7 the narrative as it appears",
		`"mad love" à la Maǧnūn`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("OCR text does not contain %q\n%s", expected, text)
		}
	}

	pageImage := page211Image(t, path)
	interesting := map[string]bool{
		"narrative": true, "courtly": true, "student": true, "tools": true,
		"Maǧnūn": true, "Bayāḍ": true, "ẓarīf": true, "maǧlis": true, "BR": true,
	}
	for _, observation := range pages[0].OCR.Observations {
		probeLine := strings.Contains(observation.Text, `"mad love"`)
		for _, word := range observationWords(observation) {
			if !interesting[word.Text] && !probeLine {
				continue
			}
			shear, improvement, ok := wordSlant(pageImage, word.Bounds)
			t.Logf("style word=%q shear=%.3f improvement=%.3f ok=%v bounds=%+v", word.Text, shear, improvement, ok, word.Bounds)
		}
	}
}

func page211Image(t *testing.T, path string) image.Image {
	t.Helper()
	images, _, err := pdf.ExtractImagePages(path, 211, 211)
	if err != nil {
		t.Fatalf("extract page 211 image: %v", err)
	}
	candidate, ok := dominantImage(images)
	if !ok {
		t.Fatal("page 211 has no image")
	}
	pageImage, _, err := image.Decode(bytes.NewReader(candidate.Data))
	if err != nil {
		t.Fatalf("decode page 211 image: %v", err)
	}
	return pageImage
}

// TestVisionLayoutDiagnostics logs the lower-page geometry for two pages that
// exercise a body/footnote boundary and a multi-paragraph lower body. It is an
// opt-in diagnostic, not a fixture-specific production rule.
func TestVisionLayoutDiagnostics(t *testing.T) {
	path := os.Getenv("TOOLBOX_PDF_OCR_LAYOUT_DOGFOOD")
	if path == "" {
		t.Skip("TOOLBOX_PDF_OCR_LAYOUT_DOGFOOD is not set")
	}
	for _, pageNumber := range []int{213, 214, 215, 216, 218, 220, 224} {
		pages, _, err := ExtractPages(context.Background(), path, pageNumber, pageNumber, visionocr.New(), Options{
			Recognition: ocr.Options{Languages: []string{"fr-FR", "en-US"}},
		})
		if err != nil {
			t.Fatalf("OCR page %d: %v", pageNumber, err)
		}
		lines := markdownLines(pages[0].OCR)
		bodyHeight := dominantOCRLineHeight(lines)
		markRunningHeaders(lines)
		normalGap := dominantOCRLineGap(lines, bodyHeight)
		classified := findFootnoteStart(lines, bodyHeight, normalGap)
		t.Logf("page=%d bodyHeight=%.5f normalGap=%.5f footnoteStart=%d", pageNumber, bodyHeight, normalGap, classified)
		previousY := 0.0
		for index, line := range lines {
			if line.y > 0.55 {
				previousY = line.y
				continue
			}
			gap := previousY - line.y
			t.Logf("page=%d index=%d x=%.5f y=%.5f h=%.5f gap=%.5f confidence=%.3f marker=%v text=%q", pageNumber, index, line.x, line.y, line.height, gap, line.observation.Confidence, startsFootnoteLike(line.plain), line.plain)
			previousY = line.y
		}
		prepared, _, _, _ := prepareMarkdownLines(pages[0].OCR)
		for _, candidate := range bodyCitationCandidates(prepared) {
			text := []rune(pages[0].OCR.Observations[candidate.sourceIndex].Text)
			t.Logf("page=%d citation source=%d range=%d:%d value=%d score=%d text=%q", pageNumber, candidate.sourceIndex, candidate.start, candidate.end, candidate.value, candidate.score, string(text[candidate.start:candidate.end]))
		}
	}
}
