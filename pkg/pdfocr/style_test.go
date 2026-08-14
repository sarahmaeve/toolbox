package pdfocr

import (
	"image"
	"image/color"
	"testing"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

func TestWordSlantSeparatesUprightAndItalicStrokes(t *testing.T) {
	t.Parallel()

	drawBars := func(shear float64) *image.Gray {
		img := image.NewGray(image.Rect(0, 0, 100, 40))
		for i := range img.Pix {
			img.Pix[i] = 0xff
		}
		for _, baseX := range []int{15, 35, 55, 75} {
			for y := 4; y < 36; y++ {
				x := baseX + int(shear*float64(35-y))
				for width := 0; width < 2; width++ {
					img.SetGray(x+width, y, color.Gray{})
				}
			}
		}
		return img
	}
	full := ocr.Bounds{Width: 1, Height: 1}
	uprightShear, uprightImprovement, ok := wordSlant(drawBars(0), full)
	if !ok || uprightShear > 0.10 || uprightImprovement > 0.05 {
		t.Fatalf("upright: shear=%.3f improvement=%.3f ok=%v", uprightShear, uprightImprovement, ok)
	}
	italicShear, italicImprovement, ok := wordSlant(drawBars(0.25), full)
	if !ok || italicShear < 0.15 || italicImprovement < 0.05 {
		t.Fatalf("italic: shear=%.3f improvement=%.3f ok=%v", italicShear, italicImprovement, ok)
	}
}

func TestMergeItalicSpansIncludesInterwordSpace(t *testing.T) {
	t.Parallel()

	spans := mergeItalicSpans("à la Maǧnūn and", []ocr.StyleSpan{
		{Start: 0, End: 1, Italic: true},
		{Start: 2, End: 4, Italic: true},
		{Start: 5, End: 11, Italic: true},
	})
	if len(spans) != 1 || spans[0].Start != 0 || spans[0].End != 11 {
		t.Fatalf("merged spans: got %+v", spans)
	}
}
