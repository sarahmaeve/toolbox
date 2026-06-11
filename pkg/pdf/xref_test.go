package pdf

import (
	"bytes"
	"strings"
	"testing"
)

// TestValidXrefOffset_BoundaryExactlyTwoToThe63 pins the guard's own
// boundary: math.MaxInt64 (2^63-1) is not representable in float64 and
// rounds up to 2^63, so a `v > math.MaxInt64` comparison accepts exactly
// 2^63 — which then overflows the int64 conversion to a negative offset
// with ok=true, the precise failure this function exists to prevent.
func TestValidXrefOffset_BoundaryExactlyTwoToThe63(t *testing.T) {
	t.Parallel()

	const twoToThe63 = float64(1 << 63) // representable exactly
	if v, ok := validXrefOffset(pdfNumber(twoToThe63)); ok {
		t.Errorf("validXrefOffset(2^63) = (%d, true), want ok=false — int64 cannot hold 2^63", v)
	}

	// The largest float64 below 2^63 is 2^63-1024 and must still pass.
	const largestValid = float64(1<<63 - 1024)
	v, ok := validXrefOffset(pdfNumber(largestValid))
	if !ok || v != 1<<63-1024 {
		t.Errorf("validXrefOffset(2^63-1024) = (%d, %v), want (%d, true)", v, ok, int64(1<<63-1024))
	}
}

// TestReadUncompressedObject_RejectsNegativeOffset: classic xref entries
// are parsed with ParseInt, which accepts "-000000001"; the offset must
// be bounds-checked before use or skipWhitespace indexes data[-1].
func TestReadUncompressedObject_RejectsNegativeOffset(t *testing.T) {
	t.Parallel()

	f := newTestPDFFile()
	f.data = []byte("5 0 obj 42 endobj")

	if _, err := f.readUncompressedObject(5, -1); err == nil {
		t.Error("expected error for negative xref offset, got nil")
	}
}

// TestReadUncompressedObject_RejectsOffsetBeyondEOF: a stale or hostile
// offset past the end of the file must be a structural error.
func TestReadUncompressedObject_RejectsOffsetBeyondEOF(t *testing.T) {
	t.Parallel()

	f := newTestPDFFile()
	f.data = []byte("5 0 obj 42 endobj")

	if _, err := f.readUncompressedObject(5, int64(len(f.data)+100)); err == nil {
		t.Error("expected error for offset beyond EOF, got nil")
	}
}

// TestParseClassicXref_TruncatedAfterLastEntry: a classic xref table that
// ends in whitespace after its last entry (no trailer) must be rejected
// with an error, not an index-out-of-range panic — skipWhitespace can
// land pos exactly at len(data) before the isDigit check.
func TestParseClassicXref_TruncatedAfterLastEntry(t *testing.T) {
	t.Parallel()

	f := newTestPDFFile()
	// One 20-byte entry, then a single trailing space and EOF.
	f.data = []byte("xref\n0 1\n0000000000 65535 f \n ")

	_, _, err := f.parseClassicXref(0)
	if err == nil {
		t.Fatal("expected error for truncated xref table, got nil")
	}
	if !strings.Contains(err.Error(), "trailer") {
		t.Errorf("error %q should report the missing trailer", err.Error())
	}
}

// TestReadUncompressedObject_RejectsStreamLengthOverflow: a /Length close
// to MaxInt64 makes the naive `pos+length > len(data)` bounds check wrap
// negative once pos exceeds the gap, bypassing it into a doomed
// make([]byte, length). The check must be written overflow-proof.
func TestReadUncompressedObject_RejectsStreamLengthOverflow(t *testing.T) {
	t.Parallel()

	f := newTestPDFFile()
	// 2 KiB of padding pushes pos far enough that pos+length wraps past
	// MaxInt64 (the largest float64 below 2^63 is 2^63-1024).
	var buf bytes.Buffer
	buf.Write(bytes.Repeat([]byte(" "), 2048))
	objOffset := int64(buf.Len())
	buf.WriteString("5 0 obj << /Length 9223372036854774784 >> stream\nXY\nendstream\nendobj")
	f.data = buf.Bytes()

	if _, err := f.readUncompressedObject(5, objOffset); err == nil {
		t.Error("expected error for stream length exceeding file size, got nil")
	}
}
