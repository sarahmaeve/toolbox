package pdf

import (
	"testing"
	"time"
)

func TestParseCMap(t *testing.T) {
	t.Parallel()

	t.Run("basic bfchar mapping", func(t *testing.T) {
		t.Parallel()
		data := []byte(`
beginbfchar
<20> <0020>
<2E> <002E>
endbfchar
`)
		table := parseCMap(data)

		if got := table[0x20]; got != " " {
			t.Errorf("table[0x20]: got %q, want %q", got, " ")
		}
		if got := table[0x2E]; got != "." {
			t.Errorf("table[0x2E]: got %q, want %q", got, ".")
		}
	})

	t.Run("ligature mapping multi-codepoint", func(t *testing.T) {
		t.Parallel()
		data := []byte(`
beginbfchar
<1D> <00660069>
endbfchar
`)
		table := parseCMap(data)

		if got := table[0x1D]; got != "fi" {
			t.Errorf("table[0x1D]: got %q, want %q", got, "fi")
		}
	})

	t.Run("bfrange arithmetic", func(t *testing.T) {
		t.Parallel()
		data := []byte(`
beginbfrange
<41> <43> <0041>
endbfrange
`)
		table := parseCMap(data)

		want := map[uint16]string{0x41: "A", 0x42: "B", 0x43: "C"}
		for code, wantStr := range want {
			if got := table[code]; got != wantStr {
				t.Errorf("table[0x%02X]: got %q, want %q", code, got, wantStr)
			}
		}
	})

	t.Run("bfrange array form", func(t *testing.T) {
		t.Parallel()
		data := []byte(`
beginbfrange
<10> <12> [<0041> <0042> <0043>]
endbfrange
`)
		table := parseCMap(data)

		want := map[uint16]string{0x10: "A", 0x11: "B", 0x12: "C"}
		for code, wantStr := range want {
			if got := table[code]; got != wantStr {
				t.Errorf("table[0x%02X]: got %q, want %q", code, got, wantStr)
			}
		}
	})

	t.Run("empty cmap produces empty table", func(t *testing.T) {
		t.Parallel()
		table := parseCMap([]byte{})
		if len(table) != 0 {
			t.Errorf("expected empty table, got %d entries", len(table))
		}
	})

	t.Run("multiple bfchar sections both parsed", func(t *testing.T) {
		t.Parallel()
		data := []byte(`
beginbfchar
<41> <0041>
endbfchar
some other content
beginbfchar
<42> <0042>
endbfchar
`)
		table := parseCMap(data)

		if got := table[0x41]; got != "A" {
			t.Errorf("table[0x41]: got %q, want %q", got, "A")
		}
		if got := table[0x42]; got != "B" {
			t.Errorf("table[0x42]: got %q, want %q", got, "B")
		}
	})

	t.Run("wraparound guard range ending at 0xFFFF", func(t *testing.T) {
		t.Parallel()
		data := []byte(`
beginbfrange
<FFFD> <FFFF> <FFFD>
endbfrange
`)
		done := make(chan cmapTable, 1)
		go func() {
			done <- parseCMap(data)
		}()

		select {
		case table := <-done:
			wantCodes := []uint16{0xFFFD, 0xFFFE, 0xFFFF}
			for _, code := range wantCodes {
				if _, ok := table[code]; !ok {
					t.Errorf("table[0x%04X] missing", code)
				}
			}
			if len(table) != 3 {
				t.Errorf("expected 3 entries, got %d", len(table))
			}
		case <-time.After(2 * time.Second):
			t.Fatal("parseCMap did not return within 2 seconds (possible infinite loop)")
		}
	})
}
