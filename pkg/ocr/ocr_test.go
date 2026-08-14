package ocr

import "testing"

func TestResultTextPreservesObservationOrder(t *testing.T) {
	t.Parallel()

	result := Result{Observations: []Observation{
		{Text: "  first line  "},
		{Text: ""},
		{Text: "second line"},
	}}
	if got, want := result.Text(), "first line\nsecond line"; got != want {
		t.Fatalf("Text(): got %q, want %q", got, want)
	}
}
