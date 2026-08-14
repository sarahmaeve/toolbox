package pdfocr

import (
	"testing"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

func TestPossibleSuperscript(t *testing.T) {
	t.Parallel()

	text := []rune("elsewhere,' narrative")
	if !possibleSuperscript(ocr.Symbol{Text: "'", RuneIndex: 10}, text) {
		t.Fatal("comma-following apostrophe was not selected")
	}
	if possibleSuperscript(ocr.Symbol{Text: "'", RuneIndex: 5}, []rune("lover's")) {
		t.Fatal("ordinary apostrophe was selected")
	}
	if !possibleSuperscript(ocr.Symbol{Text: "&", RuneIndex: 0}, []rune("& And")) {
		t.Fatal("leading ampersand was not selected")
	}
}

func TestIsolatedDigits(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{
		{Text: "elsewhere,"},
		{Text: "7"},
	}}
	if got := isolatedDigits(result); got != "7" {
		t.Fatalf("isolatedDigits: got %q, want 7", got)
	}
}

func TestReplaceRune(t *testing.T) {
	t.Parallel()

	if got := replaceRune("elsewhere,' narrative", 10, "7"); got != "elsewhere,7 narrative" {
		t.Fatalf("replaceRune: got %q", got)
	}
}

func TestReplaceObservationRangeShiftsIndexesForTwoDigitCitation(t *testing.T) {
	t.Parallel()

	observation := ocr.Observation{
		Text: "claim.' follows",
		Symbols: []ocr.Symbol{
			{Text: "'", RuneIndex: 6},
			{Text: "f", RuneIndex: 8},
		},
		Styles: []ocr.StyleSpan{{Start: 8, End: 15, Italic: true}},
	}
	replaceObservationRange(&observation, 6, 7, "12", true)
	if got, want := observation.Text, "claim.12 follows"; got != want {
		t.Fatalf("text: got %q, want %q", got, want)
	}
	if got := observation.Symbols[1].RuneIndex; got != 9 {
		t.Fatalf("following symbol index: got %d, want 9", got)
	}
	if got := observation.Styles[0]; got.Start != 9 || got.End != 16 || !got.Italic {
		t.Fatalf("following style: got %+v, want italic [9,16)", got)
	}
	last := observation.Styles[len(observation.Styles)-1]
	if last.Start != 6 || last.End != 8 || !last.Superscript {
		t.Fatalf("citation style: got %+v, want superscript [6,8)", last)
	}
}
