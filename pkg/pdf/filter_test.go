package pdf

import (
	"bytes"
	"compress/zlib"
	"strings"
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

	t.Run("rejects decompression bomb exceeding limit", func(t *testing.T) {
		t.Parallel()

		// Pack a multi-MB payload that compresses to a few KB — the classic
		// zip-bomb shape. The cap must fire before the bomb empties the
		// host's memory.
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		bomb := bytes.Repeat([]byte{0}, maxDecompressedStreamSize+1024)
		if _, err := w.Write(bomb); err != nil {
			t.Fatalf("zlib write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("zlib close: %v", err)
		}

		got, err := decompressFlate(buf.Bytes())
		if err == nil {
			t.Fatalf("expected decompression-cap error, got %d bytes", len(got))
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error %q does not name the cap (want substring \"exceeds\")", err.Error())
		}
	})

	t.Run("decodes payload exactly at limit", func(t *testing.T) {
		t.Parallel()

		// One-byte-under the cap must still succeed — the limit is exclusive
		// of the cap itself, so the error message can name the cap.
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		original := bytes.Repeat([]byte{'a'}, maxDecompressedStreamSize-1)
		if _, err := w.Write(original); err != nil {
			t.Fatalf("zlib write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("zlib close: %v", err)
		}

		got, err := decompressFlate(buf.Bytes())
		if err != nil {
			t.Fatalf("decompressFlate at limit: %v", err)
		}
		if len(got) != len(original) {
			t.Errorf("decoded length: got %d, want %d", len(got), len(original))
		}
	})
}
