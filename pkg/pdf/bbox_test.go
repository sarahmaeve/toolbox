package pdf

import (
	"math"
	"testing"
)

func bboxClose(a, b bbox, eps float64) bool {
	return math.Abs(a.X-b.X) < eps && math.Abs(a.Y-b.Y) < eps &&
		math.Abs(a.W-b.W) < eps && math.Abs(a.H-b.H) < eps
}

func TestWalkXObjectPlacements_SimpleUprightImage(t *testing.T) {
	t.Parallel()

	// "Place Im0 as a 100x200 box anchored at (50, 75) page coords."
	// cm operands: a b c d e f. For an upright placement, b=c=0,
	// a = width, d = height, e/f = translation.
	content := []byte("q 100 0 0 200 50 75 cm /Im0 Do Q")

	got := walkXObjectPlacements(content)
	want := []xobjectPlacement{
		{name: "Im0", box: bbox{X: 50, Y: 75, W: 100, H: 200}},
	}

	if len(got) != len(want) {
		t.Fatalf("placements: got %d, want %d (%+v)", len(got), len(want), got)
	}
	for i, p := range got {
		if p.name != want[i].name {
			t.Errorf("placements[%d].name: got %q, want %q", i, p.name, want[i].name)
		}
		if !bboxClose(p.box, want[i].box, 1e-6) {
			t.Errorf("placements[%d].box: got %+v, want %+v", i, p.box, want[i].box)
		}
	}
}

func TestWalkXObjectPlacements_MultiplePlacements(t *testing.T) {
	t.Parallel()

	// Three side-by-side panels of 100x50 each at y=200, abutting in x.
	content := []byte(`
		q 100 0 0 50 0 200 cm /Im0 Do Q
		q 100 0 0 50 100 200 cm /Im1 Do Q
		q 100 0 0 50 200 200 cm /Im2 Do Q
	`)

	got := walkXObjectPlacements(content)
	want := []xobjectPlacement{
		{name: "Im0", box: bbox{X: 0, Y: 200, W: 100, H: 50}},
		{name: "Im1", box: bbox{X: 100, Y: 200, W: 100, H: 50}},
		{name: "Im2", box: bbox{X: 200, Y: 200, W: 100, H: 50}},
	}

	if len(got) != len(want) {
		t.Fatalf("placements: got %d, want %d (%+v)", len(got), len(want), got)
	}
	for i, p := range got {
		if p.name != want[i].name || !bboxClose(p.box, want[i].box, 1e-6) {
			t.Errorf("placements[%d]: got %+v, want %+v", i, p, want[i])
		}
	}
}

func TestWalkXObjectPlacements_NestedSaveRestore(t *testing.T) {
	t.Parallel()

	// Outer cm sets a 2x scale; inner cm translates by (10, 20).
	// Im0 inside both should land at (10, 20) scaled by 2 -> bbox starts at (20, 40).
	// Im1 after the inner Q should land at (0, 0) scaled by 2 -> bbox at (0, 0).
	content := []byte(`
		q 2 0 0 2 0 0 cm
		  q 5 0 0 5 10 20 cm /Im0 Do Q
		  q 5 0 0 5 0 0 cm /Im1 Do Q
		Q
	`)

	got := walkXObjectPlacements(content)
	want := []xobjectPlacement{
		// /Im0: local cm scale=5, translate=(10,20); outer scale=2, translate=(0,0).
		// Combined: unit square -> (2*10, 2*20) origin, size 2*5 = 10 each side.
		{name: "Im0", box: bbox{X: 20, Y: 40, W: 10, H: 10}},
		// /Im1: local scale=5, translate=(0,0); outer scale=2 -> size 10 at origin.
		{name: "Im1", box: bbox{X: 0, Y: 0, W: 10, H: 10}},
	}

	if len(got) != len(want) {
		t.Fatalf("placements: got %d, want %d (%+v)", len(got), len(want), got)
	}
	for i, p := range got {
		if p.name != want[i].name || !bboxClose(p.box, want[i].box, 1e-6) {
			t.Errorf("placements[%d]: got %+v, want %+v", i, p, want[i])
		}
	}
}

func TestWalkXObjectPlacements_IgnoresTextAndOtherOps(t *testing.T) {
	t.Parallel()

	// A realistic-ish stream with text drawing, font setup, paths, and one Do.
	content := []byte(`
		BT /F1 12 Tf 100 700 Td (Hello) Tj ET
		0.5 0.5 0.5 rg
		q 50 0 0 50 200 300 cm /Img Do Q
		100 400 m 200 400 l S
	`)

	got := walkXObjectPlacements(content)
	want := []xobjectPlacement{
		{name: "Img", box: bbox{X: 200, Y: 300, W: 50, H: 50}},
	}

	if len(got) != len(want) {
		t.Fatalf("placements: got %d, want %d (%+v)", len(got), len(want), got)
	}
	if got[0].name != want[0].name || !bboxClose(got[0].box, want[0].box, 1e-6) {
		t.Errorf("placement: got %+v, want %+v", got[0], want[0])
	}
}

func TestWalkXObjectPlacements_RotatedImage(t *testing.T) {
	t.Parallel()

	// 90° rotation: a=0 b=1 c=-1 d=0. Unit square corners map to:
	//   (0,0) -> (0, 0)        + (50, 50) = (50, 50)
	//   (1,0) -> (0, 1)        + (50, 50) = (50, 51)
	//   (0,1) -> (-1, 0)       + (50, 50) = (49, 50)
	//   (1,1) -> (-1, 1)       + (50, 50) = (49, 51)
	// Axis-aligned bbox: x=49, y=50, w=1, h=1.
	// But the typical pattern scales first then rotates: a=0 b=W c=-H d=0 e=tx f=ty.
	// Let W=10 H=20, translate (50, 50): a=0 b=10 c=-20 d=0 e=50 f=50.
	//   (0,0) -> (50, 50)
	//   (1,0) -> (50, 60)
	//   (0,1) -> (30, 50)
	//   (1,1) -> (30, 60)
	// AABB: x=30, y=50, w=20, h=10.
	content := []byte(`q 0 10 -20 0 50 50 cm /Im0 Do Q`)

	got := walkXObjectPlacements(content)
	if len(got) != 1 {
		t.Fatalf("placements: got %d, want 1", len(got))
	}
	want := bbox{X: 30, Y: 50, W: 20, H: 10}
	if !bboxClose(got[0].box, want, 1e-6) {
		t.Errorf("rotated bbox: got %+v, want %+v", got[0].box, want)
	}
}
