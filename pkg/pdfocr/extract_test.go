package pdfocr

import (
	"testing"

	"github.com/sarahmaeve/toolbox/pkg/pdf"
)

func TestDominantImagePrefersPaintedArea(t *testing.T) {
	t.Parallel()

	images := []pdf.Image{
		{Name: "high-resolution-icon", Width: 4000, Height: 4000, BboxW: 20, BboxH: 20},
		{Name: "page-scan", Width: 1200, Height: 1800, BboxW: 600, BboxH: 800},
	}
	got, ok := dominantImage(images)
	if !ok {
		t.Fatal("dominantImage reported no image")
	}
	if got.Name != "page-scan" {
		t.Fatalf("dominantImage: got %q, want page-scan", got.Name)
	}
}

func TestDominantImageFallsBackToPixelArea(t *testing.T) {
	t.Parallel()

	images := []pdf.Image{
		{Name: "small", Width: 100, Height: 200},
		{Name: "large", Width: 1000, Height: 2000},
	}
	got, ok := dominantImage(images)
	if !ok || got.Name != "large" {
		t.Fatalf("dominantImage: got %#v, %v; want large, true", got, ok)
	}
}
