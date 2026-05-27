package pdf

import (
	"bytes"
	"compress/zlib"
	"testing"
)

func TestDecompressFlate(t *testing.T) {
	t.Parallel()

	t.Run("round-trip known data", func(t *testing.T) {
		t.Parallel()
		original := []byte("The quick brown fox jumps over the lazy dog")

		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		if _, err := w.Write(original); err != nil {
			t.Fatalf("zlib write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("zlib close: %v", err)
		}

		got, err := decompressFlate(buf.Bytes())
		if err != nil {
			t.Fatalf("decompressFlate: %v", err)
		}
		if !bytes.Equal(got, original) {
			t.Errorf("round-trip: got %q, want %q", got, original)
		}
	})

	t.Run("invalid data returns error", func(t *testing.T) {
		t.Parallel()
		_, err := decompressFlate([]byte("not compressed data"))
		if err == nil {
			t.Error("expected error for invalid zlib data, got nil")
		}
	})
}
