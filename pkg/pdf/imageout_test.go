package pdf

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestImageFileName(t *testing.T) {
	t.Parallel()
	img := Image{Page: 4, Name: "Im0", Ext: "png"}
	if got, want := ImageFileName("doc", img), "doc-p0004-Im0.png"; got != want {
		t.Errorf("ImageFileName: got %q, want %q", got, want)
	}
	stitched := Image{Page: 12, Name: "Im0+Im1", Ext: "png"}
	if got, want := ImageFileName("a", stitched), "a-p0012-Im0+Im1.png"; got != want {
		t.Errorf("ImageFileName stitched: got %q, want %q", got, want)
	}
}

func TestFilterPages(t *testing.T) {
	t.Parallel()
	imgs := []Image{
		{Page: 1, Name: "A"},
		{Page: 2, Name: "B"},
		{Page: 3, Name: "C"},
	}
	got := FilterPages(imgs, 2, 2)
	if len(got) != 1 || got[0].Name != "B" {
		t.Errorf("FilterPages(2,2): got %+v, want only B", got)
	}
}

func TestStitchAdjacent_SinglePanelPassthrough(t *testing.T) {
	t.Parallel()
	imgs := []Image{
		{Page: 1, Name: "Im0", BboxX: 100, BboxY: 100, BboxW: 50, BboxH: 50,
			Data: []byte("not decodable — must not matter for single panels")},
	}
	called := false
	got := StitchAdjacent(imgs, 2.0, func(FigureGroup, error) { called = true })
	if len(got) != 1 || got[0].Name != "Im0" {
		t.Errorf("passthrough: got %+v", got)
	}
	if called {
		t.Error("onErr must not fire for single-panel groups")
	}
}

// TestStitchAdjacent_FallbackKeepsPanelsOnDecodeError pins the
// never-lose-images contract AND the onErr seam — the two binary-local
// copies this replaced had drifted (CLI logged the fallback, MCP was
// silent).
func TestStitchAdjacent_FallbackKeepsPanelsOnDecodeError(t *testing.T) {
	t.Parallel()
	// Two vertically adjacent panels (same x/width, abutting edges)
	// whose Data cannot be decoded — StitchGroup must fail and both
	// panels must come through individually.
	imgs := []Image{
		{Page: 5, Name: "Im0", BboxX: 80, BboxY: 400, BboxW: 432, BboxH: 100, Data: []byte("junk0")},
		{Page: 5, Name: "Im1", BboxX: 80, BboxY: 299.9, BboxW: 432, BboxH: 100, Data: []byte("junk1")},
	}

	var failedPages []int
	got := StitchAdjacent(imgs, 2.0, func(g FigureGroup, err error) {
		if err == nil {
			t.Error("onErr called with nil error")
		}
		failedPages = append(failedPages, g.Page)
	})

	if len(got) != 2 {
		t.Fatalf("fallback: got %d images, want both original panels: %+v", len(got), got)
	}
	names := []string{got[0].Name, got[1].Name}
	slices.Sort(names)
	if names[0] != "Im0" || names[1] != "Im1" {
		t.Errorf("fallback names: got %v", names)
	}
	if len(failedPages) != 1 || failedPages[0] != 5 {
		t.Errorf("onErr calls: got pages %v, want [5]", failedPages)
	}
}

func TestWriteImagesWithManifest(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "out") // exercise MkdirAll
	imgs := []Image{
		{Page: 1, Name: "Im0", Ext: "png", Width: 10, Height: 20, BitsPerComponent: 8,
			ColorSpace: "DeviceRGB", Filter: "FlateDecode",
			BboxX: 1, BboxY: 2.5, BboxW: 100, BboxH: 200, Data: []byte("png-bytes-0")},
		{Page: 3, Name: "Im1", Ext: "jpg", Width: 30, Height: 40, BitsPerComponent: 8,
			ColorSpace: "DeviceGray", Filter: "DCTDecode",
			BboxX: 0, BboxY: 0, BboxW: 50, BboxH: 60, Data: []byte("jpeg-bytes-1")},
	}

	manifestPath, err := WriteImagesWithManifest(dir, "doc", imgs)
	if err != nil {
		t.Fatalf("WriteImagesWithManifest: %v", err)
	}
	if want := filepath.Join(dir, "manifest.tsv"); manifestPath != want {
		t.Errorf("manifest path: got %q, want %q", manifestPath, want)
	}

	// Image files land under their canonical names with their bytes.
	for i, img := range imgs {
		data, err := os.ReadFile(filepath.Join(dir, ImageFileName("doc", img)))
		if err != nil {
			t.Fatalf("image %d not written: %v", i, err)
		}
		if string(data) != string(img.Data) {
			t.Errorf("image %d content mismatch", i)
		}
	}

	// Manifest parses as TSV: header + one row per image, file/page/name
	// in the columns pkg/pdfclean requires.
	mf, err := os.Open(manifestPath)
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	defer mf.Close() //nolint:errcheck
	cr := csv.NewReader(mf)
	cr.Comma = '\t'
	rows, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("manifest rows: got %d, want header + 2", len(rows))
	}
	if !slices.Equal(rows[0], manifestColumns) {
		t.Errorf("header: got %v", rows[0])
	}
	if rows[1][0] != "doc-p0001-Im0.png" || rows[1][1] != "1" || rows[1][2] != "Im0" {
		t.Errorf("row 1: got %v", rows[1])
	}
	if rows[2][0] != "doc-p0003-Im1.jpg" || rows[2][1] != "3" || rows[2][2] != "Im1" {
		t.Errorf("row 2: got %v", rows[2])
	}
	if rows[1][9] != "2.50" {
		t.Errorf("bbox_y formatting: got %q, want two decimals", rows[1][9])
	}
}
