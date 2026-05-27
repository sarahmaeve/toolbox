package pdf

import "testing"

func TestDecodeXrefStreamEntries_AllThreeKinds(t *testing.T) {
	t.Parallel()

	// /W [1 2 1], /Index [0 3]. Three packed entries, 4 bytes each:
	//   entry 0:  type=0 (free), field2=0x0000, field3=0xFF       — ignored
	//   entry 1:  type=1 (uncompressed), offset=0x1234, gen=0     — xref[1]
	//   entry 2:  type=2 (compressed), streamObj=0x0005, idx=0    — xref[2]
	data := []byte{
		0x00, 0x00, 0x00, 0xFF,
		0x01, 0x12, 0x34, 0x00,
		0x02, 0x00, 0x05, 0x00,
	}

	got := map[int]xrefEntry{}
	if err := decodeXrefStreamEntries(data, [3]int{1, 2, 1}, [][2]int{{0, 3}}, got); err != nil {
		t.Fatalf("decodeXrefStreamEntries: %v", err)
	}

	if _, ok := got[0]; ok {
		t.Error("free entry 0 should not be recorded")
	}
	if e, ok := got[1]; !ok {
		t.Error("missing entry for obj 1")
	} else if e.kind != xrefUncompressed || e.offset != 0x1234 {
		t.Errorf("obj 1: got %+v, want uncompressed offset 0x1234", e)
	}
	if e, ok := got[2]; !ok {
		t.Error("missing entry for obj 2")
	} else if e.kind != xrefCompressed || e.objStmNum != 5 || e.objStmIdx != 0 {
		t.Errorf("obj 2: got %+v, want compressed stream=5 idx=0", e)
	}
}

func TestDecodeXrefStreamEntries_MultipleSubsections(t *testing.T) {
	t.Parallel()

	// /Index [0 1 10 2]. Three entries total: obj 0, obj 10, obj 11.
	//   obj 0:  type 0
	//   obj 10: type 1, offset 0x1000
	//   obj 11: type 2, stream 12, idx 3
	data := []byte{
		0x00, 0x00, 0x00, 0x00,
		0x01, 0x10, 0x00, 0x00,
		0x02, 0x00, 0x0C, 0x03,
	}

	got := map[int]xrefEntry{}
	err := decodeXrefStreamEntries(data, [3]int{1, 2, 1}, [][2]int{{0, 1}, {10, 2}}, got)
	if err != nil {
		t.Fatalf("decodeXrefStreamEntries: %v", err)
	}

	if e, ok := got[10]; !ok {
		t.Error("missing entry for obj 10")
	} else if e.kind != xrefUncompressed || e.offset != 0x1000 {
		t.Errorf("obj 10: got %+v, want uncompressed offset 0x1000", e)
	}
	if e, ok := got[11]; !ok {
		t.Error("missing entry for obj 11")
	} else if e.kind != xrefCompressed || e.objStmNum != 12 || e.objStmIdx != 3 {
		t.Errorf("obj 11: got %+v, want compressed stream=12 idx=3", e)
	}
}

func TestDecodeXrefStreamEntries_DefaultTypeWhenW1IsZero(t *testing.T) {
	t.Parallel()

	// /W [0 3 1]: when w1=0, type defaults to 1 (uncompressed) per spec.
	// One entry: offset 0x123456, gen 0.
	data := []byte{
		0x12, 0x34, 0x56, 0x00,
	}

	got := map[int]xrefEntry{}
	err := decodeXrefStreamEntries(data, [3]int{0, 3, 1}, [][2]int{{7, 1}}, got)
	if err != nil {
		t.Fatalf("decodeXrefStreamEntries: %v", err)
	}

	if e, ok := got[7]; !ok {
		t.Error("missing entry for obj 7")
	} else if e.kind != xrefUncompressed || e.offset != 0x123456 {
		t.Errorf("obj 7: got %+v, want uncompressed offset 0x123456", e)
	}
}

func TestDecodeXrefStreamEntries_FirstWriteWins(t *testing.T) {
	t.Parallel()

	// xref[3] is already populated from a newer revision; the older entry
	// from /Prev should not overwrite it.
	existing := map[int]xrefEntry{
		3: {kind: xrefUncompressed, offset: 0xDEAD},
	}
	data := []byte{
		0x01, 0xBE, 0xEF, 0x00,
	}

	err := decodeXrefStreamEntries(data, [3]int{1, 2, 1}, [][2]int{{3, 1}}, existing)
	if err != nil {
		t.Fatalf("decodeXrefStreamEntries: %v", err)
	}
	if existing[3].offset != 0xDEAD {
		t.Errorf("existing entry overwritten: got offset 0x%X, want 0xDEAD", existing[3].offset)
	}
}
