package pdf

import "testing"

func TestParseObjStmContents_TwoObjects(t *testing.T) {
	t.Parallel()

	// Layout: "5 0 8 4   42 (Hello)"
	//          0123456789012345678901
	//                    ^ First=10 (body starts here)
	//
	// Pairs: obj 5 at body offset 0, obj 8 at body offset 4.
	// Body: "42 (Hello)" — first value "42", second value "(Hello)".
	data := []byte("5 0 8 4   42 (Hello)")

	stm, err := parseObjStmContents(data, 2, 10)
	if err != nil {
		t.Fatalf("parseObjStmContents: %v", err)
	}

	if len(stm.pairs) != 2 {
		t.Fatalf("pairs: got %d, want 2", len(stm.pairs))
	}
	if stm.pairs[0] != [2]int{5, 0} {
		t.Errorf("pairs[0]: got %v, want [5 0]", stm.pairs[0])
	}
	if stm.pairs[1] != [2]int{8, 4} {
		t.Errorf("pairs[1]: got %v, want [8 4]", stm.pairs[1])
	}
	if string(stm.body) != "42 (Hello)" {
		t.Errorf("body: got %q, want %q", stm.body, "42 (Hello)")
	}
}

func TestParseObjStmContents_OffsetsRelativeToFirst(t *testing.T) {
	t.Parallel()

	// Prefix says obj 7 at offset 0, obj 9 at offset 3.
	// Whole stream: "7 0 9 3 <<>>true"
	//                01234567890123456
	//                        ^ First=8
	// body = "<<>>true"; obj 7 at body[0] = "<<>>" (empty dict),
	//                    obj 9 at body[4] = "true".
	data := []byte("7 0 9 4 <<>>true")
	stm, err := parseObjStmContents(data, 2, 8)
	if err != nil {
		t.Fatalf("parseObjStmContents: %v", err)
	}
	if stm.pairs[0] != [2]int{7, 0} {
		t.Errorf("pairs[0]: got %v, want [7 0]", stm.pairs[0])
	}
	if stm.pairs[1] != [2]int{9, 4} {
		t.Errorf("pairs[1]: got %v, want [9 4]", stm.pairs[1])
	}
	if string(stm.body) != "<<>>true" {
		t.Errorf("body: got %q, want %q", stm.body, "<<>>true")
	}
}

func TestParseObjStmContents_RejectsTooFewPairs(t *testing.T) {
	t.Parallel()

	// Claim N=3 but only 2 pairs in prefix.
	data := []byte("1 0 2 5   x")
	if _, err := parseObjStmContents(data, 3, 10); err == nil {
		t.Error("expected error for too few pairs, got nil")
	}
}

func TestParseObjStmContents_RejectsFirstOutOfBounds(t *testing.T) {
	t.Parallel()

	data := []byte("1 0")
	if _, err := parseObjStmContents(data, 1, 100); err == nil {
		t.Error("expected error for First > len(data), got nil")
	}
}

func TestReadCompressedObject_ReadsValueAtIndex(t *testing.T) {
	t.Parallel()

	// End-to-end (but in-memory): build a pdfFile with one fabricated
	// object stream already in its cache, then call readCompressedObject.
	f := newTestPDFFile()
	f.objStms[42] = &objStm{
		body:  []byte("100 (hi)"),
		pairs: [][2]int{{5, 0}, {8, 4}},
	}

	got, err := f.readCompressedObject(5, 42, 0)
	if err != nil {
		t.Fatalf("readCompressedObject obj 5: %v", err)
	}
	if n, ok := got.(pdfNumber); !ok || n != 100 {
		t.Errorf("obj 5: got %v (%T), want pdfNumber(100)", got, got)
	}

	got, err = f.readCompressedObject(8, 42, 1)
	if err != nil {
		t.Fatalf("readCompressedObject obj 8: %v", err)
	}
	if s, ok := got.(pdfString); !ok || string(s) != "hi" {
		t.Errorf("obj 8: got %v (%T), want pdfString(\"hi\")", got, got)
	}
}

func TestReadCompressedObject_RejectsIndexMismatch(t *testing.T) {
	t.Parallel()

	// Requested object number doesn't match the pair at the given index.
	f := newTestPDFFile()
	f.objStms[42] = &objStm{
		body:  []byte("100"),
		pairs: [][2]int{{5, 0}},
	}

	if _, err := f.readCompressedObject(6, 42, 0); err == nil {
		t.Error("expected error for obj-number mismatch, got nil")
	}
}
