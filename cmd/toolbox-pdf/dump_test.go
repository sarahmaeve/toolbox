package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOCRGlossary(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "terms.txt")
	if err := os.WriteFile(path, []byte("# scholarly terms\nMaǧnūn\n\nmaǧlis\n"), 0o600); err != nil {
		t.Fatalf("write glossary: %v", err)
	}
	words, err := readOCRGlossary(path)
	if err != nil {
		t.Fatalf("readOCRGlossary: %v", err)
	}
	if len(words) != 2 || words[0] != "Maǧnūn" || words[1] != "maǧlis" {
		t.Fatalf("words: got %q", words)
	}
}
