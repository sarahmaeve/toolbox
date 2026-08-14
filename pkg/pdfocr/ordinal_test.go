package pdfocr

import (
	"strings"
	"testing"

	"github.com/sarahmaeve/toolbox/pkg/ocr"
)

func TestRepairEnglishOrdinalSuffixes(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{{
		Text: `the 13* Century, early 16*' century, a 21"-century copy, and the 19 century`,
	}}}
	if got := repairEnglishOrdinalSuffixes(&result); got != 4 {
		t.Fatalf("repair count: got %d, want 4", got)
	}
	got := result.Observations[0].Text
	want := "the 13th Century, early 16th century, a 21st-century copy, and the 19th century"
	if got != want {
		t.Fatalf("ordinal text: got %q, want %q", got, want)
	}
	markdown := renderInlineRuns(observationInlineRuns(result.Observations[0]))
	for _, expected := range []string{"13<sup>th</sup>", "16<sup>th</sup>", "21<sup>st</sup>", "19<sup>th</sup>"} {
		if !strings.Contains(markdown, expected) {
			t.Errorf("ordinal Markdown missing %q: %s", expected, markdown)
		}
	}
}

func TestRepairEnglishOrdinalSuffixesLeavesOtherNumberPunctuation(t *testing.T) {
	t.Parallel()

	result := ocr.Result{Observations: []ocr.Observation{{Text: `notes 13* and 14. Another sentence.`}}}
	if got := repairEnglishOrdinalSuffixes(&result); got != 0 {
		t.Fatalf("repair count: got %d, want 0", got)
	}
	if got := result.Observations[0].Text; got != `notes 13* and 14. Another sentence.` {
		t.Fatalf("unrelated punctuation changed: %q", got)
	}
}
