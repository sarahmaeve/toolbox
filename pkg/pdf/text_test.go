package pdf

import (
	"strings"
	"testing"
)

// buildSingleByteCMap constructs a cmapTable that maps single byte values to
// their ASCII characters for the given chars.
func buildSingleByteCMap(chars string) cmapTable {
	m := cmapTable{}
	for _, ch := range []byte(chars) {
		m[uint16(ch)] = string(rune(ch))
	}
	return m
}

func assertContains(t *testing.T, got, substr, label string) {
	t.Helper()
	if !strings.Contains(got, substr) {
		t.Errorf("%s: %q does not contain %q", label, got, substr)
	}
}

func TestExtractText(t *testing.T) {
	t.Parallel()

	t.Run("basic Tj operator", func(t *testing.T) {
		t.Parallel()
		content := []byte(`BT
/F0 10 Tf
(Hello) Tj
ET
`)
		fonts := map[string]cmapTable{
			"F0": buildSingleByteCMap("Hello"),
		}
		got := extractText(content, fonts)
		assertContains(t, got, "Hello", "basic Tj")
	})

	t.Run("TJ with kerning inserts space", func(t *testing.T) {
		t.Parallel()
		content := []byte(`BT
/F0 10 Tf
[(H) -200 (i)] TJ
ET
`)
		fonts := map[string]cmapTable{
			"F0": buildSingleByteCMap("Hi"),
		}
		got := extractText(content, fonts)
		assertContains(t, got, "H", "TJ H present")
		assertContains(t, got, "i", "TJ i present")
		if !strings.Contains(got, "H i") && !strings.Contains(got, "H  i") {
			t.Errorf("TJ kerning: got %q, want space between H and i", got)
		}
	})

	t.Run("line break via Td", func(t *testing.T) {
		t.Parallel()
		content := []byte(`BT
/F0 10 Tf
(Line1) Tj
0 -12 Td
(Line2) Tj
ET
`)
		fonts := map[string]cmapTable{
			"F0": buildSingleByteCMap("Line12"),
		}
		got := extractText(content, fonts)
		assertContains(t, got, "Line1", "Td Line1")
		assertContains(t, got, "Line2", "Td Line2")
		if !strings.Contains(got, "Line1\nLine2") {
			t.Errorf("Td line break: got %q, want newline between Line1 and Line2", got)
		}
	})

	t.Run("missing font falls back to Latin-1", func(t *testing.T) {
		t.Parallel()
		content := []byte(`BT
/F1 10 Tf
(ABC) Tj
ET
`)
		got := extractText(content, map[string]cmapTable{})
		assertContains(t, got, "ABC", "Latin-1 fallback")
	})

	t.Run("empty content returns empty string", func(t *testing.T) {
		t.Parallel()
		got := extractText(nil, nil)
		if got != "" {
			t.Errorf("empty content: got %q, want %q", got, "")
		}
	})
}
