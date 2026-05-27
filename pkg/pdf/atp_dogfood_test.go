package pdf

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// atpDogfoodPath is the local sample used to drive PDF 1.5+ work end-to-end.
// The file is not part of the repo; the test skips cleanly when it is absent.
const atpDogfoodPath = "/tmp/ARN3099_ATP-3-01x81-FINAL-WEB.pdf"

// TestExtractATP exercises the full pipeline against a real US Army Techniques
// Publication PDF. ATP 3-01.81 (Counter-UAS) is a PDF 1.5+ document with
// compressed cross-reference streams and object streams, so this test is the
// load-bearing RED driver for the xref-stream / objstm / predictor work.
func TestExtractATP(t *testing.T) {
	if _, err := os.Stat(atpDogfoodPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("dogfood PDF not present at %s", atpDogfoodPath)
		}
		t.Fatalf("stat %s: %v", atpDogfoodPath, err)
	}

	pages, err := ExtractAllPages(atpDogfoodPath)
	if err != nil {
		t.Fatalf("ExtractAllPages: %v", err)
	}

	// A real doctrine PDF should have a substantial page count.
	if len(pages) < 30 {
		t.Errorf("page count: got %d, want at least 30", len(pages))
	}

	combined := strings.Join(pages, "\n")
	if len(combined) < 10_000 {
		t.Errorf("combined text length: got %d, want at least 10000", len(combined))
	}

	// C-UAS doctrine is overwhelmingly likely to contain this word. If it
	// doesn't appear at all, character decoding is broken.
	if !strings.Contains(combined, "Unmanned") {
		t.Errorf("combined text does not contain %q; sample of first 500 chars: %q",
			"Unmanned", safeHead(combined, 500))
	}
}

func safeHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
