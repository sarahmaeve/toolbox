package pdf

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"testing"
)

// TestExtractImagesATP is the load-bearing dogfood test for image extraction.
// It runs against the same ATP 3-01.81 PDF the text-extraction dogfood test
// uses. Skips cleanly when the file is absent.
//
// The test exercises every major extraction path that ships in milestone 1:
//
//   - DCTDecode passthrough (CMYK JPEGs on the early intro pages)
//   - FlateDecode → PNG with Indexed colorspace over DeviceCMYK (figures in
//     chapters 1 and 4 — the load-bearing case for the m1 extension)
//   - FlateDecode → PNG with ICCBased colorspace (a small icon on page 47)
//
// A regression in colorspace resolution would drop the image count well
// below 16 and likely fail the PNG-present assertion.
func TestExtractImagesATP(t *testing.T) {
	if _, err := os.Stat(atpDogfoodPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("dogfood PDF not present at %s", atpDogfoodPath)
		}
		t.Fatalf("stat %s: %v", atpDogfoodPath, err)
	}

	images, err := ExtractImages(atpDogfoodPath)
	if err != nil {
		t.Fatalf("ExtractImages: %v", err)
	}

	if len(images) < 16 {
		t.Errorf("image count: got %d, want at least 16", len(images))
	}

	// Tally formats to confirm every extraction path produced output.
	var jpgs, pngs int
	for _, img := range images {
		switch img.Ext {
		case "jpg":
			jpgs++
			if !bytes.HasPrefix(img.Data, []byte{0xFF, 0xD8}) {
				t.Errorf("page %d %s: JPEG missing SOI marker, got % x",
					img.Page, img.Name, img.Data[:min(4, len(img.Data))])
			}
		case "png":
			pngs++
			if !bytes.HasPrefix(img.Data, []byte{0x89, 'P', 'N', 'G'}) {
				t.Errorf("page %d %s: PNG missing magic, got % x",
					img.Page, img.Name, img.Data[:min(4, len(img.Data))])
			}
		}
		if img.Width <= 0 || img.Height <= 0 {
			t.Errorf("page %d %s: invalid dimensions %dx%d",
				img.Page, img.Name, img.Width, img.Height)
		}
	}

	if jpgs == 0 {
		t.Error("no JPEGs extracted — DCTDecode passthrough path regressed")
	}
	if pngs == 0 {
		t.Error("no PNGs extracted — Indexed/ICCBased decode path regressed")
	}
}
