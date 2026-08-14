package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSelectiveExtractionPDF creates a three-page, classic-xref PDF. Page 2
// deliberately uses an unsupported content-stream filter, making it a tripwire:
// extracting page 1 or 3 succeeds only when the implementation does not decode
// unrequested page content.
func writeSelectiveExtractionPDF(t *testing.T) string {
	t.Helper()

	stream := func(text, extraDict string) string {
		return fmt.Sprintf("<< /Length %d%s >>\nstream\n%s\nendstream", len(text), extraDict, text)
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>",
		"<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>",
		"<< /Type /Page /Parent 2 0 R /Contents 7 0 R >>",
		"<< /Type /Page /Parent 2 0 R /Contents 8 0 R >>",
		stream("BT /F0 10 Tf 1 0 0 1 20 100 Tm (Page one) Tj ET", ""),
		stream("BT /F0 10 Tf 1 0 0 1 20 100 Tm (Page two) Tj ET", " /Filter /UnsupportedForTest"),
		stream("BT /F0 10 Tf 1 0 0 1 20 100 Tm (Page three) Tj ET", ""),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		n := i + 1
		offsets[n] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, object)
	}
	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(offsets))
	buf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets), xrefOffset)

	path := filepath.Join(t.TempDir(), "selective.pdf")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write selective PDF: %v", err)
	}
	return path
}

func TestRecoverAsError_ConvertsRuntimePanicToError(t *testing.T) {
	t.Parallel()

	// Runtime panics (index out of range, nil deref, etc.) inside the
	// parser must surface as errors at the API boundary instead of taking
	// down the host. The helper is the single point where that conversion
	// happens.
	var err error
	func() {
		defer recoverAsError(&err)
		var s []byte
		idx := 7
		_ = s[idx] // triggers runtime panic
	}()
	if err == nil {
		t.Fatal("expected panic to be converted to error, got nil")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("error %q does not mention panic origin", err.Error())
	}
}

func TestRecoverAsError_PreservesPriorError(t *testing.T) {
	t.Parallel()

	// If a non-panic error already populated *errp, the recover helper
	// must not overwrite it when no panic occurred — otherwise a clean
	// "file not found" would be silently masked.
	prior := errors.New("prior failure")
	err := prior
	func() {
		defer recoverAsError(&err)
		// no panic
	}()
	if !errors.Is(err, prior) {
		t.Errorf("recoverAsError clobbered prior error: got %v, want %v", err, prior)
	}
}

func TestExtractText_HostileFileDoesNotCrash(t *testing.T) {
	t.Parallel()

	// Plant a "PDF" whose header is valid but whose body lures the parser
	// into a code path that would normally panic with index-out-of-range
	// (the trailing bytes after "%PDF-" are unparseable). Whatever the
	// parser does internally, the API must return an error — never panic
	// past the boundary.
	dir := t.TempDir()
	path := filepath.Join(dir, "hostile.pdf")
	body := []byte("%PDF-1.7\n\x00\x00\x00\x00startxref\n9999999\n%%EOF")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write hostile pdf: %v", err)
	}

	// We don't care whether the error names a specific failure mode — we
	// only care that no panic escapes. An err == nil result would be a
	// surprise (the file is garbage) but not a recovery-policy failure.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ExtractText panicked past the api boundary: %v", r)
		}
	}()
	_, _ = ExtractText(path)
}

func TestExtractPages_OnlyDecodesRequestedRange(t *testing.T) {
	t.Parallel()

	path := writeSelectiveExtractionPDF(t)

	pages, pageCount, err := ExtractPages(path, 1, 1)
	if err != nil {
		t.Fatalf("ExtractPages(1, 1): %v", err)
	}
	if pageCount != 3 {
		t.Fatalf("page count: got %d, want 3", pageCount)
	}
	if len(pages) != 1 || strings.TrimSpace(pages[0]) != "Page one" {
		t.Fatalf("pages: got %q, want [Page one]", pages)
	}

	pages, pageCount, err = ExtractPages(path, 3, 3)
	if err != nil {
		t.Fatalf("ExtractPages(3, 3): %v", err)
	}
	if pageCount != 3 {
		t.Fatalf("page count: got %d, want 3", pageCount)
	}
	if len(pages) != 1 || strings.TrimSpace(pages[0]) != "Page three" {
		t.Fatalf("pages: got %q, want [Page three]", pages)
	}

	if _, err := ExtractAllPages(path); err == nil {
		t.Fatal("ExtractAllPages unexpectedly succeeded; page 2 is deliberately undecodable")
	} else if !strings.Contains(err.Error(), "extracting page 2") {
		t.Fatalf("ExtractAllPages error: got %q, want page 2 context", err)
	}
}

func TestExtractPages_ReportsOutOfBoundsRange(t *testing.T) {
	t.Parallel()

	path := writeSelectiveExtractionPDF(t)
	_, pageCount, err := ExtractPages(path, 3, 4)
	if pageCount != 3 {
		t.Fatalf("page count: got %d, want 3", pageCount)
	}
	var rangeErr *PageRangeError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("error: got %v, want *PageRangeError", err)
	}
	if rangeErr.From != 3 || rangeErr.To != 4 || rangeErr.PageCount != 3 {
		t.Fatalf("range error: got %+v, want 3-4 against 3 pages", rangeErr)
	}
}

func TestExtractMarkdownPages_OnlyDecodesRequestedRange(t *testing.T) {
	t.Parallel()

	path := writeSelectiveExtractionPDF(t)
	pages, pageCount, err := ExtractMarkdownPages(path, 1, 1)
	if err != nil {
		t.Fatalf("ExtractMarkdownPages(1, 1): %v", err)
	}
	if pageCount != 3 {
		t.Fatalf("page count: got %d, want 3", pageCount)
	}
	if len(pages) != 1 || !strings.Contains(pages[0], "Page one") {
		t.Fatalf("pages: got %q, want selected page-one Markdown", pages)
	}
}

func TestExtractImagePages_ReportsPageCountAndBounds(t *testing.T) {
	t.Parallel()

	path := writeSelectiveExtractionPDF(t)
	images, pageCount, err := ExtractImagePages(path, 3, 3)
	if err != nil {
		t.Fatalf("ExtractImagePages(3, 3): %v", err)
	}
	if pageCount != 3 {
		t.Fatalf("page count: got %d, want 3", pageCount)
	}
	if len(images) != 0 {
		t.Fatalf("images: got %d, want 0", len(images))
	}

	_, pageCount, err = ExtractImagePages(path, 3, 4)
	if pageCount != 3 {
		t.Fatalf("out-of-bounds page count: got %d, want 3", pageCount)
	}
	var rangeErr *PageRangeError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("error: got %v, want *PageRangeError", err)
	}
}
