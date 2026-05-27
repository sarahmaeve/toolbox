package pdf

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
