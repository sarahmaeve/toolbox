package pdf

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestGroupAdjacent_SingleImagePassThrough(t *testing.T) {
	t.Parallel()

	imgs := []Image{
		{Page: 1, Name: "Im0", BboxX: 100, BboxY: 100, BboxW: 50, BboxH: 50},
	}

	groups := GroupAdjacent(imgs, 2.0)
	if len(groups) != 1 {
		t.Fatalf("groups: got %d, want 1", len(groups))
	}
	g := groups[0]
	if len(g.Parts) != 1 || g.Parts[0].Name != "Im0" {
		t.Errorf("group parts: %+v", g.Parts)
	}
}

func TestGroupAdjacent_VerticalStackThreePanels(t *testing.T) {
	t.Parallel()

	// Three panels, same x and width, stacked tightly (0.07 pt gap).
	// In PDF coords Y increases upward, so Im0 with the highest Y is the top panel.
	imgs := []Image{
		{Page: 5, Name: "Im0", BboxX: 80, BboxY: 500, BboxW: 432, BboxH: 100},
		{Page: 5, Name: "Im1", BboxX: 80, BboxY: 399.93, BboxW: 432, BboxH: 100},
		{Page: 5, Name: "Im2", BboxX: 80, BboxY: 299.86, BboxW: 432, BboxH: 100},
	}

	groups := GroupAdjacent(imgs, 2.0)
	if len(groups) != 1 {
		t.Fatalf("groups: got %d, want 1 — %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Layout != "vertical" {
		t.Errorf("Layout: got %q, want %q", g.Layout, "vertical")
	}
	if len(g.Parts) != 3 {
		t.Fatalf("parts: got %d, want 3", len(g.Parts))
	}
	// Order should be top→bottom: Im0, Im1, Im2.
	wantOrder := []string{"Im0", "Im1", "Im2"}
	for i, want := range wantOrder {
		if g.Parts[i].Name != want {
			t.Errorf("parts[%d].Name: got %q, want %q", i, g.Parts[i].Name, want)
		}
	}
}

func TestGroupAdjacent_NonAdjacentStaySeparate(t *testing.T) {
	t.Parallel()

	// Two panels with a large vertical gap — different figures.
	imgs := []Image{
		{Page: 5, Name: "ImA", BboxX: 100, BboxY: 600, BboxW: 200, BboxH: 100},
		{Page: 5, Name: "ImB", BboxX: 100, BboxY: 100, BboxW: 200, BboxH: 100}, // 400pt gap
	}

	groups := GroupAdjacent(imgs, 2.0)
	if len(groups) != 2 {
		t.Fatalf("groups: got %d, want 2", len(groups))
	}
}

func TestGroupAdjacent_HorizontalStrip(t *testing.T) {
	t.Parallel()

	// Three panels side-by-side at same Y.
	imgs := []Image{
		{Page: 5, Name: "Im0", BboxX: 0, BboxY: 200, BboxW: 100, BboxH: 50},
		{Page: 5, Name: "Im1", BboxX: 100, BboxY: 200, BboxW: 100, BboxH: 50},
		{Page: 5, Name: "Im2", BboxX: 200, BboxY: 200, BboxW: 100, BboxH: 50},
	}

	groups := GroupAdjacent(imgs, 2.0)
	if len(groups) != 1 {
		t.Fatalf("groups: got %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Layout != "horizontal" {
		t.Errorf("Layout: got %q, want %q", g.Layout, "horizontal")
	}
	if len(g.Parts) != 3 {
		t.Fatalf("parts: got %d, want 3", len(g.Parts))
	}
	wantOrder := []string{"Im0", "Im1", "Im2"}
	for i, want := range wantOrder {
		if g.Parts[i].Name != want {
			t.Errorf("parts[%d].Name: got %q, want %q", i, g.Parts[i].Name, want)
		}
	}
}

func TestGroupAdjacent_DifferentPagesStaySeparate(t *testing.T) {
	t.Parallel()

	imgs := []Image{
		{Page: 5, Name: "Im0", BboxX: 100, BboxY: 100, BboxW: 200, BboxH: 50},
		{Page: 6, Name: "Im0", BboxX: 100, BboxY: 150, BboxW: 200, BboxH: 50},
	}

	groups := GroupAdjacent(imgs, 2.0)
	if len(groups) != 2 {
		t.Fatalf("groups: got %d, want 2", len(groups))
	}
}

func TestGroupAdjacent_ZeroBboxPassesThrough(t *testing.T) {
	t.Parallel()

	// Both images have zero bbox — they should not cluster together even
	// though all coordinates are equal.
	imgs := []Image{
		{Page: 5, Name: "Im0"},
		{Page: 5, Name: "Im1"},
	}

	groups := GroupAdjacent(imgs, 2.0)
	if len(groups) != 2 {
		t.Errorf("groups: got %d, want 2", len(groups))
	}
}

func TestStitchGroup_VerticalTwoPanels(t *testing.T) {
	t.Parallel()

	// Two 4x2 gray panels stacked vertically with 0pt gap.
	// Top panel: all 0x80.
	// Bottom panel: all 0xC0.
	top := mustEncodeGrayPNG(t, 4, 2, []byte{
		0x80, 0x80, 0x80, 0x80,
		0x80, 0x80, 0x80, 0x80,
	})
	bot := mustEncodeGrayPNG(t, 4, 2, []byte{
		0xC0, 0xC0, 0xC0, 0xC0,
		0xC0, 0xC0, 0xC0, 0xC0,
	})

	g := FigureGroup{
		Page:   1,
		Layout: "vertical",
		Parts: []Image{
			{Page: 1, Name: "Im0", Ext: "png", Data: top,
				Width: 4, Height: 2, BboxX: 0, BboxY: 100, BboxW: 100, BboxH: 50},
			{Page: 1, Name: "Im1", Ext: "png", Data: bot,
				Width: 4, Height: 2, BboxX: 0, BboxY: 50, BboxW: 100, BboxH: 50},
		},
	}

	out, err := StitchGroup(g)
	if err != nil {
		t.Fatalf("StitchGroup: %v", err)
	}
	if out.Ext != "png" {
		t.Errorf("Ext: got %q, want %q", out.Ext, "png")
	}
	decoded, err := png.Decode(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != 4 || b.Dy() != 4 {
		t.Errorf("dims: got %dx%d, want 4x4", b.Dx(), b.Dy())
	}
	// Row 0 should be 0x80 (top panel), row 3 should be 0xC0 (bottom panel).
	r0, _, _, _ := decoded.At(0, 0).RGBA()
	r3, _, _, _ := decoded.At(0, 3).RGBA()
	if r0>>8 != 0x80 {
		t.Errorf("pixel(0,0) Y: got %02x, want 0x80", r0>>8)
	}
	if r3>>8 != 0xC0 {
		t.Errorf("pixel(0,3) Y: got %02x, want 0xC0", r3>>8)
	}
}

func TestStitchGroup_HorizontalTwoPanels(t *testing.T) {
	t.Parallel()

	left := mustEncodeGrayPNG(t, 2, 4, []byte{
		0x40, 0x40, 0x40, 0x40, 0x40, 0x40, 0x40, 0x40,
	})
	right := mustEncodeGrayPNG(t, 2, 4, []byte{
		0xA0, 0xA0, 0xA0, 0xA0, 0xA0, 0xA0, 0xA0, 0xA0,
	})

	g := FigureGroup{
		Page:   1,
		Layout: "horizontal",
		Parts: []Image{
			{Page: 1, Name: "Im0", Ext: "png", Data: left,
				Width: 2, Height: 4, BboxX: 0, BboxY: 100, BboxW: 50, BboxH: 100},
			{Page: 1, Name: "Im1", Ext: "png", Data: right,
				Width: 2, Height: 4, BboxX: 50, BboxY: 100, BboxW: 50, BboxH: 100},
		},
	}

	out, err := StitchGroup(g)
	if err != nil {
		t.Fatalf("StitchGroup: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != 4 || b.Dy() != 4 {
		t.Errorf("dims: got %dx%d, want 4x4", b.Dx(), b.Dy())
	}
	// Column 0 should be 0x40 (left panel), column 3 should be 0xA0.
	r0, _, _, _ := decoded.At(0, 0).RGBA()
	r3, _, _, _ := decoded.At(3, 0).RGBA()
	if r0>>8 != 0x40 {
		t.Errorf("pixel(0,0) Y: got %02x, want 0x40", r0>>8)
	}
	if r3>>8 != 0xA0 {
		t.Errorf("pixel(3,0) Y: got %02x, want 0xA0", r3>>8)
	}
}

// --- helpers ----------------------------------------------------------------

func mustEncodeGrayPNG(t *testing.T, w, h int, pix []byte) []byte {
	t.Helper()
	if len(pix) != w*h {
		t.Fatalf("pix length %d != w*h %d", len(pix), w*h)
	}
	g := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		copy(g.Pix[y*g.Stride:y*g.Stride+w], pix[y*w:(y+1)*w])
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, g); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}
