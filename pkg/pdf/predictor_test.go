package pdf

import (
	"bytes"
	"testing"
)

func TestApplyPredictor_Passthrough(t *testing.T) {
	t.Parallel()

	got, err := applyPredictor([]byte{1, 2, 3, 4}, pdfDict{
		"Predictor": pdfNumber(1),
	})
	if err != nil {
		t.Fatalf("predictor 1: %v", err)
	}
	want := []byte{1, 2, 3, 4}
	if !bytes.Equal(got, want) {
		t.Errorf("predictor 1: got %v, want %v", got, want)
	}
}

func TestApplyPredictor_PNG_Up(t *testing.T) {
	t.Parallel()

	// /Columns = 4. Three rows, all using filter byte 2 (Up).
	// Original rows:
	//   row 0: 10 20 30 40
	//   row 1: 11 22 33 44
	//   row 2: 12 24 36 48
	//
	// With Up filter applied (delta from previous row; row 0 vs zeros):
	//   row 0 filtered: 10 20 30 40
	//   row 1 filtered:  1  2  3  4
	//   row 2 filtered:  1  2  3  4
	//
	// Prepended with filter byte 2 per row:
	encoded := []byte{
		2, 10, 20, 30, 40,
		2, 1, 2, 3, 4,
		2, 1, 2, 3, 4,
	}
	want := []byte{10, 20, 30, 40, 11, 22, 33, 44, 12, 24, 36, 48}

	got, err := applyPredictor(encoded, pdfDict{
		"Predictor": pdfNumber(12),
		"Columns":   pdfNumber(4),
	})
	if err != nil {
		t.Fatalf("predictor 12 Up: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("predictor 12 Up:\ngot  %v\nwant %v", got, want)
	}
}

func TestApplyPredictor_PNG_None(t *testing.T) {
	t.Parallel()

	encoded := []byte{
		0, 7, 8, 9,
		0, 10, 11, 12,
	}
	want := []byte{7, 8, 9, 10, 11, 12}

	got, err := applyPredictor(encoded, pdfDict{
		"Predictor": pdfNumber(10),
		"Columns":   pdfNumber(3),
	})
	if err != nil {
		t.Fatalf("predictor 10 None: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("predictor 10 None:\ngot  %v\nwant %v", got, want)
	}
}

func TestApplyPredictor_PNG_Sub(t *testing.T) {
	t.Parallel()

	// /Columns = 4. bpp defaults to 1 (Colors=1, BitsPerComponent=8).
	// Original row: 5 12 20 30
	// Sub filter: each byte = orig[i] - orig[i-1] (with i-1<0 → 0)
	//   filtered:  5  7  8 10
	encoded := []byte{
		1, 5, 7, 8, 10,
	}
	want := []byte{5, 12, 20, 30}

	got, err := applyPredictor(encoded, pdfDict{
		"Predictor": pdfNumber(11),
		"Columns":   pdfNumber(4),
	})
	if err != nil {
		t.Fatalf("predictor 11 Sub: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("predictor 11 Sub:\ngot  %v\nwant %v", got, want)
	}
}

func TestApplyPredictor_PNG_PerRowFilter(t *testing.T) {
	t.Parallel()

	// Predictor 15 (optimum) lets each row pick. Exercise None then Up.
	encoded := []byte{
		0, 100, 200, 50, 25, // row 0: None
		2, 1, 2, 3, 4, // row 1: Up (delta from row 0)
	}
	want := []byte{100, 200, 50, 25, 101, 202, 53, 29}

	got, err := applyPredictor(encoded, pdfDict{
		"Predictor": pdfNumber(15),
		"Columns":   pdfNumber(4),
	})
	if err != nil {
		t.Fatalf("predictor 15 mixed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("predictor 15 mixed:\ngot  %v\nwant %v", got, want)
	}
}

func TestApplyPredictor_PNG_Average(t *testing.T) {
	t.Parallel()

	// Average filter: out[i] = filtered[i] + floor((out[i-bpp] + prev[i]) / 2)
	// bpp = 1 (default).
	// Original row 0: 10 20 30 40
	// Original row 1: 12 22 33 45
	//
	// Row 0 with filter 3 (and prev = zeros):
	//   filtered[0] = 10 - floor((0 + 0)/2) = 10
	//   filtered[1] = 20 - floor((10 + 0)/2) = 15
	//   filtered[2] = 30 - floor((20 + 0)/2) = 20
	//   filtered[3] = 40 - floor((30 + 0)/2) = 25
	// Row 1 with filter 3:
	//   filtered[0] = 12 - floor((0 + 10)/2) = 7
	//   filtered[1] = 22 - floor((12 + 20)/2) = 22 - 16 = 6
	//   filtered[2] = 33 - floor((22 + 30)/2) = 33 - 26 = 7
	//   filtered[3] = 45 - floor((33 + 40)/2) = 45 - 36 = 9
	encoded := []byte{
		3, 10, 15, 20, 25,
		3, 7, 6, 7, 9,
	}
	want := []byte{10, 20, 30, 40, 12, 22, 33, 45}

	got, err := applyPredictor(encoded, pdfDict{
		"Predictor": pdfNumber(13),
		"Columns":   pdfNumber(4),
	})
	if err != nil {
		t.Fatalf("predictor 13 Average: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("predictor 13 Average:\ngot  %v\nwant %v", got, want)
	}
}

func TestApplyPredictor_PNG_Paeth(t *testing.T) {
	t.Parallel()

	// Paeth filter: predictor based on a (left), b (above), c (above-left).
	// Round-trip a small case.
	// Original:
	//   row 0: 10 20 30 40
	//   row 1: 12 22 33 45
	// Filter byte 4 per row.
	//
	// PaethPredictor(a, b, c):
	//   p = a + b - c; pa = |p-a|; pb = |p-b|; pc = |p-c|
	//   pick a if pa<=pb && pa<=pc; else b if pb<=pc; else c
	//
	// Row 0 (prev = zeros, c = 0 always):
	//   x=0: a=0, b=0, c=0 → pred=0; filtered = 10 - 0 = 10
	//   x=1: a=10, b=0, c=0; p=10; pa=0 pb=10 pc=10 → a=10; filtered = 20 - 10 = 10
	//   x=2: a=20, b=0, c=0; pa=0 → 20; filtered = 30 - 20 = 10
	//   x=3: a=30, b=0, c=0; pa=0 → 30; filtered = 40 - 30 = 10
	// Row 1 (prev = [10,20,30,40]):
	//   x=0: a=0, b=10, c=0; p=10; pa=10 pb=0 pc=10 → b=10; filtered = 12 - 10 = 2
	//   x=1: a=12, b=20, c=10; p=22; pa=10 pb=2 pc=12 → b=20; filtered = 22 - 20 = 2
	//   x=2: a=22, b=30, c=20; p=32; pa=10 pb=2 pc=12 → b=30; filtered = 33 - 30 = 3
	//   x=3: a=33, b=40, c=30; p=43; pa=10 pb=3 pc=13 → b=40; filtered = 45 - 40 = 5
	encoded := []byte{
		4, 10, 10, 10, 10,
		4, 2, 2, 3, 5,
	}
	want := []byte{10, 20, 30, 40, 12, 22, 33, 45}

	got, err := applyPredictor(encoded, pdfDict{
		"Predictor": pdfNumber(14),
		"Columns":   pdfNumber(4),
	})
	if err != nil {
		t.Fatalf("predictor 14 Paeth: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("predictor 14 Paeth:\ngot  %v\nwant %v", got, want)
	}
}
